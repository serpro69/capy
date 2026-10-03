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
	missingDiffs, moves, recordings := 0, 0, 0
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
		expected := map[int]outcome{}
		firstByID := map[string]int{}
		physical := -1
		err = scanLines(bytes.NewReader(raw), scanLineCap, func(data []byte, oversize bool) {
			physical++
			var line codexLine
			if oversize || json.Unmarshal(data, &line) != nil || line.Type != "event_msg" {
				return
			}
			var event struct {
				Type string `json:"type"`
				Item struct {
					Type   string          `json:"type"`
					ID     string          `json:"id"`
					Status json.RawMessage `json:"status"`
				} `json:"item"`
			}
			if json.Unmarshal(line.Payload, &event) == nil && event.Type == "item_completed" && event.Item.Type == "FileChange" {
				state := "unconfirmed"
				var status string
				if json.Unmarshal(event.Item.Status, &status) == nil {
					switch status {
					case "completed", "failed", "declined":
						state = status
					}
				}
				if first, exists := firstByID[event.Item.ID]; event.Item.ID != "" && exists {
					previous := expected[first]
					previous.repeated = true
					previous.statusConflict = previous.statusConflict || previous.state != state
					expected[first] = previous
					return
				}
				if event.Item.ID != "" {
					firstByID[event.Item.ID] = physical
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
		require.Equal(t, len(expected), seen, "all recorded paginated edits remain visible")
	}
	t.Logf("viewer corpus: recordings=%d states=%v completed operations=%v moves=%d unavailable files=%d",
		recordings, states, operations, moves, missingDiffs)
	t.Logf("unavailable reasons=%v", unavailable)
	require.Positive(t, recordings, "no paginated edits compared")
	require.Zero(t, missingDiffs, "investigate actual unavailable completed changes")
	for _, category := range []string{"completed", "failed", "declined"} {
		if states[category] == 0 {
			t.Logf("category unavailable: %s (synthetic coverage only)", category)
		}
	}
	if moves == 0 {
		t.Log("category unavailable: moves (synthetic coverage only)")
	}
}
