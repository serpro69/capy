package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/require"
)

// Task 14 installs classification/config only. Task 15 replaces this guard with
// enforcement fixtures after integrating the complete security/routing pipeline.
func TestRoutineGateRemainsInactive(t *testing.T) {
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	for _, a := range []adapter.HookAdapter{&adapter.CodexAdapter{}, &adapter.ClaudeCodeAdapter{}} {
		t.Run(string(a.Platform()), func(t *testing.T) {
			project := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[routing.routine]\nenabled=true\n"), 0600))
			out, err := handleEvent("pretooluse", hookInput(t, "Bash", map[string]any{"command": "make test"}, nil), a, &project)
			require.NoError(t, err)
			if out != nil {
				require.NotEqual(t, "deny", ccParse(t, out)["permissionDecision"])
			}
		})
	}
}
