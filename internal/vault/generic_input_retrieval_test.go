package vault

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenericInputSummary_NoRetrievalDegradation is the feature-specific
// ranking/false-positive gate for the generic tool-input summary (tasks.md Task
// 3.1). The text-only vault bench harness (bench_test.go) synthesizes every
// assistant turn as a plain text block and never emits tool_use blocks, so it
// never calls toolUseSummary/genericInputSummary and cannot catch BM25
// degradation from low-signal generic keys (implementation.md § Task 3 —
// "make bench-quality is NOT a valid gate for this change").
//
// It drives the real scan+index path (writeSession -> importFixture runs
// ScanSession) and asserts two properties the bounding is meant to guarantee:
//
//	(a) No false positives: many sessions whose assistant rows carry only
//	    low-signal generic tool-call boilerplate (limit/all_projects/…) must not
//	    match a salient needle they never mention.
//	(b) Recall preserved: a salient needle embedded inside an MCP tool input,
//	    amid several generic keys, stays retrievable — the bounded summary does
//	    not drop or bury it relative to the same needle in a short Bash row.
func TestGenericInputSummary_NoRetrievalDegradation(t *testing.T) {
	s := newTestVault(t)
	root := t.TempDir()
	projDir := filepath.Join(root, "-home-user-proj")

	// A single distinctive token sidesteps phrase-vs-AND query semantics: it is
	// present iff the generic summary indexed the salient value.
	const needle = "quetzalcoatlus"

	cleanUUID := "aaaaaaaa-0000-0000-0000-000000000001"
	noisyUUID := "aaaaaaaa-0000-0000-0000-000000000002"

	// cleanNeedle: the salient token in a short Bash command (no generic-key bloat).
	writeSession(t, projDir, cleanUUID, jsonlBytes(t,
		userLine("u1", "/home/user/proj", "main", "restore it"),
		assistantLine("a1", "m1", []map[string]any{
			{"type": "tool_use", "id": "t1", "name": "Bash",
				"input": map[string]any{"command": "./scripts/" + needle + ".sh"}},
		}),
		aiTitleLine("Clean needle"),
	), nil)

	// noisyNeedle: the SAME salient token inside an MCP capy_search input carrying
	// several low-signal generic keys — exactly the shape bounding must keep small.
	writeSession(t, projDir, noisyUUID, jsonlBytes(t,
		userLine("u1", "/home/user/proj", "main", "search it"),
		assistantLine("a1", "m1", []map[string]any{
			{"type": "tool_use", "id": "t1", "name": "mcp__capy__capy_search", "input": map[string]any{
				"queries":       []string{needle + " migration"},
				"source":        "kk:arch-decisions",
				"limit":         3,
				"all_projects":  false,
				"include_kinds": []string{"durable", "session"},
				"project":       "capy",
			}},
		}),
		aiTitleLine("Noisy needle"),
	), nil)

	// Noise sessions: MCP calls with only low-signal generic keys, no needle term.
	const noiseSessions = 8
	for i := range noiseSessions {
		uuid := fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", i)
		writeSession(t, projDir, uuid, jsonlBytes(t,
			userLine("u1", "/home/user/proj", "main", "routine work"),
			assistantLine("a1", "m1", []map[string]any{
				{"type": "tool_use", "id": "t1", "name": "mcp__capy__capy_search", "input": map[string]any{
					"limit":         3,
					"all_projects":  false,
					"include_kinds": []string{"durable", "session"},
					"project":       "capy",
				}},
			}),
			aiTitleLine("Routine"),
		), nil)
	}

	require.Equal(t, noiseSessions+2, importFixture(t, s, root, ImportOptions{}).Imported)

	hits, err := s.Search(context.Background(), SearchOptions{Query: needle, Limit: 20})
	require.NoError(t, err)

	got := make(map[string]bool, len(hits))
	for _, h := range hits {
		got[h.SessionUUID] = true
	}

	// (b) recall preserved for both the short-row and the generic-summary needle.
	assert.True(t, got[cleanUUID], "the clean-needle session must be retrievable")
	assert.True(t, got[noisyUUID],
		"the needle embedded in a bounded generic summary must stay retrievable")
	// (a) no false positives from the low-signal generic-key noise sessions.
	assert.Len(t, got, 2,
		"the %d generic-key noise sessions must not produce false-positive hits for the salient needle", noiseSessions)
}
