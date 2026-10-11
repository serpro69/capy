package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchCancellationSkipsStaleRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t, nil)
	path := filepath.Join(t.TempDir(), "doc.txt")
	require.NoError(t, os.WriteFile(path, []byte("originalmarker"), 0o600))
	indexed, err := srv.getStore().IndexWithFilePath("originalmarker", "file", "", store.KindDurable, path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("replacementmarker"), 0o600))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(path, future, future))
	checks := 0
	srv.getStore().SetDenyChecker(func(string) bool { checks++; return false })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"queries": []any{"originalmarker"}, "include_kinds": []any{"durable"}}
	result, err := srv.handleSearch(ctx, req)
	require.NoError(t, err)
	assert.Contains(t, resultText(result), "context canceled")
	assert.Zero(t, checks)
	chunks, err := srv.getStore().GetChunksBySource(indexed.SourceID)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "originalmarker", chunks[0].Content)
}
