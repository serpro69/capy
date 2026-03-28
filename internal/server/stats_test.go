package server

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSessionStats(t *testing.T) {
	s := NewSessionStats()
	require.NotNil(t, s)
	assert.NotZero(t, s.SessionStart)
	assert.Empty(t, s.Calls)
	assert.Empty(t, s.BytesReturned)
	assert.Zero(t, s.BytesIndexed)
	assert.Zero(t, s.BytesSandboxed)
}

func TestTrackResponse(t *testing.T) {
	s := NewSessionStats()
	s.TrackResponse("capy_execute", 1000)
	s.TrackResponse("capy_execute", 2000)
	s.TrackResponse("capy_search", 500)

	assert.Equal(t, 2, s.Calls["capy_execute"])
	assert.Equal(t, 1, s.Calls["capy_search"])
	assert.Equal(t, int64(3000), s.BytesReturned["capy_execute"])
	assert.Equal(t, int64(500), s.BytesReturned["capy_search"])
}

func TestAddBytesIndexed(t *testing.T) {
	s := NewSessionStats()
	s.AddBytesIndexed(100)
	s.AddBytesIndexed(200)
	assert.Equal(t, int64(300), s.BytesIndexed)
}

func TestAddBytesSandboxed(t *testing.T) {
	s := NewSessionStats()
	s.AddBytesSandboxed(5000)
	assert.Equal(t, int64(5000), s.BytesSandboxed)
}

func TestSnapshot(t *testing.T) {
	s := NewSessionStats()
	s.TrackResponse("capy_execute", 1000)
	s.AddBytesIndexed(500)

	snap := s.Snapshot()

	// Mutate original — snapshot should be unaffected
	s.TrackResponse("capy_execute", 9999)
	s.AddBytesIndexed(9999)

	assert.Equal(t, 1, snap.Calls["capy_execute"])
	assert.Equal(t, int64(1000), snap.BytesReturned["capy_execute"])
	assert.Equal(t, int64(500), snap.BytesIndexed)
}

func TestSessionStats_ThreadSafety(t *testing.T) {
	s := NewSessionStats()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			s.TrackResponse("capy_execute", 10)
		}()
		go func() {
			defer wg.Done()
			s.AddBytesIndexed(5)
		}()
		go func() {
			defer wg.Done()
			_ = s.Snapshot()
		}()
	}

	wg.Wait()
	assert.Equal(t, 100, s.Calls["capy_execute"])
	assert.Equal(t, int64(1000), s.BytesReturned["capy_execute"])
	assert.Equal(t, int64(500), s.BytesIndexed)
}
