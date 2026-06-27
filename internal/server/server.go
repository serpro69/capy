package server

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/session"
	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/vault"
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
	mcpServer     *mcpserver.MCPServer
	store         *store.ContentStore
	executor      *executor.PolyglotExecutor
	security      []security.SecurityPolicy
	readDenyGlobs [][]string // cached Read deny patterns
	config        *config.Config
	stats         *SessionStats
	throttle      *searchThrottle
	storeMu       sync.Once
	bgWg          sync.WaitGroup
	projectDir    string
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
		s.store = store.NewContentStore(
			dbPath,
			s.config.DBProjectDir(s.projectDir),
			s.config.Store.TitleWeight,
			s.config.Store.MaxSourceBytes,
		)
		// Wire the Read deny-policy into stale auto-refresh so a file whose
		// deny status changed since indexing is not re-read (TOCTOU defense).
		s.store.SetDenyChecker(func(filePath string) bool {
			denied, _ := security.EvaluateFilePath(filePath, s.readDenyGlobs, s.projectDir)
			return denied
		})
	})
	return s.store
}

// ephemeralTTL resolves the ephemeral-source TTL from config with a
// safe 24h fallback. The loader rejects EphemeralTTLHours < 1 so the
// non-positive branch should be unreachable in production, but we guard
// it anyway for defense-in-depth — a 0 would cascade into cleanup.go's
// non-positive-TTL branch and log a warning on every Stats/Cleanup call.
// Also covers test paths that construct a Server without a config.
func (s *Server) ephemeralTTL() time.Duration {
	if s.config == nil || s.config.Store.Cleanup.EphemeralTTLHours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(s.config.Store.Cleanup.EphemeralTTLHours) * time.Hour
}

// sessionTTL resolves the session-source TTL from config with a safe 60-day
// fallback. Mirrors ephemeralTTL for the session kind.
func (s *Server) sessionTTL() time.Duration {
	if s.config == nil || s.config.Store.Cleanup.SessionTTLDays <= 0 {
		return 60 * 24 * time.Hour
	}
	return time.Duration(s.config.Store.Cleanup.SessionTTLDays) * 24 * time.Hour
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

	// Background session sweep: index past Claude Code conversations.
	// Derives context from the server's ctx so it cancels on shutdown.
	// WaitGroup ensures the goroutine completes before shutdown() closes the store.
	s.bgWg.Add(1)
	go func() {
		defer s.bgWg.Done()
		sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		indexed, skipped, errs := session.Sweep(sweepCtx, s.getStore(), s.projectDir)
		if indexed > 0 || errs > 0 {
			slog.Info("session sweep", "indexed", indexed, "skipped", skipped, "errors", errs)
		}
	}()

	// Background vault sweep: archive the current project's sessions into the
	// encrypted vault. Opt-in via CAPY_VAULT_KEY; runs alongside the session
	// sweep. Like it, bgWg ensures the sweep — and VaultStore.Close, which runs
	// the WAL checkpoint — completes before shutdown() returns. shutdown() closes
	// only the knowledge ContentStore, so the sweep owns the vault handle itself.
	s.bgWg.Add(1)
	go func() {
		defer s.bgWg.Done()
		sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		s.vaultSweep(sweepCtx)
	}()

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

// shutdown cleans up resources. Waits for background goroutines to finish
// before closing the store to avoid operating on a closed database.
func (s *Server) shutdown() {
	if s.executor != nil {
		s.executor.CleanupBackgrounded()
	}
	s.bgWg.Wait()
	if s.store != nil {
		_ = s.store.Close()
	}
}

// vaultSweep archives Claude Code sessions into the encrypted vault. By default
// it sweeps only the current project; with CAPY_VAULT_SWEEP_ALL set it walks
// every project under the Claude projects root instead. The all-projects path is
// opt-in because a startup walk across hundreds of projects adds latency and
// vault.db write contention. It is opt-in on the vault itself too: with
// CAPY_VAULT_KEY unset the vault is disabled and the sweep returns silently. The
// sweep owns its VaultStore for the call and Close()s it on return — Close runs
// the WAL checkpoint, and shutdown() (which closes only the knowledge
// ContentStore) never touches the vault, so closing here is what flushes
// vault.db-wal. ctx provides cooperative cancellation, so a shutdown mid-sweep
// stops at the next session boundary rather than blocking bgWg.Wait(). Failures
// are logged, never fatal — sessions stay recoverable via `capy vault import`.
func (s *Server) vaultSweep(ctx context.Context) {
	if _, err := vault.RequireVaultKey(); err != nil {
		return // vault is opt-in; not configured
	}

	allProjects := os.Getenv("CAPY_VAULT_SWEEP_ALL") != ""

	var sessionDir string
	if allProjects {
		// Walk every project under the Claude projects root. ClaudeProjectsDir
		// honors CLAUDE_CONFIG_DIR, and DiscoverSessions auto-detects a
		// projects-root input and walks each project subdir. A failure to resolve
		// the root is worth a warning here (unlike the common no-sessions-yet
		// case below) — the user explicitly opted into the all-projects sweep.
		root, err := config.ClaudeProjectsDir()
		if err != nil {
			slog.Warn("vault sweep (all projects): cannot resolve projects dir", "error", err)
			return
		}
		sessionDir = root
	} else {
		dir, err := vault.ProjectSessionDir(s.projectDir)
		if err != nil {
			slog.Warn("vault sweep: cannot resolve session directory", "project", s.projectDir, "error", err)
			return
		}
		sessionDir = dir
	}

	sessions, err := vault.DiscoverSessions(sessionDir)
	if err != nil {
		// A project with no session directory yet is the common case at startup,
		// not an error worth a warning.
		slog.Debug("vault sweep: discovery skipped", "dir", sessionDir, "error", err)
		return
	}
	if len(sessions) == 0 {
		return
	}

	st := vault.NewVaultStore(vault.VaultDBPath())
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			slog.Warn("vault sweep: closing vault store", "error", closeErr)
		}
	}()

	// Fail fast on a wrong key / corrupt vault: getDB() opens lazily, so without
	// this probe a bad key would surface only on the first batch flush — after
	// Import has scanned and hashed up to a full batch of sessions — and then as
	// N identical per-session errors instead of one clean abort.
	if err := st.Open(ctx); err != nil {
		slog.Warn("vault sweep: cannot open vault store", "error", err)
		return
	}

	if allProjects {
		// Logged after the open probe (so it never precedes an open failure) but
		// before Import, so it confirms the opt-in took effect even on a run where
		// every session is already archived (Import would then log nothing).
		slog.Info("vault sweep (all projects)",
			"projects", countProjects(sessions), "sessions", len(sessions))
	}

	res := vault.Import(ctx, st, sessions, vault.ImportOptions{})
	if res.Imported > 0 || res.Updated > 0 || res.Errors > 0 {
		slog.Info("vault sweep",
			"imported", res.Imported, "updated", res.Updated,
			"skipped", res.Skipped, "errors", res.Errors)
	}
}

// countProjects returns the number of distinct project directories represented
// in the discovered sessions, for the all-projects sweep summary log.
func countProjects(sessions []vault.SessionFile) int {
	seen := make(map[string]struct{}, len(sessions))
	for _, sf := range sessions {
		seen[sf.ProjectDir] = struct{}{}
	}
	return len(seen)
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
