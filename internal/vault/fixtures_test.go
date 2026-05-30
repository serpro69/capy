package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// jsonlBytes renders lines as newline-delimited compact JSON — the on-disk form
// of a session file. Tests write these into t.TempDir() so they never depend on
// a real ~/.claude (CI has none).
func jsonlBytes(t *testing.T, lines ...map[string]any) []byte {
	t.Helper()
	var sb strings.Builder
	for _, l := range lines {
		b, err := json.Marshal(l)
		require.NoError(t, err)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// aiTitleLine builds an ai-title entry (no timestamp, matching real sessions).
func aiTitleLine(title string) map[string]any {
	return map[string]any{"type": "ai-title", "aiTitle": title, "sessionId": "s1"}
}

// userToolResultLine builds a user entry carrying a single tool_result block —
// the shape ≈86% of real user lines take. Its text indexes as role="tool".
func userToolResultLine(uuid, text string) map[string]any {
	return map[string]any{
		"type": "user", "uuid": uuid, "timestamp": "2026-05-01T10:00:10Z",
		"cwd": "/home/user/proj", "gitBranch": "feature/x",
		"message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "t1", "content": text},
		}},
	}
}

// writeSession writes <projectDir>/<uuid>.jsonl plus any sidecars under
// <projectDir>/<uuid>/ (keys are session-relative slash paths).
func writeSession(t *testing.T, projectDir, uuid string, mainJSONL []byte, sidecars map[string][]byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, uuid+".jsonl"), mainJSONL, 0o644))
	for rel, content := range sidecars {
		p := filepath.Join(projectDir, uuid, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, content, 0o644))
	}
}

// sampleMainJSONL is a representative single-session transcript: a human turn,
// an assistant turn with text + a tool_use, a tool_result, and an ai-title.
func sampleMainJSONL(t *testing.T) []byte {
	t.Helper()
	return jsonlBytes(t,
		userLine("u1", "/home/user/proj", "feature/x", "Please fix the timeout bug"),
		assistantLine("a1", "msg1", []map[string]any{
			{"type": "text", "text": "Reading the config first."},
			{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"file_path": "/proj/config.toml"}},
		}),
		userToolResultLine("u2", "build log: pterodactyl error at line 5"),
		aiTitleLine("Fix the timeout bug"),
	)
}
