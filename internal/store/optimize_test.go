package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptimizeFTSTriggersAfterThreshold(t *testing.T) {
	s := newTestStore(t)

	// Each call indexes a small markdown doc producing a few chunks.
	// Index enough to cross the optimizeEvery threshold (50 chunks).
	for i := range 60 {
		_, err := s.Index(
			fmt.Sprintf("# Authentication Middleware %d\n\nThe middleware validates JWT tokens.\n\n## Verification\n\nTokens verified using RS256 algorithm.", i),
			fmt.Sprintf("opt-test-%d", i),
			"markdown",
			KindEphemeral,
		)
		require.NoError(t, err)
	}

	// After indexing, the counter should have been reset (optimize fired).
	assert.Less(t, s.insertCount.Load(), optimizeEvery)

	// Verify the auto-triggered optimize didn't corrupt the FTS index.
	db, err := s.getDB()
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM chunks").Scan(&count))
	assert.Greater(t, count, 0, "FTS index should have chunks after optimize")
}

func TestOptimizeFTSDoesNotBreakSearch(t *testing.T) {
	s := newTestStore(t)

	// Index content, force optimize, then verify search still works.
	_, err := s.Index(
		"# Authentication\n\nJWT token validation middleware handles auth.",
		"auth-doc",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)

	// Manually trigger optimize.
	db, err2 := s.getDB()
	require.NoError(t, err2)
	s.optimizeFTS(db)

	// Search should still return results.
	results, err := s.SearchWithFallback("authentication", 5, SearchOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, results)
}

func TestRebuildFTSRunsAndPreservesSearch(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index(
		"# Authentication\n\nJWT token validation middleware handles auth via RS256.",
		"auth-doc",
		"markdown",
		KindDurable,
	)
	require.NoError(t, err)

	require.NoError(t, s.RebuildFTS())

	// The full rebuild must not corrupt the index — search still returns hits.
	results, err := s.SearchWithFallback("authentication", 5, SearchOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, results, "search should still work after FTS rebuild")
}

func TestRebuildFTSReclaimsBloatVacuumAloneCannot(t *testing.T) {
	// Run identical index+evict churn against two stores. One reclaims with
	// VACUUM only; the other with RebuildFTS+VACUUM. Eviction leaves FTS
	// delete-tombstone segments whose pages are live (owned by the FTS data
	// tables), so VACUUM alone cannot reclaim them — only a rebuild releases
	// them. The rebuilt store must therefore end up with strictly fewer pages.
	churn := func(s *ContentStore) {
		for i := range 60 {
			body := strings.Repeat(fmt.Sprintf("authentication middleware token %d ", i), 200)
			_, err := s.Index(body, fmt.Sprintf("bloat-%d", i), "", KindDurable)
			require.NoError(t, err)
		}
		for i := range 60 {
			_, err := s.EvictByLabel(fmt.Sprintf("bloat-%d", i), false)
			require.NoError(t, err)
		}
	}

	vacuumOnly := newTestStore(t)
	churn(vacuumOnly)
	require.NoError(t, vacuumOnly.Vacuum())

	rebuilt := newTestStore(t)
	churn(rebuilt)
	require.NoError(t, rebuilt.RebuildFTS())
	require.NoError(t, rebuilt.Vacuum())

	vacuumOnlyPages := pageCount(t, vacuumOnly)
	rebuiltPages := pageCount(t, rebuilt)
	t.Logf("vacuum-only pages=%d, rebuild+vacuum pages=%d", vacuumOnlyPages, rebuiltPages)

	// Guard against a vacuous pass: if the churn stopped producing tombstone
	// bloat (e.g. an FTS5 behavior change), both stores would be tiny and the
	// comparison meaningless. The vacuum-only store must retain measurable bloat.
	require.Greater(t, vacuumOnlyPages, int64(100),
		"vacuum-only store should retain measurable FTS tombstone bloat")
	assert.Less(t, rebuiltPages, vacuumOnlyPages,
		"RebuildFTS+VACUUM should reclaim FTS tombstone pages that VACUUM alone leaves behind")
}

// pageCount returns the logical page count of the store's database. Reading via
// the pool connection reflects VACUUM's result immediately; the on-disk file is
// only truncated once the pool closes and checkpoints (ADR-016), which the
// cleanup command does via its deferred Close.
func pageCount(t *testing.T, s *ContentStore) int64 {
	t.Helper()
	db, err := s.getDB()
	require.NoError(t, err)
	var n int64
	require.NoError(t, db.QueryRow("PRAGMA page_count").Scan(&n))
	return n
}

func TestOptimizeFTSCounterResetsAfterTrigger(t *testing.T) {
	s := newTestStore(t)

	// Set counter just below threshold, then index to cross it.
	s.insertCount.Store(optimizeEvery - 1)

	_, err := s.Index(
		"# Test\n\nContent to push counter over threshold.",
		"threshold-test",
		"markdown",
		KindEphemeral,
	)
	require.NoError(t, err)

	// Counter should have been reset after optimize.
	assert.Less(t, s.insertCount.Load(), optimizeEvery)
}
