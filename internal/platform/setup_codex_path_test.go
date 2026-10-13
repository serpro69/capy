package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexSetupPathPrerequisite(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"missing", "", "PATH"},
		{"not executable", "exit 0", "PATH"},
		{"incompatible", "exit 42", "probe failed"},
		{"wrong response", "printf 'wrong\\n'", "unexpected"},
		{"no newline", "printf '" + CodexLaunchProtocol + "'", "unexpected"},
		{"stderr response", "printf '" + CodexLaunchProtocol + "\\n' >&2", "unexpected"},
		{"timeout", "exec /bin/sleep 20", "within 2s"},
		{"stdout overflow", "while :; do printf '01234567890123456789'; done", "exceeded"},
		{"combined overflow", "printf '%03000d' 1; printf '%03000d' 1 >&2", "exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pathDir, project := t.TempDir(), t.TempDir()
			t.Setenv("PATH", pathDir)
			if tc.body != "" {
				mode := os.FileMode(0o755)
				if tc.name == "not executable" {
					mode = 0o644
				}
				require.NoError(t, os.WriteFile(filepath.Join(pathDir, "capy"), []byte("#!/bin/sh\n"+tc.body+"\n"), mode))
			}
			writeCodexFixture(t, project, ".capy.toml", "[store]\nkey_file='fixture.key'\n")
			key := filepath.Join(project, "fixture.key")
			require.NoError(t, os.WriteFile(key, []byte("synthetic"), 0o600))
			require.NoError(t, os.Chmod(key, 0o644))
			writeCodexFixture(t, project, codexOwnerRelPath, codexOwnerContent(testCodexOwnerID))
			start := time.Now()
			err := SetupCodex("", project)
			require.ErrorContains(t, err, tc.want)
			require.Less(t, time.Since(start), 3*time.Second)
			info, err := os.Stat(key)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "prerequisite failure must precede chmod")
			marker, err := os.ReadFile(filepath.Join(project, codexOwnerRelPath))
			require.NoError(t, err)
			require.Equal(t, codexOwnerContent(testCodexOwnerID), string(marker))
			require.NoDirExists(t, filepath.Join(project, ".capy"))
			require.NoFileExists(t, filepath.Join(project, ".codex/config.toml"))
			require.NoFileExists(t, filepath.Join(project, codexHooksRelPath))
		})
	}
	t.Run("matching binary and one probe per prepared invocation", func(t *testing.T) {
		pathDir, project := t.TempDir(), t.TempDir()
		t.Setenv("PATH", pathDir)
		candidate := filepath.Join(pathDir, "capy")
		calls := filepath.Join(pathDir, "calls")
		body := "#!/bin/sh\n[ \"$1\" = codex-launch ] && [ \"$2\" = --check-protocol ] && [ \"$3\" = '" + CodexLaunchProtocol + "' ] || exit 42\nprintf x >> '" + calls + "'\nprintf '%s\\n' \"$3\"\n"
		require.NoError(t, os.WriteFile(candidate, []byte(body), 0o755))
		alias := filepath.Join(pathDir, "alias")
		require.NoError(t, os.Symlink(candidate, alias))
		setup, err := PrepareCodexSetup(context.Background(), alias)
		require.NoError(t, err)
		require.NoError(t, setup.Apply(project))
		before, err := os.ReadFile(filepath.Join(project, codexOwnerRelPath))
		require.NoError(t, err)
		require.NoError(t, setup.Apply(project))
		after, err := os.ReadFile(filepath.Join(project, codexOwnerRelPath))
		require.NoError(t, err)
		require.Equal(t, before, after)
		data, err := os.ReadFile(calls)
		require.NoError(t, err)
		require.Equal(t, "x", string(data))
		other := filepath.Join(pathDir, "different")
		require.NoError(t, os.WriteFile(other, []byte(body), 0o755))
		_, err = PrepareCodexSetup(context.Background(), other)
		require.ErrorContains(t, err, "--binary cannot override")
	})
	t.Run("unprepared writer rejected", func(t *testing.T) {
		require.Error(t, (&CodexSetup{}).Apply(t.TempDir()))
	})
}

func TestCodexFinalLaunchBindings(t *testing.T) {
	for _, mode := range []string{"hook", "serve"} {
		event, command := "", "capy"
		if mode == "hook" {
			event, command = "pretooluse", "bash"
		}
		args := codexLaunchArgs(testCodexOwnerID, mode, event)
		t.Run(mode, func(t *testing.T) {
			binding, owned, err := recognizeCodexInvocation(command, args)
			require.NoError(t, err)
			require.True(t, owned)
			require.Equal(t, CodexBinding{ID: testCodexOwnerID, Mode: mode, Event: event}, binding)
			for _, field := range []string{"protocol", "id", "mode", "event", "extra", "executable"} {
				t.Run(field, func(t *testing.T) {
					bad := append([]string(nil), args...)
					cmd := command
					base := 1
					if mode == "hook" {
						base = 2
					}
					switch field {
					case "protocol":
						bad[base] = "capy-codex-launch-v999"
					case "id":
						bad[base+1] = "invalid"
					case "mode":
						bad[base+2] = "other"
					case "event":
						bad[base+3] = "other"
					case "extra":
						bad = append(bad, "extra")
					case "executable":
						cmd = "other"
					}
					_, owned, err := recognizeCodexInvocation(cmd, bad)
					require.True(t, owned)
					require.Error(t, err)
				})
			}
		})
	}
	for _, forbidden := range []string{"HOME", "/opt/homebrew", "scripts/capy.sh", "command -v"} {
		require.NotContains(t, codexBootstrap, forbidden)
	}
	require.Less(t, len(codexBootstrap), 1100)
	require.NotContains(t, strings.Join(codexLaunchArgs(testCodexOwnerID, "serve", ""), " "), "bash")
}
