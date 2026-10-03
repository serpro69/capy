package vault

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexFileChangePaginated_GroupedUpdates(t *testing.T) {
	fixture := codexEditCompatibilityCases(t)[0]
	tr := decodeCodex(t, fixture)
	assertPayloadIsolation(t, fixture.name, tr)
	var change Entry
	for _, entry := range tr.Entries {
		if entry.Kind == EntryFileChange {
			require.Nil(t, change.FileChange, "one patch produces one entry")
			change = entry
		}
	}
	require.NotNil(t, change.FileChange)
	assert.Equal(t, `F@4 "exec-patch-four" completed files=4 available=true`, describeEntry(change))
	assert.Equal(t, parseJSONLTime(at(4)), change.Timestamp)
	assert.Equal(t, "/tmp/edit-demo", tr.Meta.CWD)
	assert.Len(t, tr.Entries[1].Parts, 3, "the post-event call stays attached to the assistant")
	msgs := transcriptMessages(tr, nil)
	var groups []TranscriptMessage
	for _, msg := range msgs {
		if msg.Heading != "" {
			groups = append(groups, msg)
		}
	}
	require.Len(t, groups, 1)
	m := groups[0]
	assert.Equal(t, "4 files changed (+23 −14)", m.ToolSummary)
	assert.Equal(t, "File changes · completed", m.Heading)
	assert.Equal(t, 4, m.SourceLine)
	assert.True(t, m.Collapsed && m.Diff)
	assert.Equal(t, RoleTool, m.Role)
	assert.Contains(t, m.Body, "*** Update File: file-1.txt (+5 −2)")
	assert.Contains(t, m.Body, "*** Update File: file-4.txt (+6 −2)")
	assert.Less(t, strings.Index(m.Body, "file-1.txt"), strings.Index(m.Body, "file-4.txt"))
	assert.NotContains(t, m.Body, "/tmp/edit-demo/")
	assert.Contains(t, m.Body, "@@ -1,8 +1,10 @@")
	assert.Equal(t, "/tmp/edit-demo/file-1.txt", change.FileChange.Files[0].Path)
}

func TestCodexFileChangePaginated_Status(t *testing.T) {
	for _, tc := range []struct {
		name, raw, state, summary string
	}{
		{"completed", `"completed"`, "completed", "1 file changed (+1 −1)"},
		{"failed", `"failed"`, "failed", "Patch failed"},
		{"declined", `"declined"`, "declined", "Patch declined"},
		{"missing", "", "unconfirmed", "Patch unconfirmed"},
		{"null", "null", "unconfirmed", "Patch unconfirmed"},
		{"future", `"pending"`, "unconfirmed", "Patch unconfirmed"},
		{"wrong type", "17", "unconfirmed", "Patch unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := map[string]any{"type": "FileChange", "id": "nested-edit",
				"changes": map[string]any{"/p/a": map[string]any{"type": "update", "unified_diff": "@@ -1 +1 @@\n-old\n+new\n"}},
				"stdout":  " exact output\r\n", "stderr": " diagnostic\n"}
			if tc.raw != "" {
				item["status"] = json.RawMessage(tc.raw)
			}
			raw := codexRollout(t, codexPaginated, codexItemCompletedLine(at(3), item))
			tr := decodeCodex(t, codexCase{raw: raw})
			require.Len(t, tr.Entries, 1)
			s := tr.Entries[0].FileChange
			require.NotNil(t, s)
			assert.Equal(t, FileChangeState(tc.state), s.State)
			assert.Equal(t, " exact output\r\n", s.Stdout)
			m := transcriptMessages(tr, nil)[0]
			assert.Equal(t, tc.summary, m.ToolSummary)
			assert.Equal(t, "File changes · "+tc.state, m.Heading)
			assert.Contains(t, m.Body, "stdout:\n exact output\r\n")
			assert.Contains(t, m.Body, "stderr:\n diagnostic\n")
			if tc.state == "completed" {
				assert.Contains(t, m.Body, "+new")
			} else {
				assert.Contains(t, m.Body, "*** Update File: /p/a (reported)")
				assert.NotContains(t, m.Body, "@@")
				assert.NotContains(t, m.Body, "+new")
				assert.False(t, m.Diff)
			}
		})
	}
}

func TestCodexFileChangePaginated_Unavailable(t *testing.T) {
	for _, tc := range []struct {
		name, changes, summary, body string
		count                        int
		available                    bool
	}{
		{"empty", `{}`, "Patch completed · no file changes recorded", "Patch reported completed", 0, true},
		{"missing", ``, "Patch completed · diff unavailable", "changes data is unavailable", 0, false},
		{"null", `null`, "Patch completed · diff unavailable", "changes data is unavailable", 0, false},
		{"array", `[]`, "Patch completed · diff unavailable", "changes data is unavailable", 0, false},
		{"bad file", `{"a":null}`, "1 file changed · diff incomplete", "*** File: a (diff unavailable)", 1, true},
		{"unsupported", `{"a":{"type":"add","content":""}}`, "1 file changed · diff incomplete", "*** Add File: a (diff unavailable)", 1, true},
		{"unknown", `{"a":{"type":"future"}}`, "1 file changed · diff incomplete", "*** File: a (diff unavailable)", 1, true},
		{"missing diff", `{"a":{"type":"update"}}`, "1 file changed · diff incomplete", "diff is missing", 1, true},
		{"bad diff", `{"a":{"type":"update","unified_diff":"@@ -1 +1 @@\n-old"}}`, "1 file changed · diff incomplete", "inconsistent line counts", 1, true},
		{"partial", `{"z":{"type":"delete","content":"old"},"a":{"type":"update","unified_diff":"@@ -1 +1 @@\n-old\n+new\n"}}`, "2 files changed · diff incomplete", "*** Update File: a (+1 −1)", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := codexPaginatedFileChange(codexTurnItem{Status: json.RawMessage(`"completed"`), Changes: json.RawMessage(tc.changes)})
			assert.Equal(t, tc.available, s.ChangesAvailable)
			assert.Len(t, s.Files, tc.count)
			m := viewerFileChangeMessage(Entry{FileChange: s}, "/p")
			assert.Equal(t, tc.summary, m.ToolSummary)
			assert.Contains(t, m.Body, tc.body)
		})
	}
}

func TestCodexFileChangeDiff(t *testing.T) {
	for _, tc := range []struct {
		name, text     string
		added, removed int
		valid          bool
	}{
		{"several hunks", "@@ -2,2 +2,3 @@\n context\n-old\n+new\n+extra\n@@ -9 +10 @@ hint\n-last\n+next\n", 3, 2, true},
		{"file headers", "--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n", 1, 1, true},
		{"no newline", "@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file", 1, 1, true},
		{"CRLF", "@@ -1 +1 @@\r\n-old\r\n+new\r\n", 1, 1, true},
		{"blank context", "@@ -1,2 +1,2 @@\n \n-old\n+new\n", 1, 1, true},
		{"unprefixed blank context", "@@ -1,2 +1,2 @@\n\n-old\n+new\n", 0, 0, false},
		{"zero range", "@@ -0,0 +1 @@\n+first\n", 1, 0, true},
		{"zero counts", "@@ -0,0 +0,0 @@\n", 0, 0, true},
		{"header-like content", "@@ -1 +1 @@\n--- old\n+++ new\n", 1, 1, true},
		{"missing", "", 0, 0, false},
		{"short old", "@@ -1,2 +1 @@\n-old\n+new\n", 0, 0, false},
		{"long new", "@@ -1 +1 @@\n-old\n+new\n+extra\n", 0, 0, false},
		{"bad prefix", "@@ -1 +1 @@\nold\n+new", 0, 0, false},
		{"missing range", "@@\n-old\n+new", 0, 0, false},
		{"overflow", "@@ -999999999999999999999999999 +1 @@\n-old\n+new", 0, 0, false},
		{"bad zero", "@@ -0 +1 @@\n-old\n+new", 0, 0, false},
		{"unpaired header", "--- a/file\n@@ -1 +1 @@\n-old\n+new", 0, 0, false},
		{"orphan annotation", "@@ -1 +1 @@\n\\ No newline at end of file\n-old\n+new", 0, 0, false},
		{"trailing junk", "@@ -1 +1 @@\n-old\n+new\ntrailing", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diff, reason := codexUpdateDiff(tc.text)
			if !tc.valid {
				assert.Nil(t, diff)
				assert.NotEmpty(t, reason)
				return
			}
			require.NotNil(t, diff)
			assert.Empty(t, reason)
			assert.Equal(t, tc.added, diff.Added)
			assert.Equal(t, tc.removed, diff.Removed)
			assert.Equal(t, tc.text[strings.Index(tc.text, "@@"):], diff.Text)
		})
	}
}

func TestCodexFileChangeDisplayPath(t *testing.T) {
	for _, tc := range []struct{ name, original, cwd, want string }{
		{"descendant", "/p/dir/a", "/p", "dir/a"},
		{"cwd slash", "/p/a", "/p/", "a"},
		{"root", "/p/a", "/", "p/a"},
		{"lexical", "/p/dir/../a", "/p/.", "a"},
		{"outside", "/p/../a", "/p", "/p/../a"},
		{"prefix collision", "/project/a", "/p", "/project/a"},
		{"relative", "a", "/p", "a"},
		{"relative cwd", "/p/a", "p", "/p/a"},
		{"windows", `C:\p\a`, `C:\p`, `C:\p\a`},
		{"UNC", "//host/p/a", "//host/p", "//host/p/a"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, fileChangeDisplayPath(tc.original, tc.cwd)) })
	}
}

func TestCodexFileChangePaginated_AnchorsAndWarnings(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	raw := codexRollout(t, codexPaginated,
		codexFileChangeEvent(at(3), "id", "completed", map[string]any{"a": map[string]any{
			"type": "update", "unified_diff": "@@ -1 +1 @@\n-old\n+new\n"}}))
	prefix := "{bad json}\n" + strings.Repeat("x", 600) + "\n\n"
	tr, err := (codexDecoder{lineCap: 500}).Decode(withSource("synthetic", strings.NewReader(prefix+string(raw))))
	require.NoError(t, err)
	require.Len(t, tr.Entries, 1)
	assert.Equal(t, 3, tr.Entries[0].LineIndex)
	logs.Reset()
	ScanTranscript(tr)
	displayMessages(tr)
	transcriptMessages(tr, nil)
	assert.Empty(t, logs.String(), "all consumers explicitly recognize the new kind")
	s := codexPaginatedFileChange(codexTurnItem{Status: json.RawMessage(`"private-status"`),
		Changes: json.RawMessage(`{"/private/path":{"type":"update","unified_diff":"private-patch"}}`),
		Stderr:  json.RawMessage(`"private-error"`)})
	codexLogFileChange(slog.Default(), s, 7)
	assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"))
	assert.Contains(t, logs.String(), "line=7")
	assert.NotContains(t, logs.String(), "private")
}
