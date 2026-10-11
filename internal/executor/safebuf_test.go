package executor

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeBufferSnapshotAndDiscard(t *testing.T) {
	var buf safeBuffer
	prefix := strings.Repeat("captured output\n", 1024)
	n, err := buf.Write([]byte(prefix))
	require.NoError(t, err)
	require.Equal(t, len(prefix), n)
	require.EqualValues(t, len(prefix), buf.Size())
	require.Equal(t, prefix, buf.SnapshotAndDiscard())

	payload := make([]byte, 64*1024)
	for range 4096 { // 256 MiB after detach must retain no storage.
		n, err = buf.Write(payload)
		require.NoError(t, err)
		require.Equal(t, len(payload), n)
	}
	assert.Zero(t, buf.Size())
	assert.Empty(t, buf.String())
	assert.Zero(t, cap(buf.data), "release the original backing allocation")
	assert.Empty(t, buf.SnapshotAndDiscard(), "discard is permanent")
	assert.Zero(t, testing.AllocsPerRun(100, func() { buf.Write(payload) }))
}

func TestSafeBufferConcurrentSnapshotAndDiscard(t *testing.T) {
	t.Parallel()
	var buf safeBuffer
	var writers sync.WaitGroup
	start := make(chan struct{})
	afterSnapshot := make(chan struct{})
	for range 8 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			for range 1000 {
				buf.Write([]byte("before\n"))
			}
			<-afterSnapshot
			for range 1000 {
				buf.Write([]byte("after\n"))
			}
		}()
	}
	close(start)
	snapshot := buf.SnapshotAndDiscard()
	close(afterSnapshot)
	writers.Wait()
	assert.Equal(t, strings.Repeat("before\n", strings.Count(snapshot, "before\n")), snapshot)
	assert.Zero(t, buf.Size(), "a racing writer must not restore the byte count")
	assert.Empty(t, buf.String())
	assert.Zero(t, cap(buf.data))
}
