package server

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreparedReadPolicyDirectFiles(t *testing.T) {
	for _, origin := range []string{"project", "user"} {
		t.Run(origin, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			settingsDir := filepath.Join(project, ".claude")
			pattern := "Read(secret.txt)"
			if origin == "user" {
				settingsDir = filepath.Join(home, ".claude")
				pattern = "Read(/secret.txt)"
			}
			require.NoError(t, os.MkdirAll(settingsDir, 0o755))
			require.NoError(t, os.WriteFile(settingsDir+"/settings.json", []byte(`{"permissions":{"deny":["`+pattern+`"]}}`), 0o600))
			target := filepath.Join(project, "secret.txt")
			if origin == "user" {
				target = settingsDir + "/secret.txt"
			}
			require.NoError(t, os.WriteFile(target, []byte("forbiddenpolicytoken"), 0o600))
			require.NoError(t, os.Symlink(target, project+"/innocent.txt"))
			srv := newTestServerWithProjectDir(t, nil, project)
			rel, err := filepath.Rel(project, target)
			require.NoError(t, err)
			for _, path := range []string{rel, target, "innocent.txt", project + "/innocent.txt"} {
				result := callIndex(t, srv, map[string]any{"path": path})
				require.True(t, result.IsError, "%s: %s", path, resultText(result))
				assert.Contains(t, resultText(result), "Read deny pattern")
				assert.Contains(t, resultText(result), settingsDir)
				marker := project + "/spawned"
				result = callExecuteFile(t, srv, map[string]any{
					"path": path, "language": "shell", "code": "touch " + marker,
				})
				require.True(t, result.IsError)
				assert.Contains(t, resultText(result), "Read deny pattern")
				_, err := os.Stat(marker)
				require.True(t, os.IsNotExist(err), "denied read must not spawn")
			}
			results, err := srv.getStore().SearchWithFallback("forbiddenpolicytoken", 5, store.SearchOptions{})
			require.NoError(t, err)
			assert.Empty(t, results)
		})
	}
}

func TestPreparedReadPolicyInvalidBlocksOnlyFileAdmission(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(project+"/.claude", 0o755))
	require.NoError(t, os.WriteFile(project+"/.claude/settings.json", []byte(`{"permissions":{"deny":["Read([ab])"]}}`), 0o600))
	srv := newTestServerWithProjectDir(t, nil, project)
	result := callIndex(t, srv, map[string]any{"path": "missing.txt"})
	require.True(t, result.IsError)
	assert.Contains(t, resultText(result), "invalid Read policy")
	result = callExecuteFile(t, srv, map[string]any{"path": "missing.txt", "language": "shell", "code": "echo nope"})
	require.True(t, result.IsError)
	assert.Contains(t, resultText(result), "invalid Read policy")
	result = callIndex(t, srv, map[string]any{"content": "inline notes", "source": "inline"})
	require.False(t, result.IsError, resultText(result))
}

func TestPreparedReadPolicyStaleRefresh(t *testing.T) {
	for _, policy := range []string{
		`{"permissions":{"deny":["Read(./secret.txt)"]}}`,
		`{"permissions":{"deny":["Read([unsupported])"]}}`,
	} {
		t.Run(policy, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			project := t.TempDir()
			path := project + "/secret.txt"
			require.NoError(t, os.WriteFile(path, []byte("originalpolicytoken"), 0o600))
			first := newTestServerWithProjectDir(t, nil, project)
			result := callIndex(t, first, map[string]any{"path": path, "source": "retained"})
			require.False(t, result.IsError, resultText(result))
			require.NoError(t, first.getStore().Close())
			require.NoError(t, os.MkdirAll(project+"/.claude", 0o755))
			require.NoError(t, os.WriteFile(project+"/.claude/settings.json", []byte(policy), 0o600))
			require.NoError(t, os.WriteFile(path, []byte("forbiddenreplacementtoken"), 0o600))
			future := time.Now().Add(time.Minute)
			require.NoError(t, os.Chtimes(path, future, future))
			second := newTestServerWithProjectDir(t, nil, project)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			results, err := second.getStore().SearchWithFallback("originalpolicytoken", 5, store.SearchOptions{Source: "retained"})
			require.NoError(t, err)
			require.NotEmpty(t, results, "denied refresh must retain cached content")
			assert.Contains(t, logs.String(), "Read policy blocked file")
			results, err = second.getStore().SearchWithFallback("forbiddenreplacementtoken", 5, store.SearchOptions{})
			require.NoError(t, err)
			assert.Empty(t, results)
		})
	}
}
