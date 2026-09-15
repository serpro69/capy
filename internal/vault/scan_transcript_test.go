package vault

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scan_transcript_test.go pins the SCANNER-side policies ScanTranscript applies
// over a hand-built Transcript — the consumer boundary that Slice 3 moved out of
// the Claude reader loop. These tests are decoder-independent on purpose: the
// entries are constructed directly, so they hold for any platform's decoder
// (the Codex decoder in Slice 6 reuses this consumer unchanged). Claude
// end-to-end behaviour is pinned by golden_test.go and scanner_test.go.

var (
	scanTestT0 = time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	scanTestT1 = scanTestT0.Add(5 * time.Second)
)

func callPart(id, name, summary string) Part {
	return Part{Call: &ToolCall{ID: id, Name: name, Summary: summary}}
}

func TestScanTranscript_CopiesMetaIntoScanOutput(t *testing.T) {
	tr := &Transcript{Meta: Meta{
		Platform:   PlatformCodex,
		PlatformID: "0199c0ff-ee00-7000-8000-000000000001",
		CWD:        "/home/u/proj",
		Branch:     "feat/x",
		StartTime:  scanTestT0,
		EndTime:    scanTestT1,
		ParentUUID: "0199c0ff-ee00-7000-8000-000000000000",
		Source:     "subagent",
	}}

	out := ScanTranscript(tr)

	assert.Equal(t, PlatformCodex, out.Platform)
	assert.Equal(t, tr.Meta.PlatformID, out.PlatformID)
	assert.Equal(t, "/home/u/proj", out.CWD)
	assert.Equal(t, "feat/x", out.Branch)
	assert.Equal(t, scanTestT0, out.StartTime)
	assert.Equal(t, scanTestT1, out.EndTime)
	assert.Equal(t, tr.Meta.ParentUUID, out.ParentUUID)
	assert.Equal(t, "subagent", out.Source)
	assert.Empty(t, out.Results)
	assert.Equal(t, 0, out.MessageCount)
	assert.Empty(t, out.Title)
}

func TestScanTranscript_TurnsRolesAndPolicies(t *testing.T) {
	longBody := strings.Repeat("x", maxToolResultChars+100)
	tr := &Transcript{
		Meta: Meta{Platform: PlatformClaudeCode},
		Entries: []Entry{
			// line 0: human → turn 0, msg 0
			{Kind: EntryHuman, LineIndex: 0, Timestamp: scanTestT0, Text: "fix the timeout"},
			// line 1: assistant with text → call → text in part order
			{Kind: EntryAssistant, LineIndex: 1, Timestamp: scanTestT1, Parts: []Part{
				{Text: "Looking."},
				callPart("t1", "Read", "Read /proj/config.toml"),
				callPart("t2", "Bash", "Bash go test ./..."),
				{Text: "Then I will edit."},
				callPart("t3", "Edit", "Edit /proj/main.go"),
				callPart("t4", "", ""), // nameless call with no summary contributes nothing
			}},
			// line 2: results — same turn as the human above
			{Kind: EntryToolResult, LineIndex: 2, CallID: "t1", CallName: "Read", CallSummary: "Read /proj/config.toml", Body: "timeout = 30"},
			{Kind: EntryToolResult, LineIndex: 2, CallID: "t2", CallName: "Bash", CallSummary: "Bash go test ./...", Body: "ok\tpkg 0.1s"},
			{Kind: EntryToolResult, LineIndex: 2, CallID: "t3", CallName: "Edit", CallSummary: "Edit /proj/main.go", Body: "", Diff: &Diff{Text: "@@ -1 +1 @@\n-a\n+b", Added: 1, Removed: 1}},
			{Kind: EntryToolResult, LineIndex: 2, CallID: "zz", Body: "unmatched call output"},
			{Kind: EntryToolResult, LineIndex: 2, CallID: "t2", CallName: "Bash", CallSummary: "Bash long", Body: longBody},
			// line 3: tool-only assistant → still an assistant row
			{Kind: EntryAssistant, LineIndex: 3, Parts: []Part{callPart("t5", "Grep", "Grep TODO")}},
			// line 4: system entries — SearchOnly is indexed by the scanner
			{Kind: EntrySystem, LineIndex: 4, Text: "PR #7 o/r https://x/pull/7"},
			{Kind: EntrySystem, LineIndex: 4, Text: "attached notes.md", SearchOnly: true},
			// line 5: second human → turn 1, msg 0
			{Kind: EntryHuman, LineIndex: 5, Text: "  my key is sk-ant-abcdefghijklmnopqrstuvwxyz0123 keep it  ", Queued: true},
		},
	}

	out := ScanTranscript(tr)

	// Row-by-row shape: (turn, msg, line, role).
	type row struct {
		turn, msg, line int
		role            string
	}
	var got []row
	for _, r := range out.Results {
		got = append(got, row{r.TurnIndex, r.MessageIndex, r.LineIndex, r.Role})
	}
	assert.Equal(t, []row{
		{0, 0, 0, roleUser},
		{0, 1, 1, roleAssistant},
		{0, 2, 2, roleTool}, // Bash result (Read excluded, Edit empty-body skipped)
		{0, 3, 2, roleTool}, // unmatched id
		{0, 4, 2, roleTool}, // long Bash result
		{0, 5, 3, roleAssistant},
		{0, 6, 4, roleSystem},
		{0, 7, 4, roleSystem}, // SearchOnly indexed
		{1, 0, 5, roleUser},
	}, got)

	// Assistant row text is the parts in order, calls by Summary, empty summary dropped.
	assert.Equal(t, "Looking.\nRead /proj/config.toml\nBash go test ./...\nThen I will edit.\nEdit /proj/main.go", out.Results[1].ContentText)
	assert.Equal(t, []string{"Read", "Bash", "Edit"}, out.Results[1].ToolNames, "nameless call is not a tool name")
	assert.Equal(t, scanTestT1, out.Results[1].Timestamp)

	// Tool result policy: excluded body absent, prefix applied, unmatched unprefixed.
	all := strings.Join(collectContent(out.Results), "\n")
	assert.NotContains(t, all, "timeout = 30", "Read body is FTS-excluded")
	assert.Equal(t, "Bash go test ./...\nok\tpkg 0.1s", out.Results[2].ContentText)
	assert.Equal(t, "unmatched call output", out.Results[3].ContentText)

	// Truncation happens AFTER prefixing, so the label survives in the head.
	long := out.Results[4].ContentText
	assert.True(t, strings.HasPrefix(long, "Bash long\n"), "prefix survives truncation")
	assert.Contains(t, long, "…")
	assert.LessOrEqual(t, len([]rune(long)), maxToolResultChars+1, "bounded to the cap plus the ellipsis")

	// Tool-only assistant is still an assistant row.
	assert.Equal(t, "Grep TODO", out.Results[5].ContentText)
	assert.Equal(t, []string{"Grep"}, out.Results[5].ToolNames)

	// System rows, SearchOnly included.
	assert.Equal(t, "PR #7 o/r https://x/pull/7", out.Results[6].ContentText)
	assert.Equal(t, "attached notes.md", out.Results[7].ContentText)

	// Second human: trimmed, sanitized, new turn. Queued has no scanner effect.
	assert.NotContains(t, out.Results[8].ContentText, "sk-ant-abcdefghijklmnopqrstuvwxyz0123")
	assert.Contains(t, out.Results[8].ContentText, "[REDACTED_SECRET]")
	assert.True(t, strings.HasPrefix(out.Results[8].ContentText, "my key"), "TrimSpace'd")

	// user + assistant rows only.
	assert.Equal(t, 4, out.MessageCount)
}

func TestScanTranscript_ToolResultBeforeAnyHumanContinuesTurnZero(t *testing.T) {
	// A transcript that opens with results (or a tool-only assistant) must not
	// bump the turn: only a Human entry after something was emitted does.
	tr := &Transcript{Entries: []Entry{
		{Kind: EntryToolResult, LineIndex: 0, CallName: "Bash", CallSummary: "Bash ls", Body: "a b"},
		{Kind: EntryHuman, LineIndex: 1, Text: "first prompt"},
		{Kind: EntryHuman, LineIndex: 2, Text: "second prompt"},
	}}
	out := ScanTranscript(tr)
	require.Len(t, out.Results, 3)
	assert.Equal(t, 0, out.Results[0].TurnIndex)
	assert.Equal(t, 1, out.Results[1].TurnIndex, "human after an emitted row starts a new turn")
	assert.Equal(t, 0, out.Results[1].MessageIndex)
	assert.Equal(t, 2, out.Results[2].TurnIndex)
}

func TestScanTranscript_EmptyAfterSanitizeIsDropped(t *testing.T) {
	// The decoder guarantees non-empty text, but the scanner's own policy may
	// empty it (whitespace-only after TrimSpace). Such rows are not emitted and
	// do not consume a message index.
	tr := &Transcript{Entries: []Entry{
		{Kind: EntryHuman, LineIndex: 0, Text: "   "},
		{Kind: EntryAssistant, LineIndex: 1, Parts: []Part{callPart("t1", "X", "")}},
		{Kind: EntrySystem, LineIndex: 2, Text: "\t"},
		{Kind: EntryHuman, LineIndex: 3, Text: "real"},
	}}
	out := ScanTranscript(tr)
	require.Len(t, out.Results, 1)
	assert.Equal(t, "real", out.Results[0].ContentText)
	assert.Equal(t, 0, out.Results[0].TurnIndex, "nothing was emitted before, so no turn bump")
	assert.Equal(t, 0, out.Results[0].MessageIndex)
	assert.Equal(t, 1, out.MessageCount)
}

func TestScanTranscript_UnknownKindWarnsAndSkips(t *testing.T) {
	// Fail loud: a Kind the consumer does not know (a decoder bug, incl. the
	// EntryUnknown zero value) is skipped WITH a warning, never silently, and
	// the surrounding rows are unaffected.
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tr := &Transcript{Meta: Meta{Platform: PlatformClaudeCode}, Entries: []Entry{
		{Kind: EntryHuman, LineIndex: 0, Text: "hi"},
		{LineIndex: 1, Text: "forgotten kind"}, // zero Kind
		{Kind: EntryKind(99), LineIndex: 2, Text: "future kind"},
		{Kind: EntryAssistant, LineIndex: 3, Parts: []Part{{Text: "hello"}}},
	}}
	out := ScanTranscript(tr)

	require.Len(t, out.Results, 2)
	assert.Equal(t, roleUser, out.Results[0].Role)
	assert.Equal(t, roleAssistant, out.Results[1].Role)
	assert.Equal(t, 1, out.Results[1].MessageIndex, "skipped entries consume no message index")
	assert.Equal(t, 2, out.MessageCount)
	assert.Len(t, h.messagesWithPrefix("vault scanner: skipping transcript entry of unknown kind"), 2)
}

func TestScanTranscript_Title(t *testing.T) {
	secret := "sk-ant-abcdefghijklmnopqrstuvwxyz0123"

	t.Run("explicit title wins and is sanitized, not truncated", func(t *testing.T) {
		long := strings.Repeat("t", titleMaxChars+50)
		out := ScanTranscript(&Transcript{Meta: Meta{ExplicitTitle: long + " " + secret, TitleFallback: "fallback"}})
		assert.True(t, strings.HasPrefix(out.Title, long), "explicit title is not truncated")
		assert.NotContains(t, out.Title, secret)
		assert.Contains(t, out.Title, "[REDACTED_SECRET]")
	})

	t.Run("fallback is sanitized then truncated", func(t *testing.T) {
		// Put the secret past the truncation point: sanitize-before-truncate
		// means the placeholder still lands in the (bounded) title's rune budget
		// or is cut cleanly — either way no fragment of the secret survives.
		pad := strings.Repeat("p", titleMaxChars-10)
		out := ScanTranscript(&Transcript{Meta: Meta{TitleFallback: pad + " " + secret + " tail"}})
		assert.LessOrEqual(t, len([]rune(out.Title)), titleMaxChars+1)
		assert.True(t, strings.HasSuffix(out.Title, "…"))
		assert.NotContains(t, out.Title, "sk-ant-")
	})

	t.Run("short fallback kept verbatim", func(t *testing.T) {
		out := ScanTranscript(&Transcript{Meta: Meta{TitleFallback: "fix the build"}})
		assert.Equal(t, "fix the build", out.Title)
	})
}

func TestScanSession_DispatchesOnPlatform(t *testing.T) {
	body := `{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","cwd":"/p","message":{"role":"user","content":"hello"}}` + "\n"

	t.Run("claude", func(t *testing.T) {
		out, err := ScanSession(PlatformClaudeCode, strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, PlatformClaudeCode, out.Platform)
		assert.Empty(t, out.PlatformID, "Claude does not extract a platform id")
		assert.Empty(t, out.ParentUUID)
		assert.Empty(t, out.Source)
		assert.Equal(t, "/p", out.CWD)
		assert.Equal(t, 1, out.MessageCount)
	})

	t.Run("codex dispatches to the codex decoder", func(t *testing.T) {
		// Claude lines are unknown envelope types to the Codex decoder: skipped,
		// never an error, and never scanned as Claude.
		out, err := ScanSession(PlatformCodex, strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, PlatformCodex, out.Platform)
		assert.Equal(t, 0, out.MessageCount)
		assert.Empty(t, out.CWD)
	})

	t.Run("unknown platform fails loudly, never defaults to Claude", func(t *testing.T) {
		out, err := ScanSession(Platform("bogus"), strings.NewReader(body))
		require.Error(t, err)
		assert.Nil(t, out)
		assert.True(t, errors.Is(err, ErrUnknownPlatform), "got %v", err)
	})
}

// collectContent returns every result's ContentText, in order.
func collectContent(rs []ScanResult) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ContentText)
	}
	return out
}
