package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *ContentStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s := NewContentStore(dbPath, dir)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSchemaIdempotency(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open, init, close, repeat.
	for range 2 {
		s := NewContentStore(dbPath, dir)
		_, err := s.getDB()
		require.NoError(t, err)
		require.NoError(t, s.Close())
	}
}

func TestDBDirectoryCreated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "deep", "test.db")
	s := NewContentStore(dbPath, dir)
	defer s.Close()

	_, err := s.getDB()
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "sub", "deep"))
	assert.NoError(t, err)
}

// --- Content type detection ---

func TestDetectContentTypeJSON(t *testing.T) {
	assert.Equal(t, "json", DetectContentType(`{"key": "value"}`))
	assert.Equal(t, "json", DetectContentType(`[1, 2, 3]`))
}

func TestDetectContentTypeMarkdown(t *testing.T) {
	md := "# Heading\n\nSome text with [a link](http://example.com)\n\n```go\nfmt.Println()\n```"
	assert.Equal(t, "markdown", DetectContentType(md))
}

func TestDetectContentTypePlaintext(t *testing.T) {
	assert.Equal(t, "plaintext", DetectContentType("just some plain text\nnothing special here"))
}

// --- Markdown chunking ---

func TestChunkMarkdownHeadings(t *testing.T) {
	md := "# Title\n\nIntro text\n\n## Section A\n\nContent A\n\n## Section B\n\nContent B"
	chunks := chunkMarkdown(md, maxChunkBytes)

	require.GreaterOrEqual(t, len(chunks), 2)
	assert.Contains(t, chunks[0].Title, "Title")
}

func TestChunkMarkdownCodeBlocks(t *testing.T) {
	md := "# Code Example\n\n```go\nfunc main() {}\n```\n\nSome prose after"
	chunks := chunkMarkdown(md, maxChunkBytes)

	found := false
	for _, c := range chunks {
		if c.HasCode {
			found = true
		}
	}
	assert.True(t, found, "should detect code block")
}

func TestChunkMarkdownOversized(t *testing.T) {
	// Create content that exceeds maxChunkBytes.
	var sb strings.Builder
	sb.WriteString("# Big Section\n\n")
	for range 100 {
		sb.WriteString("This is a paragraph of text that takes up space. Lorem ipsum dolor sit amet.\n\n")
	}
	chunks := chunkMarkdown(sb.String(), 500)
	assert.Greater(t, len(chunks), 1, "oversized content should be split")
}

func TestChunkMarkdownNoHeadings(t *testing.T) {
	text := "Just some text\nwith no headings\nat all"
	chunks := chunkMarkdown(text, maxChunkBytes)
	require.Len(t, chunks, 1)
	assert.Equal(t, "Content", chunks[0].Title)
}

func TestChunkMarkdownHorizontalRules(t *testing.T) {
	md := "# A\n\nText\n\n---\n\n# B\n\nMore text"
	chunks := chunkMarkdown(md, maxChunkBytes)
	assert.GreaterOrEqual(t, len(chunks), 2)
}

// --- Plaintext chunking ---

func TestChunkPlainTextBlankLineSplit(t *testing.T) {
	var sections []string
	for i := range 10 {
		sections = append(sections, strings.Repeat("x", 50)+string(rune('A'+i)))
	}
	text := strings.Join(sections, "\n\n")
	chunks := chunkPlainText(text, 5)
	assert.Equal(t, 10, len(chunks))
}

func TestChunkPlainTextFixedLine(t *testing.T) {
	// No blank lines, many lines.
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("x", 50)
	}
	text := strings.Join(lines, "\n")
	chunks := chunkPlainText(text, 20)
	assert.Greater(t, len(chunks), 1)
}

func TestChunkPlainTextSingleChunk(t *testing.T) {
	text := "line one\nline two\nline three"
	chunks := chunkPlainText(text, 20)
	require.Len(t, chunks, 1)
	assert.Equal(t, "Output", chunks[0].Title)
}

// --- JSON chunking ---

func TestChunkJSONFlat(t *testing.T) {
	j := `{"name": "test", "value": 42}`
	chunks := chunkJSON(j, maxChunkBytes)
	require.GreaterOrEqual(t, len(chunks), 1)
}

func TestChunkJSONNested(t *testing.T) {
	j := `{"outer": {"inner": {"deep": "value"}}}`
	chunks := chunkJSON(j, maxChunkBytes)
	assert.GreaterOrEqual(t, len(chunks), 1)
	// Should have key-path titles.
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Title, "outer") || strings.Contains(c.Title, "inner") {
			found = true
		}
	}
	assert.True(t, found, "should have key-path titles")
}

func TestChunkJSONArrayWithIdentity(t *testing.T) {
	j := `[{"id": 1, "name": "first"}, {"id": 2, "name": "second"}, {"id": 3, "name": "third"}]`
	chunks := chunkJSON(j, maxChunkBytes)
	require.GreaterOrEqual(t, len(chunks), 1)
}

func TestChunkJSONParseFailure(t *testing.T) {
	chunks := chunkJSON("not json at all {{{", maxChunkBytes)
	assert.GreaterOrEqual(t, len(chunks), 1, "should fall back to plaintext")
}

func TestChunkJSONObjectDeterministicOrder(t *testing.T) {
	// Nested object forces recursion, producing one chunk per key.
	j := `{"zebra": {"v": 1}, "alpha": {"v": 2}, "middle": {"v": 3}}`
	// Run multiple times — if order were random, at least one would differ.
	var first []string
	for i := range 10 {
		chunks := chunkJSON(j, maxChunkBytes)
		var titles []string
		for _, c := range chunks {
			titles = append(titles, c.Title)
		}
		if i == 0 {
			first = titles
		} else {
			assert.Equal(t, first, titles, "chunk order should be deterministic across runs")
		}
	}
	// Verify sorted key order in titles: alpha before middle before zebra.
	chunks := chunkJSON(j, maxChunkBytes)
	require.GreaterOrEqual(t, len(chunks), 3)
	allTitles := ""
	for _, c := range chunks {
		allTitles += c.Title + " "
	}
	alphaIdx := strings.Index(allTitles, "alpha")
	middleIdx := strings.Index(allTitles, "middle")
	zebraIdx := strings.Index(allTitles, "zebra")
	require.NotEqual(t, -1, alphaIdx, "alpha should appear in titles")
	assert.Less(t, alphaIdx, middleIdx)
	assert.Less(t, middleIdx, zebraIdx)
}

// --- Identity field ---

func TestFindIdentityField(t *testing.T) {
	arr := []any{
		map[string]any{"id": 1, "name": "a"},
		map[string]any{"id": 2, "name": "b"},
	}
	assert.Equal(t, "id", findIdentityField(arr))
}

func TestFindIdentityFieldEmpty(t *testing.T) {
	assert.Equal(t, "", findIdentityField(nil))
}

func TestFindIdentityFieldNonObject(t *testing.T) {
	arr := []any{"a", "b", "c"}
	assert.Equal(t, "", findIdentityField(arr))
}

// --- Indexing ---

func TestIndexAndDedup(t *testing.T) {
	s := newTestStore(t)

	r1, err := s.Index("hello world content", "test-source", "plaintext")
	require.NoError(t, err)
	assert.False(t, r1.AlreadyIndexed)
	assert.Greater(t, r1.TotalChunks, 0)

	// Same content = dedup.
	r2, err := s.Index("hello world content", "test-source", "plaintext")
	require.NoError(t, err)
	assert.True(t, r2.AlreadyIndexed)
	assert.Equal(t, r1.SourceID, r2.SourceID)
}

func TestIndexChangedContent(t *testing.T) {
	s := newTestStore(t)

	r1, err := s.Index("version one", "src", "plaintext")
	require.NoError(t, err)

	r2, err := s.Index("version two", "src", "plaintext")
	require.NoError(t, err)
	assert.False(t, r2.AlreadyIndexed)
	assert.NotEqual(t, r1.SourceID, r2.SourceID, "should get new source ID after re-index")
}

func TestIndexAutoDetectsContentType(t *testing.T) {
	s := newTestStore(t)

	r, err := s.Index(`{"key": "value"}`, "json-src", "")
	require.NoError(t, err)
	assert.Equal(t, "json", r.ContentType)
}

func TestIndexConcurrentSameLabelNoDuplicates(t *testing.T) {
	s := newTestStore(t)
	// Allow multiple DB connections to widen the race window.
	db, err := s.getDB()
	require.NoError(t, err)
	db.SetMaxOpenConns(10)

	const goroutines = 50
	var wg sync.WaitGroup
	// Barrier ensures all goroutines start simultaneously.
	start := make(chan struct{})
	errs := make([]error, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = s.Index(
				fmt.Sprintf("content-%d", idx),
				"same-label",
				"plaintext",
			)
		}(i)
	}
	close(start)
	wg.Wait()

	// All calls should succeed (no panics, no unhandled errors).
	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d", i)
	}

	// Exactly one source should exist for this label — the last writer wins,
	// but there must never be duplicates.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM sources WHERE label = 'same-label'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "concurrent Index with same label must produce exactly one source")
}

// --- Vocabulary ---

func TestVocabularyExtraction(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index("The authentication middleware validates tokens correctly", "vocab-test", "plaintext")
	require.NoError(t, err)

	db, _ := s.getDB()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&count)
	require.NoError(t, err)
	assert.Greater(t, count, 0)

	// "the" is a stopword and should not be in vocabulary.
	var theCount int
	err = db.QueryRow("SELECT COUNT(*) FROM vocabulary WHERE word = 'the'").Scan(&theCount)
	require.NoError(t, err)
	assert.Equal(t, 0, theCount)
}

func TestVocabularyBatched(t *testing.T) {
	s := newTestStore(t)

	// Index content with many unique words — all should be inserted.
	var words []string
	for i := range 200 {
		words = append(words, fmt.Sprintf("uniqueword%d", i))
	}
	content := strings.Join(words, " ")

	_, err := s.Index(content, "vocab-batch-test", "plaintext")
	require.NoError(t, err)

	db, _ := s.getDB()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&count)
	require.NoError(t, err)
	// All 200 words are >= 3 chars and not stopwords.
	assert.Equal(t, 200, count, "all unique words should be inserted in batch")
}

// --- Stopwords ---

func TestIsStopword(t *testing.T) {
	assert.True(t, IsStopword("the"))
	assert.True(t, IsStopword("update"))
	assert.False(t, IsStopword("authentication"))
	assert.False(t, IsStopword("middleware"))
}
