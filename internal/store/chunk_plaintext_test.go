package store

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withoutWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func TestChunkPlainTextByteCap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"at limit", strings.Repeat("a", 4096)},
		{"above limit", strings.Repeat("a", 4097)},
		{"few huge lines", strings.Repeat(strings.Repeat("x", 6000)+"\n", 19)},
		{"blank sections 4097", strings.Repeat(strings.Repeat("x", 4088)+strings.Repeat("\ny", 4)+"z\n\n", 5)},
		{"blank sections 5000", strings.Repeat(strings.Repeat("x", 4991)+strings.Repeat("\ny", 4)+"z\n\n", 5)},
		{"long single line", strings.Repeat("x", 100000)},
		{"two byte boundary", strings.Repeat("x", 4095) + strings.Repeat("é", 100)},
		{"three byte boundary", strings.Repeat("界", 3000)},
		{"four byte boundary", strings.Repeat("x", 4093) + strings.Repeat("🦫", 2000)},
		{"unicode whitespace", strings.Repeat("word\u2003", 1000)},
		{"long unicode titles", strings.Repeat(strings.Repeat("界", 30)+strings.Repeat("\nx", 5)+"\n\n", 4)},
		{"empty", ""},
		{"whitespace", strings.Repeat(" \t\n\u2003", 2000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			chunks := chunkPlainText(tc.content, 20)
			var combined strings.Builder
			for _, c := range chunks {
				assert.LessOrEqual(t, len(c.Content), MaxChunkBytes)
				assert.True(t, utf8.ValidString(c.Content), "invalid content UTF-8")
				assert.True(t, utf8.ValidString(c.Title), "invalid title UTF-8")
				assert.LessOrEqual(t, len(c.Title), 80)
				combined.WriteString(c.Content)
			}
			assert.Equal(t, withoutWhitespace(tc.content), withoutWhitespace(combined.String()))
			assert.Equal(t, chunks, chunkPlainText(tc.content, 20), "deterministic content and titles")
			if tc.name == "at limit" {
				require.Len(t, chunks, 1)
			}
		})
	}
}

func TestChunkPlainTextBoundaryPreference(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content, first string
	}{
		{"line before space", "first line\n" + strings.Repeat("word ", 1000), "first line"},
		{"word boundary", strings.Repeat("word ", 1000), strings.TrimSpace(strings.Repeat("word ", 819))},
		{"unicode boundary", strings.Repeat("界", 2000), strings.Repeat("界", 1365)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := chunkPlainText(tc.content, 20)
			require.Greater(t, len(chunks), 1)
			assert.Equal(t, tc.first, chunks[0].Content)
		})
	}
}

func TestChunkPlainTextFixedLineOverlap(t *testing.T) {
	t.Parallel()
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%02d %s", i, strings.Repeat("x", 200))
	}
	for _, giant := range []bool{false, true} {
		t.Run(fmt.Sprintf("giant=%t", giant), func(t *testing.T) {
			input := append([]string(nil), lines...)
			if giant {
				input[18] = "giantmarker " + strings.Repeat("界", 5000)
			}
			chunks := chunkPlainText(strings.Join(input, "\n"), 20)
			var combined strings.Builder
			for _, c := range chunks {
				assert.LessOrEqual(t, len(c.Content), MaxChunkBytes)
				assert.True(t, utf8.ValidString(c.Content))
				combined.WriteString(c.Content)
				combined.WriteByte('\n')
			}
			text := combined.String()
			for i := range input {
				marker := fmt.Sprintf("line%02d", i)
				want := 1
				if i == 18 || i == 19 || i == 36 || i == 37 {
					want = 2
				}
				if giant {
					// The huge overlap line is emitted once; the following ordinary
					// line still overlaps. Later groups start one line later.
					want = 1
					if i == 19 || i == 37 || i == 38 {
						want = 2
					}
					if i == 18 {
						marker = "giantmarker"
					}
				}
				assert.Equal(t, want, strings.Count(text, marker), marker)
			}
			if giant {
				assert.Equal(t, 5000, strings.Count(text, "界"), "huge overlap line retained exactly once")
			}
		})
	}
}

func TestIndexPlaintextByteCap(t *testing.T) {
	for _, tc := range []struct {
		name, content, contentType string
	}{
		{"single line", strings.Repeat("word ", 4000) + "tailneedle", "plaintext"},
		{"sections", strings.Repeat(strings.Repeat("word ", 890)+strings.Repeat("\nline", 5)+"\n\n", 5) + "tailneedle", "plaintext"},
		{"few huge lines", strings.Repeat(strings.Repeat("界", 2000)+"\n", 5) + "tailneedle", "plaintext"},
		{"fixed lines", strings.Repeat(strings.Repeat("word ", 60)+"\n", 80) + "tailneedle", "plaintext"},
		{"malformed JSON", "{broken " + strings.Repeat("word ", 4000) + "tailneedle", "json"},
		{"empty", "", "plaintext"},
		{"whitespace", strings.Repeat(" \t\n\u2003", 2000), "plaintext"},
		{"JSON whitespace fallback", strings.Repeat(" \t\n", 2000), "json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			result, err := s.Index(tc.content, tc.name, tc.contentType, KindDurable)
			require.NoError(t, err)
			db, err := s.getDB()
			require.NoError(t, err)
			for _, table := range []string{"chunks", "chunks_trigram"} {
				var maxBytes, count int
				// Table names are a fixed test-owned list; content size is bytes,
				// not SQLite's character count for TEXT.
				err := db.QueryRow("SELECT COALESCE(MAX(length(CAST(content AS BLOB))), 0), COUNT(*) FROM "+table+" WHERE source_id = ?", result.SourceID).Scan(&maxBytes, &count)
				require.NoError(t, err)
				assert.LessOrEqual(t, maxBytes, MaxChunkBytes, table)
				assert.Equal(t, result.TotalChunks, count, table)
				assert.Positive(t, count, "keep a bounded empty-source fallback")
			}
			if strings.Contains(tc.content, "tailneedle") {
				hits, err := s.SearchWithFallback("tailneedle", 5, SearchOptions{Source: tc.name, SourceMatchMode: "exact"})
				require.NoError(t, err)
				require.NotEmpty(t, hits)
				assert.Contains(t, hits[0].Content, "tailneedle")
			}
		})
	}
}

func TestPlaintextLegacyChunksRequireReindex(t *testing.T) {
	s := newTestStore(t)
	content := strings.Repeat("word ", 2000)
	legacy, err := s.IndexChunked(content, "legacy", "plaintext", KindDurable, []Chunk{{Title: "Output", Content: content}})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	same, err := s.IndexPlainText(content, "legacy", KindDurable)
	require.NoError(t, err)
	assert.True(t, same.AlreadyIndexed)
	assert.Equal(t, legacy.SourceID, same.SourceID)
	meta, err := s.GetSourceMeta("legacy")
	require.NoError(t, err)
	assert.Equal(t, 1, meta.ChunkCount)
	_, err = s.EvictByLabel("legacy", false)
	require.NoError(t, err)
	reindexed, err := s.IndexPlainText(content, "legacy", KindDurable)
	require.NoError(t, err)
	assert.False(t, reindexed.AlreadyIndexed)
	assert.Greater(t, reindexed.TotalChunks, 1)
}

func FuzzPlaintextByteCap(f *testing.F) {
	f.Add("")
	f.Add(strings.Repeat("界", 3000))
	f.Add(strings.Repeat("word\n", 2000))
	f.Add(strings.Repeat("\x80", 9000))
	f.Add(strings.Repeat(" \t\n", 2000))
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > DefaultMaxSourceBytes {
			t.Skip()
		}
		// Force the short-line strategy to exercise splitting without overlap.
		chunks := chunkPlainText(text, strings.Count(text, "\n")+1)
		validUTF8 := utf8.ValidString(text)
		var combined strings.Builder
		for _, c := range chunks {
			if len(c.Content) > MaxChunkBytes {
				t.Fatalf("chunk has %d bytes", len(c.Content))
			}
			if validUTF8 && !utf8.ValidString(c.Content) {
				t.Fatal("split valid UTF-8 inside a rune")
			}
			combined.WriteString(c.Content)
		}
		if withoutWhitespace(text) != withoutWhitespace(combined.String()) {
			t.Fatal("lost or duplicated non-whitespace content")
		}
	})
}

func BenchmarkChunkPlainText(b *testing.B) {
	for _, tc := range []struct{ name, content string }{
		{"normal", strings.Repeat("normal line of output\n", 100)},
		{"single-line", strings.Repeat("word ", 20000)},
		{"blank-sections", strings.Repeat(strings.Repeat("word ", 890)+strings.Repeat("\nline", 5)+"\n\n", 20)},
		{"overlap-giant", strings.Repeat("line\n", 18) + strings.Repeat("界", 100000) + strings.Repeat("\nline", 30)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.content)))
			for b.Loop() {
				chunkPlainText(tc.content, 20)
			}
		})
	}
}
