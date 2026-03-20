package executor

import (
	"sync"
	"sync/atomic"
)

// safeBuffer is a thread-safe byte buffer with an atomic byte counter.
// The counter can be read without locking for hard cap monitoring.
type safeBuffer struct {
	mu   sync.Mutex
	data []byte
	size atomic.Int64
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.data = append(b.data, p...)
	b.mu.Unlock()
	b.size.Add(int64(len(p)))
	return len(p), nil
}

// Size returns the current byte count (lock-free).
func (b *safeBuffer) Size() int64 {
	return b.size.Load()
}

// String returns the buffered content.
func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
