package store

import "time"

// SourceKind classifies a source's lifecycle for search filtering and cleanup.
type SourceKind string

const (
	KindDurable   SourceKind = "durable"
	KindEphemeral SourceKind = "ephemeral"
)

// Valid reports whether k is a recognized source kind.
func (k SourceKind) Valid() bool {
	return k == KindDurable || k == KindEphemeral
}

// Chunk represents a unit of indexed content.
type Chunk struct {
	Title       string
	Content     string
	HasCode     bool
	ContentType string // "code" or "prose"
}

// SearchOptions controls filtering for search queries.
type SearchOptions struct {
	Source          string // partial match filter (LIKE '%source%')
	ContentType     string // "code", "prose", or "" (no filter) — internal only, not in MCP schema
	SourceMatchMode string // "like" (default) or "exact"
	MaxPerSource    int    // per-source result cap for diversification; 0 = default (2)
}

// SearchResult is a single result from a search query.
type SearchResult struct {
	Label       string
	Title       string
	Content     string
	SourceID    int64
	ContentType string
	Highlighted string
	Rank        float64
	FusedScore  float64
	MatchLayer  string
}

// SourceInfo describes an indexed source.
type SourceInfo struct {
	ID             int64
	Label          string
	ContentType    string
	ChunkCount     int
	CodeChunkCount int
	IndexedAt      time.Time
	LastAccessedAt time.Time
	AccessCount    int
	ContentHash    string
	Kind           SourceKind
	Tier           string  // "hot", "warm", "cold", "evictable"
	RetentionScore float64 // computed at query time from salience, decay, and access boost
}

// StoreStats contains knowledge base statistics.
type StoreStats struct {
	SourceCount    int
	ChunkCount     int
	VocabCount     int
	DBSizeBytes    int64
	HotCount       int
	WarmCount      int
	ColdCount      int
	EvictableCount int
}

// SourceMeta is lightweight metadata for a single source (used by TTL cache).
type SourceMeta struct {
	Label      string
	ChunkCount int
	IndexedAt  time.Time
	Kind       SourceKind
}

// IndexResult is returned after indexing content.
type IndexResult struct {
	SourceID       int64
	Label          string
	TotalChunks    int
	CodeChunks     int
	ContentType    string
	Kind           SourceKind
	AlreadyIndexed bool
}
