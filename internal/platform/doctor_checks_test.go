package platform

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
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
		assert.Contains(t, r.Detail, "2 below index v3")
		assert.Contains(t, r.Detail, "`capy vault reindex`")
	})
}
