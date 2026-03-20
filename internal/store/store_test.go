package store

import (
	"os"
	"path/filepath"
	"strings"
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

// --- Stopwords ---

func TestIsStopword(t *testing.T) {
	assert.True(t, IsStopword("the"))
	assert.True(t, IsStopword("update"))
	assert.False(t, IsStopword("authentication"))
	assert.False(t, IsStopword("middleware"))
}
