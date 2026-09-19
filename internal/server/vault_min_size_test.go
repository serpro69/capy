package server

import (
	"context"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVaultSweepMinSessionBytes(t *testing.T) {
	for _, platform := range []vault.Platform{vault.PlatformClaudeCode, vault.PlatformCodex} {
		t.Run(string(platform), func(t *testing.T) {
			var project string
			want := 1
			if platform == vault.PlatformClaudeCode {
				project, _, _ = setupVaultSweepProject(t)
				t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
				want = 2
			} else {
				home := setupCodexSweepEnv(t)
				project = t.TempDir()
				const uuid = "01900000-0000-7000-8000-000000000001"
				writeCodexRollout(t, home, codexRolloutRel(uuid), codexRolloutLines(t, uuid, project, "hi"))
			}
			srv := newTestServerWithProjectDir(t, nil, project)
			srv.config.Vault.MinSessionBytes = 1 << 20
			sum := srv.vaultSweep(context.Background())
			assert.Equal(t, want, sum.claude.Excluded+sum.codex.Excluded)
			assert.Zero(t, sum.claude.Errors+sum.codex.Errors)
			sessions, err := srv.getVault().ListSessions(context.Background(), vault.ListOptions{})
			require.NoError(t, err)
			assert.Empty(t, sessions)

			srv.config.Vault.MinSessionBytes = 0
			sum = srv.vaultSweep(context.Background())
			assert.Equal(t, want, sum.claude.Imported+sum.codex.Imported)
		})
	}
}
