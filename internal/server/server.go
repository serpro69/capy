package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/version"
)

// searchThrottle tracks progressive search throttling per session.
type searchThrottle struct {
	mu          sync.Mutex
	count       int
	windowStart time.Time
}

// advance increments the call count and returns the new count and window age.
// If the window has expired, it resets the window atomically before incrementing.
// Combined into a single lock acquisition to avoid TOCTOU between separate
// increment/age/reset calls.
func (t *searchThrottle) advance(window time.Duration) (int, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	age := time.Since(t.windowStart)
	if age > window {
		t.count = 0
		t.windowStart = time.Now()
		age = 0
	}
	t.count++
	return t.count, age
}

// Server is the capy MCP server.
type Server struct {
	mcpServer      *mcpserver.MCPServer
	store          *store.ContentStore
	executor       *executor.PolyglotExecutor
	security       []security.SecurityPolicy
	readDenyGlobs  [][]string // cached Read deny patterns
	config         *config.Config
	stats          *SessionStats
	throttle       *searchThrottle
	storeMu        sync.Once
	projectDir     string
}

// NewServer creates a new Server. The store is lazily initialized on first use.
func NewServer(
	cfg *config.Config,
	policies []security.SecurityPolicy,
	exec *executor.PolyglotExecutor,
	projectDir string,
) *Server {
	return &Server{
		config:        cfg,
		security:      policies,
		readDenyGlobs: security.ReadToolDenyPatterns("Read", projectDir, ""),
		executor:      exec,
		stats:         NewSessionStats(),
		throttle:      &searchThrottle{windowStart: time.Now()},
		projectDir:    projectDir,
	}
}

// getStore returns the lazily-initialized ContentStore.
func (s *Server) getStore() *store.ContentStore {
	s.storeMu.Do(func() {
		dbPath := s.config.ResolveDBPath(s.projectDir)
		s.store = store.NewContentStore(dbPath, s.projectDir, s.config.Store.TitleWeight)
	})
	return s.store
}

// Serve starts the MCP server on stdio and blocks until shutdown.
func (s *Server) Serve(ctx context.Context) error {
	// Unhandled panic recovery
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "capy: unhandled panic: %v\n", r)
		}
	}()

	s.mcpServer = mcpserver.NewMCPServer(
		"capy",
		version.Version,
		mcpserver.WithToolCapabilities(false),
	)

	s.registerTools()

	// Ensure cleanup runs on all exit paths (normal return, signals, parent death).
	defer s.shutdown()

	// Lifecycle guard: shutdown on parent death or signals
	stopGuard := StartLifecycleGuard(func() {
		s.shutdown()
		os.Exit(0)
	})
	defer stopGuard()

	stdio := mcpserver.NewStdioServer(s.mcpServer)
	stdio.SetErrorLogger(log.New(os.Stderr, "capy: ", log.LstdFlags))
	return stdio.Listen(ctx, os.Stdin, os.Stdout)
}

// shutdown cleans up resources.
func (s *Server) shutdown() {
	if s.executor != nil {
		s.executor.CleanupBackgrounded()
	}
	if s.store != nil {
		_ = s.store.Close()
	}
}

// registerToolsForTest is a test helper that only creates the MCP server
// and registers tools, without starting stdio transport.
func (s *Server) registerToolsForTest() {
	s.mcpServer = mcpserver.NewMCPServer(
		"capy",
		version.Version,
		mcpserver.WithToolCapabilities(false),
	)
	s.registerTools()
}

// textResult is a convenience helper for tool handlers.
func textResult(text string) *mcp.CallToolResult {
	return mcp.NewToolResultText(text)
}

// errorResult is a convenience helper for tool handlers returning errors.
func errorResult(text string) *mcp.CallToolResult {
	return mcp.NewToolResultError(text)
}
