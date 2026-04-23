package store

import (
	"fmt"
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
