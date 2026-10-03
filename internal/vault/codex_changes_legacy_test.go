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

func legacyChangeEvent(ts, id string, success bool, status string) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{
		"type": "patch_apply_end", "call_id": id, "success": success, "status": status,
		"changes": map[string]any{"/p/a": map[string]any{"type": "add", "content": "recorded content\n"}},
	})
}

func TestCodexFileChangeLegacy_Status(t *testing.T) {
	for _, success := range []string{"true", "false", "", "null", `"true"`, "42"} {
		for _, status := range []string{`"completed"`, `"failed"`, `"declined"`, "", "null", `"future"`, `""`, "42"} {
			t.Run("success="+success+"/status="+status, func(t *testing.T) {
				event := legacyChangeEvent(at(3), "legacy-id", true, "completed")
				payload := event["payload"].(map[string]any)
				for field, raw := range map[string]string{"success": success, "status": status} {
					delete(payload, field)
					if raw != "" {
						payload[field] = json.RawMessage(raw)
					}
				}
				payload["stdout"], payload["stderr"] = " output\r\n", " diagnostic\n"
				// Wire shape wins even when the session declares paginated history.
				tr := decodeCodexRaw(t, codexRollout(t, codexPaginated,
					codexSessionMetaLine(at(0), codexPaginated, codexMetaOpts{}), event))
				require.Len(t, tr.Entries, 1)
				e := tr.Entries[0]
				want := FileChangeUnconfirmed
				switch {
				case success == "true" && (status == `"completed"` || status == ""):
					want = FileChangeCompleted
				case success == "false" && (status == `"failed"` || status == ""):
					want = FileChangeFailed
				case success == "false" && status == `"declined"`:
					want = FileChangeDeclined
				}
				require.NotNil(t, e.FileChange)
				assert.Equal(t, want, e.FileChange.State)
				assert.Equal(t, 1, e.LineIndex)
				assert.Equal(t, parseJSONLTime(at(3)), e.Timestamp)
				assert.Equal(t, "legacy-id", e.FileChange.ID)
				m := transcriptMessages(tr, nil)[0]
				assert.Equal(t, "File changes · "+string(want), m.Heading)
				assert.Contains(t, m.Body, "stdout:\n output\r\n")
				assert.Contains(t, m.Body, "stderr:\n diagnostic\n")
				if want == FileChangeCompleted {
					assert.Equal(t, "1 file changed (+1 −0)", m.ToolSummary)
					assert.Contains(t, m.Body, "+recorded content\n")
				} else {
					assert.Equal(t, "Patch "+string(want), m.ToolSummary)
					assert.NotContains(t, m.Body, "+recorded content")
					assert.False(t, m.Diff)
				}
			})
		}
	}
}

func TestCodexFileChangeLegacy_MixedFamilies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(map[string]any)
		conflict bool
	}{
		{"identical", func(p map[string]any) { p["stdout"], p["stderr"] = nil, nil }, false},
		{"status", func(p map[string]any) { p["status"] = "failed" }, true},
		{"stdout", func(p map[string]any) { p["stdout"] = "different\r\n" }, true},
		{"stderr", func(p map[string]any) { p["stderr"] = "different" }, true},
		{"diagnostic", func(p map[string]any) { p["stdout"] = 42 }, true},
		{"content", func(p map[string]any) {
			p["changes"] = map[string]any{"a": map[string]any{"type": "add", "content": "different"}}
		}, true},
	} {
		for _, legacyFirst := range []bool{true, false} {
			name := tc.name + "/paginated-first"
			if legacyFirst {
				name = tc.name + "/legacy-first"
			}
			t.Run(name, func(t *testing.T) {
				legacy := legacyChangeEvent(at(2), "same", true, "completed")
				payload := legacy["payload"].(map[string]any)
				paginated := codexFileChangeEvent(at(5), "same", "completed", payload["changes"].(map[string]any))
				tc.mutate(paginated["payload"].(map[string]any)["item"].(map[string]any))
				first, second := legacy, paginated
				if !legacyFirst {
					first, second = second, first
				}
				tr := decodeCodexRaw(t, codexRollout(t, codexLegacy, first, second, first, second))
				require.Len(t, tr.Entries, 1)
				assert.Equal(t, 0, tr.Entries[0].LineIndex)
				m := transcriptMessages(tr, nil)[0]
				if tc.conflict {
					assert.Equal(t, "Patch unconfirmed", m.ToolSummary)
					assert.NotContains(t, m.Body, "+recorded content")
					assert.Contains(t, m.Body, "Source lines (1-based): 1 2 4.")
				} else {
					assert.Equal(t, "1 file changed (+1 −0)", m.ToolSummary)
				}
			})
		}
	}
}

func TestCodexFileChangeDirectResult_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status, output string
		state                FileChangeState
		positive, collapsed  bool
		exit                 string
	}{
		{"completed zero", "completed", `{"output":" detail\r\n","metadata":{"exit_code":0}}`, FileChangeCompleted, true, true, "0"},
		{"completed nonzero", "completed", `{"output":"Success.","metadata":{"exit_code":7}}`, FileChangeUnconfirmed, false, false, "7"},
		{"negative exit", "completed", `{"output":"error","metadata":{"exit_code":-1}}`, FileChangeUnconfirmed, false, false, "-1"},
		{"failed zero", "failed", `{"output":"ok","metadata":{"exit_code":0}}`, FileChangeUnconfirmed, true, false, "0"},
		{"declined zero", "declined", `{"output":"ok","metadata":{"exit_code":0}}`, FileChangeUnconfirmed, true, false, "0"},
		{"failed nonzero", "failed", `{"output":"error","metadata":{"exit_code":1}}`, FileChangeFailed, false, false, "1"},
		{"unknown zero", "future", `{"output":"ok","metadata":{"exit_code":0}}`, FileChangeUnconfirmed, true, false, "0"},
		{"completed no exit", "completed", `{"output":"unrecognized body"}`, FileChangeCompleted, false, false, ""},
		{"completed legacy positive", "completed", `{"output":"Success. old client\n"}`, FileChangeCompleted, true, true, ""},
		{"failed legacy positive", "failed", `{"output":"Success. old client\n"}`, FileChangeFailed, true, false, ""},
		{"null exit", "completed", `{"output":"Success. old client","metadata":{"exit_code":null}}`, FileChangeCompleted, true, true, ""},
		{"plain success", "completed", "Success. Updated all files.", FileChangeCompleted, false, false, ""},
		{"status-looking text", "completed", "Process exited with code 1\nScript completed", FileChangeCompleted, false, false, ""},
		{"missing body", "completed", `{"metadata":{"exit_code":1}}`, FileChangeUnconfirmed, false, false, "1"},
		{"malformed exit", "completed", `{"output":"Success.","metadata":{"exit_code":"1"}}`, FileChangeCompleted, false, false, ""},
		{"empty body", "completed", `{"output":""}`, FileChangeCompleted, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := decodeCodexRaw(t, codexRollout(t, codexLegacy,
				codexCustomToolCallLine(at(0), "direct", "apply_patch", codexAddPatch),
				legacyChangeEvent(at(1), "direct", tc.status == "completed", tc.status),
				codexCustomToolOutput(at(2), "direct", tc.output)))
			require.Len(t, tr.Entries, 3)
			assert.Equal(t, tc.state, tr.Entries[1].FileChange.State)
			result := tr.Entries[2]
			assert.Nil(t, result.Diff, "recognized events always supersede the input fallback")
			assert.Equal(t, "direct", result.FileChangeID)
			assert.Equal(t, tc.positive, result.ReportedSuccess)
			if tc.exit == "" {
				assert.Nil(t, result.ExitCode)
			} else {
				require.NotNil(t, result.ExitCode)
				assert.JSONEq(t, tc.exit, string(marshalGolden(t, *result.ExitCode)))
			}
			msgs := transcriptMessages(tr, nil)
			if result.Body == "" {
				assert.Len(t, msgs, 2)
				return
			}
			require.Len(t, msgs, 3)
			assert.Equal(t, tc.collapsed, msgs[2].Collapsed)
			if tc.collapsed {
				assert.Equal(t, "apply_patch · output", msgs[2].ToolSummary)
				assert.Equal(t, "Tool result", msgs[2].Heading)
				assert.Equal(t, result.Body, msgs[2].Body)
				assert.False(t, msgs[2].Diff)
			} else {
				assert.Equal(t, prefixToolResult(result.CallSummary, result.Body), msgs[2].Body)
			}
		})
	}
}

func TestCodexFileChangeDirectResult_IdentityAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		lines                func(*testing.T) []map[string]any
		associated, fallback bool
		state                FileChangeState
	}{
		{"eventless", func(t *testing.T) []map[string]any { return nil }, false, true, ""},
		{"unrelated", func(t *testing.T) []map[string]any {
			return []map[string]any{legacyChangeEvent(at(1), "other", false, "failed")}
		}, false, true, FileChangeFailed},
		{"empty event ID", func(t *testing.T) []map[string]any {
			return []map[string]any{legacyChangeEvent(at(1), "", false, "failed")}
		}, false, true, FileChangeFailed},
		{"bad envelope identity", func(t *testing.T) []map[string]any {
			e := legacyChangeEvent(at(1), "direct", false, "failed")
			e["payload"].(map[string]any)["call_id"] = 42
			return []map[string]any{e}
		}, false, true, ""},
		{"unavailable changes", func(t *testing.T) []map[string]any {
			e := legacyChangeEvent(at(1), "direct", true, "completed")
			e["payload"].(map[string]any)["changes"] = []any{42}
			return []map[string]any{e}
		}, true, false, FileChangeCompleted},
		{"duplicate calls", func(t *testing.T) []map[string]any {
			return []map[string]any{
				legacyChangeEvent(at(1), "direct", true, "completed"), codexCustomToolCallLine(at(2), "direct", "apply_patch", codexAddPatch)}
		}, false, false, FileChangeUnconfirmed},
		{"duplicate eventless calls", func(t *testing.T) []map[string]any {
			return []map[string]any{
				codexCustomToolCallLine(at(2), "direct", "apply_patch", codexAddPatch)}
		}, false, false, ""},
		{"duplicate result IDs", func(t *testing.T) []map[string]any {
			return []map[string]any{
				legacyChangeEvent(at(1), "direct", true, "completed"), codexCustomToolOutput(at(2), "direct", codexCustomOutputJSON(t, "Error", 1))}
		}, false, false, FileChangeUnconfirmed},
		{"event conflict", func(t *testing.T) []map[string]any {
			return []map[string]any{
				legacyChangeEvent(at(1), "direct", true, "completed"), legacyChangeEvent(at(2), "direct", false, "failed")}
		}, true, false, FileChangeUnconfirmed},
		{"identical duplicate event", func(t *testing.T) []map[string]any {
			return []map[string]any{
				legacyChangeEvent(at(1), "direct", true, "completed"), legacyChangeEvent(at(2), "direct", true, "completed")}
		}, true, false, FileChangeCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := append([]map[string]any{codexCustomToolCallLine(at(0), "direct", "apply_patch", codexAddPatch)}, tc.lines(t)...)
			lines = append(lines, codexCustomToolOutput(at(3), "direct", codexCustomOutputJSON(t, "Success. result stays intact\n", 0)))
			tr := decodeCodexRaw(t, codexRollout(t, codexLegacy, lines...))
			result := tr.Entries[len(tr.Entries)-1]
			assert.Equal(t, tc.associated, result.FileChangeID != "")
			assert.Equal(t, tc.fallback, result.Diff != nil)
			assert.Equal(t, "Process exited with code 0\nSuccess. result stays intact\n", result.Body)
			var events int
			for _, e := range tr.Entries {
				if e.FileChange != nil {
					events++
					assert.Equal(t, tc.state, e.FileChange.State)
				}
			}
			if tc.state == "" {
				assert.Zero(t, events)
			} else {
				assert.Equal(t, 1, events)
			}
		})
	}
}

func TestCodexFileChangeDirectResult_IndependentEventsAndOrder(t *testing.T) {
	for _, name := range []string{"exec", "exec_command", ""} {
		t.Run("not direct="+name, func(t *testing.T) {
			tr := decodeCodexRaw(t, codexRollout(t, codexLegacy,
				codexCustomToolCallLine(at(0), "call_wrapper", name, codexAddPatch),
				legacyChangeEvent(at(1), "exec-first", true, "completed"),
				legacyChangeEvent(at(2), "exec-second", true, "completed"),
				legacyChangeEvent(at(3), "call_wrapper", true, "completed"),
				codexCustomToolOutput(at(4), "call_wrapper", codexCustomOutputJSON(t, "error", 1))))
			require.Len(t, tr.Entries, 5)
			assert.Empty(t, tr.Entries[4].FileChangeID)
			assert.Nil(t, tr.Entries[4].Diff)
			for _, e := range tr.Entries[1:4] {
				assert.Equal(t, FileChangeCompleted, e.FileChange.State)
			}
		})
	}
	// Whole-transcript matching: even a result before its event collapses.
	tr := decodeCodexRaw(t, codexRollout(t, codexLegacy,
		codexCustomToolOutput(at(0), "direct", codexCustomOutputJSON(t, "ok", 0)),
		legacyChangeEvent(at(1), "direct", true, "completed"),
		codexCustomToolCallLine(at(2), "direct", "apply_patch", codexAddPatch)))
	assert.Equal(t, "direct", tr.Entries[0].FileChangeID)
	assert.Equal(t, "apply_patch · output", transcriptMessages(tr, nil)[0].ToolSummary)
}

func TestCodexFileChangeLegacy_AnchorsAndWarnings(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	raw := codexRollout(t, codexLegacy, codexUserEvent(at(0), "Edit the example."),
		codexCustomToolCallLine(at(1), "private-id", "apply_patch", codexAddPatch),
		legacyChangeEvent(at(2), "private-id", true, "completed"),
		codexCustomToolOutput(at(3), "private-id", codexCustomOutputJSON(t, "private-error", 1)))
	tr, err := (codexDecoder{}).Decode(withSource("synthetic", bytes.NewReader(raw)))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"))
	assert.Contains(t, logs.String(), "category=patch_apply_end reason=outcome")
	assert.Contains(t, logs.String(), "file=synthetic")
	assert.NotContains(t, logs.String(), "private")
	assert.Equal(t, []int{2, 3}, tr.Entries[2].FileChange.Diagnostics[0].SourceLines)

	logs.Reset()
	raw = codexRollout(t, codexLegacy,
		codexUserEvent(at(0), "Edit the example."),
		legacyChangeEvent(at(1), "failed", false, "failed"), legacyChangeEvent(at(2), "declined", false, "declined"))
	decodeCodexRaw(t, raw)
	assert.Empty(t, logs.String(), "ordinary failures and declines are not format warnings")

	// A legacy event cannot close the assistant attach window, even between
	// malformed/oversize neighbors. Freeze consumer parity against no event.
	before := codexRollout(t, codexLegacy, codexCustomToolCallLine(at(0), "one", "exec", "text(1)"))
	raw = append(before, []byte("{broken}\n"+strings.Repeat("x", 700)+"\n")...)
	raw = append(raw, codexRollout(t, codexLegacy, legacyChangeEvent(at(3), "event", true, "completed"),
		codexCustomToolCallLine(at(4), "two", "exec", "text(2)"))...)
	tr, err = (codexDecoder{lineCap: 600}).Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Len(t, tr.Entries, 2)
	assert.Len(t, tr.Entries[0].Parts, 2)
	assert.Equal(t, 3, tr.Entries[1].LineIndex)
}
