package executor

import (
	"sync"
	"sync/atomic"
)

// safeBuffer is a thread-safe byte buffer with an atomic byte counter.
// The counter can be read without locking for hard cap monitoring.
type safeBuffer struct {
	mu      sync.Mutex
	data    []byte
	discard bool
	size    atomic.Int64
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.discard {
		b.data = append(b.data, p...)
		b.size.Add(int64(len(p)))
	}
	return len(p), nil
}

// Size returns the retained byte count (lock-free).
func (b *safeBuffer) Size() int64 {
	return b.size.Load()
}

// String returns the buffered content.
func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// SnapshotAndDiscard atomically takes the captured output and releases its
// backing storage. Subsequent writes succeed without retaining any bytes, so
// pipe readers can keep draining without blocking or breaking the child.
func (b *safeBuffer) SnapshotAndDiscard() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := string(b.data)
	b.discard = true
	b.data = nil
	b.size.Store(0)
	return snapshot
}
