package server

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
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
	vault         *vault.VaultStore
	executor      *executor.PolyglotExecutor
	security      []security.SecurityPolicy
	readDenyGlobs [][]string // cached Read deny patterns
	config        *config.Config
	stats         *SessionStats
	throttle      *searchThrottle
	storeMu       sync.Once
	vaultMu       sync.Once
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

// getVault returns the server's long-lived VaultStore, or nil when the vault is
// disabled (CAPY_VAULT_KEY unset). The store is constructed once (vaultMu); its
// connection opens lazily on first use (VaultStore.getDB), so merely calling
// getVault never creates vault.db. shutdown() Close()s the handle once, running
// the WAL checkpoint. This single handle is shared by the startup sweep, the
// doctor/stats readers, and — from Task 7/8 — capy_vault_search and capy_search
// federation, so the search path never pays a per-call open.
//
// The enablement decision is made once, on the first call: the key is read
// inside the sync.Once, so a later env change does not flip an already-created
// handle. That matches production (the key is fixed at startup) and keeps each
// test's fresh Server independent.
func (s *Server) getVault() *vault.VaultStore {
	s.vaultMu.Do(func() {
		if _, err := vault.RequireVaultKey(); err != nil {
			return // vault is opt-in; leave s.vault nil so callers can degrade loudly
		}
		s.vault = vault.NewVaultStore(vault.VaultDBPath())
	})
	return s.vault
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

	// Background vault sweep: archive the current project's sessions into the
	// encrypted vault. Opt-in via CAPY_VAULT_KEY. This is the sole session
	// ingestion path — the legacy knowledge.db session sweep was removed once
	// the vault became the searchable session corpus (vault-session-search D8).
	// bgWg ensures the sweep finishes before shutdown() returns; shutdown() then
	// Close()s the server-owned vault handle (WAL checkpoint) — the sweep no
	// longer owns or closes the handle (Task 6).
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
	// Close the server-owned vault handle after the sweep goroutine has finished
	// (bgWg.Wait). Close runs the WAL checkpoint that flushes vault.db-wal; it is
	// a no-op when the handle was never opened (key set but nothing to sweep).
	if s.vault != nil {
		if err := s.vault.Close(); err != nil {
			slog.Warn("closing vault store", "error", err)
		}
	}
}

// sweepSummary reports what one vaultSweep run did, per platform. Production
// only logs it; the sweep tests read it to pin the accepted per-startup bound
// (design § Server startup sweep) — the vault package's rollout-open hook is
// unexported, and DiscoveryReport.FirstLineReads counts exactly the opens the
// Codex walker performs, so asserting on the report is the same observation.
type sweepSummary struct {
	claudeDiscovered int                // Claude sessions discovered for this run's scope
	claude           vault.ImportResult // zero when no Claude session was discovered
	codexReport      vault.DiscoveryReport
	codexDiscovered  int                // rollouts that survived the skip predicate (first line read)
	codexMatched     int                // …and the project filter (all of them under CAPY_VAULT_SWEEP_ALL)
	codex            vault.ImportResult // zero when no rollout matched
}

// vaultSweep archives the current project's sessions from every platform root
// present on this machine into the encrypted vault: Claude Code first (the
// project's mangled session dir), then Codex rollouts whose recorded cwd is
// this project (design § Server startup sweep). With CAPY_VAULT_SWEEP_ALL set
// it sweeps every project of both platforms instead; that is opt-in because a
// startup walk across hundreds of projects adds latency and vault.db write
// contention. It is opt-in on the vault itself too: with CAPY_VAULT_KEY unset
// getVault returns nil and the sweep returns silently.
//
// Each platform is discovered INDEPENDENTLY: a missing root, a discovery error
// or an empty result for one platform is logged (debug for the common
// nothing-here cases, warn for a real failure) and never returns early, so a
// Codex-only project is swept even though it has no Claude session dir. The
// vault is opened once, before Codex discovery, because the Codex walker is
// handed a skip predicate built from the rows already archived; it is not
// opened at all when neither platform has anything here, so an empty startup
// never creates vault.db.
//
// Accepted per-startup bound (design § Server startup sweep, Assumption 12):
// the predicate drops, from directory metadata alone, every plain rollout
// already archived at its current (relative path, on-disk size) and every
// archived .zst rollout at its path — rollouts are append-only and compressed
// ones immutable, so those are unchanged and are never opened. Every OTHER
// rollout costs one bounded first-line read on EVERY server start: rollouts
// appended since the last sweep (desired), and rollouts this vault has not
// archived at their current location — including every rollout whose cwd is
// another project, which the default sweep never imports and therefore never
// puts in the map. The cost is O(unarchived corpus) per start and O(changed)
// once the corpus is archived (`capy vault import` or CAPY_VAULT_SWEEP_ALL);
// the 30-second budget defers the remainder to the next start. A negative
// cache for non-matching rollouts is deliberately not built (design § Not
// Doing).
//
// The sweep reuses the server-owned VaultStore (getVault) and does NOT close it
// — shutdown() Close()s the handle (WAL checkpoint) after bgWg.Wait(). ctx
// provides cooperative cancellation, so a shutdown mid-sweep stops at the next
// session boundary rather than blocking bgWg.Wait(). Failures are logged,
// never fatal — sessions stay recoverable via `capy vault import`.
func (s *Server) vaultSweep(ctx context.Context) sweepSummary {
	var sum sweepSummary
	st := s.getVault()
	if st == nil {
		return sum // vault is opt-in; not configured
	}

	allProjects := os.Getenv("CAPY_VAULT_SWEEP_ALL") != ""

	claude := s.discoverClaudeForSweep(ctx, allProjects)
	codexHome, codexPresent := codexHomeForSweep()
	sum.claudeDiscovered = len(claude)
	if len(claude) == 0 && !codexPresent {
		return sum // nothing to sweep on this machine; never create vault.db for it
	}

	// Fail fast on a wrong key / corrupt vault: getDB() opens lazily, so without
	// this probe a bad key would surface only on the first batch flush — after
	// Import has scanned and hashed up to a full batch of sessions — and then as
	// N identical per-session errors instead of one clean abort. The handle stays
	// open for later readers; shutdown() closes it. The Codex skip predicate
	// needs the open handle too (CodexLocationSizes).
	if err := st.Open(ctx); err != nil {
		slog.Warn("vault sweep: cannot open vault store", "error", err)
		return sum
	}

	if len(claude) > 0 {
		if allProjects {
			// Logged after the open probe (so it never precedes an open failure) but
			// before Import, so it confirms the opt-in took effect even on a run where
			// every session is already archived (Import would then log nothing).
			slog.Info("vault sweep (all projects)", "platform", vault.PlatformClaudeCode,
				"projects", countProjects(claude), "sessions", len(claude))
		}
		sum.claude = vault.Import(ctx, st, claude, s.vaultImportOptions())
		if r := sum.claude; r.Imported > 0 || r.Updated > 0 || r.Excluded > 0 || r.Errors > 0 {
			slog.Info("vault sweep", "platform", vault.PlatformClaudeCode,
				"imported", r.Imported, "updated", r.Updated,
				"skipped", r.Skipped, "excluded", r.Excluded, "errors", r.Errors)
		}
	}

	if codexPresent {
		if ctx.Err() != nil {
			// The budget ran out during the Claude pass; the next start picks
			// Codex up (Import itself stops at the session boundary the same way).
			slog.Debug("vault sweep: cancelled before the codex pass")
			return sum
		}
		s.sweepCodex(ctx, st, codexHome, allProjects, &sum)
	}
	return sum
}

// discoverClaudeForSweep discovers the Claude Code sessions in this run's scope:
// the current project's mangled session dir, or every project under the Claude
// projects root with allProjects. Any failure yields nil — logged, never fatal —
// so the Codex pass still runs.
func (s *Server) discoverClaudeForSweep(ctx context.Context, allProjects bool) []vault.SessionFile {
	var sessionDir string
	if allProjects {
		// ClaudeProjectsDir honors CLAUDE_CONFIG_DIR, and DiscoverSessions
		// auto-detects a projects-root input and walks each project subdir. A
		// failure to resolve the root is worth a warning here (unlike the common
		// no-sessions-yet case below) — the user explicitly opted into the
		// all-projects sweep.
		root, err := config.ClaudeProjectsDir()
		if err != nil {
			slog.Warn("vault sweep (all projects): cannot resolve projects dir", "error", err)
			return nil
		}
		sessionDir = root
	} else {
		dir, err := vault.ProjectSessionDir(s.projectDir)
		if err != nil {
			slog.Warn("vault sweep: cannot resolve session directory", "project", s.projectDir, "error", err)
			return nil
		}
		sessionDir = dir
	}

	sessions, err := vault.DiscoverSessions(ctx, sessionDir)
	if err != nil {
		// A project with no session directory yet is the common case at startup,
		// not an error worth a warning. A cancelled walk (budget spent) is a
		// partial list that Import would refuse anyway — drop it like a failure.
		slog.Debug("vault sweep: claude discovery skipped", "dir", sessionDir, "error", err)
		return nil
	}
	return sessions
}

// codexHomeForSweep resolves the Codex home (config.CodexHome, honoring
// CODEX_HOME) and reports whether it holds a rollout root. A machine without
// Codex is the common case and is a debug line, not a warning (design §
// Observability).
func codexHomeForSweep() (home string, present bool) {
	home, err := config.CodexHome()
	if err != nil {
		slog.Debug("vault sweep: cannot resolve codex home", "error", err)
		return "", false
	}
	if !vault.HasCodexRolloutRoot(home) {
		slog.Debug("vault sweep: no codex rollout root, skipping", "home", home)
		return "", false
	}
	return home, true
}

// sweepCodex runs the Codex half of vaultSweep against an already-open vault:
// load the archived (relative path → uncompressed size) map once, hand the
// walker a skip predicate built from it so unchanged rollouts are never opened,
// filter the survivors to this project by canonical cwd (or keep all of them
// with allProjects), import, and log. See vaultSweep for the accepted bound.
func (s *Server) sweepCodex(ctx context.Context, st *vault.VaultStore, home string, allProjects bool, sum *sweepSummary) {
	archived, err := st.CodexLocationSizes(ctx)
	if err != nil {
		slog.Warn("vault sweep: cannot load archived codex locations", "error", err)
		return
	}
	skip := func(relPath string, onDiskSize int64, compressed bool) bool {
		size, ok := archived[relPath]
		if !ok {
			return false
		}
		// A plain rollout is unchanged when its on-disk size equals the archived
		// (uncompressed) size — rollouts are append-only, so a same-size file at
		// the same path has the same content. A .zst rollout is immutable once
		// written, so its path alone identifies it; its on-disk size is the
		// compressed size and is not comparable.
		return compressed || size == onDiskSize
	}

	sessions, report, err := vault.DiscoverCodexSessions(ctx, home, vault.CodexDiscoverOptions{Skip: skip})
	sum.codexReport = report
	if err != nil {
		if ctx.Err() != nil {
			// The 30 s budget ran out mid-walk: the partial list is dropped
			// (Import would refuse it at its first session boundary anyway) and
			// the next start resumes from what is already archived — the same
			// "defer to the next start" the Claude pass applies.
			slog.Debug("vault sweep: codex discovery cancelled", "home", home,
				"first_line_reads", report.FirstLineReads, "error", err)
			return
		}
		slog.Warn("vault sweep: codex discovery failed", "home", home, "error", err)
		return
	}
	sum.codexDiscovered = len(sessions)

	matched := sessions
	if !allProjects {
		matched = filterCodexByProject(sessions, s.projectDir)
	} else if len(sessions) > 0 {
		slog.Info("vault sweep (all projects)", "platform", vault.PlatformCodex,
			"projects", countProjects(sessions), "sessions", len(sessions))
	}
	sum.codexMatched = len(matched)
	if len(matched) > 0 {
		sum.codex = vault.Import(ctx, st, matched, s.vaultImportOptions())
	}

	// One line per start carrying every count the design asks for (per-platform
	// results, predicate skips, first-line reads, skipped revert variants). It is
	// info when the run changed the vault, excluded sessions, or skipped revert
	// variants the user should know about; otherwise debug, like a quiet Claude run.
	level := slog.LevelDebug
	if r := sum.codex; r.Imported > 0 || r.Updated > 0 || r.Excluded > 0 || r.Errors > 0 || len(report.SkippedRevertVariants) > 0 {
		level = slog.LevelInfo
	}
	slog.Log(ctx, level, "vault sweep", "platform", vault.PlatformCodex,
		"discovered", len(sessions), "matched", len(matched),
		"skipped_by_predicate", report.SkippedByPredicate,
		"first_line_reads", report.FirstLineReads,
		"skipped_revert_variants", len(report.SkippedRevertVariants),
		"imported", sum.codex.Imported, "updated", sum.codex.Updated,
		"skipped", sum.codex.Skipped, "excluded", sum.codex.Excluded, "errors", sum.codex.Errors)
}

func (s *Server) vaultImportOptions() vault.ImportOptions {
	var opts vault.ImportOptions
	if s.config != nil {
		opts.MinSessionBytes = s.config.Vault.MinSessionBytes
	}
	return opts
}

// filterCodexByProject keeps the rollouts whose recorded cwd (the discovery
// first-line hint) is projectDir, comparing both sides in canonical form
// (canonicalDir). A rollout with no cwd hint cannot be attributed to any project
// and is dropped; CAPY_VAULT_SWEEP_ALL or `capy vault import` reaches it.
// Canonicalization stats the filesystem (EvalSymlinks), so it is memoized per
// distinct cwd: a corpus of many rollouts spans only a handful of projects, and
// the per-start cost is then O(projects) syscalls, not O(rollouts).
func filterCodexByProject(sessions []vault.SessionFile, projectDir string) []vault.SessionFile {
	want := canonicalDir(projectDir)
	canonical := make(map[string]string)
	var out []vault.SessionFile
	for _, sf := range sessions {
		if sf.ProjectPath == "" {
			continue
		}
		got, ok := canonical[sf.ProjectPath]
		if !ok {
			got = canonicalDir(sf.ProjectPath)
			canonical[sf.ProjectPath] = got
		}
		if got == want {
			out = append(out, sf)
		}
	}
	return out
}

// canonicalDir returns dir as a cleaned, absolute, symlink-resolved path for
// equality comparison (design Assumption 10). A path that cannot be resolved —
// a cwd whose directory no longer exists — falls back to its cleaned absolute
// form, so two such stale paths still compare consistently with each other.
func canonicalDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}

// countProjects returns the number of distinct projects represented in the
// discovered sessions, for the all-projects sweep summary log: a Claude session
// is keyed by its mangled project dir, a Codex rollout by its cwd hint.
func countProjects(sessions []vault.SessionFile) int {
	seen := make(map[string]struct{}, len(sessions))
	for _, sf := range sessions {
		key := sf.ProjectDir
		if sf.Platform.OrClaude() == vault.PlatformCodex {
			key = sf.ProjectPath
		}
		seen[key] = struct{}{}
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
