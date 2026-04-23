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
			"# Heading\n\nSome content for optimization test.\n\n## Details\n\nMore details here.",
			fmt.Sprintf("opt-test-%d", i),
			"markdown",
			KindEphemeral,
		)
		require.NoError(t, err)
	}

	// After indexing, the counter should have been reset (optimize fired).
	// The counter value should be less than optimizeEvery.
	assert.Less(t, s.insertCount.Load(), optimizeEvery)
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
	s.optimizeFTS()

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
