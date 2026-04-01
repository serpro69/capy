package server

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callBatch(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := srv.handleBatchExecute(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestBatchExecute_BasicSearch(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callBatch(t, srv, map[string]any{
		"commands": []any{
			map[string]any{"label": "OS Info", "command": "uname -a"},
			map[string]any{"label": "Date", "command": "date"},
		},
		"queries": []any{"kernel version", "current date"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Executed 2 commands")
	assert.Contains(t, text, "Indexed Sections")
	assert.Contains(t, text, "OS Info")
}

func TestBatchExecute_SecurityDeny(t *testing.T) {
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(sudo *)"}},
	}
	srv := newTestServer(t, policies)
	r := callBatch(t, srv, map[string]any{
		"commands": []any{
			map[string]any{"label": "Safe", "command": "echo ok"},
			map[string]any{"label": "Bad", "command": "sudo rm -rf /"},
		},
		"queries": []any{"anything"},
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "blocked by security policy")
}

func TestBatchExecute_Timeout(t *testing.T) {
	srv := newTestServer(t, nil)
	start := time.Now()
	r := callBatch(t, srv, map[string]any{
		"commands": []any{
			map[string]any{"label": "Slow", "command": "sleep 30"},
			map[string]any{"label": "After", "command": "echo never"},
		},
		"queries": []any{"anything"},
		"timeout": float64(3000), // 3 seconds — first command will time out
	})
	elapsed := time.Since(start)
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Executed 2 commands")
	assert.Contains(t, text, "Slow")
	assert.Contains(t, text, "After")
	// Should complete much faster than 30s (the sleep duration)
	assert.Less(t, elapsed, 10*time.Second, "batch should not wait for full sleep")
}

func TestBatchExecute_InputCoercion(t *testing.T) {
	srv := newTestServer(t, nil)
	// String commands should be coerced to {label, command} objects
	r := callBatch(t, srv, map[string]any{
		"commands": []any{"echo hello", "echo world"},
		"queries":  []any{"hello"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Executed 2 commands")
}

func TestBatchExecute_ExactSourceScoping(t *testing.T) {
	srv := newTestServer(t, nil)

	// Pre-index content with a label that partially matches "batch:*"
	// to verify exact scoping doesn't leak results from other sources.
	callIndex(t, srv, map[string]any{
		"content": "# Leaked Secret\n\nThis should never appear in batch results. The secret keyword is supercalifragilistic.",
		"source":  "batch:old-run",
	})

	// Run a batch that produces output NOT containing "supercalifragilistic"
	r := callBatch(t, srv, map[string]any{
		"commands": []any{
			map[string]any{"label": "Echo Test", "command": "echo hello world"},
		},
		"queries": []any{"supercalifragilistic"},
	})
	assert.False(t, r.IsError)
	text := resultText(r)

	// The query should find nothing in this batch — no cross-source leak
	assert.Contains(t, text, "No matching sections found")
	assert.NotContains(t, text, "Leaked Secret")
	assert.NotContains(t, text, "previously indexed")

	// Should have the cross-batch search tip
	assert.Contains(t, text, "capy_search")
}

func TestCoerceStringArray(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []string
	}{
		{"string slice", []string{"a", "b"}, []string{"a", "b"}},
		{"any slice", []any{"a", "b"}, []string{"a", "b"}},
		{"json string", `["a","b"]`, []string{"a", "b"}},
		{"invalid json", "not json", nil},
		{"nil", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, coerceStringArray(tt.input))
		})
	}
}

func TestCoerceCommandsArray(t *testing.T) {
	t.Run("objects", func(t *testing.T) {
		input := []any{
			map[string]any{"label": "A", "command": "echo a"},
			map[string]any{"label": "B", "command": "echo b"},
		}
		cmds := coerceCommandsArray(input)
		require.Len(t, cmds, 2)
		assert.Equal(t, "A", cmds[0].Label)
		assert.Equal(t, "echo a", cmds[0].Command)
	})

	t.Run("plain strings", func(t *testing.T) {
		input := []any{"echo a", "echo b"}
		cmds := coerceCommandsArray(input)
		require.Len(t, cmds, 2)
		assert.Equal(t, "cmd_1", cmds[0].Label)
		assert.Equal(t, "echo a", cmds[0].Command)
	})

	t.Run("json string", func(t *testing.T) {
		input := `[{"label":"X","command":"ls"}]`
		cmds := coerceCommandsArray(input)
		require.Len(t, cmds, 1)
		assert.Equal(t, "X", cmds[0].Label)
	})
}
