package vault

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strconv"
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
		{"empty file", `{"a":{"type":"add","content":""}}`, "1 file changed (+0 −0)", "*** Add File: a (+0 −0)", 1, true},
		{"unknown", `{"a":{"type":"future"}}`, "1 file changed · diff incomplete", "*** File: a (diff unavailable)", 1, true},
		{"missing diff", `{"a":{"type":"update"}}`, "1 file changed · diff incomplete", "diff is missing", 1, true},
		{"bad diff", `{"a":{"type":"update","unified_diff":"@@ -1 +1 @@\n-old"}}`, "1 file changed · diff incomplete", "inconsistent line counts", 1, true},
		{"partial", `{"z":{"type":"delete"},"a":{"type":"update","unified_diff":"@@ -1 +1 @@\n-old\n+new\n"}}`, "2 files changed · diff incomplete", "*** Update File: a (+1 −1)", 2, true},
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

func TestCodexFileChangeDiff_Content(t *testing.T) {
	for _, operation := range []string{"add", "delete"} {
		t.Run(operation, func(t *testing.T) {
			for _, tc := range []struct {
				name, content, lines string
				count                int
			}{
				{"empty", "", "", 0},
				{"blank", "\n", "\n", 1},
				{"terminated", "one\ntwo\n", "one\ntwo\n", 2},
				{"unterminated", "one\ntwo", "one\ntwo\n\\ No newline at end of file\n", 2},
				{"CRLF", "one\r\n\r\n", "one\r\n\r\n", 2},
				{"bare CR", "one\r", "one\r\n\\ No newline at end of file\n", 1},
				{"trailing blank", "one\n\n", "one\n\n", 2},
			} {
				t.Run(tc.name, func(t *testing.T) {
					raw := marshalGolden(t, map[string]any{"type": operation, "content": tc.content})
					f := codexNormalizeFileChange("/p/a", raw)
					require.NotNil(t, f.Diff)
					assert.Empty(t, f.Diagnostics)
					assert.Equal(t, tc.content, f.Content)
					prefix := "-"
					header := "@@ -1," + strconv.Itoa(tc.count) + " +0,0 @@\n"
					if operation == "add" {
						prefix = "+"
						header = "@@ -0,0 +1," + strconv.Itoa(tc.count) + " @@\n"
						assert.Equal(t, tc.count, f.Diff.Added)
						assert.Zero(t, f.Diff.Removed)
					} else {
						assert.Equal(t, tc.count, f.Diff.Removed)
						assert.Zero(t, f.Diff.Added)
					}
					if tc.count == 0 {
						assert.Empty(t, f.Diff.Text)
						return
					}
					lines := strings.Split(strings.TrimSuffix(tc.lines, "\n"), "\n")
					for i := range lines {
						if i < tc.count {
							lines[i] = prefix + lines[i]
						}
					}
					assert.Equal(t, header+strings.Join(lines, "\n")+"\n", f.Diff.Text)
				})
			}
			for _, raw := range []string{"", "null", "17", `{"text":"bad"}`} {
				t.Run("invalid content "+raw, func(t *testing.T) {
					wire := map[string]any{"type": operation}
					if raw != "" {
						wire["content"] = json.RawMessage(raw)
					}
					f := codexNormalizeFileChange("a", marshalGolden(t, wire))
					assert.Nil(t, f.Diff)
					require.Len(t, f.Diagnostics, 1)
					assert.Equal(t, "content", f.Diagnostics[0].Code)
				})
			}
		})
	}
}

func TestCodexFileChangeDiff_Move(t *testing.T) {
	for _, tc := range []struct {
		name, raw, destination string
		valid                  bool
	}{
		{"absent", "", "", true},
		{"null", "null", "", true},
		{"destination", `"/p/new"`, "/p/new", true},
		{"outside", `"/elsewhere/new"`, "/elsewhere/new", true},
		{"empty", `""`, "", false},
		{"number", "42", "", false},
		{"object", `{}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := map[string]any{"type": "update", "unified_diff": "@@ -1 +1 @@\n-before\n+after\n"}
			if tc.raw != "" {
				wire["move_path"] = json.RawMessage(tc.raw)
			}
			f := codexNormalizeFileChange("/p/old", marshalGolden(t, wire))
			assert.Equal(t, tc.destination, f.MovePath)
			if !tc.valid {
				assert.Nil(t, f.Diff)
				require.Len(t, f.Diagnostics, 1)
				assert.Equal(t, "destination", f.Diagnostics[0].Code)
				return
			}
			require.NotNil(t, f.Diff)
			assert.Equal(t, 1, f.Diff.Added)
			assert.Equal(t, 1, f.Diff.Removed)
			s := &FileChangeSet{State: FileChangeCompleted, ChangesAvailable: true, Files: []FileChange{f}}
			m := viewerFileChangeMessage(Entry{FileChange: s}, "/p")
			assert.Equal(t, "1 file changed (+1 −1)", m.ToolSummary)
			header := "*** Update File: old (+1 −1)"
			if tc.destination != "" {
				header = "*** Move File: old → " + strings.TrimPrefix(tc.destination, "/p/") + " (+1 −1)"
			}
			assert.Contains(t, m.Body, header)
		})
	}
}

func TestCodexFileChangeDiff_PureMove(t *testing.T) {
	for _, destination := range []string{"", "/p/new"} {
		t.Run("destination="+destination, func(t *testing.T) {
			for _, tc := range []struct {
				name, diff string
				valid      bool
			}{
				{"explicitly empty", `""`, true},
				{"missing", "", false},
				{"null", "null", false},
				{"wrong type", "17", false},
				{"whitespace", `"\n"`, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					wire := map[string]any{"type": "update"}
					if destination != "" {
						wire["move_path"] = destination
					}
					if tc.diff != "" {
						wire["unified_diff"] = json.RawMessage(tc.diff)
					}
					f := codexNormalizeFileChange("/p/old", marshalGolden(t, wire))
					if !tc.valid {
						assert.Nil(t, f.Diff)
						assert.NotEmpty(t, f.Diagnostics)
						return
					}
					require.NotNil(t, f.Diff)
					assert.Equal(t, &Diff{}, f.Diff)
					s := &FileChangeSet{State: FileChangeCompleted, ChangesAvailable: true, Files: []FileChange{f}}
					m := viewerFileChangeMessage(Entry{FileChange: s}, "/p")
					assert.Equal(t, "1 file changed (+0 −0)", m.ToolSummary)
					if destination == "" {
						assert.Contains(t, m.Body, "*** Update File: old (+0 −0)")
					} else {
						assert.Contains(t, m.Body, "*** Move File: old → new (+0 −0)")
					}
					assert.NotContains(t, m.Body, "@@")
				})
			}
		})
	}
}

func TestCodexFileChangeIdentity_Equality(t *testing.T) {
	makeSet := func() *FileChangeSet {
		return &FileChangeSet{ID: "id", State: FileChangeCompleted, ChangesAvailable: true,
			Stdout: "output\r\n", Stderr: "error\n",
			Files: []FileChange{{Path: "a", Kind: "update", MovePath: "b", Content: "recorded\r\n",
				Diff: &Diff{Text: "rendered", Added: 1}, Diagnostics: []FileChangeDiagnostic{{Code: "hunk", Field: "unified_diff", Value: "bad"}}}},
			Diagnostics: []FileChangeDiagnostic{{Code: "status", Field: "status", Value: "unknown"}},
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*FileChangeSet)
		equal  bool
	}{
		{"state", func(s *FileChangeSet) { s.State = FileChangeFailed }, false},
		{"availability", func(s *FileChangeSet) { s.ChangesAvailable = false }, false},
		{"stdout", func(s *FileChangeSet) { s.Stdout = "output\n" }, false},
		{"stderr", func(s *FileChangeSet) { s.Stderr = "error" }, false},
		{"file count", func(s *FileChangeSet) { s.Files = nil }, false},
		{"path", func(s *FileChangeSet) { s.Files[0].Path = "other" }, false},
		{"operation", func(s *FileChangeSet) { s.Files[0].Kind = "add" }, false},
		{"destination", func(s *FileChangeSet) { s.Files[0].MovePath = "c" }, false},
		{"content bytes", func(s *FileChangeSet) { s.Files[0].Content = "recorded\n" }, false},
		{"reason code", func(s *FileChangeSet) { s.Diagnostics[0].Code = "output" }, false},
		{"reason field", func(s *FileChangeSet) { s.Diagnostics[0].Field = "stderr" }, false},
		{"reason value", func(s *FileChangeSet) { s.Diagnostics[0].Value = "other" }, false},
		{"reason count", func(s *FileChangeSet) { s.Diagnostics = nil }, false},
		{"file reason", func(s *FileChangeSet) { s.Files[0].Diagnostics[0].Value = "other" }, false},
		{"derived diff", func(s *FileChangeSet) { s.Files[0].Diff = nil }, true},
		{"display wording", func(s *FileChangeSet) { s.Diagnostics[0].Message = "different" }, true},
		{"diagnostic location", func(s *FileChangeSet) { s.Diagnostics[0].SourceLines = []int{99} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := makeSet(), makeSet()
			tc.mutate(b)
			assert.Equal(t, tc.equal, codexFileChangesEqual(a, b))
			assert.Equal(t, tc.equal, codexFileChangesEqual(b, a))
		})
	}
}

func TestCodexFileChangeIdentity_Duplicates(t *testing.T) {
	for _, tc := range []struct {
		name, second string
		conflict     bool
	}{
		{"identical", `{"type":"FileChange","id":"same","status":"completed","changes":{"a":{"type":"add","content":"new\n"}}}`, false},
		{"unknown fields", `{"ignored":42,"changes":{"a":{"ignored":true,"content":"new\n","type":"add"}},"status":"completed","id":"same","type":"FileChange","stdout":null,"stderr":null}`, false},
		{"status", `{"type":"FileChange","id":"same","status":"failed","changes":{"a":{"type":"add","content":"new\n"}}}`, true},
		{"stdout only", `{"type":"FileChange","id":"same","status":"completed","stdout":" output\n","changes":{"a":{"type":"add","content":"new\n"}}}`, true},
		{"stderr only", `{"type":"FileChange","id":"same","status":"completed","stderr":"error","changes":{"a":{"type":"add","content":"new\n"}}}`, true},
		{"diagnostic only", `{"type":"FileChange","id":"same","status":"completed","stdout":42,"changes":{"a":{"type":"add","content":"new\n"}}}`, true},
		{"same rendered lines", `{"type":"FileChange","id":"same","status":"completed","changes":{"a":{"type":"add","content":"new\r\n"}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := codexFileChangeEvent(at(3), "same", "completed", map[string]any{"a": map[string]any{"type": "add", "content": "new\n"}})
			second := codexEnv(at(7), "event_msg", map[string]any{"type": "item_completed", "item": json.RawMessage(tc.second)})
			raw := append([]byte("{broken}\n"), codexRollout(t, codexPaginated, first)...)
			raw = append(raw, '\n')
			raw = append(raw, codexRollout(t, codexPaginated, second, first, second)...)
			tr := decodeCodex(t, codexCase{raw: raw})
			require.Len(t, tr.Entries, 1)
			e := tr.Entries[0]
			assert.Equal(t, 1, e.LineIndex)
			assert.Equal(t, parseJSONLTime(at(3)), e.Timestamp)
			m := transcriptMessages(tr, nil)[0]
			if !tc.conflict {
				assert.Equal(t, FileChangeCompleted, e.FileChange.State)
				assert.Equal(t, "1 file changed (+1 −0)", m.ToolSummary)
				return
			}
			assert.Equal(t, FileChangeUnconfirmed, e.FileChange.State)
			assert.Equal(t, "Patch unconfirmed", m.ToolSummary)
			assert.Equal(t, "File changes · unconfirmed", m.Heading)
			assert.False(t, m.Diff)
			assert.NotContains(t, m.Body, "+new")
			assert.NotContains(t, m.Body, "@@")
			assert.Contains(t, m.Body, "*** Add File: a (reported)")
			assert.Contains(t, m.Body, "Source lines (1-based): 2 4 6.")
			assert.Equal(t, []int{1, 3, 5}, e.FileChange.Diagnostics[0].SourceLines)
		})
	}
}

func TestCodexFileChangeIdentity_Normalization(t *testing.T) {
	for _, tc := range []struct{ name, first, second string }{
		{"map order and null destination",
			`{"a":{"type":"update","unified_diff":"@@ -1 +1 @@\n-old\n+new\n"},"z":{"type":"delete","content":"old"}}`,
			`{"z":{"content":"old","type":"delete"},"a":{"unified_diff":"@@ -1 +1 @@\n-old\n+new\n","type":"update","move_path":null}}`},
		{"diagnostic member order",
			`{"a":{"type":"update","move_path":{"x":1,"y":2}}}`,
			`{"a":{"move_path": {"y": 2, "x": 1}, "type":"update"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := func(ts, changes string) map[string]any {
				return codexItemCompletedLine(ts, map[string]any{"type": "FileChange", "id": "id", "status": "completed", "changes": json.RawMessage(changes)})
			}
			tr := decodeCodex(t, codexCase{raw: codexRollout(t, codexPaginated,
				event(at(3), tc.first), event(at(8), tc.second))})
			require.Len(t, tr.Entries, 1)
			assert.Equal(t, FileChangeCompleted, tr.Entries[0].FileChange.State)
		})
	}
	for _, ids := range [][2]string{{"", ""}, {"", "other"}, {"one", "two"}} {
		t.Run("independent "+ids[0]+"/"+ids[1], func(t *testing.T) {
			tr := decodeCodex(t, codexCase{raw: codexRollout(t, codexPaginated,
				codexFileChangeEvent(at(3), ids[0], "completed", map[string]any{}),
				codexFileChangeEvent(at(4), ids[1], "completed", map[string]any{}))})
			require.Len(t, tr.Entries, 2)
		})
	}
}

func TestCodexFileChangePaginated_AllOperationsAndStates(t *testing.T) {
	for _, tc := range []struct{ status, summary string }{
		{"completed", "4 files changed (+3 −3)"},
		{"failed", "Patch failed"},
		{"declined", "Patch declined"},
		{"future", "Patch unconfirmed"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			raw := codexRollout(t, codexPaginated, codexFileChangeEvent(at(1), "id", tc.status, map[string]any{
				"a": map[string]any{"type": "add", "content": "added\n"},
				"b": map[string]any{"type": "delete", "content": "deleted\n"},
				"c": map[string]any{"type": "update", "unified_diff": "@@ -1 +1 @@\n-old\n+new\n"},
				"d": map[string]any{"type": "update", "move_path": "e", "unified_diff": "@@ -1 +1 @@\n-before\n+after\n"},
			}))
			msgs := ParseTranscript(PlatformCodex, raw, nil)
			require.Len(t, msgs, 1)
			m := msgs[0]
			assert.Equal(t, tc.summary, m.ToolSummary)
			assert.Contains(t, m.Body, "*** Add File: a")
			assert.Contains(t, m.Body, "*** Delete File: b")
			assert.Contains(t, m.Body, "*** Update File: c")
			assert.Contains(t, m.Body, "*** Move File: d → e")
			if tc.status == "completed" {
				assert.Contains(t, m.Body, "+added\n")
				assert.Contains(t, m.Body, "-deleted\n")
				assert.Contains(t, m.Body, "+after\n")
			} else {
				assert.Equal(t, 4, strings.Count(m.Body, "(reported)"))
				assert.NotContains(t, m.Body, "@@")
				assert.NotContains(t, m.Body, "+added")
				assert.NotContains(t, m.Body, "-deleted")
				assert.NotContains(t, m.Body, "(+")
				assert.False(t, m.Diff)
			}
		})
	}
}

func TestCodexFileChangeIdentity_WarningsAndAnchors(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	previous := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(previous) })
	changes := map[string]any{"private-path": map[string]any{"type": "add", "content": "private-body"}}
	raw := codexRollout(t, codexPaginated, codexUserItem(at(0), "Edit the example."),
		codexFileChangeEvent(at(1), "private-id", "failed", changes),
		codexFileChangeEvent(at(2), "declined-id", "declined", changes))
	_, err := (codexDecoder{}).Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	assert.Empty(t, logs.String(), "ordinary adverse outcomes are not parser warnings")
	raw = append(raw, []byte(strings.Repeat("x", 600)+"\n{broken}\n")...)
	raw = append(raw, codexRollout(t, codexPaginated,
		codexFileChangeEvent(at(3), "private-id", "completed", changes),
		codexFileChangeEvent(at(4), "private-id", "completed", changes))...)
	tr, err := (codexDecoder{lineCap: 500}).Decode(withSource("synthetic", bytes.NewReader(raw)))
	require.NoError(t, err)
	require.Len(t, tr.Entries, 3)
	assert.Equal(t, 1, tr.Entries[1].LineIndex)
	assert.Equal(t, FileChangeUnconfirmed, tr.Entries[1].FileChange.State)
	assert.Equal(t, []int{1, 5, 6}, tr.Entries[1].FileChange.Diagnostics[0].SourceLines)
	assert.Equal(t, 1, strings.Count(logs.String(), "reason=identity"))
	assert.Contains(t, logs.String(), "file=synthetic")
	assert.Contains(t, logs.String(), "line=5 first_line=1")
	assert.NotContains(t, logs.String(), "private")
}
