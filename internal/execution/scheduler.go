package execution

import (
	"context"
	"time"

	"github.com/serpro69/capy/internal/runstore"
)

// All queue fields except immutable child contexts/rows are protected by mu.
// Rotation after each dispatch gives eligible batches round-robin turns.
type batchQueue struct {
	children              []batchChild
	work                  chan batchWork
	next, active, ceiling int
	cutoff                time.Time
	timeout               time.Duration
	parallel, stopped     bool
}
type batchChild struct {
	ctx  context.Context
	row  runstore.Admitted
	call *runCall
}
type batchWork struct {
	ordinal  int
	deadline time.Time
	reason   string
}

func (q *batchQueue) reason(now time.Time) string {
	if q.children[q.next].ctx.Err() != nil {
		return "cancelled"
	}
	if !now.Before(q.cutoff) {
		return "queue_timeout"
	}
	if q.stopped {
		return "batch_stopped"
	}
	return ""
}

func (s *Service) eligibleBatchLocked(now time.Time) bool {
	for _, q := range s.queue {
		if q.next < len(q.children) && q.active < q.ceiling && q.reason(now) == "" {
			return true
		}
	}
	return false
}

func (s *Service) dispatchLocked(now time.Time) {
	for {
		dispatched := false
		for i, q := range s.queue {
			if q.next == len(q.children) {
				continue
			}
			reason := q.reason(now)
			if reason == "" && (s.unverified || s.closed) {
				reason = "batch_stopped"
			}
			if reason == "" && (q.active >= q.ceiling || s.active >= s.policy.MaxActive) {
				continue
			}
			work := batchWork{ordinal: q.next, reason: reason, deadline: q.cutoff}
			if reason == "" {
				if q.parallel {
					work.deadline = now.Add(q.timeout)
				}
				child := q.children[q.next].call
				child.reservedAt, child.deadline = now, work.deadline
				q.active++
				s.active++
			}
			q.next++
			s.pending--
			q.work <- work // buffer holds the bounded full inventory; never blocks
			copy(s.queue[i:], s.queue[i+1:])
			s.queue[len(s.queue)-1] = q
			dispatched = true
			break
		}
		if !dispatched {
			return
		}
	}
}
