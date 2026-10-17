package execution

import (
	"errors"
	"sync/atomic"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

const captureQueueBlocks = (1 << 20) / executor.EventBlockBytes
const captureBatchBlocks = 16

// captureQueue decouples pipe draining from bounded storage writes. There is one
// producer (executor's sequencer), one writer, and at most 1 MiB queued plus a
// 256 KiB in-flight batch. The first lost block ends capture permanently.
type captureQueue struct {
	events       chan executor.OutputEvent
	done         chan struct{}
	failed       atomic.Bool
	captured     int64
	lastSequence uint64
	reason       atomic.Pointer[string]
}

type captureFailureRecorder interface{ CaptureFailed(string) error }

type batchAppender interface {
	AppendBatch([]executor.OutputEvent) error
}

func newCaptureQueue(store CaptureStore) *captureQueue {
	q := &captureQueue{events: make(chan executor.OutputEvent, captureQueueBlocks), done: make(chan struct{})}
	go func() {
		defer close(q.done)
		writeFailed := false
		lossRecorded := false
		for event := range q.events {
			batch := []executor.OutputEvent{event}
		collect:
			for len(batch) < captureBatchBlocks {
				select {
				case next, ok := <-q.events:
					if !ok {
						break collect
					}
					batch = append(batch, next)
				default:
					break collect
				}
			}
			if writeFailed {
				continue
			}
			var err error
			if writer, ok := store.(batchAppender); ok {
				err = writer.AppendBatch(batch)
				if err == nil {
					for _, e := range batch {
						q.committed(e)
					}
				}
			} else {
				for _, e := range batch {
					if err = store.Append(e); err != nil {
						break
					}
					q.committed(e)
				}
			}
			if err != nil {
				writeFailed = true
				reason := "append_failed"
				var typed *runstore.Error
				if errors.As(err, &typed) && typed.Code == "capture_limit" {
					reason = typed.Code
				}
				q.lose(reason)
			}
			if !lossRecorded {
				if reason := q.reason.Load(); reason != nil {
					if recorder, ok := store.(captureFailureRecorder); ok {
						// The terminal outcome retains the in-memory failure even if this
						// best-effort bounded status write fails along with output storage.
						if err := recorder.CaptureFailed(*reason); err == nil {
							lossRecorded = true
						}
					} else {
						lossRecorded = true
					}
				}
			}
		}
	}()
	return q
}

func (q *captureQueue) lose(reason string) {
	q.reason.CompareAndSwap(nil, &reason)
	q.failed.Store(true)
}

func (q *captureQueue) append(e executor.OutputEvent) error {
	if q.failed.Load() {
		return errors.New("capture stopped")
	}
	select {
	case q.events <- e:
		return nil
	default:
		q.lose("queue_saturated")
		return errors.New("capture queue saturated")
	}
}

// close is called only after the sequencer and both pipe drains have stopped.
func (q *captureQueue) close() string {
	close(q.events)
	<-q.done
	if reason := q.reason.Load(); reason != nil {
		return *reason
	}
	return ""
}

func (q *captureQueue) committed(e executor.OutputEvent) {
	q.captured += int64(len(e.Data))
	q.lastSequence = e.Sequence
}
