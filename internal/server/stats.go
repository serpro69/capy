package server

import (
	"maps"
	"sync"
	"time"
)

// SessionStats tracks per-session usage metrics for the MCP server.
type SessionStats struct {
	SessionStart   time.Time
	Calls          map[string]int
	BytesReturned  map[string]int64
	BytesIndexed   int64
	BytesSandboxed int64
	CacheHits       int64
	CacheBytesSaved int64
	mu              sync.Mutex
}

// NewSessionStats creates a new SessionStats with initialized maps.
func NewSessionStats() *SessionStats {
	return &SessionStats{
		SessionStart:  time.Now(),
		Calls:         make(map[string]int),
		BytesReturned: make(map[string]int64),
	}
}

// TrackResponse records a tool call and its response size.
func (s *SessionStats) TrackResponse(toolName string, responseBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls[toolName]++
	s.BytesReturned[toolName] += responseBytes
}

// AddBytesIndexed adds to the total bytes indexed counter.
func (s *SessionStats) AddBytesIndexed(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BytesIndexed += n
}

// AddBytesSandboxed adds to the total bytes kept out of context.
func (s *SessionStats) AddBytesSandboxed(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BytesSandboxed += n
}

// AddCacheHit records a TTL cache hit and its estimated byte savings.
func (s *SessionStats) AddCacheHit(estimatedBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CacheHits++
	s.CacheBytesSaved += estimatedBytes
}

// Reset zeroes all usage counters and re-initializes the maps under the mutex.
// Used by capy_cleanup's purge_all to clear session metrics alongside the
// knowledge-base wipe. SessionStart is intentionally preserved — the MCP
// session has not restarted, so uptime stays accurate.
func (s *SessionStats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = make(map[string]int)
	s.BytesReturned = make(map[string]int64)
	s.BytesIndexed = 0
	s.BytesSandboxed = 0
	s.CacheHits = 0
	s.CacheBytesSaved = 0
}

// Snapshot returns a copy of the current stats, safe for concurrent reads.
func (s *SessionStats) Snapshot() SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	calls := make(map[string]int, len(s.Calls))
	maps.Copy(calls, s.Calls)
	bytesReturned := make(map[string]int64, len(s.BytesReturned))
	maps.Copy(bytesReturned, s.BytesReturned)

	return SessionStats{
		SessionStart:   s.SessionStart,
		Calls:          calls,
		BytesReturned:  bytesReturned,
		BytesIndexed:   s.BytesIndexed,
		BytesSandboxed: s.BytesSandboxed,
		CacheHits:       s.CacheHits,
		CacheBytesSaved: s.CacheBytesSaved,
	}
}
