package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 7: capy_vault_search runs the chunk corpus through the shared retrieval
// engine and degrades loudly when the vault is disabled or holds a reindex
// backlog. These tests mirror vault_sweep_test.go's fixture layout.

// vaultSearchReq builds a capy_vault_search CallToolRequest from raw args.
func vaultSearchReq(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = "capy_vault_search"
	req.Params.Arguments = args
	return req
}

func TestVaultSearch_ArchivedSessionsReturnHits(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background()) // archive + chunk-index both fixtures

	// Default scope = current project. The database-configuration fixture (uuid1)
	// must rank for a query about configuring the database.
	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"configure the database"},
	}))
	require.NoError(t, err)
	require.False(t, res.IsError, "a populated vault must not return an error result")

	out := resultText(res)
	assert.Contains(t, out, "session:"+uuid1, "the database session should be a ranked hit")
	assert.NotContains(t, out, "No results found.")
}

func TestVaultSearch_AllProjectsWidensScope(t *testing.T) {
	projectA, _, _, uuidB := setupVaultSweepMultiProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")

	srv := newTestServerWithProjectDir(t, nil, projectA)
	srv.vaultSweep(context.Background())

	// The goroutine-scheduling fixture lives in project B. A default (current
	// project = A) search must miss it; all_projects must find it.
	scoped, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"goroutine scheduler work stealing"},
	}))
	require.NoError(t, err)
	assert.NotContains(t, resultText(scoped), "session:"+uuidB,
		"default scope is the current project — project B must not appear")

	widened, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries":      []any{"goroutine scheduler work stealing"},
		"all_projects": true,
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(widened), "session:"+uuidB,
		"all_projects must reach sibling-project sessions")
}

func TestVaultSearch_ProjectStarWidensScope(t *testing.T) {
	projectA, _, _, uuidB := setupVaultSweepMultiProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")

	srv := newTestServerWithProjectDir(t, nil, projectA)
	srv.vaultSweep(context.Background())

	// project:"*" is the string equivalent of all_projects:true.
	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"goroutine scheduler work stealing"},
		"project": "*",
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(res), "session:"+uuidB,
		`project:"*" must reach sibling-project sessions`)
}

func TestVaultSearch_NoQuery(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)

	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{}))
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, resultText(res), "provide query or queries")
}

func TestVaultSearch_InvalidDate(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())

	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"database"},
		"after":   "not-a-date",
	}))
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, resultText(res), "invalid date")
}

func TestVaultSearch_DisabledWithoutKey(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", "") // vault is opt-in

	srv := newTestServerWithProjectDir(t, nil, projectDir)

	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"anything"},
	}))
	require.NoError(t, err)
	require.True(t, res.IsError, "disabled vault must return an error result")
	assert.Contains(t, resultText(res), "CAPY_VAULT_KEY")

	// Degrade-loudly must not create the vault file as a side effect.
	_, statErr := os.Stat(vault.VaultDBPath())
	assert.True(t, os.IsNotExist(statErr), "a disabled search must not create the vault DB")
}

func TestVaultSearch_BacklogHintOnZeroHits(t *testing.T) {
	projectDir, uuid1, uuid2 := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(context.Background())

	// Force a reindex backlog: stamp every archived session below the current
	// index version so it counts as OutdatedSessions. UpdateSessionFTS also
	// clears its chunk rows, which only reinforces the zero-hit path.
	stampVaultIndexVersion(t, uuid1, 1)
	stampVaultIndexVersion(t, uuid2, 1)

	// A query that matches nothing must still surface the reindex hint.
	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries":      []any{"zzzzz-no-such-term-anywhere"},
		"all_projects": true, // widen so scope can't be blamed for the miss
	}))
	require.NoError(t, err)
	require.False(t, res.IsError, "a zero-hit search with a backlog is informational, not an error result")
	out := resultText(res)
	assert.Contains(t, out, "No results found.")
	assert.Contains(t, out, "capy vault reindex", "a zero-hit search with a backlog must name the reindex command")
}

// Task 11.1: every vault hit's meta line ends with the stored platform token,
// and a sub-agent hit is marked with its parent's short id. A Claude hit is
// otherwise byte-identical to the pre-platform format — pinned by comparing
// against the old layout spelled out by hand.
func TestFormatVaultHit_PlatformAndParent(t *testing.T) {
	end := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	claude := vault.SearchResult{
		SessionUUID: "abcd1234-0000-4000-8000-000000000001",
		LineIndex:   7,
		Snippet:     "configure the database",
		Title:       "Database setup",
		ProjectPath: "/home/u/proj",
		EndTime:     end,
		// Platform left empty on purpose: a SearchResult built in memory is
		// Claude (OrClaude), exactly like a stored default row.
	}
	// The pre-Task-11 layout, spelled out by hand: header, title, meta, snippet.
	legacyPrefix := "--- [session:abcd1234-0000-4000-8000-000000000001] ---\n### Database setup\n/home/u/proj · 2026-05-01 · line 7"
	got := formatVaultHit(claude)
	assert.True(t, strings.HasPrefix(got, legacyPrefix), "everything before the platform label is unchanged:\n%s", got)
	assert.Equal(t, legacyPrefix+" · claude-code\n\nconfigure the database", got,
		"a Claude hit gains exactly the platform label and nothing else")

	codexChild := vault.SearchResult{
		SessionUUID: "019e2200-0000-7000-8000-000000000002",
		LineIndex:   1,
		Snippet:     "explore the scheduler",
		Title:       "Rook · explorer",
		EndTime:     end,
		Platform:    vault.PlatformCodex,
		ParentUUID:  "019e2200-0000-7000-8000-000000000001",
	}
	assert.Equal(t,
		"--- [session:019e2200-0000-7000-8000-000000000002] ---\n### Rook · explorer\n2026-05-01 · line 1 · codex · child of 019e2200-000\n\nexplore the scheduler",
		formatVaultHit(codexChild), "a Codex child names its platform and its parent's 12-char id")

	codexRoot := codexChild
	codexRoot.ParentUUID = ""
	assert.NotContains(t, formatVaultHit(codexRoot), "child of", "a top-level session carries no parent marker")
}

// End to end: a Codex parent/child pair swept into the vault comes back from
// capy_vault_search with the platform token and the child marker, and the
// session:<uuid> tag is unchanged.
func TestVaultSearch_CodexChildHitIsLabeled(t *testing.T) {
	home := setupCodexSweepEnv(t)
	project := t.TempDir()
	parent, child := codexSweepMatch, codexSweepOther
	writeCodexRollout(t, home, codexRolloutRel(parent),
		codexRolloutLines(t, parent, project, "plan the migration rollout"))
	writeCodexRollout(t, home, codexRolloutRel(child),
		codexChildRolloutLines(t, child, parent, project, "inspect the goroutine scheduler internals"))

	srv := newTestServerWithProjectDir(t, nil, project)
	sum := srv.vaultSweep(context.Background())
	require.Equal(t, 2, sum.codex.Imported, "parent and child are both archived")

	res, err := srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"goroutine scheduler internals"},
	}))
	require.NoError(t, err)
	require.False(t, res.IsError)
	out := resultText(res)
	assert.Contains(t, out, "[session:"+child+"]", "the child is a hit under its own uuid")
	assert.Contains(t, out, " · codex · child of "+vault.PlatformCodex.ShortID(parent),
		"the meta line names the platform and the parent's short id")

	// The parent is a top-level Codex session: platform, no child marker.
	res, err = srv.handleVaultSearch(context.Background(), vaultSearchReq(map[string]any{
		"queries": []any{"plan the migration rollout"},
	}))
	require.NoError(t, err)
	out = resultText(res)
	require.Contains(t, out, "[session:"+parent+"]")
	parentBlock := out[strings.Index(out, "[session:"+parent+"]"):]
	if i := strings.Index(parentBlock, "\n\n---"); i >= 0 {
		parentBlock = parentBlock[:i]
	}
	assert.Contains(t, parentBlock, " · codex\n")
	assert.NotContains(t, parentBlock, "child of")
}
