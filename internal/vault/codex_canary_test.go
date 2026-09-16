package vault

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/serpro69/capy/internal/config"
)

// codex_canary_test.go runs the Codex decoder over the REAL local corpus under
// $CODEX_HOME/{sessions,archived_sessions} and asserts the design's testable
// bets (design.md § Assumptions 1, 2, 3, 11 and § Observability) hold on it.
// It is decoder-level only in Slice 6; the consumer pass (ScanSession /
// RenderText / ParseTranscript over Codex bytes) joins in Slice 7.6. It skips
// when the corpus is absent (CI) and under -short.
//
// Like parity_canary_test.go it is a drift detector, not a unit test: a
// failure means either a Codex upgrade changed the rollout format or the
// decoder regressed — inspect the reported paths before touching either.

// codexCanaryHome resolves the Codex home for the canary ("" when the home
// directory itself cannot be resolved).
func codexCanaryHome() string {
	home, err := config.CodexHome()
	if err != nil {
		return ""
	}
	return home
}

// codexCanaryFile is one discovered rollout.
type codexCanaryFile struct {
	path, uuid string
	compressed bool
	revert     bool // _<rollout_id> variant: recognised, not decoded (v1 skips them)
}

// discoverCodexCanaryFiles walks the corpus with the canary's own loop rather
// than DiscoverCodexSessions: the canary wants revert variants in the list (to
// count them) and must not depend on the first-line read the discoverer adds.
// The filename grammar is shared through parseRolloutFilename.
func discoverCodexCanaryFiles(t *testing.T, home string) []codexCanaryFile {
	t.Helper()
	var files []codexCanaryFile
	for _, sub := range codexRolloutRoots {
		root := filepath.Join(home, sub)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
				return err
			}
			name, ok := parseRolloutFilename(d.Name())
			if !ok {
				return nil
			}
			files = append(files, codexCanaryFile{path: path, uuid: name.UUID, compressed: name.Compressed, revert: name.RolloutID != ""})
			return nil
		})
		require.NoError(t, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files
}

func readCodexCanaryFile(t *testing.T, f codexCanaryFile) []byte {
	t.Helper()
	raw, err := os.ReadFile(f.path)
	require.NoError(t, err, f.path)
	if f.compressed {
		raw, err = decodeBlob(encodingZstd, raw)
		require.NoError(t, err, f.path)
	}
	return raw
}

// codexRawTally is the decoder-INDEPENDENT reading of one rollout used for the
// Assumption 2 reconciliation: what the event stream says versus what the
// noise-filtered response_item user messages say.
type codexRawTally struct {
	firstLineIsMeta bool
	metaID          string
	isSubagent      bool
	historyMode     string
	eventTexts      []string // user_message.message (legacy) + UserMessage item text (paginated)
	legacyEvents    int
	filteredTexts   []string // noise-filtered response_item user texts
	taskComplete    int
	taskStarted     int
	callIDs         map[string]bool // function_call / custom_tool_call ids
	outputIDs       []string        // *_output call_ids, in order
}

func tallyCodexRaw(t *testing.T, raw []byte) codexRawTally {
	t.Helper()
	tally := codexRawTally{callIDs: map[string]bool{}}
	for i, data := range bytes.Split(raw, []byte{'\n'}) {
		data = trimEOL(data)
		if len(data) == 0 {
			continue
		}
		var line codexLine
		require.NoError(t, json.Unmarshal(data, &line), "line %d", i)
		switch line.Type {
		case codexSessionMetaType:
			var m codexSessionMeta
			require.NoError(t, json.Unmarshal(line.Payload, &m))
			if i == 0 {
				tally.firstLineIsMeta = true
				tally.metaID = m.ID
				tally.isSubagent = codexSourceLabel(m.Source) == "subagent"
				tally.historyMode = m.HistoryMode
			}
		case codexTypeResponseItem:
			var probe struct {
				Type   string             `json:"type"`
				Role   string             `json:"role"`
				CallID string             `json:"call_id"`
				Parts  []codexContentPart `json:"content"`
			}
			if err := json.Unmarshal(line.Payload, &probe); err != nil {
				continue // a message whose content is not an array, etc. — the decoder warns on these
			}
			switch probe.Type {
			case "message":
				if probe.Role == "user" {
					if text := codexFallbackHumanText(probe.Parts); text != "" {
						tally.filteredTexts = append(tally.filteredTexts, text)
					}
				}
			case "function_call", "custom_tool_call":
				if probe.CallID != "" {
					tally.callIDs[probe.CallID] = true
				}
			case "function_call_output", "custom_tool_call_output":
				tally.outputIDs = append(tally.outputIDs, probe.CallID)
			}
		case codexTypeEventMsg:
			var probe struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Item    struct {
					Type    string             `json:"type"`
					Content []codexContentPart `json:"content"`
				} `json:"item"`
			}
			require.NoError(t, json.Unmarshal(line.Payload, &probe), "line %d", i)
			switch probe.Type {
			case "user_message":
				if text := strings.TrimSpace(probe.Message); text != "" {
					tally.eventTexts = append(tally.eventTexts, text)
					tally.legacyEvents++
				}
			case "item_completed":
				if probe.Item.Type == "UserMessage" {
					if text := codexTextParts(probe.Item.Content); text != "" {
						tally.eventTexts = append(tally.eventTexts, text)
					}
				}
			case "task_complete":
				tally.taskComplete++
			case "task_started":
				tally.taskStarted++
			}
		}
	}
	return tally
}

func TestCodexCanary(t *testing.T) {
	if testing.Short() {
		t.Skip("real-corpus canary skipped under -short")
	}
	home := codexCanaryHome()
	if home == "" {
		t.Skip("no home directory")
	}
	files := discoverCodexCanaryFiles(t, home)
	if len(files) == 0 {
		t.Skipf("no Codex rollouts under %s/{sessions,archived_sessions}", home)
	}

	h := captureSlog(t)

	var (
		decoded, legacy, paginated, subagents, compressed, reverts int
		shells                                                     []string
		zeroHumanNonShell                                          []string
		humansTotal, assistantsTotal, resultsTotal                 int
		unmatchedResults                                           []string
		knownIDs                                                   = map[string]bool{}
		childLinks                                                 = map[string]string{} // child uuid → parent path
		mismatches                                                 []string
		scanned, ftsRows, openableMarkers, diffMarkers             int // consumer pass
	)
	for _, f := range files {
		if f.revert {
			reverts++
			t.Logf("revert variant recognised and skipped (v1): %s", f.path)
			continue
		}
		if f.compressed {
			compressed++
		}
		raw := readCodexCanaryFile(t, f)
		knownIDs[f.uuid] = true

		// Assumption 1: line 0 is session_meta, and its id is the filename uuid.
		p, err := DetectFormat(raw)
		require.NoError(t, err, f.path)
		assert.Equal(t, PlatformCodex, p, "Assumption 1: first line is session_meta: %s", f.path)

		tally := tallyCodexRaw(t, raw)
		assert.True(t, tally.firstLineIsMeta, "Assumption 1: %s", f.path)
		assert.Equal(t, f.uuid, tally.metaID, "Assumption 1: session_meta.id == filename uuid: %s", f.path)
		if tally.historyMode == "paginated" {
			paginated++
		} else {
			legacy++
		}

		tr, err := codexDecoder{}.Decode(bytes.NewReader(raw))
		require.NoError(t, err, f.path)
		require.NotNil(t, tr)
		decoded++
		assert.Equal(t, f.uuid, tr.Meta.PlatformID, f.path)
		assert.NotEmpty(t, tr.Meta.CWD, "session_meta.cwd: %s", f.path)
		assert.False(t, tr.Meta.StartTime.IsZero(), f.path)
		assert.False(t, tr.Meta.EndTime.IsZero(), f.path)
		assert.False(t, tr.Meta.EndTime.Before(tr.Meta.StartTime), "end before start: %s", f.path)

		humans, assistants, results := 0, 0, 0
		for _, e := range tr.Entries {
			switch e.Kind {
			case EntryHuman:
				humans++
			case EntryAssistant:
				assistants++
				for _, part := range e.Parts {
					if part.Call != nil && part.Call.Launch != nil && part.Call.Launch.ChildUUID != "" {
						childLinks[part.Call.Launch.ChildUUID] = f.path
					}
				}
			case EntryToolResult:
				results++
				// Assumption 3: every *_output call_id matches an earlier call.
				if e.CallID == "" || e.CallName == "" {
					unmatchedResults = append(unmatchedResults, fmt.Sprintf("%s line %d call_id=%q", f.path, e.LineIndex, e.CallID))
				}
			}
		}
		humansTotal += humans
		assistantsTotal += assistants
		resultsTotal += results
		for _, id := range tally.outputIDs {
			if !tally.callIDs[id] {
				unmatchedResults = append(unmatchedResults, fmt.Sprintf("%s raw output call_id=%q has no call", f.path, id))
			}
		}

		if tr.Meta.Source == "subagent" {
			subagents++
			assert.True(t, tally.isSubagent, f.path)
			assert.NotEmpty(t, tr.Meta.ParentUUID, "a thread_spawn child names its parent: %s", f.path)
			assert.NotEmpty(t, tr.Meta.TitleFallback, "a child titles itself from its prompt or agent identity: %s", f.path)
		} else {
			// Assumption 2 (non-subagent files): the event stream is complete —
			// event-derived prompts == noise-filtered response_item user messages,
			// and every legacy event text appears among them verbatim.
			if len(tally.eventTexts) != len(tally.filteredTexts) {
				mismatches = append(mismatches, fmt.Sprintf("%s: %d event prompts vs %d filtered response_item prompts (history_mode=%q)", f.path, len(tally.eventTexts), len(tally.filteredTexts), tally.historyMode))
			} else if tally.legacyEvents > 0 {
				set := map[string]bool{}
				for _, s := range tally.filteredTexts {
					set[s] = true
				}
				for _, s := range tally.eventTexts {
					if !set[s] {
						mismatches = append(mismatches, fmt.Sprintf("%s: legacy event text not among response items: %q", f.path, short(s)))
					}
				}
			}
			assert.Equal(t, len(tally.eventTexts), humans, "decoder human count == event prompt count (events present): %s", f.path)
		}

		switch {
		case humans == 0 && assistants == 0:
			// An aborted-at-startup shell: nothing was answered, so nothing was
			// completed either.
			assert.Zero(t, tally.taskComplete, "0-human/0-assistant file has a task_complete: %s", f.path)
			shells = append(shells, f.path)
		case humans == 0 && tr.Meta.Source != "subagent":
			zeroHumanNonShell = append(zeroHumanNonShell, f.path)
		}

		// Consumer pass (Slice 7.6): the platform-blind consumers over Codex bytes.
		// Success criterion 2 — no rollout with a human turn scans to zero
		// messages, and every human turn indexes as role=user; every non-shell
		// rollout is archivable (MessageCount ≥ 1); show names the platform; the
		// TUI transcript never panics and every resolved spawn is openable.
		out := ScanTranscript(tr)
		if humans > 0 || assistants > 0 {
			assert.GreaterOrEqual(t, out.MessageCount, 1, "non-shell rollout scans to zero messages: %s", f.path)
			scanned++
		}
		if humans > 0 {
			assert.NotEmpty(t, rowsWithRole(out.Results, roleUser), "rollout with a human turn has no role=user row: %s", f.path)
		}
		ftsRows += len(out.Results)
		if assistants > 0 {
			assert.Contains(t, RenderText(PlatformCodex, raw), "[Codex]", "show heading: %s", f.path)
		}
		for _, m := range ParseTranscript(PlatformCodex, raw, nil) {
			if m.Role == RoleSubagent && m.ChildUUID != "" {
				assert.True(t, m.Openable, "a marker with a ChildUUID must be openable: %s", f.path)
				openableMarkers++
			}
			if m.Diff {
				diffMarkers++
			}
		}
	}

	// Assumption 11: every parent-resolved child id names a discovered rollout.
	for child, parent := range childLinks {
		assert.True(t, knownIDs[child], "Assumption 11: child %s (spawned from %s) is not a discovered rollout", child, parent)
	}

	assert.Empty(t, mismatches, "Assumption 2 reconciliation:\n%s", strings.Join(mismatches, "\n"))
	assert.Empty(t, unmatchedResults, "Assumption 3 (call↔result correlation):\n%s", strings.Join(unmatchedResults, "\n"))
	assert.Empty(t, zeroHumanNonShell, "non-subagent files with assistant entries but no human turn (the zero-human warning cases):\n%s", strings.Join(zeroHumanNonShell, "\n"))
	assert.Empty(t, h.recordsWithMessage(codexWarnNoHuman), "the zero-human warning must never fire on the corpus")
	assert.Empty(t, h.messagesWithPrefix("vault codex decoder: skipping"), "no malformed lines or payloads in the corpus")
	assert.Empty(t, h.messagesWithPrefix("vault scanner: skipping oversize"))

	drift := h.recordsWithMessage(codexDebugDrift)
	driftTypes := map[string]int{}
	for _, r := range drift {
		if types, ok := recordAttrs(r)["types"].([]string); ok {
			for _, ty := range types {
				driftTypes[ty]++
			}
		}
	}
	shellLevel := len(h.recordsWithMessage(codexDebugShell))

	t.Logf("codex canary: %d rollouts decoded (%d legacy, %d paginated, %d sub-agents, %d compressed, %d revert variants skipped) under %s",
		decoded, legacy, paginated, subagents, compressed, reverts, home)
	t.Logf("codex canary: %d human, %d assistant, %d tool-result entries; %d parent→child links resolved", humansTotal, assistantsTotal, resultsTotal, len(childLinks))
	t.Logf("codex canary: %d aborted-at-startup shells (0 human, 0 assistant; %d logged at debug); %d files fingerprinted unknown record types: %v",
		len(shells), shellLevel, len(drift), driftTypes)
	t.Logf("codex canary consumers: %d non-shell rollouts scan to ≥ 1 message (%d FTS rows); %d openable child markers, %d apply_patch diff markers",
		scanned, ftsRows, openableMarkers, diffMarkers)
	assert.Equal(t, len(childLinks), openableMarkers, "every resolved launch is exactly one openable marker")
	if len(driftTypes) > 0 {
		t.Logf("codex canary: unknown record types are skipped (ADR-021); add them to codex_types.go's known-type sets once inspected")
	}
}

// TestCodexCanary_Discovery runs the Codex DISCOVERER (Slice 7.2) over the real
// corpus. Discovery has the same skip-never-fail contract as the decoder (an
// unparseable name or first line is a warning plus skip), so — as the Task 6
// canary taught — "no error" proves nothing: the test asserts ZERO warnings of
// any kind, that every base rollout the canary's own walk sees is discovered
// with a project hint, and the design's cold-discovery time bound (Assumption
// 12: all first lines read, well under the sweep's budget).
func TestCodexCanary_Discovery(t *testing.T) {
	if testing.Short() {
		t.Skip("real-corpus canary skipped under -short")
	}
	home := codexCanaryHome()
	if home == "" {
		t.Skip("no home directory")
	}
	files := discoverCodexCanaryFiles(t, home)
	if len(files) == 0 {
		t.Skipf("no Codex rollouts under %s/{sessions,archived_sessions}", home)
	}
	wantUUIDs := map[string]bool{}
	var wantReverts int
	for _, f := range files {
		if f.revert {
			wantReverts++
			continue
		}
		wantUUIDs[f.uuid] = true
	}

	h := captureSlog(t)
	start := time.Now()
	sessions, report, err := DiscoverCodexSessions(home, CodexDiscoverOptions{})
	elapsed := time.Since(start)
	require.NoError(t, err)

	for _, r := range allRecords(h) {
		assert.NotEqual(t, slog.LevelWarn, r.Level, "discovery must not warn on the real corpus: %s %v", r.Message, recordAttrs(r))
	}

	gotUUIDs := map[string]bool{}
	var noHint []string
	for _, sf := range sessions {
		gotUUIDs[sf.UUID] = true
		assert.Equal(t, PlatformCodex, sf.Platform)
		assert.True(t, strings.HasPrefix(sf.RelativePath, "sessions/") || strings.HasPrefix(sf.RelativePath, "archived_sessions/"),
			"RelativePath must be home-relative: %s", sf.RelativePath)
		assert.False(t, strings.HasSuffix(sf.RelativePath, ".zst"), "RelativePath must strip .zst: %s", sf.RelativePath)
		assert.Positive(t, sf.OnDiskSize, sf.Path)
		if sf.ProjectPath == "" {
			noHint = append(noHint, sf.RelativePath)
		}
	}
	// Assumption 1 (line 0 is session_meta) implies every rollout yields a cwd.
	assert.Empty(t, noHint, "rollouts discovered without a project hint")
	assert.Equal(t, wantUUIDs, gotUUIDs, "discovered thread set differs from the canary's own walk")
	assert.Len(t, report.SkippedRevertVariants, wantReverts)
	assert.Equal(t, len(sessions), report.FirstLineReads, "manual discovery (no predicate) reads exactly one first line per rollout")
	assert.Equal(t, 0, report.SkippedByPredicate)

	// Assumption 12: a cold discovery over the whole corpus fits comfortably
	// inside the sweep's 30-second budget.
	assert.Less(t, elapsed, 5*time.Second, "cold codex discovery over %d rollouts", len(sessions))
	t.Logf("codex canary: discovered %d rollouts (%d first-line reads, %d revert variants skipped) in %s",
		len(sessions), report.FirstLineReads, len(report.SkippedRevertVariants), elapsed)
}
