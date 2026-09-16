package platform

import (
	"errors"
	"go/build"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shared knowledge-base / vault checks used by both `capy doctor` and the
// capy_doctor MCP tool (issue #82: the two surfaces had diverged).

func TestCheckKnowledgeBaseStats(t *testing.T) {
	r := CheckKnowledgeBaseStats(12, 340)
	assert.Equal(t, Pass, r.Status)
	assert.Equal(t, "Knowledge base", r.Name)
	assert.Equal(t, "12 sources, 340 chunks", r.Detail)
}

func TestCheckKnowledgeBaseError(t *testing.T) {
	r := CheckKnowledgeBaseError(errors.New("CAPY_DB_KEY not set"))
	assert.Equal(t, Warn, r.Status)
	assert.Equal(t, "Knowledge base", r.Name)
	assert.Contains(t, r.Detail, "error reading stats (CAPY_DB_KEY not set)")
}

func TestCheckLegacySessions(t *testing.T) {
	r := CheckLegacySessions(3, "capy cleanup --kind session --force")
	assert.Equal(t, Warn, r.Status)
	assert.Equal(t, "Legacy sessions", r.Name)
	assert.Contains(t, r.Detail, "3 legacy knowledge.db session row(s)")
	assert.Contains(t, r.Detail, "`capy cleanup --kind session --force`")
}

func TestCheckVaultDisabled(t *testing.T) {
	r := CheckVaultDisabled()
	assert.Equal(t, Warn, r.Status)
	assert.Equal(t, "Vault", r.Name)
	assert.Contains(t, r.Detail, "CAPY_VAULT_KEY not set")
}

func TestCheckVault(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		r := CheckVault(0, 0, 0, errors.New("boom"))
		assert.Equal(t, Warn, r.Status)
		assert.Contains(t, r.Detail, "error reading stats (boom)")
	})

	t.Run("healthy", func(t *testing.T) {
		r := CheckVault(7, 0, 3, nil)
		assert.Equal(t, Pass, r.Status)
		assert.Equal(t, "7 sessions archived", r.Detail)
	})

	t.Run("reindex backlog", func(t *testing.T) {
		r := CheckVault(7, 2, 3, nil)
		assert.Equal(t, Warn, r.Status)
		assert.Contains(t, r.Detail, "7 sessions archived")
		assert.Contains(t, r.Detail, "2 indexed by an older version (v3)")
		assert.Contains(t, r.Detail, "`capy vault reindex`")
	})
}

// CheckVaultPlatforms (codex-vault-sessions Task 12): which platform session
// roots exist on disk and how many sessions of each are archived.
func TestCheckVaultPlatforms(t *testing.T) {
	both := []VaultPlatformRoot{
		{Name: "claude-code", Root: "/home/u/.claude/projects", RootExists: true, Archived: 42},
		{Name: "codex", Root: "/home/u/.codex", RootExists: true, Archived: 145},
	}

	t.Run("both roots present", func(t *testing.T) {
		r := CheckVaultPlatforms(both, nil)
		assert.Equal(t, Pass, r.Status)
		assert.Equal(t, "Vault platforms", r.Name)
		assert.Equal(t,
			"claude-code: 42 archived (/home/u/.claude/projects); codex: 145 archived (/home/u/.codex)",
			r.Detail)
	})

	t.Run("codex root absent stays pass", func(t *testing.T) {
		roots := []VaultPlatformRoot{
			both[0],
			{Name: "codex", Root: "/home/u/.codex", RootExists: false, Archived: 3},
		}
		r := CheckVaultPlatforms(roots, nil)
		assert.Equal(t, Pass, r.Status, "a Claude-only machine has no Codex home — that is not a problem")
		assert.Contains(t, r.Detail, "claude-code: 42 archived (/home/u/.claude/projects)")
		// Rows merged from another machine still count even though this
		// machine cannot sweep them; the root is named so the user knows why.
		assert.Contains(t, r.Detail, "codex: 3 archived (/home/u/.codex absent — not swept)")
	})

	t.Run("no root at all warns", func(t *testing.T) {
		roots := []VaultPlatformRoot{
			{Name: "claude-code", Root: "/home/u/.claude/projects"},
			{Name: "codex", Root: "/home/u/.codex"},
		}
		r := CheckVaultPlatforms(roots, nil)
		assert.Equal(t, Warn, r.Status)
		assert.Contains(t, r.Detail, "no platform session root found on disk")
		assert.Contains(t, r.Detail, "claude-code: 0 archived (/home/u/.claude/projects absent — not swept)")
		assert.Contains(t, r.Detail, "codex: 0 archived (/home/u/.codex absent — not swept)")
	})

	t.Run("empty input warns like no roots", func(t *testing.T) {
		r := CheckVaultPlatforms(nil, nil)
		assert.Equal(t, Warn, r.Status)
		assert.Contains(t, r.Detail, "no platform session root found on disk")
	})

	t.Run("resolution error", func(t *testing.T) {
		r := CheckVaultPlatforms(nil, errors.New("resolving codex home: $HOME is not defined"))
		assert.Equal(t, Warn, r.Status)
		assert.Equal(t, "Vault platforms", r.Name)
		assert.Equal(t, "error resolving platform roots (resolving codex home: $HOME is not defined)", r.Detail)
	})
}

// internal/platform is a leaf the doctor and setup share; it must never import
// internal/vault (VaultPlatformRoot is strings-only for exactly this reason).
// A direct-import check suffices: the only other capy packages it imports are
// config and version, and config cannot import vault (vault imports config).
func TestPlatformPackageDoesNotImportVault(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	require.NoError(t, err)
	for _, imp := range pkg.Imports {
		assert.NotContains(t, imp, "/internal/vault",
			"internal/platform must stay strings-only towards the vault (see VaultPlatformRoot)")
	}
}
