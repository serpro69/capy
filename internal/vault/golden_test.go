package vault

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// golden_test.go freezes the observable output of the four public Claude JSONL
// readers — ScanSession (+ ScanSubagent for sidecars), RenderText,
// RenderMarkdown and ParseTranscript — for a fixture table that covers every
// `case` arm of the three `switch line.Type` blocks and every row of
// testdata/golden/DIVERGENCES.md. It is the byte-identical gate for the
// transcript-model refactor (docs/feat/done/codex-vault-sessions/, Slices 3–4):
// a refactor that changes any golden file has changed Claude behaviour.
//
// Regenerate deliberately with:
//
//	go test -tags fts5 -run TestGolden ./internal/vault/ -update
//
// and review the diff — a golden change is a behaviour change.
var updateGolden = flag.Bool("update", false, "rewrite testdata/golden files from the current readers")

// goldenDir holds one file per (case, reader): <case>.<reader> where reader is
// scan.json | text.txt | markdown.md | transcript.json.
const goldenDir = "testdata/golden"

// goldenReaders lists the per-case output files in the order they are compared.
var goldenReaders = []string{"scan.json", "text.txt", "markdown.md", "transcript.json"}

// goldenCase is one fixture: the main session bytes, optional subagent sidecars
// (id → bytes of subagents/agent-<id>.jsonl) and an optional lowered per-line
// cap (0 = production caps) for the oversize-line case.
type goldenCase struct {
	name     string
	raw      []byte
	sidecars map[string][]byte
	lineCap  int
}

// goldenScanResult / goldenScanOutput mirror ScanResult / ScanOutput with times
// rendered as RFC3339Nano UTC strings so the serialisation is deterministic and
// zone-independent.
type goldenScanResult struct {
	TurnIndex    int
	MessageIndex int
	LineIndex    int
	Role         string
	SubagentID   string
	ContentText  string
	Timestamp    string
	ToolNames    []string
}

type goldenScanOutput struct {
	Results      []goldenScanResult
	Title        string
	CWD          string
	Branch       string
	StartTime    string
	EndTime      string
	MessageCount int
}

// goldenScanFile is the scan.json shape: the main ScanOutput plus ScanSubagent
// results per sidecar id. goldenTranscriptFile is the transcript.json analogue.
type goldenScanFile struct {
	Main      goldenScanOutput
	Subagents map[string][]goldenScanResult `json:",omitempty"`
}

type goldenTranscriptFile struct {
	Main      []TranscriptMessage
	Subagents map[string][]TranscriptMessage `json:",omitempty"`
}

func goldenTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func goldenResults(rs []ScanResult) []goldenScanResult {
	out := make([]goldenScanResult, len(rs))
	for i, r := range rs {
		out[i] = goldenScanResult{
			TurnIndex:    r.TurnIndex,
			MessageIndex: r.MessageIndex,
			LineIndex:    r.LineIndex,
			Role:         r.Role,
			SubagentID:   r.SubagentID,
			ContentText:  r.ContentText,
			Timestamp:    goldenTime(r.Timestamp),
			ToolNames:    r.ToolNames,
		}
	}
	return out
}

func goldenScan(out *ScanOutput) goldenScanOutput {
	return goldenScanOutput{
		Results:      goldenResults(out.Results),
		Title:        out.Title,
		CWD:          out.CWD,
		Branch:       out.Branch,
		StartTime:    goldenTime(out.StartTime),
		EndTime:      goldenTime(out.EndTime),
		MessageCount: out.MessageCount,
	}
}

func marshalGolden(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	return append(b, '\n')
}

// sortedKeys returns m's keys in sorted order (the sidecar id order used for
// ParseTranscript's subagentIDs and for the per-sidecar sections).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// readerOutputs runs the four public readers over a session (and ScanSubagent /
// the three display readers over each sidecar) and returns the serialised
// output per goldenReaders entry. Shared with parity_canary_test.go so the
// fixture goldens and the real-corpus digest freeze the same bytes.
func readerOutputs(t *testing.T, raw []byte, sidecars map[string][]byte) map[string][]byte {
	t.Helper()
	ids := sortedKeys(sidecars)
	var subagentIDs []string
	if len(ids) > 0 {
		subagentIDs = ids
	}

	scanOut, err := ScanSession(PlatformClaudeCode, bytes.NewReader(raw))
	require.NoError(t, err)
	scanFile := goldenScanFile{Main: goldenScan(scanOut)}
	transcriptFile := goldenTranscriptFile{Main: ParseTranscript(PlatformClaudeCode, raw, subagentIDs)}

	var text, md strings.Builder
	text.WriteString(RenderText(PlatformClaudeCode, raw))
	md.WriteString(RenderMarkdown(PlatformClaudeCode, raw))

	for _, id := range ids {
		sc := sidecars[id]
		results, err := ScanSubagent(bytes.NewReader(sc), id)
		require.NoError(t, err)
		if scanFile.Subagents == nil {
			scanFile.Subagents = map[string][]goldenScanResult{}
			transcriptFile.Subagents = map[string][]TranscriptMessage{}
		}
		scanFile.Subagents[id] = goldenResults(results)
		transcriptFile.Subagents[id] = ParseTranscript(PlatformClaudeCode, sc, nil)
		fmt.Fprintf(&text, "\n===== subagent %s =====\n%s", id, RenderText(PlatformClaudeCode, sc))
		fmt.Fprintf(&md, "\n===== subagent %s =====\n%s", id, RenderMarkdown(PlatformClaudeCode, sc))
	}

	return map[string][]byte{
		"scan.json":       marshalGolden(t, scanFile),
		"text.txt":        []byte(text.String()),
		"markdown.md":     []byte(md.String()),
		"transcript.json": marshalGolden(t, transcriptFile),
	}
}

// setLineCaps lowers the shared per-line cap (scanLineCap — the one cap every
// consumer reads through the Claude decoder, D11) for the duration of a test (0
// leaves the production value). Not safe under t.Parallel — TestGolden does not
// use it.
func setLineCaps(t *testing.T, limit int) {
	t.Helper()
	if limit <= 0 {
		return
	}
	prev := scanLineCap
	scanLineCap = limit
	t.Cleanup(func() { scanLineCap = prev })
}

func TestGolden(t *testing.T) {
	cases := goldenCases(t)
	if *updateGolden {
		require.NoError(t, os.MkdirAll(goldenDir, 0o755))
	}
	names := map[string]bool{}
	for _, c := range cases {
		require.False(t, names[c.name], "duplicate golden case %q", c.name)
		names[c.name] = true
		t.Run(c.name, func(t *testing.T) {
			setLineCaps(t, c.lineCap)
			outputs := readerOutputs(t, c.raw, c.sidecars)
			for _, reader := range goldenReaders {
				got := outputs[reader]
				path := filepath.Join(goldenDir, c.name+"."+reader)
				if *updateGolden {
					require.NoError(t, os.WriteFile(path, got, 0o644))
					continue
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("missing golden %s (run with -update to create it): %v", path, err)
				}
				assert.Equal(t, string(want), string(got), "golden mismatch: %s (a change here is a behaviour change; regenerate with -update only deliberately)", path)
			}
		})
	}

	// Every file in the golden dir must belong to a live case, so a renamed or
	// removed case cannot leave a stale golden behind unnoticed.
	entries, err := os.ReadDir(goldenDir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "DIVERGENCES.md" {
			continue
		}
		base, reader, ok := strings.Cut(e.Name(), ".")
		if !ok || !names[base] || !slices.Contains(goldenReaders, reader) {
			path := filepath.Join(goldenDir, e.Name())
			if *updateGolden {
				require.NoError(t, os.Remove(path))
				t.Logf("removed stale golden %s", path)
				continue
			}
			t.Errorf("stale golden file %s has no matching case (run with -update to remove it)", path)
		}
	}
}

// TestGolden_CoversEveryLineType enforces the "one case per switch arm" rule
// mechanically: across all fixtures, every handled line type and both skip
// paths (malformed JSON, oversize line) must appear at least once.
//
// Limitation: the want-list below is a snapshot of TODAY's switch arms. It
// cannot notice a new arm added later (it is not derived from the readers), so
// whoever adds a line type to the Claude decoder (Slice 2/3) must extend this
// list — and add a golden case — in the same change.
func TestGolden_CoversEveryLineType(t *testing.T) {
	seen := map[string]bool{}
	oversize, malformed := false, false
	for _, c := range goldenCases(t) {
		for _, line := range bytes.Split(c.raw, []byte{'\n'}) {
			line = trimEOL(line)
			if len(line) == 0 {
				continue
			}
			if c.lineCap > 0 && len(line) > c.lineCap {
				oversize = true
				continue
			}
			var l jsonlLine
			if err := json.Unmarshal(line, &l); err != nil {
				malformed = true
				continue
			}
			if l.Type == "" {
				var m jsonlMessage
				if len(l.Message) > 0 && json.Unmarshal(l.Message, &m) == nil {
					l.Type = m.Role
				}
			}
			seen[l.Type] = true
			if l.Type == "system" {
				seen["system/"+l.Subtype] = true
			}
		}
	}
	for _, want := range []string{"user", "assistant", "ai-title", "pr-link", "attachment", "system", "system/away_summary"} {
		assert.True(t, seen[want], "no golden case exercises line type %q", want)
	}
	assert.True(t, oversize, "no golden case exercises an oversize line")
	assert.True(t, malformed, "no golden case exercises a malformed JSON line")
}

// --- fixture helpers -------------------------------------------------------

// at formats a fixture timestamp n seconds after 2026-05-01T10:00:00Z.
func at(n int) string {
	return fmt.Sprintf("2026-05-01T10:%02d:%02dZ", n/60, n%60)
}

func textBlock(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func toolUse(id, name string, input any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func toolResult(id string, content any) map[string]any {
	return map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}
}

// resultLine builds a user line carrying tool_result (and/or text) blocks, with
// an optional top-level toolUseResult (Edit/Write structuredPatch — A3).
func resultLine(uuid, ts string, blocks []map[string]any, toolUseResult map[string]any) map[string]any {
	l := map[string]any{
		"type": "user", "uuid": uuid, "timestamp": ts,
		"message": map[string]any{"role": "user", "content": blocks},
	}
	if toolUseResult != nil {
		l["toolUseResult"] = toolUseResult
	}
	return l
}

func structuredPatch(hunks ...map[string]any) map[string]any {
	return map[string]any{"filePath": "/p/config.toml", "structuredPatch": hunks}
}

func hunk(oldStart, oldLines, newStart, newLines int, lines ...string) map[string]any {
	return map[string]any{
		"oldStart": oldStart, "oldLines": oldLines, "newStart": newStart, "newLines": newLines,
		"lines": lines,
	}
}

func numberedLines(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %04d of the tool output\n", i)
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// fakeSecret is a credential-shaped token StripSecrets redacts; the goldens
// freeze that the scanner redacts it and the display readers do not (D22).
const fakeSecret = "sk-ant-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// goldenCases is the fixture table. Names are stable identifiers (they are file
// name prefixes under testdata/golden); every DIVERGENCES.md row names the case
// that pins it.
//
// It takes testing.TB (not *testing.T) so fuzz targets can seed their corpus
// from the same fixtures via a *testing.F (claude_decoder_test.go
// FuzzClaudeDecoder_NeverPanics); jsonlBytes is widened for the same reason.
func goldenCases(t testing.TB) []goldenCase {
	t.Helper()
	patch := hunk(3, 2, 3, 2, " timeout = 30", "-retries = 1", "+retries = 3")
	longPrompt := strings.Repeat("investigate the flaky quokka test in the vault package ", 4) // 224 runes

	sidecarA := jsonlBytes(t,
		userLineAt("sa-u1", "/p", at(20), "Investigate the flaky quokka test"),
		assistantLineAt("sa-a1", "sa-m1", at(21), []map[string]any{textBlock("The quokka test races on the shared port.")}),
	)
	sidecarB := jsonlBytes(t,
		userLineAt("sb-u1", "/p", at(22), "Explore the repo layout"),
		assistantLineAt("sb-a1", "sb-m1", at(23), []map[string]any{
			toolUse("sb-t1", "Bash", map[string]any{"command": "rg --files"}),
		}),
		resultLine("sb-u2", at(24), []map[string]any{toolResult("sb-t1", "a.go\nb.go")}, nil),
	)
	sidecarC := jsonlBytes(t,
		userLineAt("sc-u1", "/p", at(25), "Summarise findings"),
	)

	launchMain := func(t testing.TB) []byte {
		return jsonlBytes(t,
			userLine("u1", "/p", "main", "Delegate the investigation"),
			assistantLine("a1", "m1", []map[string]any{
				textBlock("Delegating."),
				toolUse("t1", "Task", map[string]any{"description": "Investigate flaky test", "prompt": longPrompt}),
				toolUse("t2", "Agent", map[string]any{"subagent_type": "Explore", "prompt": "look around"}),
				toolUse("t3", "Agent", map[string]any{"prompt": longPrompt}),
				toolUse("t4", "Agent", map[string]any{}),
			}),
			resultLine("u2", at(30), []map[string]any{
				toolResult("t1", "agent done: the port is shared"),
				toolResult("t2", "explored"),
			}, nil),
		)
	}

	return []goldenCase{
		{
			name: "plain_user_assistant",
			raw: jsonlBytes(t,
				userLine("u1", "/home/user/proj", "main", "Please fix the timeout bug"),
				assistantLine("a1", "m1", []map[string]any{textBlock("Sure — looking now.")}),
				userLineAt("u2", "/home/user/proj", at(9), "thanks"),
			),
		},
		{
			// Read (excluded), Edit + structuredPatch, Bash (indexed), unknown id,
			// a text block beside a tool_result, a block-array result with an image
			// block, and an empty-bodied result.
			name: "block_user_tool_results",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "Read config, edit it, run tests"),
				assistantLine("a1", "m1", []map[string]any{
					toolUse("t1", "Read", map[string]any{"file_path": "/p/config.toml"}),
					toolUse("t2", "Edit", map[string]any{"file_path": "/p/config.toml", "old_string": "retries = 1", "new_string": "retries = 3"}),
					toolUse("t3", "Bash", map[string]any{"command": "go test ./..."}),
				}),
				resultLine("u2", at(10), []map[string]any{toolResult("t1", "timeout = 30\nretries = 1\n[db]\npath = x")}, nil),
				resultLine("u3", at(11), []map[string]any{toolResult("t2", "The file /p/config.toml has been updated successfully.")}, structuredPatch(patch)),
				resultLine("u4", at(12), []map[string]any{
					textBlock("also note: <system-reminder>hidden</system-reminder> keep going"),
					toolResult("t3", []map[string]any{textBlock("ok  1 passed"), {"type": "image", "source": map[string]any{"type": "base64"}}}),
				}, nil),
				resultLine("u5", at(13), []map[string]any{toolResult("t9", "orphan output with no matching call")}, nil),
				resultLine("u6", at(14), []map[string]any{toolResult("t3", "")}, nil),
			),
		},
		{
			// TUI collapse thresholds: > 20 lines, and a single > 2000-byte line.
			name: "bash_long_result",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "run the long thing"),
				assistantLine("a1", "m1", []map[string]any{
					toolUse("t1", "Bash", map[string]any{"command": "make verbose"}),
					toolUse("t2", "Bash", map[string]any{"command": "cat blob"}),
				}),
				resultLine("u2", at(10), []map[string]any{toolResult("t1", numberedLines(25))}, nil),
				resultLine("u3", at(11), []map[string]any{toolResult("t2", strings.Repeat("x", 2500))}, nil),
			),
		},
		{
			// Scanner head/tail truncation at maxToolResultChars; display unbounded.
			name: "tool_result_head_tail_truncation",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "dump it"),
				assistantLine("a1", "m1", []map[string]any{toolUse("t1", "Bash", map[string]any{"command": "dump"})}),
				resultLine("u2", at(10), []map[string]any{toolResult("t1", numberedLines(700))}, nil),
			),
		},
		{
			// Edit two hunks; Write; Edit without patch; two diff results on one
			// line (first claims the patch); empty body + patch (D18); all-empty hunks.
			name: "edit_variants",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "make the edits"),
				assistantLine("a1", "m1", []map[string]any{
					toolUse("t1", "Edit", map[string]any{"file_path": "/p/config.toml"}),
					toolUse("t2", "Write", map[string]any{"file_path": "/p/new.go", "content": "package p"}),
					toolUse("t3", "Edit", map[string]any{"file_path": "/p/other.go"}),
					toolUse("t4", "Edit", map[string]any{"file_path": "/p/third.go"}),
				}),
				resultLine("u2", at(10), []map[string]any{toolResult("t1", "The file /p/config.toml has been updated successfully.")},
					structuredPatch(patch, hunk(10, 1, 10, 2, " [db]", "+path = y"))),
				resultLine("u3", at(11), []map[string]any{toolResult("t2", "File created successfully at: /p/new.go")},
					map[string]any{"type": "create", "filePath": "/p/new.go", "content": "package p", "structuredPatch": []map[string]any{hunk(1, 0, 1, 1, "+package p")}}),
				resultLine("u4", at(12), []map[string]any{toolResult("t3", "The file /p/other.go has been updated successfully.")},
					map[string]any{"filePath": "/p/other.go"}),
				resultLine("u5", at(13), []map[string]any{
					toolResult("t4", "The file /p/third.go has been updated successfully."),
					toolResult("t1", "The file /p/config.toml has been updated successfully."),
				}, structuredPatch(patch)),
				resultLine("u6", at(14), []map[string]any{toolResult("t4", "")}, structuredPatch(patch)),
				resultLine("u7", at(15), []map[string]any{toolResult("t3", "updated")}, structuredPatch(hunk(1, 1, 1, 1))),
			),
		},
		{
			// Progressive snapshots by message.id, then by uuid when id is absent.
			name: "snapshots",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "look"),
				assistantLineAt("a1", "m1", at(5), []map[string]any{textBlock("Let me look.")}),
				assistantLineAt("a2", "m1", at(6), []map[string]any{textBlock("Let me look."), toolUse("t1", "Bash", map[string]any{"command": "ls"})}),
				assistantLineAt("a3", "m1", at(7), []map[string]any{textBlock("Let me look."), toolUse("t1", "Bash", map[string]any{"command": "ls"}), textBlock("Done.")}),
				assistantLineAt("a4", "", at(8), []map[string]any{textBlock("no id, keyed by uuid")}),
				assistantLineAt("a4", "", at(9), []map[string]any{textBlock("no id, keyed by uuid"), textBlock("second")}),
				resultLine("u2", at(10), []map[string]any{toolResult("t1", "a.go\nb.go")}, nil),
			),
		},
		{
			// Part order is load-bearing: text → tool_use → text in one message.
			name: "text_tool_text",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "check a.go"),
				assistantLine("a1", "m1", []map[string]any{
					textBlock("Before the call."),
					toolUse("t1", "Read", map[string]any{"file_path": "/p/a.go"}),
					textBlock("After the call."),
				}),
				resultLine("u2", at(10), []map[string]any{toolResult("t1", "package p")}, nil),
			),
		},
		{
			name: "ai_title",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "first prompt becomes the fallback"),
				aiTitleLine("First title"),
				assistantLine("a1", "m1", []map[string]any{textBlock("ok")}),
				aiTitleLine("Second title wins"),
				aiTitleLine(""),
			),
		},
		{
			// D1: block-array text, <-prefixed text and queued prompts are not
			// eligible; the first plain string is taken RAW (no cleanText).
			name: "title_fallback_rules",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", []map[string]any{textBlock("block text is not eligible")}),
				userLineAt("u2", "/p", at(1), "<command-name>/clear</command-name>"),
				map[string]any{
					"type": "attachment", "uuid": "at1", "timestamp": at(2),
					"attachment": map[string]any{"type": "queued_command", "prompt": "queued prompts are not eligible", "commandMode": "prompt"},
				},
				userLineAt("u3", "/p", at(3), "  eligible title <system-reminder>hidden reminder</system-reminder> continues  "),
				userLineAt("u4", "/p", at(4), "second eligible, ignored"),
			),
		},
		{
			// Sanitize before truncate; multibyte runes; 120-rune cap.
			name: "title_fallback_long",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "Deploy with token "+fakeSecret+" "+strings.Repeat("ünïcödé ", 20)),
			),
		},
		{
			name: "pr_link",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "open a PR"),
				map[string]any{
					"type": "pr-link", "sessionId": "s", "timestamp": at(60),
					"prUrl": "https://github.com/acme/widgets/pull/42", "prRepository": "acme/widgets", "prNumber": 42,
				},
				map[string]any{"type": "pr-link", "sessionId": "s", "timestamp": at(61), "prUrl": "https://github.com/acme/widgets/pull/43"},
				map[string]any{"type": "pr-link", "sessionId": "s", "timestamp": at(62), "prNumber": 0},
			),
		},
		{
			name: "away_summary",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "start"),
				map[string]any{"type": "system", "subtype": "away_summary", "timestamp": at(3600), "content": "  We configured the dependencies and set up CI.  "},
				map[string]any{"type": "system", "subtype": "turn_duration", "timestamp": at(3601), "durationMs": 1234},
				map[string]any{"type": "system", "subtype": "away_summary", "timestamp": at(3602), "content": "   "},
			),
		},
		{
			// A2: queue-operation lines are ignored, the queued_command attachment
			// is a user turn (cleanText applied), task_reminder yields nothing.
			name: "queued_command",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "Start the work"),
				assistantLine("a1", "m1", []map[string]any{toolUse("t1", "Bash", map[string]any{"command": "ls"})}),
				map[string]any{"type": "queue-operation", "operation": "enqueue", "timestamp": at(6), "content": "the mcp has no access to vault"},
				resultLine("u2", at(7), []map[string]any{toolResult("t1", "file1 file2")}, nil),
				map[string]any{"type": "queue-operation", "operation": "remove", "timestamp": at(8)},
				map[string]any{
					"type": "attachment", "uuid": "at1", "timestamp": at(8),
					"attachment": map[string]any{"type": "queued_command", "prompt": "the mcp has no access to vault <system-reminder>noise</system-reminder>", "commandMode": "prompt"},
				},
				map[string]any{
					"type": "attachment", "uuid": "at2", "timestamp": at(9),
					"attachment": map[string]any{"type": "task_reminder", "content": []any{}, "itemCount": 0},
				},
				assistantLineAt("a2", "m2", at(10), []map[string]any{textBlock("noted")}),
			),
		},
		{
			// D7: generic attachment message content is a scanner-only system row;
			// a queued_command on the same line wins over the message.
			name: "attachment_message",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "attach things"),
				map[string]any{
					"type": "attachment", "uuid": "at1", "timestamp": at(5),
					"message": map[string]any{"role": "user", "content": []map[string]any{
						textBlock("attached note"),
						{"type": "file", "filename": "a.txt", "name": "a.txt", "path": "/p/a.txt"},
					}},
				},
				map[string]any{
					"type": "attachment", "uuid": "at2", "timestamp": at(6),
					"message": map[string]any{"role": "user", "content": "plain attachment text"},
				},
				map[string]any{
					"type": "attachment", "uuid": "at3", "timestamp": at(7),
					"attachment": map[string]any{"type": "queued_command", "prompt": "queued wins", "commandMode": "prompt"},
					"message":    map[string]any{"role": "user", "content": "ignored because queued wins"},
				},
			),
		},
		{
			// Line 1 exceeds the lowered cap: skipped, but line 2 keeps index 2.
			name:    "oversize_line",
			lineCap: 600,
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "start"),
				userLineAt("u2", "/p", at(1), strings.Repeat("oversize ", 200)),
				assistantLineAt("a1", "m1", at(2), []map[string]any{textBlock("after the oversize line")}),
			),
		},
		{
			// Non-JSON, empty, message:null, message as string, non-array assistant
			// content, role inference, unknown type with timestamp, bad block element,
			// CRLF, and a final line without a trailing newline.
			name: "malformed_lines",
			raw: []byte(strings.Join([]string{
				string(bytes.TrimSuffix(jsonlBytes(t, userLine("u1", "/p", "main", "hello")), []byte{'\n'})),
				`not json at all`,
				``,
				`{"type":"assistant","uuid":"a1","timestamp":"` + at(1) + `","message":null}`,
				`{"type":"assistant","uuid":"a2","timestamp":"` + at(2) + `","message":"a string"}`,
				`{"type":"assistant","uuid":"a3","timestamp":"` + at(3) + `","message":{"id":"m3","role":"assistant","content":"not an array"}}`,
				`{"uuid":"a4","timestamp":"` + at(4) + `","message":{"id":"m4","role":"assistant","content":[{"type":"text","text":"inferred assistant"}]}}`,
				`{"type":"progress","timestamp":"` + at(5) + `"}`,
				`{"type":"user","uuid":"u2","timestamp":"` + at(6) + `","message":{"role":"user","content":[{"type":"text","text":"array with bad block"},5]}}`,
				`{"type":"user","uuid":"u3","timestamp":"` + at(7) + `","message":{"role":"user","content":"crlf line"}}` + "\r",
				`{"type":"user","uuid":"u4","timestamp":"` + at(8) + `","message":{"role":"user","content":"trailing line without newline"}}`,
			}, "\n")),
		},
		{
			// D3/D4: CWD/Branch from a message-less user line; timestamps from
			// unhandled types; assistant cwd ignored; ai-title has no timestamp.
			name: "metadata_lines",
			raw: jsonlBytes(t,
				map[string]any{"type": "queue-operation", "operation": "enqueue", "timestamp": at(0)},
				map[string]any{"type": "user", "uuid": "u0", "timestamp": at(1), "cwd": "/from/messageless", "gitBranch": "dev"},
				map[string]any{
					"type": "assistant", "uuid": "a1", "timestamp": at(2), "cwd": "/assistant/cwd", "gitBranch": "ignored",
					"message": map[string]any{"id": "m1", "role": "assistant", "content": []map[string]any{textBlock("hi")}},
				},
				userLineAt("u1", "/second", at(3), "real prompt"),
				map[string]any{"type": "file-history-snapshot", "messageId": "x", "timestamp": at(4), "snapshot": map[string]any{}},
				aiTitleLine("Metadata title"),
				map[string]any{"type": "custom-title", "customTitle": "user renamed", "sessionId": "s"},
				map[string]any{"type": "agent-name", "agentName": "helper", "sessionId": "s"},
				map[string]any{"type": "permission-mode", "permissionMode": "default", "sessionId": "s"},
			),
		},
		{
			// Four launches, four sidecars → openable markers in order. Labels:
			// description › subagent_type › prompt (≤100 runes) › "subagent".
			name:     "subagent_markers_openable",
			raw:      launchMain(t),
			sidecars: map[string][]byte{"aaa": sidecarA, "bbb": sidecarB, "ccc": sidecarC, "ddd": sidecarC},
		},
		{
			// Count mismatch (four launches, one sidecar) → markers stay non-openable.
			name:     "subagent_markers_mismatch",
			raw:      launchMain(t),
			sidecars: map[string][]byte{"aaa": sidecarA},
		},
		{
			name: "thinking_only",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "think about it"),
				assistantLine("a1", "m1", []map[string]any{{"type": "thinking", "thinking": "private reasoning", "signature": "sig"}}),
				assistantLineAt("a2", "m2", at(6), []map[string]any{
					{"type": "thinking", "thinking": "more reasoning", "signature": "sig"},
					textBlock("visible answer"),
				}),
			),
		},
		{
			// toolUseSummary fall-through: priority keys, case-collision, nested
			// object/array placeholders, redaction, +N omitted marker, long key,
			// non-object input → bare name, WebFetch.
			name: "mcp_generic_summary",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "search the knowledge base"),
				assistantLine("a1", "m1", []map[string]any{
					toolUse("t1", "mcp__capy__capy_search", map[string]any{
						"queries": []string{"tool input", "second"}, "limit": 3, "source": "kk:",
						"nested": map[string]any{"x": 1}, "arr": []any{map[string]any{"a": 1}, 2, "s"},
						"api_key": fakeSecret, "Query": "case collision", "zeta": true,
						"a_very_long_key_name_that_exceeds_forty_characters_easily": "v",
					}),
					toolUse("t2", "WebFetch", map[string]any{"url": "https://example.com/doc", "prompt": "summarise"}),
					toolUse("t3", "mcp__x__y", "not an object"),
					toolUse("t4", "ToolSearch", map[string]any{}),
				}),
				resultLine("u2", at(10), []map[string]any{
					toolResult("t1", "3 results"),
					toolResult("t2", "fetched 12 KB"),
					toolResult("t3", "bare"),
					toolResult("t4", "no tools"),
				}, nil),
			),
		},
		{
			// D22: the scanner redacts; show/TUI render verbatim.
			name: "secrets",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "my token is "+fakeSecret+" please use it"),
				assistantLine("a1", "m1", []map[string]any{
					textBlock("Using " + fakeSecret + " now."),
					toolUse("t1", "Bash", map[string]any{"command": "export TOKEN=" + fakeSecret}),
				}),
				resultLine("u2", at(10), []map[string]any{toolResult("t1", "exported "+fakeSecret)}, nil),
			),
		},
		{
			// cleanText: every noise tag, in plain-string and text-block form.
			name: "noise_tags",
			raw: jsonlBytes(t,
				userLine("u1", "/p", "main", "<system-reminder>ignore</system-reminder>real prompt <command-name>/x</command-name><command-message>m</command-message><command-args>a</command-args> tail"),
				userLineAt("u2", "/p", at(1), []map[string]any{
					textBlock("<local-command-caveat>c</local-command-caveat>block prompt<local-command-stdout>out</local-command-stdout>"),
					textBlock("<system-reminder>only noise</system-reminder>"),
				}),
				userLineAt("u3", "/p", at(2), "<system-reminder>entirely noise</system-reminder>"),
			),
		},
		{
			name: "user_empty_content",
			raw: jsonlBytes(t,
				map[string]any{"type": "user", "uuid": "u1", "timestamp": at(0), "message": map[string]any{"role": "user"}},
				userLineAt("u2", "/p", at(1), ""),
				userLineAt("u3", "/p", at(2), []map[string]any{}),
				userLineAt("u4", "/p", at(3), "   "),
			),
		},
		{
			name: "empty",
			raw:  []byte{},
		},
	}
}
