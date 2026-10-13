package server

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

type failingStdioOutput struct {
	fail atomic.Bool
	err  error
}

func (w *failingStdioOutput) Write(p []byte) (int, error) {
	if w.fail.Load() {
		return 0, w.err
	}
	return len(p), nil
}

// A non-pipe output failure has no SIGPIPE to cancel the lifecycle guard.
// Exercise the real MCP worker join with stdin deliberately kept open.
func TestStdioWriteFailureCancelsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input, producer := io.Pipe()
	defer input.Close()
	defer producer.Close()
	want := errors.New("synthetic output failure")
	output := &failingStdioOutput{err: want}
	transport := &cancelingStdio{input: input, output: output, cancel: cancel}
	srv := mcpserver.NewMCPServer("fixture", "1")
	started, stopped := make(chan struct{}), make(chan struct{})
	srv.AddTool(mcp.NewTool("block"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return mcp.NewToolResultText("canceled"), nil
	})
	done := make(chan error, 1)
	go func() { done <- mcpserver.NewStdioServer(srv).Listen(ctx, transport, transport) }()
	t.Cleanup(func() { cancel(); input.Close(); producer.Close() })
	for _, message := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"block","arguments":{}}}`,
	} {
		_, err := io.WriteString(producer, message+"\n")
		require.NoError(t, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	output.fail.Store(true)
	_, err := io.WriteString(producer, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`+"\n")
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("output failure did not cancel before worker join")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("active worker did not finish")
	}
	require.ErrorIs(t, transport.Err(), want)
}
