package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claude_decoder_test.go pins the Claude decoder's output for every golden
// fixture in golden_test.go (the same table the byte-identical gate uses), plus
// the contract points DIVERGENCES.md § Decoder contract implications lists.
// Expectations are written as a compact one-line-per-entry description
// (describeEntries) so a whole transcript is reviewable at a glance; field-level
// assertions for the load-bearing details follow in dedicated tests.
//
// The expectation table is HAND-DERIVED from the fixture builders (and, for the
// hard cases, cross-checked against the committed golden files, which were
// generated on master before the decoder existed). Never regenerate it from the
// decoder's own output — that would make the test tautological and silently
// accept whatever the decoder does. When a fixture changes, re-derive its rows
// by reading the fixture, not by pasting describeEntries output.

// describeEntry renders one entry as a single line. Long strings are shortened
// via short() so a 20 KB tool body does not swamp the expectation.
//
//	H@<line> "<text>" [queued]
//	A@<line> text("…") call(<Name> "<Summary>") [launch("<Label>")] …
//	R@<line> <CallID> "<CallName>" "<CallSummary>" body="…" [diff(+a -b)]
//	S@<line> "<text>" [searchonly]
func describeEntry(e Entry) string {
	var sb strings.Builder
	switch e.Kind {
	case EntryHuman:
		fmt.Fprintf(&sb, "H@%d %q", e.LineIndex, short(e.Text))
		if e.Queued {
			sb.WriteString(" queued")
		}
	case EntryAssistant:
		fmt.Fprintf(&sb, "A@%d", e.LineIndex)
		for _, p := range e.Parts {
			if p.Call == nil {
				fmt.Fprintf(&sb, " text(%q)", short(p.Text))
				continue
			}
			fmt.Fprintf(&sb, " call(%s %q)", p.Call.Name, short(p.Call.Summary))
			if p.Call.Launch != nil {
				fmt.Fprintf(&sb, " launch(%q)", short(p.Call.Launch.Label))
			}
		}
	case EntryToolResult:
		fmt.Fprintf(&sb, "R@%d %s %q %q body=%q", e.LineIndex, e.CallID, e.CallName, short(e.CallSummary), short(e.Body))
		if e.Diff != nil {
			fmt.Fprintf(&sb, " diff(+%d -%d)", e.Diff.Added, e.Diff.Removed)
		}
	case EntrySystem:
		fmt.Fprintf(&sb, "S@%d %q", e.LineIndex, short(e.Text))
		if e.SearchOnly {
			sb.WriteString(" searchonly")
		}
	default:
		fmt.Fprintf(&sb, "?@%d kind=%s", e.LineIndex, e.Kind)
	}
	return sb.String()
}

func describeEntries(tr *Transcript) string {
	lines := make([]string, 0, len(tr.Entries))
	for _, e := range tr.Entries {
		lines = append(lines, describeEntry(e))
	}
	return strings.Join(lines, "\n")
}

// describeMeta renders the Claude-relevant Meta fields (PlatformID, ParentUUID
// and Source are asserted empty separately — they are Codex-only).
func describeMeta(m Meta) string {
	return fmt.Sprintf("platform=%s title=%q fallback=%q cwd=%q branch=%q start=%s end=%s",
		m.Platform, m.ExplicitTitle, m.TitleFallback, m.CWD, m.Branch, goldenTime(m.StartTime), goldenTime(m.EndTime))
}

// metaLine builds the expected describeMeta string. start/end are fixture
// offsets for at(); a negative offset means the zero time. (at(3600) renders
// "10:60:00Z", which does not parse — so away_summary's end stays at(0).)
func metaLine(title, fallback, cwd, branch string, start, end int) string {
	tsAt := func(n int) string {
		if n < 0 {
			return ""
		}
		return goldenTime(parseJSONLTime(at(n)))
	}
	return fmt.Sprintf("platform=claude-code title=%q fallback=%q cwd=%q branch=%q start=%s end=%s",
		title, fallback, cwd, branch, tsAt(start), tsAt(end))
}

// short folds newlines to ⏎ and bounds a string to 40 runes + "…[<total>]" when
// it exceeds 60, so bodies stay one readable line.
func short(s string) string {
	s = strings.ReplaceAll(s, "\n", "⏎")
	r := []rune(s)
	if len(r) <= 60 {
		return s
	}
	return string(r[:40]) + fmt.Sprintf("…[%d]", len(r))
}

func TestShort(t *testing.T) {
	assert.Equal(t, "a⏎b", short("a\nb"))
	assert.Equal(t, strings.Repeat("x", 60), short(strings.Repeat("x", 60)))
	assert.Equal(t, strings.Repeat("x", 40)+"…[61]", short(strings.Repeat("x", 61)))
	assert.Equal(t, strings.Repeat("ü", 40)+"…[70]", short(strings.Repeat("ü", 70)), "rune-bounded")
}

func goldenCaseByName(t *testing.T, name string) goldenCase {
	t.Helper()
	for _, c := range goldenCases(t) {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no golden case %q", name)
	return goldenCase{}
}

func decodeGolden(t *testing.T, c goldenCase) *Transcript {
	t.Helper()
	tr, err := claudeDecoder{lineCap: c.lineCap}.Decode(bytes.NewReader(c.raw))
	require.NoError(t, err)
	require.NotNil(t, tr)
	return tr
}

// TestClaudeDecoder_GoldenCases asserts Meta and the full entry list for every
// golden fixture. A case missing from this table fails the test, so a new golden
// fixture must state its decoder expectation here too.
func TestClaudeDecoder_GoldenCases(t *testing.T) {
	longPrompt := strings.Repeat("investigate the flaky quokka test in the vault package ", 4) // 224 runes
	agentLong := "Agent " + truncateRunes(longPrompt, agentPromptMaxChars)                     // Summary: prompt ≤ 200
	launchLong := truncateRunes(longPrompt, subagentLabelMaxChars)                             // Launch.Label: prompt ≤ 100
	launchEntries := []string{
		`H@0 "Delegate the investigation"`,
		fmt.Sprintf(`A@1 text("Delegating.") call(Task %q) launch("Investigate flaky test") call(Agent "Agent look around") launch("Explore") call(Agent %q) launch(%q) call(Agent "Agent") launch("subagent")`,
			short(agentLong), short(agentLong), short(launchLong)),
		fmt.Sprintf(`R@2 t1 "Task" %q body="agent done: the port is shared"`, short(agentLong)),
		`R@2 t2 "Agent" "Agent look around" body="explored"`,
	}
	// The generic summary is pinned by mcp_generic_summary.scan.json; the decoder
	// must produce the identical label (in-label sanitisation included, D22).
	mcpSummary := `mcp__capy__capy_search Query=case collision queries=["tool input","second"] source=kk: a_very_long_key_name_that_exceeds_forty_…=v api_key=[REDACTED_SECRET] arr=[{…},2,"s"] +3`
	secretPrompt := "my token is " + fakeSecret + " please use it"
	longFallback := strings.TrimSpace("Deploy with token " + fakeSecret + " " + strings.Repeat("ünïcödé ", 20))

	want := map[string]struct {
		meta    string
		entries []string
	}{
		"plain_user_assistant": {
			meta: metaLine("", "Please fix the timeout bug", "/home/user/proj", "main", 0, 9),
			entries: []string{
				`H@0 "Please fix the timeout bug"`,
				`A@1 text("Sure — looking now.")`,
				`H@2 "thanks"`,
			},
		},
		"block_user_tool_results": {
			meta: metaLine("", "Read config, edit it, run tests", "/p", "main", 0, 14),
			entries: []string{
				`H@0 "Read config, edit it, run tests"`,
				`A@1 call(Read "Read /p/config.toml") call(Edit "Edit /p/config.toml") call(Bash "Bash go test ./...")`,
				`R@2 t1 "Read" "Read /p/config.toml" body="timeout = 30⏎retries = 1⏎[db]⏎path = x"`,
				`R@3 t2 "Edit" "Edit /p/config.toml" body="The file /p/config.toml has been updated successfully." diff(+1 -1)`,
				`H@4 "also note:  keep going"`,                            // <system-reminder> stripped, two spaces remain
				`R@4 t3 "Bash" "Bash go test ./..." body="ok  1 passed"`,  // image block skipped
				`R@5 t9 "" "" body="orphan output with no matching call"`, // unknown id: no call name/summary
				`R@6 t3 "Bash" "Bash go test ./..." body=""`,              // D18: empty body still emitted
			},
		},
		"bash_long_result": {
			meta: metaLine("", "run the long thing", "/p", "main", 0, 11),
			entries: []string{
				`H@0 "run the long thing"`,
				`A@1 call(Bash "Bash make verbose") call(Bash "Bash cat blob")`,
				fmt.Sprintf(`R@2 t1 "Bash" "Bash make verbose" body=%q`, short(numberedLines(25))),
				fmt.Sprintf(`R@3 t2 "Bash" "Bash cat blob" body=%q`, short(strings.Repeat("x", 2500))),
			},
		},
		"tool_result_head_tail_truncation": {
			meta: metaLine("", "dump it", "/p", "main", 0, 10),
			entries: []string{
				`H@0 "dump it"`,
				`A@1 call(Bash "Bash dump")`,
				fmt.Sprintf(`R@2 t1 "Bash" "Bash dump" body=%q`, short(numberedLines(700))), // untruncated: 700 lines
			},
		},
		"edit_variants": {
			meta: metaLine("", "make the edits", "/p", "main", 0, 15),
			entries: []string{
				`H@0 "make the edits"`,
				`A@1 call(Edit "Edit /p/config.toml") call(Write "Write /p/new.go") call(Edit "Edit /p/other.go") call(Edit "Edit /p/third.go")`,
				`R@2 t1 "Edit" "Edit /p/config.toml" body="The file /p/config.toml has been updated successfully." diff(+2 -1)`, // two hunks
				`R@3 t2 "Write" "Write /p/new.go" body="File created successfully at: /p/new.go" diff(+1 -0)`,
				`R@4 t3 "Edit" "Edit /p/other.go" body="The file /p/other.go has been updated successfully."`,             // toolUseResult without a patch
				`R@5 t4 "Edit" "Edit /p/third.go" body="The file /p/third.go has been updated successfully." diff(+1 -1)`, // first diff result claims the line's patch
				`R@5 t1 "Edit" "Edit /p/config.toml" body="The file /p/config.toml has been updated successfully."`,       // second one does not
				`R@6 t4 "Edit" "Edit /p/third.go" body="" diff(+1 -1)`,                                                    // D18: empty body, patch kept
				`R@7 t3 "Edit" "Edit /p/other.go" body="updated"`,                                                         // all-empty hunks → no diff
			},
		},
		"snapshots": {
			meta: metaLine("", "look", "/p", "main", 0, 10),
			entries: []string{
				`H@0 "look"`,
				`A@1 text("Let me look.") call(Bash "Bash ls") text("Done.")`, // three snapshots merged at the first line
				`A@4 text("no id, keyed by uuid") text("second")`,             // keyed by line uuid when message.id is absent
				`R@6 t1 "Bash" "Bash ls" body="a.go⏎b.go"`,
			},
		},
		"text_tool_text": {
			meta: metaLine("", "check a.go", "/p", "main", 0, 10),
			entries: []string{
				`H@0 "check a.go"`,
				`A@1 text("Before the call.") call(Read "Read /p/a.go") text("After the call.")`,
				`R@2 t1 "Read" "Read /p/a.go" body="package p"`,
			},
		},
		"ai_title": {
			meta: metaLine("Second title wins", "first prompt becomes the fallback", "/p", "main", 0, 5),
			entries: []string{
				`H@0 "first prompt becomes the fallback"`,
				`A@2 text("ok")`,
			},
		},
		"title_fallback_rules": {
			// D1: raw (no cleanText), plain-string only, skips block text, `<`-prefixed and queued.
			meta: metaLine("", "eligible title <system-reminder>hidden reminder</system-reminder> continues", "/p", "main", 0, 4),
			entries: []string{
				`H@0 "block text is not eligible"`,
				// line 1: "<command-name>/clear</command-name>" cleans to "" → no entry
				`H@2 "queued prompts are not eligible" queued`,
				`H@3 "eligible title  continues"`,
				`H@4 "second eligible, ignored"`,
			},
		},
		"title_fallback_long": {
			meta:    metaLine("", longFallback, "/p", "main", 0, 0), // unsanitized, untruncated
			entries: []string{fmt.Sprintf(`H@0 %q`, short(longFallback))},
		},
		"pr_link": {
			meta: metaLine("", "open a PR", "/p", "main", 0, 62),
			entries: []string{
				`H@0 "open a PR"`,
				`S@1 "PR #42 acme/widgets https://github.com/acme/widgets/pull/42"`,
				`S@2 "https://github.com/acme/widgets/pull/43"`,
				// line 3: prNumber 0 and nothing else → no text → no entry
			},
		},
		"away_summary": {
			meta: metaLine("", "start", "/p", "main", 0, 0), // 10:60:00Z timestamps do not parse
			entries: []string{
				`H@0 "start"`,
				`S@1 "We configured the dependencies and set up CI."`,
			},
		},
		"queued_command": {
			meta: metaLine("", "Start the work", "/p", "main", 0, 10),
			entries: []string{
				`H@0 "Start the work"`,
				`A@1 call(Bash "Bash ls")`,
				`R@3 t1 "Bash" "Bash ls" body="file1 file2"`,
				`H@5 "the mcp has no access to vault" queued`, // cleanText applied to the queued prompt
				`A@7 text("noted")`,
			},
		},
		"attachment_message": {
			meta: metaLine("", "attach things", "/p", "main", 0, 7),
			entries: []string{
				`H@0 "attach things"`,
				`S@1 "attached note a.txt /p/a.txt" searchonly`, // D7: scanner-only system text
				`S@2 "plain attachment text" searchonly`,
				`H@3 "queued wins" queued`, // queued_command beats the message on the same line
			},
		},
		"oversize_line": {
			meta: metaLine("", "start", "/p", "main", 0, 2),
			entries: []string{
				`H@0 "start"`,
				`A@2 text("after the oversize line")`, // line 1 skipped but still counted
			},
		},
		"malformed_lines": {
			meta: metaLine("", "hello", "/p", "main", 0, 8),
			entries: []string{
				`H@0 "hello"`,
				`A@6 text("inferred assistant")`, // type inferred from message.role
				`H@9 "crlf line"`,
				`H@10 "trailing line without newline"`,
			},
		},
		"metadata_lines": {
			// D3: cwd/branch from the message-less user line; assistant cwd ignored.
			// D4: start from queue-operation, end from file-history-snapshot.
			meta: metaLine("Metadata title", "real prompt", "/from/messageless", "dev", 0, 4),
			entries: []string{
				`A@2 text("hi")`,
				`H@3 "real prompt"`,
			},
		},
		"subagent_markers_openable": {
			meta:    metaLine("", "Delegate the investigation", "/p", "main", 0, 30),
			entries: launchEntries,
		},
		"subagent_markers_mismatch": {
			meta:    metaLine("", "Delegate the investigation", "/p", "main", 0, 30),
			entries: launchEntries, // sidecar mapping is consumer policy (D23); same decode
		},
		"thinking_only": {
			meta: metaLine("", "think about it", "/p", "main", 0, 6),
			entries: []string{
				`H@0 "think about it"`,
				// line 1: thinking-only assistant → no parts → no entry
				`A@2 text("visible answer")`,
			},
		},
		"mcp_generic_summary": {
			meta: metaLine("", "search the knowledge base", "/p", "main", 0, 10),
			entries: []string{
				`H@0 "search the knowledge base"`,
				fmt.Sprintf(`A@1 call(mcp__capy__capy_search %q) call(WebFetch "WebFetch url=https://example.com/doc prompt=summarise") call(mcp__x__y "mcp__x__y") call(ToolSearch "ToolSearch")`, short(mcpSummary)),
				fmt.Sprintf(`R@2 t1 "mcp__capy__capy_search" %q body="3 results"`, short(mcpSummary)),
				`R@2 t2 "WebFetch" "WebFetch url=https://example.com/doc prompt=summarise" body="fetched 12 KB"`,
				`R@2 t3 "mcp__x__y" "mcp__x__y" body="bare"`,
				`R@2 t4 "ToolSearch" "ToolSearch" body="no tools"`,
			},
		},
		"secrets": {
			// D22: the decoder is verbatim; only the scanner redacts.
			meta: metaLine("", secretPrompt, "/p", "main", 0, 10),
			entries: []string{
				fmt.Sprintf(`H@0 %q`, short(secretPrompt)),
				fmt.Sprintf(`A@1 text(%q) call(Bash %q)`, "Using "+fakeSecret+" now.", "Bash export TOKEN="+fakeSecret),
				fmt.Sprintf(`R@2 t1 "Bash" %q body=%q`, "Bash export TOKEN="+fakeSecret, "exported "+fakeSecret),
			},
		},
		"noise_tags": {
			meta: metaLine("", "", "/p", "main", 0, 2), // every plain prompt is `<`-prefixed → no fallback
			entries: []string{
				`H@0 "real prompt  tail"`,
				`H@1 "block prompt"`,
				// line 2: entirely noise → "" → no entry
			},
		},
		"user_empty_content": {
			meta:    metaLine("", "", "/p", "main", 0, 3),
			entries: nil,
		},
		"empty": {
			meta:    metaLine("", "", "", "", -1, -1),
			entries: nil,
		},
	}

	cases := goldenCases(t)
	for _, c := range cases {
		exp, ok := want[c.name]
		require.True(t, ok, "golden case %q has no decoder expectation — add it to this table", c.name)
		t.Run(c.name, func(t *testing.T) {
			tr := decodeGolden(t, c)
			assert.Equal(t, exp.meta, describeMeta(tr.Meta))
			assert.Empty(t, tr.Meta.PlatformID, "Claude does not extract a platform id")
			assert.Empty(t, tr.Meta.ParentUUID)
			assert.Empty(t, tr.Meta.Source)
			assert.Equal(t, strings.Join(exp.entries, "\n"), describeEntries(tr))
		})
	}
	for name := range want {
		found := false
		for _, c := range cases {
			if c.name == name {
				found = true
				break
			}
		}
		assert.True(t, found, "expectation %q has no golden case", name)
	}
}

// TestClaudeDecoder_PayloadIsolation checks, over every golden fixture, that an
// entry populates only its Kind's field group — a consumer switching on Kind
// must never find stray payload on the wrong kind.
func TestClaudeDecoder_PayloadIsolation(t *testing.T) {
	for _, c := range goldenCases(t) {
		assertPayloadIsolation(t, c.name, decodeGolden(t, c))
	}
}

// assertPayloadIsolation is the per-transcript check shared by both decoders'
// isolation sweeps (TestClaudeDecoder_PayloadIsolation,
// TestCodexDecoder_PayloadIsolation): every entry populates only its Kind's
// field group, and every Part has exactly one side set.
func assertPayloadIsolation(t *testing.T, name string, tr *Transcript) {
	t.Helper()
	for i, e := range tr.Entries {
		where := fmt.Sprintf("%s entry %d (%s)", name, i, describeEntry(e))
		switch e.Kind {
		case EntryHuman:
			assert.NotEmpty(t, e.Text, where)
			assert.Nil(t, e.Parts, where)
			assert.Empty(t, e.CallID+e.CallName+e.CallSummary+e.Body, where)
			assert.Nil(t, e.Diff, where)
			assert.False(t, e.SearchOnly, where)
		case EntryAssistant:
			assert.NotEmpty(t, e.Parts, where)
			assert.Empty(t, e.Text, where)
			assert.False(t, e.Queued, where)
			assert.Empty(t, e.CallID+e.CallName+e.CallSummary+e.Body, where)
			assert.Nil(t, e.Diff, where)
			for _, p := range e.Parts {
				if p.Call == nil {
					assert.NotEmpty(t, p.Text, where)
				} else {
					assert.Empty(t, p.Text, where)
				}
			}
		case EntryToolResult:
			assert.Empty(t, e.Text, where)
			assert.Nil(t, e.Parts, where)
			assert.False(t, e.Queued, where)
			assert.False(t, e.SearchOnly, where)
			if e.CallName == "" {
				assert.Empty(t, e.CallSummary, where)
			}
		case EntrySystem:
			assert.NotEmpty(t, e.Text, where)
			assert.Nil(t, e.Parts, where)
			assert.False(t, e.Queued, where)
			assert.Empty(t, e.CallID+e.CallName+e.CallSummary+e.Body, where)
			assert.Nil(t, e.Diff, where)
		default:
			t.Errorf("%s: invalid kind %v", where, e.Kind)
		}
	}
}

// Merged progressive snapshots anchor at the FIRST snapshot's line and carry
// its timestamp (D5/D6) — the FTS line_index and the viewer's scroll anchor.
func TestClaudeDecoder_SnapshotAnchorsAtFirstLine(t *testing.T) {
	tr := decodeGolden(t, goldenCaseByName(t, "snapshots"))
	var assistants []Entry
	for _, e := range tr.Entries {
		if e.Kind == EntryAssistant {
			assistants = append(assistants, e)
		}
	}
	require.Len(t, assistants, 2)

	byID := assistants[0]
	assert.Equal(t, 1, byID.LineIndex, "three snapshots at lines 1–3 merge onto line 1")
	assert.Equal(t, parseJSONLTime(at(5)), byID.Timestamp, "first snapshot's timestamp, not the last's")
	require.Len(t, byID.Parts, 3)
	assert.Equal(t, "Let me look.", byID.Parts[0].Text)
	require.NotNil(t, byID.Parts[1].Call)
	assert.Equal(t, "t1", byID.Parts[1].Call.ID)
	assert.Equal(t, "Done.", byID.Parts[2].Text)

	byUUID := assistants[1]
	assert.Equal(t, 4, byUUID.LineIndex, "id-less snapshots merge by line uuid")
	assert.Equal(t, parseJSONLTime(at(8)), byUUID.Timestamp)
}

// Part order is load-bearing: text → call → text must survive as-is.
func TestClaudeDecoder_PartsPreserveBlockOrder(t *testing.T) {
	tr := decodeGolden(t, goldenCaseByName(t, "text_tool_text"))
	require.Len(t, tr.Entries, 3)
	a := tr.Entries[1]
	require.Equal(t, EntryAssistant, a.Kind)
	require.Len(t, a.Parts, 3)
	assert.False(t, a.Parts[0].IsCall())
	assert.Equal(t, "Before the call.", a.Parts[0].Text)
	require.True(t, a.Parts[1].IsCall())
	assert.Equal(t, "Read", a.Parts[1].Call.Name)
	assert.Equal(t, "Read /p/a.go", a.Parts[1].Call.Summary)
	assert.JSONEq(t, `{"file_path":"/p/a.go"}`, string(a.Parts[1].Call.Input), "raw input kept for future generic rendering")
	assert.Nil(t, a.Parts[1].Call.Launch, "Read is not a launch")
	assert.False(t, a.Parts[2].IsCall())
	assert.Equal(t, "After the call.", a.Parts[2].Text)

	r := tr.Entries[2]
	require.Equal(t, EntryToolResult, r.Kind)
	assert.Equal(t, "t1", r.CallID)
	assert.Equal(t, "Read", r.CallName, "correlated to the call by tool_use_id")
	assert.Equal(t, "Read /p/a.go", r.CallSummary)
}

// The Diff text is diffBodyFromToolResult's unified diff, verbatim prefixes kept.
func TestClaudeDecoder_DiffText(t *testing.T) {
	tr := decodeGolden(t, goldenCaseByName(t, "block_user_tool_results"))
	var edit *Entry
	for i := range tr.Entries {
		if e := &tr.Entries[i]; e.Kind == EntryToolResult && e.CallID == "t2" {
			edit = e
			break
		}
	}
	require.NotNil(t, edit)
	require.NotNil(t, edit.Diff)
	assert.Equal(t, "@@ -3,2 +3,2 @@\n timeout = 30\n-retries = 1\n+retries = 3", edit.Diff.Text)
	assert.Equal(t, 1, edit.Diff.Added)
	assert.Equal(t, 1, edit.Diff.Removed)
	assert.Equal(t, "The file /p/config.toml has been updated successfully.", edit.Body, "the success body is kept beside the diff")
}

// Task/Agent calls carry both the Summary (Agent <prompt≤200>) and a Launch label
// (description › subagent_type › prompt≤100 › "subagent") — D19. No other call
// gets a Launch, and Claude never resolves a ChildUUID (D23).
func TestClaudeDecoder_LaunchLabels(t *testing.T) {
	tr := decodeGolden(t, goldenCaseByName(t, "subagent_markers_openable"))
	require.Len(t, tr.Entries, 4)
	a := tr.Entries[1]
	require.Equal(t, EntryAssistant, a.Kind)
	require.Len(t, a.Parts, 5)

	var calls []*ToolCall
	for _, p := range a.Parts[1:] {
		require.True(t, p.IsCall())
		calls = append(calls, p.Call)
	}
	longPrompt := strings.Repeat("investigate the flaky quokka test in the vault package ", 4)

	require.NotNil(t, calls[0].Launch)
	assert.Equal(t, "Task", calls[0].Name)
	assert.Equal(t, "Agent "+truncateRunes(longPrompt, agentPromptMaxChars), calls[0].Summary)
	assert.Equal(t, "Investigate flaky test", calls[0].Launch.Label, "description wins")

	require.NotNil(t, calls[1].Launch)
	assert.Equal(t, "Explore", calls[1].Launch.Label, "subagent_type when no description")

	require.NotNil(t, calls[2].Launch)
	assert.Equal(t, truncateRunes(longPrompt, subagentLabelMaxChars), calls[2].Launch.Label, "prompt bounded to 100 runes")

	require.NotNil(t, calls[3].Launch)
	assert.Equal(t, "subagent", calls[3].Launch.Label)
	assert.Equal(t, "Agent", calls[3].Summary, "empty input → bare name")

	for _, c := range calls {
		assert.Empty(t, c.Launch.ChildUUID, "Claude records no child session id")
	}

	bash := decodeGolden(t, goldenCaseByName(t, "bash_long_result"))
	for _, p := range bash.Entries[1].Parts {
		require.True(t, p.IsCall())
		assert.Nil(t, p.Call.Launch, "Bash is not a launch")
	}
}

// A subagent sidecar is decoded exactly like a main session.
func TestClaudeDecoder_Sidecar(t *testing.T) {
	c := goldenCaseByName(t, "subagent_markers_openable")
	tr, err := claudeDecoder{}.Decode(bytes.NewReader(c.sidecars["bbb"]))
	require.NoError(t, err)
	assert.Equal(t, metaLine("", "Explore the repo layout", "/p", "main", 22, 24), describeMeta(tr.Meta))
	assert.Equal(t, strings.Join([]string{
		`H@0 "Explore the repo layout"`,
		`A@1 call(Bash "Bash rg --files")`,
		`R@2 sb-t1 "Bash" "Bash rg --files" body="a.go⏎b.go"`,
	}, "\n"), describeEntries(tr))
}

// A zero lineCap follows the package cap the golden harness lowers, so the
// decoder DecoderFor returns exercises the same oversize path as the readers.
func TestClaudeDecoder_ZeroLineCapFollowsPackageCap(t *testing.T) {
	c := goldenCaseByName(t, "oversize_line")
	require.Greater(t, c.lineCap, 0)

	full, err := claudeDecoder{}.Decode(bytes.NewReader(c.raw))
	require.NoError(t, err)
	assert.Len(t, full.Entries, 3, "under the production cap nothing is oversize")

	setLineCaps(t, c.lineCap)
	capped, err := DecoderFor(PlatformClaudeCode).Decode(bytes.NewReader(c.raw))
	require.NoError(t, err)
	explicit := decodeGolden(t, c)
	assert.Equal(t, describeEntries(explicit), describeEntries(capped))
	assert.Len(t, capped.Entries, 2)
}

// recordingHandler collects slog records so a test can assert on warnings
// without depending on the default handler's output format. Attrs bound with
// Logger.With (the decoders' "file" label, decoderLogger) are folded into
// each record so recordAttrs sees them like the built-in handlers would print
// them; every WithAttrs derivative records into the root handler's slice.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record

	root  *recordingHandler // nil on the root; a WithAttrs child records into root
	attrs []slog.Attr       // attrs bound via WithAttrs, prepended to each record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	if len(h.attrs) > 0 {
		r = r.Clone()
		r.AddAttrs(h.attrs...)
	}
	root := h
	if h.root != nil {
		root = h.root
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	root.records = append(root.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	root := h
	if h.root != nil {
		root = h.root
	}
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &recordingHandler{root: root, attrs: merged}
}
func (h *recordingHandler) WithGroup(string) slog.Handler { return h }

func (h *recordingHandler) messagesWithPrefix(prefix string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if strings.HasPrefix(r.Message, prefix) {
			out = append(out, r.Message)
		}
	}
	return out
}

// D9 (log-only): the decoder keeps the scanner's warnings — one per malformed
// JSONL line and one per malformed assistant content — and nothing else warns.
func TestClaudeDecoder_WarnsOnMalformedContent(t *testing.T) {
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	decodeGolden(t, goldenCaseByName(t, "malformed_lines"))

	got := h.messagesWithPrefix("vault claude decoder:")
	assert.Equal(t, []string{
		"vault claude decoder: skipping malformed JSONL line",        // `not json at all`
		"vault claude decoder: skipping malformed assistant content", // content "not an array"
	}, got)
}

// A real Claude session contained a torn assistant snapshot immediately followed
// by a complete user record with no separating newline. Recover the complete
// suffix at the same physical-line anchor instead of dropping the user turn.
func TestClaudeDecoder_RecoversTrailingRecordFromMalformedLine(t *testing.T) {
	h := captureSlog(t)
	const (
		path    = "/home/user/.claude/projects/-home-user-proj/abc.jsonl"
		partial = `{"parentUuid":"p0","type":"assistant","uuid":"a0","message":{"role":"assistant","content":[],"usage":{"iterations":[{"input`
		suffix  = `{"parentUuid":"a0","type":"user","uuid":"u1","timestamp":"2026-09-17T14:14:56.748Z","cwd":"/home/user/proj","gitBranch":"main","message":{"role":"user","content":"share this with the dev team"}}`
	)

	tr, err := claudeDecoder{}.Decode(withSource(path, strings.NewReader(partial+suffix+"\n")))
	require.NoError(t, err)
	require.Len(t, tr.Entries, 1)
	assert.Equal(t, Entry{
		Kind: EntryHuman, LineIndex: 0, Timestamp: parseJSONLTime("2026-09-17T14:14:56.748Z"),
		Text: "share this with the dev team",
	}, tr.Entries[0])
	assert.Equal(t, "share this with the dev team", tr.Meta.TitleFallback)
	assert.Equal(t, "/home/user/proj", tr.Meta.CWD)
	assert.Equal(t, "main", tr.Meta.Branch)

	records := h.recordsWithMessage("vault claude decoder: recovered trailing record from malformed JSONL line")
	require.Len(t, records, 1)
	attrs := recordAttrs(records[0])
	assert.Equal(t, path, attrs["file"])
	assert.EqualValues(t, 0, attrs["line"])
	assert.EqualValues(t, len(partial), attrs["discarded_bytes"])
	assert.Empty(t, h.recordsWithMessage("vault claude decoder: skipping malformed JSONL line"))
}

// Both Claude skip warnings name the file when the reader is labelled
// (withSource), mirroring TestCodexDecoder_WarningsNameTheSource.
func TestClaudeDecoder_WarningsNameTheSource(t *testing.T) {
	h := captureSlog(t)
	c := goldenCaseByName(t, "malformed_lines")
	const path = "/home/user/.claude/projects/-home-user-proj/abc.jsonl"
	_, err := claudeDecoder{lineCap: c.lineCap}.Decode(withSource(path, bytes.NewReader(c.raw)))
	require.NoError(t, err)

	records := h.recordsWithMessage("vault claude decoder: skipping malformed JSONL line")
	records = append(records, h.recordsWithMessage("vault claude decoder: skipping malformed assistant content")...)
	require.Len(t, records, 2)
	for _, r := range records {
		assert.Equal(t, path, recordAttrs(r)["file"], r.Message)
	}
}

// withSource / decoderLogger: an empty label leaves the reader untouched, a
// labelled reader and an *os.File (whose Name is its path) both yield a logger
// carrying "file", and a plain reader yields one without it.
func TestDecoderLogger_SourceLabel(t *testing.T) {
	h := captureSlog(t)
	plain := strings.NewReader("")
	assert.Same(t, plain, withSource("", plain).(*strings.Reader), "empty label is a no-op")

	f, err := os.CreateTemp(t.TempDir(), "session-*.jsonl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	decoderLogger(plain).Warn("plain")
	decoderLogger(withSource("label", plain)).Warn("labelled")
	decoderLogger(f).Warn("file")

	_, has := recordAttrs(h.recordsWithMessage("plain")[0])["file"]
	assert.False(t, has)
	assert.Equal(t, "label", recordAttrs(h.recordsWithMessage("labelled")[0])["file"])
	assert.Equal(t, f.Name(), recordAttrs(h.recordsWithMessage("file")[0])["file"])
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// A genuine read error is the only error Decode returns, wrapped as ScanSession
// reports it today (D10).
func TestClaudeDecoder_ReadError(t *testing.T) {
	boom := errors.New("disk on fire")
	tr, err := claudeDecoder{}.Decode(errReader{err: boom})
	require.Error(t, err)
	assert.Nil(t, tr)
	assert.True(t, errors.Is(err, boom))
	assert.Equal(t, "reading session: disk on fire", err.Error())
}

func TestClaudeDecoder_EmptyInput(t *testing.T) {
	tr, err := claudeDecoder{}.Decode(bytes.NewReader(nil))
	require.NoError(t, err)
	assert.Equal(t, Meta{Platform: PlatformClaudeCode}, tr.Meta)
	assert.Nil(t, tr.Entries)

	tr, err = claudeDecoder{}.Decode(io.LimitReader(strings.NewReader("\n\n"), 2))
	require.NoError(t, err)
	assert.Nil(t, tr.Entries, "blank lines produce nothing")
}

func TestEntryKind_String(t *testing.T) {
	assert.Equal(t, "human", EntryHuman.String())
	assert.Equal(t, "assistant", EntryAssistant.String())
	assert.Equal(t, "tool_result", EntryToolResult.String())
	assert.Equal(t, "system", EntrySystem.String())
	assert.Equal(t, "EntryKind(0)", EntryUnknown.String())
}

// FuzzClaudeDecoder_NeverPanics pins the Decoder contract: bad CONTENT is never
// an error (only a read error is), nothing panics, and every entry is well-formed
// (a valid Kind, a non-negative line index, one side of each Part set).
func FuzzClaudeDecoder_NeverPanics(f *testing.F) {
	for _, c := range goldenCases(f) {
		f.Add(c.raw)
	}
	f.Add([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"text","text":1}]}]}}`))
	f.Add([]byte(`{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use","name":"Edit","input":{"file_path":5}}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result"}]},"toolUseResult":{"structuredPatch":[{"lines":["+a"]}]}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		tr, err := claudeDecoder{}.Decode(bytes.NewReader(raw))
		require.NoError(t, err, "content is never an error")
		require.NotNil(t, tr)
		assert.Equal(t, PlatformClaudeCode, tr.Meta.Platform)
		prev := -1
		for _, e := range tr.Entries {
			assert.NotEqual(t, EntryUnknown, e.Kind)
			assert.GreaterOrEqual(t, e.LineIndex, prev, "entries are in source order")
			prev = e.LineIndex
			for _, p := range e.Parts {
				assert.NotEqual(t, p.Text == "", p.Call == nil, "exactly one of Text/Call is set")
			}
		}
	})
}
