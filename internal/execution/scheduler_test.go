package execution

import (
	"context"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSchedulerFairTurnsAndEligibility(t *testing.T) {
	now := time.Now()
	s := NewService("", "", config.RunsConfig{MaxActive: 2})
	queue := func(ceiling int) *batchQueue {
		q := &batchQueue{work: make(chan batchWork, 4), cutoff: now.Add(time.Minute), timeout: time.Minute, ceiling: ceiling, parallel: true}
		for range 4 {
			q.children = append(q.children, batchChild{ctx: t.Context(), call: &runCall{}})
		}
		return q
	}
	a, b := queue(1), queue(2)
	s.queue = []*batchQueue{a, b}
	s.pending = 8
	s.dispatchLocked(now)
	require.Equal(t, 0, (<-a.work).ordinal)
	require.Equal(t, 0, (<-b.work).ordinal)
	require.Equal(t, 2, s.active)
	require.Equal(t, 6, s.pending)
	// A is ineligible at its ceiling, so B takes a freed owner slot.
	s.active--
	b.active--
	s.dispatchLocked(now.Add(time.Second))
	next := <-b.work
	require.Equal(t, 1, next.ordinal)
	require.Equal(t, now.Add(61*time.Second), next.deadline)
	// When both become eligible, the rotated queue grants A's next ordinal first.
	s.active--
	a.active--
	s.dispatchLocked(now.Add(2 * time.Second))
	require.Equal(t, 1, (<-a.work).ordinal)
	require.Empty(t, a.work)
	require.Empty(t, b.work)
	// Standalone admission cannot overtake dispatchable children.
	s.active--
	a.active--
	_, _, err := s.begin(t.Context(), true)
	require.Error(t, err)
	require.Equal(t, 2, (<-b.work).ordinal)
}

func TestSchedulerDeadlineCancellationAndSerialBudget(t *testing.T) {
	now := time.Now()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		cutoff time.Time
		want   string
	}{
		{"expired", t.Context(), now, "queue_timeout"}, {"cancelled", cancelled, now.Add(time.Hour), "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewService("", "", config.RunsConfig{MaxActive: 1})
			s.active = 1
			s.pending = 1
			q := &batchQueue{children: []batchChild{{ctx: tc.ctx, call: &runCall{}}}, work: make(chan batchWork, 1), cutoff: tc.cutoff, ceiling: 1}
			s.queue = []*batchQueue{q}
			s.dispatchLocked(now)
			require.Equal(t, tc.want, (<-q.work).reason)
			require.Equal(t, 1, s.active)
			require.Zero(t, s.pending)
		})
	}
}
