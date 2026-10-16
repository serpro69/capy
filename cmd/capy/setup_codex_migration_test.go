package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
)

func TestSetupCodexLegacyMCPCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, mode := range []string{"codex", "both local"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex/scripts"), 0o755))
			legacy := "[tui]\nanimations = false\n\n[mcp_servers.\"capy\"]\ncommand = 'bash'\nargs = ['.codex/scripts/capy.sh', 'serve']\nenv_vars = ['CAPY_DB_KEY', 'CAPY_VAULT_KEY']\n"
			configPath := filepath.Join(dir, ".codex/config.toml")
			require.NoError(t, os.WriteFile(configPath, []byte(legacy), 0o600))
			wrapper := filepath.Join(dir, ".codex/scripts/capy.sh")
			wrapperText := "#!/bin/sh\necho invoked > wrapper-invoked\nexit 99\n"
			require.NoError(t, os.WriteFile(wrapper, []byte(wrapperText), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte("[store]\npath = '.capy/knowledge.db'\n"), 0o600))
			env := stdioEnv(t, "")
			env[0] = "PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH")
			args := []string{"setup", "--project-dir", dir}
			if mode == "codex" {
				args = append(args, "--platform", "codex")
			} else {
				args = append(args, "--local")
			}
			setup := func() {
				t.Helper()
				cmd := exec.Command(bin, args...)
				cmd.Dir, cmd.Env = dir, env
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", output)
				require.Contains(t, string(output), "Codex setup complete")
				if mode == "both local" {
					require.Contains(t, string(output), "Claude Code setup complete")
				}
			}
			setup()
			before := make(map[string][]byte)
			for _, rel := range []string{".codex/capy-owner", ".codex/hooks.json", ".codex/config.toml"} {
				data, err := os.ReadFile(filepath.Join(dir, rel))
				require.NoError(t, err)
				before[rel] = data
			}
			setup()
			for rel, expected := range before {
				data, err := os.ReadFile(filepath.Join(dir, rel))
				require.NoError(t, err)
				require.Equal(t, expected, data)
			}
			require.NoFileExists(t, filepath.Join(dir, ".capy/knowledge.db"))
			code, out, stderr := runLaunch(t, dir, env, []string{"-c", launchHookCommand(t, dir, "SessionStart")}, `{"session_id":"migration-fixture","source":"startup"}`)
			require.Zero(t, code, "%s", stderr)
			require.Contains(t, out, "capy_execute")
			var config struct {
				Servers map[string]struct {
					Command string   `toml:"command"`
					Args    []string `toml:"args"`
				} `toml:"mcp_servers"`
			}
			require.NoError(t, toml.Unmarshal(before[".codex/config.toml"], &config))
			require.Equal(t, "capy", config.Servers["capy"].Command)
			p := startStdioProcess(t, stdioCommand{
				executable: bin, args: config.Servers["capy"].Args, dir: dir,
				env: stdioEnv(t, "synthetic-legacy-migration-key"),
			})
			p.initialize(t)
			require.Contains(t, p.tool(t, "capy_execute", map[string]any{"language": "shell", "code": "pwd"}), dir+"\n")
			p.close(t)
			require.NoFileExists(t, filepath.Join(dir, "wrapper-invoked"))
			data, err := os.ReadFile(wrapper)
			require.NoError(t, err)
			require.Equal(t, wrapperText, string(data), "obsolete wrapper is neither executed nor removed")
		})
	}
}
