package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexArchivedRolloutRel is codexRolloutRel's twin under archived_sessions/.
func codexArchivedRolloutRel(uuid string) string {
	return strings.Replace(codexRolloutRel(uuid), "sessions/", "archived_sessions/", 1)
}

// TestVaultSweep_Codex_DuplicateCopiesDoNotOscillateTheHint pins the Task 14
// review's P2: with identical copies of a thread under sessions/ and
// archived_sessions/, the first sweep stores the active path and skips the
// archived copy in-run; the SECOND sweep's skip predicate drops the active copy
// unopened, so the archived copy used to arrive at import as the run's first
// sighting — same hash, different path — and MOVE the hint; the third sweep
// moved it back. One metadata write per startup and a restore target that
// alternated. The location policy now moves the hint only when the file at the
// stored hint is gone (codexRolloutPresent), so a lingering duplicate is
// `skipped` on every sweep and the hint is stable.
func TestVaultSweep_Codex_DuplicateCopiesDoNotOscillateTheHint(t *testing.T) {
	home := setupCodexSweepEnv(t)
	project := t.TempDir()
	lines := codexRolloutLines(t, codexSweepMatch, project, "a thread that lingers under both roots")
	writeCodexRollout(t, home, codexRolloutRel(codexSweepMatch), lines)
	writeCodexRollout(t, home, codexArchivedRolloutRel(codexSweepMatch), lines)

	srv := newTestServerWithProjectDir(t, nil, project)
	ctx := context.Background()

	first := srv.vaultSweep(ctx)
	assert.Equal(t, 1, first.codex.Imported)
	assert.Equal(t, 1, first.codex.Skipped, "the archived copy is the in-run duplicate")
	assert.Equal(t, 0, first.codex.Updated)
	require.Equal(t, codexRolloutRel(codexSweepMatch), archivedUUIDs(t)[codexSweepMatch].ClaudeProjectDir, "the active path is stored")

	for i, name := range []string{"second", "third", "fourth"} {
		sum := srv.vaultSweep(ctx)
		assert.Equal(t, 1, sum.codexReport.SkippedByPredicate, "%s sweep: the copy at the stored hint is dropped unopened", name)
		assert.Equal(t, 1, sum.codexReport.FirstLineReads, "%s sweep: only the other copy is read", name)
		assert.Equal(t, 0, sum.codex.Updated, "%s sweep: a lingering duplicate never moves the hint", name)
		assert.Equal(t, 0, sum.codex.Imported, "%s sweep", name)
		assert.Equal(t, 1, sum.codex.Skipped, "%s sweep: the duplicate is reported skipped", name)
		assert.Equal(t, codexRolloutRel(codexSweepMatch), archivedUUIDs(t)[codexSweepMatch].ClaudeProjectDir,
			"%s sweep (%d): the hint must stay on the active path", name, i+2)
	}
}

// A genuine archive (the active copy is gone) still moves the hint on the next
// sweep — the duplicate guard must not turn every move into a skip.
func TestVaultSweep_Codex_GenuineMoveStillFollowsTheFile(t *testing.T) {
	home := setupCodexSweepEnv(t)
	project := t.TempDir()
	lines := codexRolloutLines(t, codexSweepMatch, project, "a thread that gets archived between starts")
	active := writeCodexRollout(t, home, codexRolloutRel(codexSweepMatch), lines)

	srv := newTestServerWithProjectDir(t, nil, project)
	ctx := context.Background()
	require.Equal(t, 1, srv.vaultSweep(ctx).codex.Imported)

	require.NoError(t, os.Remove(active))
	writeCodexRollout(t, home, codexArchivedRolloutRel(codexSweepMatch), lines)

	moved := srv.vaultSweep(ctx)
	assert.Equal(t, 1, moved.codex.Updated, "the move is reported once")
	assert.Equal(t, 0, moved.codex.Skipped)
	assert.Equal(t, codexArchivedRolloutRel(codexSweepMatch), archivedUUIDs(t)[codexSweepMatch].ClaudeProjectDir, "the hint follows the file")

	settled := srv.vaultSweep(ctx)
	assert.Equal(t, 1, settled.codexReport.SkippedByPredicate, "archived at its new path and size: dropped unopened")
	assert.Equal(t, 0, settled.codex.Updated)
}
