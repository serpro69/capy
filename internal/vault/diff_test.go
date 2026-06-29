package vault

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffBodyFromToolResult_Edit(t *testing.T) {
	// An Edit-shaped toolUseResult (filePath/oldString/newString/structuredPatch).
	raw := mustJSON(t, map[string]any{
		"filePath":  "/p/tasks.md",
		"oldString": "x", "newString": "y",
		"structuredPatch": []map[string]any{
			{"oldStart": 244, "oldLines": 2, "newStart": 244, "newLines": 2, "lines": []string{
				" context line", "-old status", "+new status",
			}},
		},
	})

	body, added, removed, ok := diffBodyFromToolResult(raw)
	require.True(t, ok)
	assert.Equal(t, 1, added)
	assert.Equal(t, 1, removed)
	assert.Contains(t, body, "@@ -244,2 +244,2 @@", "hunk header is emitted")
	assert.Contains(t, body, "-old status")
	assert.Contains(t, body, "+new status")
	assert.Contains(t, body, " context line", "context lines keep their leading-space prefix")
}

func TestDiffBodyFromToolResult_Write(t *testing.T) {
	// A Write-shaped toolUseResult (content/type/structuredPatch) — same diff path.
	raw := mustJSON(t, map[string]any{
		"filePath": "/p/new.go", "type": "create", "content": "package p\n",
		"structuredPatch": []map[string]any{
			{"oldStart": 1, "oldLines": 0, "newStart": 1, "newLines": 1, "lines": []string{"+package p"}},
		},
	})

	body, added, removed, ok := diffBodyFromToolResult(raw)
	require.True(t, ok)
	assert.Equal(t, 1, added)
	assert.Equal(t, 0, removed)
	assert.Contains(t, body, "+package p")
}

func TestDiffBodyFromToolResult_MultipleHunks(t *testing.T) {
	raw := mustJSON(t, map[string]any{
		"structuredPatch": []map[string]any{
			{"oldStart": 1, "oldLines": 1, "newStart": 1, "newLines": 1, "lines": []string{"-a", "+b"}},
			{"oldStart": 9, "oldLines": 1, "newStart": 9, "newLines": 2, "lines": []string{" c", "+d", "+e"}},
		},
	})

	body, added, removed, ok := diffBodyFromToolResult(raw)
	require.True(t, ok)
	assert.Equal(t, 3, added, "+b, +d, +e across both hunks")
	assert.Equal(t, 1, removed, "-a")
	assert.Contains(t, body, "@@ -1,1 +1,1 @@")
	assert.Contains(t, body, "@@ -9,1 +9,2 @@", "each hunk emits its own header")
}

func TestDiffBodyFromToolResult_NoPatch(t *testing.T) {
	// Empty, absent-patch, and malformed inputs all signal a fallback (ok=false).
	for name, raw := range map[string]json.RawMessage{
		"nil":           nil,
		"empty object":  mustJSON(t, map[string]any{}),
		"no hunks":      mustJSON(t, map[string]any{"filePath": "/p/x", "structuredPatch": []any{}}),
		"malformed":     json.RawMessage(`{not json`),
		"patch is null": mustJSON(t, map[string]any{"structuredPatch": nil}),
		"empty hunks": mustJSON(t, map[string]any{"structuredPatch": []map[string]any{
			{"oldStart": 1, "oldLines": 0, "newStart": 1, "newLines": 0, "lines": []string{}},
		}}),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, ok := diffBodyFromToolResult(raw)
			assert.False(t, ok)
		})
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
