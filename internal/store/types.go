package store

import "time"

// Chunk represents a unit of indexed content.
type Chunk struct {
	Title       string
	Content     string
	HasCode     bool
	ContentType string // "code" or "prose"
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
	Tier           string // "hot", "warm", "cold"
}

// StoreStats contains knowledge base statistics.
type StoreStats struct {
	SourceCount int
	ChunkCount  int
	VocabCount  int
	DBSizeBytes int64
	HotCount    int
	WarmCount   int
	ColdCount   int
}

// IndexResult is returned after indexing content.
type IndexResult struct {
	SourceID       int64
	Label          string
	TotalChunks    int
	CodeChunks     int
	ContentType    string
	AlreadyIndexed bool
}
