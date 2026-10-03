package vault

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Opt-in, read-only viewer evidence alongside the separate scanner/export
// parity gate. Only aggregate counts escape; no private paths or bodies appear
// in assertions or decoder logs. Synthetic tests cover absent corpus categories.
func TestCodexFileChangeCanary(t *testing.T) {
	if os.Getenv("CAPY_CODEX_CHANGES_CANARY") != "1" {
		t.Skip("CAPY_CODEX_CHANGES_CANARY not set")
	}
	files, err := discoverCodexParityFiles(codexCanaryHome())
	require.NoError(t, err)
	if len(files) == 0 {
		t.Skip("no local Codex corpus")
	}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { slog.SetDefault(previous) })
	states, operations := map[string]int{}, map[string]int{}
	unavailable := map[string]int{}
	missingDiffs, moves, recordings, legacyEvents, directResults := 0, 0, 0, 0, 0
	for _, file := range files {
		if file.revert {
			continue
		}
		raw, err := os.ReadFile(file.path)
		require.NoError(t, codexParityIOError("reading viewer corpus", err))
		if file.compressed {
			raw, err = decodeBlob(encodingZstd, raw)
			require.NoError(t, err, "decompressing viewer corpus")
		}
		// Read explicit wire status separately so the check cannot silently pass
		// after an adapter starts interpreting failed edits as completed.
		type outcome struct {
			state                    string
			repeated, statusConflict bool
		}
		type responseCall struct {
			name        string
			line, count int
		}
		calls := map[string]responseCall{}
		results := map[string][]int{}
		expected := map[int]outcome{}
		firstByID := map[string]int{}
		physical := -1
		err = scanLines(bytes.NewReader(raw), scanLineCap, func(data []byte, oversize bool) {
			physical++
			var line codexLine
			if oversize || json.Unmarshal(data, &line) != nil {
				return
			}
			if line.Type == "response_item" {
				var response struct {
					Type, Name string
					CallID     string `json:"call_id"`
				}
				if json.Unmarshal(line.Payload, &response) != nil || response.CallID == "" {
					return
				}
				switch response.Type {
				case "custom_tool_call", "function_call":
					calls[response.CallID] = responseCall{response.Name, physical, calls[response.CallID].count + 1}
				case "custom_tool_call_output", "function_call_output":
					results[response.CallID] = append(results[response.CallID], physical)
				}
				return
			}
			if line.Type != "event_msg" {
				return
			}
			var event struct {
				Type    string          `json:"type"`
				CallID  string          `json:"call_id"`
				Success json.RawMessage `json:"success"`
				Status  json.RawMessage `json:"status"`
				Item    struct {
					Type   string          `json:"type"`
					ID     string          `json:"id"`
					Status json.RawMessage `json:"status"`
				} `json:"item"`
			}
			if json.Unmarshal(line.Payload, &event) == nil && (event.Type == "patch_apply_end" || event.Type == "item_completed" && event.Item.Type == "FileChange") {
				id, rawStatus := event.Item.ID, event.Item.Status
				legacy := event.Type == "patch_apply_end"
				if legacy {
					id, rawStatus = event.CallID, event.Status
					legacyEvents++
				}
				state := "unconfirmed"
				var status string
				if json.Unmarshal(rawStatus, &status) == nil {
					switch status {
					case "completed", "failed", "declined":
						state = status
					}
				}
				if legacy {
					var success *bool
					if json.Unmarshal(event.Success, &success) != nil || success == nil {
						state = "unconfirmed"
					} else if len(rawStatus) == 0 {
						state = "failed"
						if *success {
							state = "completed"
						}
					} else if *success != (state == "completed") {
						state = "unconfirmed"
					}
				}
				if first, exists := firstByID[id]; id != "" && exists {
					previous := expected[first]
					previous.repeated = true
					previous.statusConflict = previous.statusConflict || previous.state != state
					expected[first] = previous
					return
				}
				if id != "" {
					firstByID[id] = physical
				}
				expected[physical] = outcome{state: state}
			}
		})
		require.NoError(t, err)
		if len(expected) == 0 {
			continue
		}
		recordings++
		tr, err := DecoderFor(PlatformCodex).Decode(bytes.NewReader(raw))
		require.NoError(t, err)
		// Independently account for explicit result contradictions and repeated
		// response IDs before checking viewer states. Bodies remain private.
		for id, first := range firstByID {
			call := calls[id]
			if call.count > 1 || call.name == "apply_patch" && len(results[id]) > 1 {
				want := expected[first]
				want.state = "unconfirmed"
				expected[first] = want
			}
		}
		for _, e := range tr.Entries {
			if e.Kind != EntryToolResult {
				continue
			}
			first, hasEvent := firstByID[e.CallID]
			call := calls[e.CallID]
			associated := hasEvent && call.name == "apply_patch" && call.count == 1 && len(results[e.CallID]) == 1
			require.True(t, (e.FileChangeID != "") == associated, "only unique exact direct IDs associate")
			if !associated {
				continue
			}
			require.True(t, e.FileChangeID == e.CallID, "event association is exact")
			require.True(t, e.Diff == nil, "structured event supersedes fallback")
			want := expected[first]
			if e.ExitCode != nil && (want.state == "completed" && *e.ExitCode != 0 ||
				(want.state == "failed" || want.state == "declined") && *e.ExitCode == 0) {
				want.state = "unconfirmed"
				expected[first] = want
			}
			directResults++
			require.True(t, call.line < first && first < e.LineIndex, "observed direct call-event-output order")
		}
		messages := transcriptMessages(tr, nil)
		messagesByLine := make(map[int]TranscriptMessage, len(messages))
		for _, m := range messages {
			messagesByLine[m.SourceLine] = m
		}
		for _, e := range tr.Entries {
			if e.Kind != EntryToolResult || e.FileChangeID == "" || !e.ReportedSuccess || e.Body == "" {
				continue
			}
			want := expected[firstByID[e.FileChangeID]]
			if want.repeated || want.state != "completed" {
				continue
			}
			m := messagesByLine[e.LineIndex]
			require.True(t, m.Collapsed && !m.Diff && m.ToolSummary == "apply_patch · output" && m.Heading == "Tool result", "compact direct output")
			require.True(t, m.Body == e.Body, "full direct output retained")
		}
		seen := 0
		for _, e := range tr.Entries {
			if e.Kind != EntryFileChange {
				continue
			}
			m := viewerFileChangeMessage(e, tr.Meta.CWD)
			want, exists := expected[m.SourceLine]
			require.True(t, exists, "edit detail must retain its physical event anchor")
			status := string(e.FileChange.State)
			// Exact content/diagnostic equality is covered by synthetic tests.
			// Repeated IDs may be unconfirmed even when raw statuses agree.
			validStatus := status == want.state
			if want.repeated {
				validStatus = validStatus || status == "unconfirmed"
			}
			if want.statusConflict {
				validStatus = status == "unconfirmed"
			}
			require.True(t, validStatus, "wire/viewer status")
			require.True(t, m.Heading == "File changes · "+status, "state heading")
			states[status]++
			seen++
			if status != "completed" {
				require.False(t, m.Diff, "adverse event must not expose applied diffs")
				require.True(t, m.ToolSummary == "Patch "+status, "adverse summary")
				continue
			}
			for _, change := range e.FileChange.Files {
				operations[change.Kind]++
				if change.MovePath != "" {
					moves++
					header := "*** Move File: " + fileChangeDisplayPath(change.Path, tr.Meta.CWD) + " → " + fileChangeDisplayPath(change.MovePath, tr.Meta.CWD)
					require.True(t, strings.Contains(m.Body, header), "moves retain both paths")
				}
				if change.Diff == nil {
					missingDiffs++
					for _, diagnostic := range change.Diagnostics {
						unavailable[diagnostic.Code]++
					}
					if change.Content == "" && change.MovePath != "" {
						unavailable["empty move diff"]++
					}
					if change.Content == "" && change.MovePath == "" {
						unavailable["empty update diff"]++
					}
				}
			}
		}
		require.Equal(t, len(expected), seen, "all recorded edits remain visible")
	}
	t.Logf("viewer corpus: recordings=%d states=%v completed operations=%v moves=%d unavailable files=%d",
		recordings, states, operations, moves, missingDiffs)
	t.Logf("unavailable reasons=%v", unavailable)
	t.Logf("legacy events=%d exact direct results=%d", legacyEvents, directResults)
	require.Positive(t, recordings, "no edits compared")
	require.Zero(t, missingDiffs, "investigate actual unavailable completed changes")
	for _, category := range []string{"completed", "failed", "declined"} {
		if states[category] == 0 {
			t.Logf("category unavailable: %s (synthetic coverage only)", category)
		}
	}
	if moves == 0 {
		t.Log("category unavailable: moves (synthetic coverage only)")
	}
	if legacyEvents == 0 {
		t.Log("category unavailable: legacy edits (synthetic coverage only)")
	}
}
