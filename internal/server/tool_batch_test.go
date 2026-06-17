package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
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

// TestBatchExecute_ConcurrencySerialDefault verifies that concurrency=1 (the
// default) routes through the serial path and behaves like the legacy handler.
func TestBatchExecute_ConcurrencySerialDefault(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callBatch(t, srv, map[string]any{
		"commands": []any{
			map[string]any{"label": "One", "command": "echo aaa"},
			map[string]any{"label": "Two", "command": "echo bbb"},
		},
		"queries":     []any{"aaa"},
		"concurrency": float64(1),
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Executed 2 commands")
	assert.Contains(t, text, "One")
	assert.Contains(t, text, "Two")
}

// TestBatchExecute_ConcurrencyParallel exercises the parallel path end-to-end
// through the handler and confirms the [1, len(commands)] clamp (4 → 3).
func TestBatchExecute_ConcurrencyParallel(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callBatch(t, srv, map[string]any{
		"commands": []any{
			map[string]any{"label": "One", "command": "echo aaa"},
			map[string]any{"label": "Two", "command": "echo bbb"},
			map[string]any{"label": "Three", "command": "echo ccc"},
		},
		"queries":     []any{"bbb"},
		"concurrency": float64(4), // clamped to len(commands)=3
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Executed 3 commands")
	assert.Contains(t, text, "One")
	assert.Contains(t, text, "Three")
}

// TestExecuteBatchParallel_Speedup proves commands run concurrently: four 1s
// sleeps with a pool of 4 finish in ~1s wall-clock, far below the ~4s a serial
// run would take.
func TestExecuteBatchParallel_Speedup(t *testing.T) {
	exec := executor.NewExecutor(t.TempDir(), 0)
	// Warm up runtime detection so its one-time cost doesn't skew the timing.
	_, _ = exec.Execute(context.Background(), executor.ExecRequest{
		Language: executor.Shell, Code: "true", TimeoutSec: 5,
	})

	commands := []CommandInput{
		{Label: "s1", Command: "sleep 1"},
		{Label: "s2", Command: "sleep 1"},
		{Label: "s3", Command: "sleep 1"},
		{Label: "s4", Command: "sleep 1"},
	}

	start := time.Now()
	out := executeBatchParallel(context.Background(), commands, 30000, 4, exec)
	elapsed := time.Since(start)

	require.Len(t, out, 4)
	assert.Less(t, elapsed, 3*time.Second, "commands should run concurrently, not serially")
}

// TestExecuteBatchParallel_OrderPreserved verifies results stay in input order
// even when commands complete out of order (the index-keyed result slice).
func TestExecuteBatchParallel_OrderPreserved(t *testing.T) {
	exec := executor.NewExecutor(t.TempDir(), 0)
	commands := []CommandInput{
		{Label: "first", Command: "sleep 1; echo ALPHA"},
		{Label: "second", Command: "echo BRAVO"},
		{Label: "third", Command: "sleep 1; echo CHARLIE"},
	}
	out := executeBatchParallel(context.Background(), commands, 30000, 3, exec)
	require.Len(t, out, 3)
	// "second" completes first but must remain in slot 1.
	assert.Contains(t, out[0], "first")
	assert.Contains(t, out[0], "ALPHA")
	assert.Contains(t, out[1], "second")
	assert.Contains(t, out[1], "BRAVO")
	assert.Contains(t, out[2], "third")
	assert.Contains(t, out[2], "CHARLIE")
}

// TestExecuteBatchParallel_ErrorIsolation verifies a failing command lands its
// error in its own slot without skipping or corrupting its siblings.
func TestExecuteBatchParallel_ErrorIsolation(t *testing.T) {
	exec := executor.NewExecutor(t.TempDir(), 0)
	commands := []CommandInput{
		{Label: "ok-one", Command: "echo HELLO"},
		{Label: "broken", Command: "ls /capy-nonexistent-xyz"},
		{Label: "ok-two", Command: "echo WORLD"},
	}
	out := executeBatchParallel(context.Background(), commands, 30000, 3, exec)
	require.Len(t, out, 3)
	assert.Contains(t, out[0], "HELLO")
	assert.Contains(t, out[2], "WORLD")
	// The failing command's stderr (merged via 2>&1) lands in its own slot.
	assert.Contains(t, out[1], "broken")
	assert.Contains(t, strings.ToLower(out[1]), "no such file")
}

// TestExecuteBatchParallel_TimeoutIsolated verifies a timed-out command records
// "(timed out)" without delaying or affecting its siblings.
func TestExecuteBatchParallel_TimeoutIsolated(t *testing.T) {
	exec := executor.NewExecutor(t.TempDir(), 0)
	commands := []CommandInput{
		{Label: "slow", Command: "sleep 30"},
		{Label: "fast", Command: "echo QUICK"},
	}
	start := time.Now()
	out := executeBatchParallel(context.Background(), commands, 2000, 2, exec) // 2s timeout
	elapsed := time.Since(start)

	require.Len(t, out, 2)
	assert.Contains(t, out[0], "(timed out)")
	assert.Contains(t, out[1], "QUICK")
	assert.Less(t, elapsed, 10*time.Second, "fast command must not wait for the slow command's full sleep")
}

// TestExecuteBatchParallel_SubSecondTimeout is a regression for the ms→s
// truncation bug: a sub-second batch timeout must clamp to 1s, not truncate to
// 0 and fall back to the executor's 30s default. Without the guard, "sleep 5"
// would run to completion (no timeout) instead of being cut off near 1s.
func TestExecuteBatchParallel_SubSecondTimeout(t *testing.T) {
	exec := executor.NewExecutor(t.TempDir(), 0)
	commands := []CommandInput{
		{Label: "slow", Command: "sleep 5"},
	}
	start := time.Now()
	out := executeBatchParallel(context.Background(), commands, 500, 1, exec) // 500ms → clamp to 1s
	elapsed := time.Since(start)

	require.Len(t, out, 1)
	assert.Contains(t, out[0], "(timed out)")
	assert.Less(t, elapsed, 4*time.Second, "sub-second timeout must clamp to 1s, not the 30s executor default")
}
