package vault

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are test-only exceptions for investigated historical damage, not a
// decoder policy. Never add an entry just to make the canary pass: investigate
// the original bytes, record the evidence, and add a redacted regression first.
// See docs/feat/done/issue-110-malformed-codex-rollouts/investigation.md.
type codexCanaryCorruption struct {
	filename  string // basename, independent of CODEX_HOME and compression
	lineIndex int    // physical, zero-based; never the rollout ordinal
	sha256    string // record bytes after trimEOL, not the entire growing file
}

var knownCodexCanaryCorruptions = []codexCanaryCorruption{
	{
		filename:  "rollout-2026-09-17T08-31-24-01a0ae10-15df-7ba1-909c-aaa8b95ac150.jsonl",
		lineIndex: 182,
		sha256:    "c6f3f5c1c31505eb3fb9a48a2ae62a683d23836619f00ab86c8af24f2df13df9",
	},
	{
		filename:  "rollout-2026-09-17T08-31-24-01a0ae10-15df-7ba1-909c-aaa8b95ac150.jsonl",
		lineIndex: 1674,
		sha256:    "506ed3c6f5f17eef8a0355eaf58cb60f37b4522ec8bd88c6d04eb6ad24b9569b",
	},
	{
		filename:  "rollout-2026-09-17T16-12-24-01a0afb6-2241-70a1-a6cd-c9d591f83297.jsonl",
		lineIndex: 2,
		sha256:    "94c8cf086110368837b51c9028c4419a1c9f9ae266f60fb3af567bd058ec26ad",
	},
}

func codexCanaryLineHash(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func knownCodexCanaryCorruption(known []codexCanaryCorruption, path string, lineIndex int, data []byte) bool {
	filename := strings.TrimSuffix(filepath.Base(path), ".zst")
	for _, entry := range known {
		if entry.filename == filename && entry.lineIndex == lineIndex && entry.sha256 == codexCanaryLineHash(data) {
			return true
		}
	}
	return false
}

// Match every skip warning, in order, on ONE decode pass. A missing/extra
// warning, payload warning, wrong physical index or severity still fails;
// accepting an investigated syntax error cannot hide a decoder regression.
func checkCodexCanarySkipWarnings(records []slog.Record, wantLines []int) error {
	var seen int
	for _, record := range records {
		if !strings.HasPrefix(record.Message, "vault codex decoder: skipping") {
			continue
		}
		if record.Message != "vault codex decoder: skipping malformed JSONL line" || record.Level != slog.LevelWarn {
			return fmt.Errorf("unexpected decoder skip: level=%s message=%q", record.Level, record.Message)
		}
		line, ok := recordAttrs(record)["line"].(int64)
		if !ok || seen >= len(wantLines) || line != int64(wantLines[seen]) {
			return fmt.Errorf("unexpected malformed-line warning at physical line %v; expected lines %v", recordAttrs(record)["line"], wantLines)
		}
		seen++
	}
	if seen != len(wantLines) {
		return fmt.Errorf("got %d malformed-line warnings; expected lines %v", seen, wantLines)
	}
	return nil
}

func codexHistoricalCorruptionFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/codex/malformed_history.jsonl")
	require.NoError(t, err)
	return raw
}

func codexHistoricalCorruptionPolicy(raw []byte) []codexCanaryCorruption {
	lines := bytes.Split(raw, []byte{'\n'})
	known := make([]codexCanaryCorruption, 0, 3)
	for _, i := range []int{3, 5, 8} {
		known = append(known, codexCanaryCorruption{
			filename: "fixture.jsonl", lineIndex: i, sha256: codexCanaryLineHash(trimEOL(lines[i])),
		})
	}
	return known
}

func TestTallyCodexRaw_InvestigatedCorruption(t *testing.T) {
	raw := codexHistoricalCorruptionFixture(t)
	known := codexHistoricalCorruptionPolicy(raw)
	for _, path := range []string{"/sessions/fixture.jsonl", "/archived_sessions/fixture.jsonl.zst"} {
		t.Run(path, func(t *testing.T) {
			tally, err := tallyCodexRaw(path, raw, known)
			require.NoError(t, err)
			assert.True(t, tally.firstLineIsMeta)
			assert.Equal(t, []int{3, 5, 8}, tally.malformedLines)
			assert.Equal(t, []string{"continue after torn records"}, tally.eventTexts)
			assert.Equal(t, tally.eventTexts, tally.filteredTexts)
			assert.Equal(t, map[string]bool{"call_kept": true}, tally.callIDs)
			assert.Equal(t, []string{"call_kept"}, tally.outputIDs)
		})
	}
	t.Run("line endings are not record content", func(t *testing.T) {
		lf := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
		crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
		assert.Equal(t, known, codexHistoricalCorruptionPolicy(crlf), "fixture fingerprints must be independent of checkout line endings")
		tally, err := tallyCodexRaw("fixture.jsonl", crlf, known)
		require.NoError(t, err)
		assert.Equal(t, []int{3, 5, 8}, tally.malformedLines)
	})
	t.Run("clean files need no exceptions", func(t *testing.T) {
		tally, err := tallyCodexRaw("clean.jsonl", codexCaseByName(t, "legacy_basic").raw, nil)
		require.NoError(t, err)
		assert.Empty(t, tally.malformedLines)
		assert.Equal(t, []string{codexPrompt}, tally.eventTexts)
	})
}

func TestTallyCodexRaw_RejectsUnreviewedDamageAndDrift(t *testing.T) {
	raw := codexHistoricalCorruptionFixture(t)
	known := codexHistoricalCorruptionPolicy(raw)
	for _, tc := range []struct {
		name  string
		path  string
		raw   []byte
		known []codexCanaryCorruption
	}{
		{"no exceptions", "fixture.jsonl", raw, nil},
		{"different file", "other.jsonl", raw, known},
		{"moved record", "fixture.jsonl", bytes.Replace(raw, []byte("\n"), []byte("\n\n"), 1), known},
		{"changed bytes", "fixture.jsonl", bytes.Replace(raw, []byte("formatted_output"), []byte("formatted_output_changed"), 1), known},
		{"new malformed record", "fixture.jsonl", append(bytes.Clone(raw), []byte("{broken\n")...), known},
		{"event payload drift", "fixture.jsonl", append(bytes.Clone(raw), []byte("{\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":42}}\n")...), known},
		{"metadata payload drift", "fixture.jsonl", bytes.Replace(raw, []byte("\"cli_version\":\"0.147.0\""), []byte("\"cli_version\":147"), 1), known},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tallyCodexRaw(tc.path, tc.raw, tc.known)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.path+": line ")
		})
	}

	t.Run("matching hash cannot exempt valid JSON with envelope drift", func(t *testing.T) {
		lines := bytes.Split(bytes.Clone(raw), []byte{'\n'})
		lines[3] = []byte("{\"timestamp\":42,\"type\":\"event_msg\",\"payload\":{}}")
		policy := append([]codexCanaryCorruption(nil), known...)
		policy[0].sha256 = codexCanaryLineHash(lines[3])
		_, err := tallyCodexRaw("fixture.jsonl", bytes.Join(lines, []byte{'\n'}), policy)
		require.ErrorContains(t, err, "line 3")
	})
	t.Run("matching hash cannot exempt broken first line", func(t *testing.T) {
		lines := bytes.Split(bytes.Clone(raw), []byte{'\n'})
		lines[0] = []byte("{broken")
		policy := append([]codexCanaryCorruption(nil), known...)
		policy = append(policy, codexCanaryCorruption{
			filename: "fixture.jsonl", lineIndex: 0, sha256: codexCanaryLineHash(lines[0]),
		})
		_, err := tallyCodexRaw("fixture.jsonl", bytes.Join(lines, []byte{'\n'}), policy)
		require.ErrorContains(t, err, "line 0")
	})
}

func TestCodexCanaryCorruption_SurvivingRecords(t *testing.T) {
	raw := codexHistoricalCorruptionFixture(t)
	for _, consumer := range []string{"decoder", "render", "transcript"} {
		t.Run(consumer, func(t *testing.T) {
			h := captureSlog(t)
			switch consumer {
			case "decoder":
				tr, err := codexDecoder{}.Decode(withSource("fixture.jsonl", bytes.NewReader(raw)))
				require.NoError(t, err)
				require.Len(t, tr.Entries, 4)
				assert.Equal(t, []EntryKind{EntryHuman, EntryAssistant, EntryToolResult, EntryAssistant},
					[]EntryKind{tr.Entries[0].Kind, tr.Entries[1].Kind, tr.Entries[2].Kind, tr.Entries[3].Kind})
				assert.Equal(t, []int{1, 6, 7, 9},
					[]int{tr.Entries[0].LineIndex, tr.Entries[1].LineIndex, tr.Entries[2].LineIndex, tr.Entries[3].LineIndex})
				assert.Equal(t, "exec", tr.Entries[2].CallName)
				assert.Equal(t, "call_kept", tr.Entries[2].CallID)
				assert.Contains(t, tr.Entries[2].Body, "result survived")
				require.Len(t, tr.Entries[3].Parts, 1)
				assert.Equal(t, "answer survived", tr.Entries[3].Parts[0].Text)
				for _, record := range h.recordsWithMessage("vault codex decoder: skipping malformed JSONL line") {
					assert.Equal(t, "fixture.jsonl", recordAttrs(record)["file"])
				}
			case "render":
				text := RenderText(PlatformCodex, raw)
				assert.Contains(t, text, "continue after torn records")
				assert.Contains(t, text, "answer survived")
			case "transcript":
				messages := ParseTranscript(PlatformCodex, raw, nil)
				require.NotEmpty(t, messages)
				assert.Equal(t, 9, messages[len(messages)-1].SourceLine)
			}
			require.NoError(t, checkCodexCanarySkipWarnings(allRecords(h), []int{3, 5, 8}))
		})
	}
}

func TestCodexCanaryCorruption_WarningContract(t *testing.T) {
	warning := func(line int) slog.Record {
		record := slog.NewRecord(time.Time{}, slog.LevelWarn, "vault codex decoder: skipping malformed JSONL line", 0)
		record.AddAttrs(slog.Int("line", line))
		return record
	}
	payload := warning(3)
	payload.Message = "vault codex decoder: skipping malformed payload"
	debug := warning(3)
	debug.Level = slog.LevelDebug
	missingLine := slog.NewRecord(time.Time{}, slog.LevelWarn, "vault codex decoder: skipping malformed JSONL line", 0)
	for _, tc := range []struct {
		name      string
		records   []slog.Record
		wantLines []int
		wantError bool
	}{
		{"clean", nil, nil, false},
		{"exact", []slog.Record{warning(3), warning(5)}, []int{3, 5}, false},
		{"missing", nil, []int{3}, true},
		{"extra", []slog.Record{warning(3), warning(3)}, []int{3}, true},
		{"unexpected on clean file", []slog.Record{warning(3)}, nil, true},
		{"wrong anchor", []slog.Record{warning(4)}, []int{3}, true},
		{"payload drift", []slog.Record{payload}, []int{3}, true},
		{"wrong severity", []slog.Record{debug}, []int{3}, true},
		{"missing anchor", []slog.Record{missingLine}, []int{3}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCodexCanarySkipWarnings(tc.records, tc.wantLines)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
