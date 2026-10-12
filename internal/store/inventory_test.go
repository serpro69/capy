package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSectionInventory(t *testing.T) {
	s := newTestStore(t)
	var content strings.Builder
	for i := range 8000 {
		fmt.Fprintf(&content, "# %04d %s\n\n界\x00tail\n\n", i, strings.Repeat("界", 60))
	}
	indexed, err := s.Index(content.String(), "many sections", "markdown", KindEphemeral)
	require.NoError(t, err)
	for range 2 {
		total, sections, err := s.GetSectionInventory(context.Background(), indexed.SourceID)
		require.NoError(t, err)
		assert.Equal(t, 8000, total)
		require.Len(t, sections, 40)
		for i, section := range sections {
			assert.True(t, strings.HasPrefix(section.Title, fmt.Sprintf("%04d ", i)))
			assert.LessOrEqual(t, len(section.Title), 160)
			assert.True(t, utf8.ValidString(section.Title))
			assert.True(t, strings.HasSuffix(section.Title, "…"))
			assert.Equal(t, len("界\x00tail"), section.Bytes, "byte lengths include Unicode and NUL tails")
		}
		indexed, err = s.Index(content.String(), "many sections", "markdown", KindEphemeral)
		require.NoError(t, err)
		require.True(t, indexed.AlreadyIndexed)
		require.Zero(t, indexed.TotalChunks, "inventory must consult the stored total")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = s.GetSectionInventory(ctx, indexed.SourceID)
	assert.ErrorIs(t, err, context.Canceled)
	_, _, err = s.GetSectionInventory(context.Background(), -1)
	assert.Error(t, err)
}

func TestGetSectionInventoryTitleBoundaries(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct{ name, title string }{
		{"exact ASCII", strings.Repeat("a", 160)},
		{"exact multibyte", strings.Repeat("界", 53) + "a"},
		{"over multibyte", strings.Repeat("界", 54)},
		{"short", "Short heading"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			indexed, err := s.Index("# "+tc.title+"\n\nbody", tc.name, "markdown", KindEphemeral)
			require.NoError(t, err)
			total, sections, err := s.GetSectionInventory(context.Background(), indexed.SourceID)
			require.NoError(t, err)
			assert.Equal(t, 1, total)
			require.Len(t, sections, 1)
			assert.LessOrEqual(t, len(sections[0].Title), 160)
			assert.True(t, utf8.ValidString(sections[0].Title))
			if len(tc.title) <= 160 {
				assert.Equal(t, tc.title, sections[0].Title)
			}
		})
	}
}
