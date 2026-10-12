package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestDoctorCodexPlatformParity(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, variant := range []string{"complete", "partial", "disabled", "invalid owner"} {
		t.Run(variant, func(t *testing.T) {
			dir := stdioProject(t)
			_, env := installCodexLaunch(t, bin, dir)
			for i, entry := range env {
				if strings.HasPrefix(entry, "CAPY_DB_KEY=") {
					env[i] = "CAPY_DB_KEY=synthetic-doctor-parity-key"
				}
			}
			switch variant {
			case "partial":
				path := filepath.Join(dir, ".codex/hooks.json")
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				var root map[string]any
				require.NoError(t, json.Unmarshal(data, &root))
				delete(root["hooks"].(map[string]any), "SessionEnd")
				data, err = json.Marshal(root)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0o600))
			case "disabled":
				path := filepath.Join(dir, ".codex/config.toml")
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, append(data, []byte("\n[features]\nhooks=false\n")...), 0o600))
			case "invalid owner":
				require.NoError(t, os.Remove(filepath.Join(dir, ".codex/capy-owner")))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cli := exec.CommandContext(ctx, bin, "doctor", "--project-dir", dir)
			cli.Dir, cli.Env = dir, env
			output, err := cli.CombinedOutput()
			require.NoError(t, err, "%s", output)
			// Direct serve remains usable for diagnostics of damaged registration.
			// No generated handler or host trust gate is bypassed by this fixture.
			p := startStdioProcess(t, stdioCommand{
				executable: bin, args: []string{"serve", "--project-dir", dir}, dir: dir, env: env,
			})
			p.initialize(t)
			mcp := p.tool(t, "capy_doctor", map[string]any{})
			p.close(t)
			for _, check := range platform.CheckPlatformRegistrations(dir) {
				require.Contains(t, string(output), check.String())
				require.Contains(t, mcp, check.String())
			}
			for _, report := range []string{string(output), mcp} {
				require.NotContains(t, report, "Claude Code hooks")
				require.NotContains(t, report, ".mcp.json not found")
				require.Contains(t, report, "Knowledge credential:")
				require.Contains(t, report, "Vault: disabled")
				switch variant {
				case "complete":
					require.Contains(t, report, "[x] Codex hooks: 5/5")
					require.Contains(t, report, "[x] Codex MCP: registered")
				case "partial":
					require.Contains(t, report, "[-] Codex hooks: 4/5")
				case "disabled":
					require.Contains(t, report, "visible features.hooks = false")
				case "invalid owner":
					require.Contains(t, report, "[ ] Codex hooks:")
					require.Contains(t, report, "no adjacent .codex/capy-owner")
				}
			}
		})
	}
}
