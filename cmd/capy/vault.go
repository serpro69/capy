package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/serpro69/capy/internal/terminal"
	"github.com/serpro69/capy/internal/vault"
	"github.com/serpro69/capy/internal/vault/tui"
	"github.com/spf13/cobra"
)

// vaultEnv carries state resolved once in the vault group's PersistentPreRunE and
// shared by every subcommand.
type vaultEnv struct {
	dbPath string
}

// newVaultCmd builds the `capy vault` command group. The shared PersistentPreRunE
// verifies CAPY_VAULT_KEY (every subcommand needs the encrypted DB) and resolves
// the vault DB path. The --tui flag is shared across subcommands; the interactive
// UI lands in a later task, so the read/browse commands fail loud if it is set.
func newVaultCmd() *cobra.Command {
	env := &vaultEnv{}

	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Archive, search, and restore Claude Code and Codex sessions",
		Long: `Vault keeps a durable, cross-project, encrypted archive of every Claude
Code session and Codex rollout — searchable and restorable after compaction,
auto-cleanup, or accidental deletion.

Requires CAPY_VAULT_KEY (the vault DB is encrypted at rest).`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if _, err := vault.RequireVaultKey(); err != nil {
				return err
			}
			// --path overrides the CAPY_VAULT_PATH env / XDG default resolved by
			// vault.VaultDBPath(). The flag is inherited by every subcommand.
			if p, _ := cmd.Flags().GetString("path"); p != "" {
				env.dbPath = p
			} else {
				env.dbPath = vault.VaultDBPath()
			}
			return nil
		},
	}

	cmd.PersistentFlags().String("path", "", "vault DB file (default: CAPY_VAULT_PATH, else XDG data dir)")
	cmd.PersistentFlags().Bool("tui", false, "interactive terminal UI (list, search, show)")

	cmd.AddCommand(
		newVaultImportCmd(env),
		newVaultReindexCmd(env),
		newVaultListCmd(env),
		newVaultSearchCmd(env),
		newVaultShowCmd(env),
		newVaultStatsCmd(env),
		newVaultCheckpointCmd(env),
		newVaultCompactCmd(env),
		newVaultRekeyCmd(env),
		newVaultMergeCmd(env),
		newVaultRestoreCmd(env),
		newVaultResumeCmd(env),
		newVaultDeleteCmd(env),
		newVaultRenameCmd(env),
		newVaultProjectCmd(env),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// import
// ---------------------------------------------------------------------------

func newVaultImportCmd(env *vaultEnv) *cobra.Command {
	var (
		source       string
		project      string
		platformFlag string
		dryRun       bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Scan and archive Claude Code and Codex sessions into the vault",
		Long: `Discover agent-CLI sessions and archive them into the vault.

By default every platform root that exists on disk is scanned: Claude Code's
projects directory (honoring CLAUDE_CONFIG_DIR) and the Codex home (CODEX_HOME,
default ~/.codex — both sessions/ and archived_sessions/). Pass --platform to
restrict the run to one of them, or --source to import from one directory
(its layout is autodetected, see the flag).

The MCP server's startup sweep only archives the current project. Run
'capy vault import' periodically (e.g. via cron) to capture sessions across
all projects before Claude Code's 30-day cleanup removes them.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			platform, err := parsePlatformFlag(platformFlag)
			if err != nil {
				return err
			}
			minSessionBytes, err := resolveVaultMinSessionBytes(cmd)
			if err != nil {
				return err
			}

			var (
				sessions []vault.SessionFile
				report   vault.DiscoveryReport
			)
			switch {
			case source != "":
				sessions, report, err = vault.DiscoverSessionsReport(cmd.Context(), source)
			case platform != "":
				// Scope discovery, not just the import: the other platform's tree
				// is never walked (and never warns) on a single-platform run, and
				// its root is not even resolved — so --platform codex works with
				// CODEX_HOME set on a machine whose $HOME cannot be resolved.
				sessions, report, err = vault.DiscoverAll(cmd.Context(), nil, platform)
			default:
				sessions, report, err = vault.DiscoverAll(cmd.Context(), nil)
			}
			if err != nil {
				return fmt.Errorf("discovering sessions: %w", err)
			}
			if len(sessions) == 0 {
				fmt.Println("capy vault import: no sessions found")
				printSkippedVariants(report)
				return nil
			}

			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()
			// Fail fast on a wrong key / corrupt DB: Import has no error return and
			// would otherwise report the same open failure once per session.
			if err := st.Open(cmd.Context()); err != nil {
				return err
			}

			res := vault.Import(cmd.Context(), st, sessions, vault.ImportOptions{
				Project: project, DryRun: dryRun, Platform: platform, MinSessionBytes: minSessionBytes,
			})
			printImportResult(res, &report, dryRun)
			if res.Errors > 0 {
				return fmt.Errorf("%d session(s) failed to import", res.Errors)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "import from this directory only — a Claude config dir, projects dir or single project dir, or a Codex home holding sessions/ or archived_sessions/ (default: every platform root that exists)")
	cmd.Flags().StringVar(&project, "project", "", "only import sessions whose Claude project dir or Codex project path matches this substring")
	cmd.Flags().StringVar(&platformFlag, "platform", "", "only import sessions of this platform: claude-code|codex (default: all)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview what would be imported without writing")
	cmd.Flags().Int64("min-size-bytes", 0, "minimum uncompressed size for new sessions, including sidecars (overrides vault.min_session_bytes; 0 disables)")
	return cmd
}

// resolveVaultMinSessionBytes uses the invocation project's config, just like
// serve. A cross-project import/merge applies this one policy to the whole run.
func resolveVaultMinSessionBytes(cmd *cobra.Command) (int64, error) {
	projectDir := config.DetectProjectRoot()
	if f := cmd.Flag("project-dir"); f != nil && f.Value.String() != "" {
		projectDir = f.Value.String()
	}
	cfg, err := config.Load(projectDir)
	if err != nil {
		return 0, fmt.Errorf("loading vault configuration: %w", err)
	}
	minimum := cfg.Vault.MinSessionBytes
	if cmd.Flags().Changed("min-size-bytes") {
		minimum, err = cmd.Flags().GetInt64("min-size-bytes")
		if err != nil {
			return 0, err
		}
	}
	if minimum < 0 {
		return 0, fmt.Errorf("--min-size-bytes must be >= 0 (0 disables size filtering)")
	}
	return minimum, nil
}

// importPlatformOrder fixes the order the per-platform summary lines print in.
var importPlatformOrder = []vault.Platform{vault.PlatformClaudeCode, vault.PlatformCodex}

// printImportResult prints the per-session table and the summary of an import or
// merge run. report is the discovery report of an import run (nil for merge,
// which discovers nothing); its skipped revert-variant count is printed after
// the summary. When the run touched more than one platform, a per-platform
// summary line follows the total.
func printImportResult(res vault.ImportResult, report *vault.DiscoveryReport, dryRun bool) {
	if dryRun {
		fmt.Println("DRY RUN — no changes written")
	}
	if len(res.Sessions) == 0 {
		fmt.Println("no sessions matched")
		if report != nil {
			printSkippedVariants(*report)
		}
		return
	}
	fmt.Printf("%-12s  %-8s  %-28s  %8s  %-50s  %s\n", "UUID", "STATUS", "PROJECT", "SIZE", "TITLE", "REASON")
	for _, s := range res.Sessions {
		if s.Status == vault.StatusError && s.Err != nil {
			fmt.Fprintf(os.Stderr, "  error %s: %v\n", shortUUID(s.UUID, s.Platform), s.Err)
		}
		fmt.Printf("%-12s  %-8s  %-28s  %8s  %-50s  %s\n",
			shortUUID(s.UUID, s.Platform), s.Status, truncate(displayPath(s.ProjectPath), 28),
			formatSize(s.SizeBytes), truncate(s.Title, 50), s.Reason)
	}
	fmt.Printf("\n%s\n", importCounts{
		imported: res.Imported, updated: res.Updated, skipped: res.Skipped, excluded: res.Excluded, errs: res.Errors,
	})

	byPlatform := map[vault.Platform]*importCounts{}
	for _, s := range res.Sessions {
		if s.Platform == "" {
			continue // a row that predates the field (never from Import or MergeFrom today)
		}
		if byPlatform[s.Platform] == nil {
			byPlatform[s.Platform] = &importCounts{}
		}
		byPlatform[s.Platform].add(s.Status)
	}
	if len(byPlatform) > 1 {
		for _, p := range importPlatformOrder {
			if c := byPlatform[p]; c != nil {
				fmt.Printf("  %s: %s\n", p, c)
			}
		}
	}
	if report != nil {
		printSkippedVariants(*report)
	}
}

// importCounts tallies per-status outcomes for one summary line (the whole run,
// or one platform of it).
type importCounts struct{ imported, updated, skipped, excluded, errs int }

func (c *importCounts) add(status string) {
	switch status {
	case vault.StatusNew:
		c.imported++
	case vault.StatusUpdated:
		c.updated++
	case vault.StatusSkipped:
		c.skipped++
	case vault.StatusExcluded:
		c.excluded++
	case vault.StatusError:
		c.errs++
	}
}

func (c importCounts) String() string {
	return fmt.Sprintf("imported %d, updated %d, skipped %d, excluded %d, errors %d",
		c.imported, c.updated, c.skipped, c.excluded, c.errs)
}

// printSkippedVariants reports the Codex `_<rollout_id>` revert rollouts
// discovery skipped (the paths were already logged as warnings by discovery).
func printSkippedVariants(report vault.DiscoveryReport) {
	if n := len(report.SkippedRevertVariants); n > 0 {
		fmt.Printf("skipped %d codex revert rollout variant(s) — not archived in this version (see the discovery warnings for paths)\n", n)
	}
}

// ---------------------------------------------------------------------------
// reindex
// ---------------------------------------------------------------------------

func newVaultReindexCmd(env *vaultEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild the search index for sessions indexed by an older version",
		Long: `Re-scan archived sessions whose search index predates the current indexer
and rebuild their FTS rows in place. Reads from the vault DB (not disk), so it
upgrades even sessions that were archived and later deleted from your Claude
projects directory. Only the search index is rewritten — transcripts and sidecars
are left untouched.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()
			if err := st.Open(cmd.Context()); err != nil {
				return err
			}

			res, err := vault.Reindex(cmd.Context(), st)
			if err != nil {
				return err
			}
			fmt.Printf("reindexed %d, errors %d\n", res.Reindexed, res.Errors)
			if res.Errors > 0 {
				return fmt.Errorf("%d session(s) failed to reindex", res.Errors)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

func newVaultListCmd(env *vaultEnv) *cobra.Command {
	var (
		project         string
		name            string
		platformFlag    string
		includeChildren bool
		limit           int
		jsonOut         bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List archived sessions, newest first",
		Long: `List archived sessions, newest first.

Sub-agent sessions (Codex child rollouts, which carry a parent session) are
hidden by default — mirroring Codex's own pickers, which reach them from the
parent. Pass --include-children to list them; a child row shows "↳ <parent id>"
before its title.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			platform, err := parsePlatformFlag(platformFlag)
			if err != nil {
				return err
			}
			if tuiRequested(cmd) {
				return launchTUI(cmd, env, tui.Options{Mode: "list", Platform: platform})
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sessions, err := st.ListSessions(cmd.Context(), vault.ListOptions{
				Project: project, Name: name, Limit: limit,
				Platform: platform, IncludeChildren: includeChildren,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(sessionsToJSON(sessions))
			}
			printSessionTable(sessions)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "filter by effective project substring (custom name or imported path; literal, ASCII case-insensitive)")
	cmd.Flags().StringVar(&name, "name", "", "filter by title substring (case-insensitive, literal; matches the effective title)")
	cmd.Flags().StringVar(&platformFlag, "platform", "", "only sessions from this platform: claude-code|codex")
	cmd.Flags().BoolVar(&includeChildren, "include-children", false, "also list sub-agent (child) sessions, hidden by default")
	cmd.Flags().IntVar(&limit, "limit", 50, "max sessions to list (0 = no limit)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

// parsePlatformFlag validates a --platform value; "" means no restriction.
func parsePlatformFlag(s string) (vault.Platform, error) {
	if s == "" {
		return "", nil
	}
	p, err := vault.ParsePlatform(s)
	if err != nil {
		return "", fmt.Errorf("invalid --platform %q (want %s|%s)", s, vault.PlatformClaudeCode, vault.PlatformCodex)
	}
	return p, nil
}

// platformColumnWidth fits the longest stored platform value ("claude-code").
const platformColumnWidth = 11

// uuidColumnWidth fits the 12-character Codex short id (shortUUID); Claude's
// 8-character id is padded to it.
const uuidColumnWidth = 12

func printSessionTable(sessions []vault.Session) {
	if len(sessions) == 0 {
		fmt.Println("no sessions archived")
		return
	}
	fmt.Printf("%-*s  %-*s  %-10s  %5s  %8s  %-28s  %s\n",
		uuidColumnWidth, "UUID", platformColumnWidth, "PLATFORM", "DATE", "MSGS", "SIZE", "PROJECT", "TITLE")
	for _, s := range sessions {
		fmt.Printf("%-*s  %-*s  %-10s  %5d  %8s  %-28s  %s\n",
			uuidColumnWidth, shortUUID(s.UUID, s.Platform), platformColumnWidth, s.Platform.OrClaude(),
			fmtDate(s.EndTime), s.MessageCount, formatSize(s.SizeBytes),
			truncate(displaySessionProject(s), 28), truncate(sessionTitleCell(s), 60))
	}
}

// sessionTitleCell is a list row's title, prefixed with "↳ <parent id>" for a
// child session so its parent is visible without a second lookup. The parent
// id is rendered with the child's platform: parent and child always share one
// (a Codex child is spawned by a Codex parent).
func sessionTitleCell(s vault.Session) string {
	title := s.EffectiveTitle()
	if s.ParentUUID == "" {
		return title
	}
	return "↳ " + shortUUID(s.ParentUUID, s.Platform) + "  " + title
}

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

func newVaultSearchCmd(env *vaultEnv) *cobra.Command {
	var (
		raw     bool
		project string
		role    string
		after   string
		before  string
		limit   int
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across archived sessions",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tuiRequested(cmd) {
				return launchTUI(cmd, env, tui.Options{Mode: "search", Query: strings.Join(args, " ")})
			}
			afterT, err := parseDateFlag(after, false)
			if err != nil {
				return err
			}
			beforeT, err := parseDateFlag(before, true)
			if err != nil {
				return err
			}
			if role != "" && !validRole(role) {
				return fmt.Errorf("invalid --role %q (want user|assistant|tool|system)", role)
			}

			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			results, err := st.Search(cmd.Context(), vault.SearchOptions{
				Query:   strings.Join(args, " "),
				Raw:     raw,
				Project: project,
				Role:    role,
				After:   afterT,
				Before:  beforeT,
				Limit:   limit,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(resultsToJSON(results))
			}
			printSearchResults(results)
			return nil
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "pass the query as raw FTS5 MATCH syntax (default: plain keywords)")
	cmd.Flags().StringVar(&project, "project", "", "filter by project path substring")
	cmd.Flags().StringVar(&role, "role", "", "filter by role: user|assistant|tool|system")
	cmd.Flags().StringVar(&after, "after", "", "only matches on or after this date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&before, "before", "", "only matches on or before this date (YYYY-MM-DD)")
	cmd.Flags().IntVar(&limit, "limit", 20, "max results")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func printSearchResults(results []vault.SearchResult) {
	if len(results) == 0 {
		fmt.Println("no matches")
		return
	}
	fmt.Printf("%-*s  %-*s  %-10s  %-10s  %-24s  %-20s  %s\n",
		uuidColumnWidth, "UUID", searchPlatformColumnWidth, "PLATFORM", "DATE", "ROLE", "PROJECT", "TITLE", "SNIPPET")
	for _, r := range results {
		role := r.Role
		if r.SubagentID != "" {
			role += "*" // subagent match
		}
		fmt.Printf("%-*s  %-*s  %-10s  %-10s  %-24s  %-20s  %s\n",
			uuidColumnWidth, shortUUID(r.SessionUUID, r.Platform), searchPlatformColumnWidth, searchPlatformCell(r),
			fmtDate(r.EndTime), truncate(role, 10),
			truncate(displayPath(r.ProjectPath), 24), truncate(r.Title, 20), oneLine(r.Snippet))
	}
}

// childMarker tags a search hit from a child (sub-agent) session.
const childMarker = "(child)"

// searchPlatformColumnWidth fits "claude-code (child)".
const searchPlatformColumnWidth = platformColumnWidth + 1 + len(childMarker)

// searchPlatformCell is a hit's platform, followed by childMarker when the hit's
// session is a child of another session.
func searchPlatformCell(r vault.SearchResult) string {
	cell := r.Platform.OrClaude().String()
	if r.ParentUUID != "" {
		cell += " " + childMarker
	}
	return cell
}

// ---------------------------------------------------------------------------
// show
// ---------------------------------------------------------------------------

func newVaultShowCmd(env *vaultEnv) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "show <session-id>",
		Short: "Display a full archived session (partial UUID, 8+ chars)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tuiRequested(cmd) {
				return launchTUI(cmd, env, tui.Options{Mode: "view", SessionID: args[0]})
			}
			format = strings.ToLower(format)
			if format != "text" && format != "markdown" && format != "json" {
				return fmt.Errorf("invalid --format %q (want text|markdown|json)", format)
			}

			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sess, err := st.GetSession(cmd.Context(), args[0])
			if err != nil {
				return handleLookupError(args[0], err)
			}

			if format == "json" {
				return writeRaw(sess.RawJSONL)
			}

			files, err := st.GetFiles(cmd.Context(), sess.UUID)
			if err != nil {
				return err
			}
			children, err := st.Children(cmd.Context(), sess.UUID)
			if err != nil {
				return err
			}
			markdown := format == "markdown"
			content := renderShow(sess, files, children, markdown)
			if markdown {
				fmt.Print(content)
				return nil
			}
			return pageOrPrint(content)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|markdown|json")
	return cmd
}

// renderShow composes a session's main transcript with each subagent transcript
// appended as its own clearly-marked section. Non-JSONL sidecars (tool-results,
// meta.json) are archive-only and not rendered. Inline interleaving of subagents
// at their launch point is a TUI concern (Task 6); the design blesses standalone
// subagent rendering as spec-conformant. children are the session's child
// sessions (Codex sub-agent rollouts, metadata only); they are named in the
// header, not rendered — each is its own archived session.
func renderShow(sess *vault.Session, files []vault.File, children []vault.Session, markdown bool) string {
	render := vault.RenderText
	if markdown {
		render = vault.RenderMarkdown
	}

	// The stored platform (migration 0006) selects the decoder for the main
	// transcript; the assistant heading follows it ("Claude" / "Codex").
	var sb strings.Builder
	writeShowHeader(&sb, sess, children, markdown)
	sb.WriteString(render(sess.Platform.OrClaude(), sess.RawJSONL))

	for _, f := range files {
		id := subagentDisplayID(f.RelativePath)
		if id == "" {
			continue
		}
		if markdown {
			fmt.Fprintf(&sb, "\n---\n\n# Subagent %s\n\n", id)
		} else {
			fmt.Fprintf(&sb, "\n\n=== Subagent %s ===\n\n", id)
		}
		// Sidecars are a Claude Code concept: a subagent transcript is always
		// Claude JSONL regardless of the platform column.
		sb.WriteString(render(vault.PlatformClaudeCode, f.RawContent))
	}
	return sb.String()
}

// writeShowHeader writes the session header: title, uuid, platform, project,
// branch, dates; for a child session its parent; for a parent its children (in
// spawn order, as Children returns them). Parent and child ids use the
// session's platform for their short form — a child always shares its parent's
// platform.
func writeShowHeader(sb *strings.Builder, sess *vault.Session, children []vault.Session, markdown bool) {
	title := sess.EffectiveTitle()
	if title == "" {
		title = "(untitled)"
	}
	platform := sess.Platform.OrClaude()
	childIDs := make([]string, 0, len(children))
	for _, c := range children {
		childIDs = append(childIDs, shortUUID(c.UUID, platform))
	}
	if markdown {
		fmt.Fprintf(sb, "# %s\n\n", title)
		fmt.Fprintf(sb, "- **UUID:** %s\n- **Platform:** %s\n- **Project:** %s\n",
			sess.UUID, platform, displaySessionProject(*sess))
		if sess.EffectiveProject() != sess.ProjectPath {
			fmt.Fprintf(sb, "- **Original path:** %s\n", displayPath(sess.ProjectPath))
		}
		fmt.Fprintf(sb, "- **Branch:** %s\n- **Dates:** %s – %s\n",
			orDash(sess.GitBranch), fmtDateTime(sess.StartTime), fmtDateTime(sess.EndTime))
		if sess.ParentUUID != "" {
			fmt.Fprintf(sb, "- **Parent:** %s\n", shortUUID(sess.ParentUUID, platform))
		}
		if len(childIDs) > 0 {
			fmt.Fprintf(sb, "- **Children:** %s\n", strings.Join(childIDs, ", "))
		}
		sb.WriteString("\n")
	} else {
		fmt.Fprintf(sb, "%s\n", title)
		fmt.Fprintf(sb, "uuid: %s  platform: %s  project: %s  branch: %s\n",
			sess.UUID, platform, displaySessionProject(*sess), orDash(sess.GitBranch))
		if sess.EffectiveProject() != sess.ProjectPath {
			fmt.Fprintf(sb, "original path: %s\n", displayPath(sess.ProjectPath))
		}
		fmt.Fprintf(sb, "dates: %s – %s\n", fmtDateTime(sess.StartTime), fmtDateTime(sess.EndTime))
		if sess.ParentUUID != "" {
			fmt.Fprintf(sb, "parent: %s\n", shortUUID(sess.ParentUUID, platform))
		}
		if len(childIDs) > 0 {
			fmt.Fprintf(sb, "children: %s\n", strings.Join(childIDs, ", "))
		}
		sb.WriteString("\n")
	}
}

// subagentDisplayID returns a short display id for a subagent JSONL file.
// For "subagents/agent-<id>.jsonl" it strips the "agent-" prefix; for other
// JSONL files under subagents/ it returns the bare filename. Returns "" for
// non-subagent or non-JSONL sidecars.
func subagentDisplayID(rel string) string {
	if !strings.HasPrefix(rel, "subagents/") || !strings.HasSuffix(rel, ".jsonl") {
		return ""
	}
	base := strings.TrimSuffix(strings.TrimPrefix(rel, "subagents/"), ".jsonl")
	return strings.TrimPrefix(base, "agent-")
}

// ---------------------------------------------------------------------------
// stats
// ---------------------------------------------------------------------------

func newVaultStatsCmd(env *vaultEnv) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show vault session counts, size, and per-project breakdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			stats, err := st.Stats(cmd.Context())
			if err != nil {
				return err
			}
			dbBytes := dbFileSize(env.dbPath)
			if jsonOut {
				return printJSON(statsToJSON(stats, dbBytes))
			}
			printStats(stats, dbBytes)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func printStats(s *vault.VaultStats, dbBytes int64) {
	fmt.Printf("Sessions:      %d\n", s.Sessions)
	fmt.Printf("Children:      %d  (sub-agent sessions; hidden from 'list' by default)\n", s.Children)
	fmt.Printf("Content size:  %s\n", formatSize(s.TotalBytes))
	fmt.Printf("DB file size:  %s\n", formatSize(dbBytes))
	fmt.Printf("Oldest:        %s\n", fmtDate(s.Oldest))
	fmt.Printf("Newest:        %s\n", fmtDate(s.Newest))
	indexLine := fmt.Sprintf("Index version: %d", s.IndexVersion)
	if s.OutdatedSessions > 0 {
		indexLine += fmt.Sprintf("  (%d session(s) below current — run 'capy vault reindex')", s.OutdatedSessions)
	}
	fmt.Println(indexLine)
	if len(s.ByPlatform) > 0 {
		fmt.Println("\nPer platform:")
		for _, p := range s.ByPlatform {
			fmt.Printf("  %5d  %8s  %s\n", p.Sessions, formatSize(p.Bytes), p.Platform.OrClaude())
		}
	}
	if len(s.ByProject) > 0 {
		fmt.Println("\nPer project:")
		for _, p := range s.ByProject {
			fmt.Printf("  %5d  %s\n", p.Count, displayPath(p.ProjectPath))
		}
	}
}

// ---------------------------------------------------------------------------
// checkpoint
// ---------------------------------------------------------------------------

func newVaultCheckpointCmd(env *vaultEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "checkpoint",
		Short: "Flush the WAL into vault.db (run before copying it to another machine)",
		Long: `Merge the SQLite write-ahead log into the main vault.db file so the
database is self-contained — required before copying vault.db to another
machine, since recent writes may otherwise live only in vault.db-wal.

No other capy process must hold the vault open during checkpoint.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat(env.dbPath); os.IsNotExist(err) {
				fmt.Printf("capy vault checkpoint: no vault at %s\n", env.dbPath)
				return nil
			}

			st := vault.NewVaultStore(env.dbPath)
			if err := st.Checkpoint(); err != nil {
				return fmt.Errorf("checkpoint failed: %w", err)
			}

			incomplete := false
			for _, suffix := range []string{"-wal", "-shm"} {
				if info, err := os.Stat(env.dbPath + suffix); err == nil && info.Size() > 0 {
					incomplete = true
					fmt.Fprintf(os.Stderr, "capy vault checkpoint: warning: %s still has data (%d bytes) — is another process using the vault?\n",
						env.dbPath+suffix, info.Size())
				}
			}
			if !incomplete {
				fmt.Printf("capy vault checkpoint: %s — WAL flushed\n", env.dbPath)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// compact
// ---------------------------------------------------------------------------

func newVaultCompactCmd(env *vaultEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "compact",
		Short: "Recompress legacy uncompressed blobs and reclaim disk space (VACUUM)",
		Long: `Rewrite sessions archived before compression existed — their transcripts and
sidecars are stored uncompressed — through the zstd codec, then VACUUM the
database to reclaim the freed pages. Sessions already compressed (or whose blobs
do not shrink) are left untouched, so re-running compact is a no-op.

Stop the MCP server first: compact aborts up front if another process still
holds the vault open. It also refuses to run while CAPY_VAULT_NO_COMPRESS is set
(it would rewrite every blob without compressing it).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			if _, err := os.Stat(env.dbPath); os.IsNotExist(err) {
				fmt.Printf("capy vault compact: no vault at %s\n", env.dbPath)
				return nil
			}

			before := dbFileSize(env.dbPath)
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			res, err := st.Compact(cmd.Context())
			if err != nil {
				return err
			}
			printCompactResult(res, before, dbFileSize(env.dbPath))
			return nil
		},
	}
}

// formatSize renders a byte count as a human-friendly B/KB/MB string.
func formatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.0fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

func printCompactResult(res vault.CompactResult, before, after int64) {
	if res.SessionsRewritten == 0 && res.FilesRewritten == 0 {
		fmt.Println("capy vault compact: nothing to compact (no uncompressed sessions remain)")
		return
	}
	fmt.Printf("recompressed %d session(s) and %d file(s)\n", res.SessionsRewritten, res.FilesRewritten)
	line := fmt.Sprintf("vault size: %s → %s", formatSize(before), formatSize(after))
	if after < before {
		line += fmt.Sprintf(" (reclaimed %s)", formatSize(before-after))
	}
	fmt.Println(line)
}

// ---------------------------------------------------------------------------
// rekey
// ---------------------------------------------------------------------------

func newVaultRekeyCmd(env *vaultEnv) *cobra.Command {
	var removeBackup bool
	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Rotate the vault's encryption key to the current CAPY_VAULT_KEY",
		Long: `Re-encrypt vault.db under a new key without a decrypt-and-reimport cycle.

Set CAPY_VAULT_KEY to the NEW passphrase first, then run this command and enter
the CURRENT (old) passphrase when prompted. The vault is copied into a fresh
database encrypted with the new key, and the pre-rotation database is preserved
as <vault>.bak.

Note this differs from 'capy encrypt': rekey requires the new key in
CAPY_VAULT_KEY and refuses to run if it is unset, and it rejects a new key
identical to the old (a forgotten env-var update would otherwise silently
"rotate" a compromised key to itself).

STOP THE MCP SERVER FIRST. Rotation finishes by renaming files into place, which
SQLite's locking does not mediate — a still-attached server could keep writing to
the old file descriptor and lose those writes. Unlike 'vault compact' (whose
VACUUM is genuinely lock-protected), there is no reliable busy check here; the
old-key checkpoint inside rekey is best-effort only. Stopping the server is the
operator's responsibility.

The <vault>.bak left behind is still decryptable by the OLD key. When you are
rotating a COMPROMISED key, that backup is a liability — pass --remove-backup to
delete it once the new vault verifies open. Deletion is NOT guaranteed erasure:
on SSD and copy-on-write filesystems, wear-levelling and CoW can leave
recoverable copies. True erasure depends on your disk and filesystem and is the
operator's responsibility.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			if _, err := os.Stat(env.dbPath); os.IsNotExist(err) {
				fmt.Printf("capy vault rekey: no vault at %s\n", env.dbPath)
				return nil
			}

			// The vault group's PersistentPreRunE already verified CAPY_VAULT_KEY is
			// set; its value is the NEW key the vault rotates to.
			newKey, err := vault.RequireVaultKey()
			if err != nil {
				return err
			}

			oldKey, err := terminal.ReadPassphrase("Current vault passphrase: ")
			if err != nil {
				return fmt.Errorf("reading current passphrase: %w", err)
			}
			if oldKey == "" {
				return fmt.Errorf("current passphrase cannot be empty")
			}
			// Fail fast on a no-op rotation before prompting to confirm. The same
			// guard inside runVaultRekey is the authoritative check (and keeps the
			// core unit-testable); this one only spares the user a confirmation for a
			// rotation that would be rejected anyway.
			if oldKey == newKey {
				return fmt.Errorf("the new key in CAPY_VAULT_KEY is identical to the current passphrase — nothing to rotate (did you forget to update CAPY_VAULT_KEY?)")
			}

			// Confirm: CAPY_VAULT_KEY is the rotation target, so a forgotten env
			// update would otherwise rotate the vault to a key the operator did not
			// intend. With no interactive stdin this returns the safe default (no).
			if !promptYesNo("rotating the vault to the key currently in CAPY_VAULT_KEY — proceed?", false) {
				fmt.Println("aborted")
				return nil
			}

			return runVaultRekey(env.dbPath, oldKey, newKey, removeBackup)
		},
	}
	cmd.Flags().BoolVar(&removeBackup, "remove-backup", false,
		"delete the old-key <vault>.bak after the new vault verifies open (deletion, not guaranteed erasure on SSD/CoW)")
	return cmd
}

// runVaultRekey is the prompt-free core of `vault rekey`, shared with its unit
// test. It rejects a no-op rotation (new == old), rotates via the shared
// backup-API helper, then either removes the old-key .bak or warns that it
// remains decryptable by the old key.
func runVaultRekey(dbPath, oldKey, newKey string, removeBackup bool) error {
	// security: plain == is fine here — this is a local no-op guard comparing the
	// operator's own two passphrases, not a trust-boundary authentication check, so
	// crypto/subtle constant-time comparison buys nothing (no remote attacker can
	// time it; reaching this point already requires local process access).
	if oldKey == newKey {
		return fmt.Errorf("the new key in CAPY_VAULT_KEY is identical to the current passphrase — nothing to rotate (did you forget to update CAPY_VAULT_KEY?)")
	}

	res, err := sqliteutil.Rekey(dbPath, oldKey, newKey)
	if err != nil {
		return err
	}
	fmt.Printf("capy vault rekey: done. Vault re-encrypted with the new CAPY_VAULT_KEY: %s\n", dbPath)

	if removeBackup {
		if err := os.Remove(res.BackupPath); err != nil {
			return fmt.Errorf("vault rekeyed, but removing the old-key backup %s failed: %w", res.BackupPath, err)
		}
		fmt.Printf("capy vault rekey: removed old-key backup %s\n", res.BackupPath)
		fmt.Fprintln(os.Stderr, "capy vault rekey: note: deletion is not guaranteed erasure — on SSD/CoW filesystems recoverable copies may remain")
		return nil
	}

	fmt.Fprintf(os.Stderr, "capy vault rekey: warning: %s is still decryptable by the OLD key.\n", res.BackupPath)
	fmt.Fprintln(os.Stderr, "  When rotating a compromised key, delete it manually (or pass --remove-backup on the next rotation).")
	return nil
}

// ---------------------------------------------------------------------------
// merge
// ---------------------------------------------------------------------------

func newVaultMergeCmd(env *vaultEnv) *cobra.Command {
	var (
		from    string
		keyFlag string
		project string
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "merge --from <vault.db>",
		Short: "Merge sessions from another vault into this one (cross-machine union)",
		Long: `Import the sessions of another machine's vault into this one, non-destructively.

Unlike copying vault.db (which replaces the whole archive), merge unites the two:
distinct sessions are added, and where both vaults hold the same session UUID the
larger-total-content copy wins. Re-running is idempotent.

Provide the source vault's key with --key or CAPY_VAULT_MERGE_KEY; when both
machines share a passphrase it falls back to CAPY_VAULT_KEY. The source vault must
be WRITABLE (merge checkpoints its WAL before reading) — copy a read-only source
(backup media, read-only mount) to a writable location first and point --from at
the copy.

No "stop the server" requirement: merge writes only this (destination) vault and
tolerates a concurrent server sweep via busy-timeout retry, the same as import.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			if from == "" {
				return fmt.Errorf("--from is required (path to the source vault.db)")
			}
			minSessionBytes, err := resolveVaultMinSessionBytes(cmd)
			if err != nil {
				return err
			}
			// A source path that does not exist would otherwise be CREATED as a fresh
			// empty encrypted DB by sql.Open and merge silently as a no-op — fail loud.
			if _, err := os.Stat(from); err != nil {
				return fmt.Errorf("source vault %s: %w", from, err)
			}
			// Merging a vault into itself opens a second handle on the same file and
			// is never intended — reject it.
			if same, err := samePath(from, env.dbPath); err != nil {
				return err
			} else if same {
				return fmt.Errorf("source and destination are the same vault (%s)", env.dbPath)
			}

			srcKey, srcKeyEnv := resolveMergeKey(keyFlag)

			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()
			// Fail fast on a wrong destination key / corrupt DB before reading the source.
			if err := st.Open(cmd.Context()); err != nil {
				return err
			}

			res, err := vault.MergeFrom(cmd.Context(), st, from, srcKey, srcKeyEnv,
				vault.MergeOptions{Project: project, DryRun: dryRun, MinSessionBytes: minSessionBytes})
			if err != nil {
				return err
			}
			printImportResult(res, nil, dryRun)
			if res.Errors > 0 {
				return fmt.Errorf("%d session(s) failed to merge", res.Errors)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "path to the source vault.db to merge from (required)")
	cmd.Flags().StringVar(&keyFlag, "key", "", "source vault passphrase (default: CAPY_VAULT_MERGE_KEY, then CAPY_VAULT_KEY)")
	cmd.Flags().StringVar(&project, "project", "", "only merge sessions whose location hint (mangled Claude project dir, e.g. -home-user-capy, or Codex rollout path) or project path contains this substring")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview what would be merged without writing")
	cmd.Flags().Int64("min-size-bytes", 0, "minimum uncompressed size for new sessions, including sidecars (overrides vault.min_session_bytes; 0 disables)")
	return cmd
}

// resolveMergeKey picks the source-vault passphrase by precedence — the --key
// flag, then CAPY_VAULT_MERGE_KEY, then CAPY_VAULT_KEY (the shared-passphrase
// case; the vault group's PersistentPreRunE already verified it is set). It
// returns the resolved key and a label naming its origin for error messages.
func resolveMergeKey(keyFlag string) (key, keyEnv string) {
	if keyFlag != "" {
		return keyFlag, "--key"
	}
	if k := os.Getenv("CAPY_VAULT_MERGE_KEY"); k != "" {
		return k, "CAPY_VAULT_MERGE_KEY"
	}
	// CAPY_VAULT_KEY is guaranteed set here (PersistentPreRunE). RequireVaultKey's
	// error is therefore unreachable; ignore it rather than complicate the signature.
	k, _ := vault.RequireVaultKey()
	return k, "CAPY_VAULT_KEY"
}

// samePath reports whether two paths refer to the same file. It compares
// cleaned absolute paths; when both exist it also compares os.SameFile so a
// symlinked or differently-spelled source is still caught.
func samePath(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", a, err)
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", b, err)
	}
	if absA == absB {
		return true, nil
	}
	infoA, errA := os.Stat(absA)
	infoB, errB := os.Stat(absB)
	if errA == nil && errB == nil {
		return os.SameFile(infoA, infoB), nil
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

func newVaultRestoreCmd(env *vaultEnv) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "restore <session-id>",
		Short: "Restore an archived session's files to disk (partial UUID, 8+ chars)",
		Long: `Write a session's main JSONL and every preserved sidecar back to disk.

By default a Claude Code session restores into its Claude Code project
directory (honoring CLAUDE_CONFIG_DIR) so Claude Code can find it again, and a
Codex session restores into the Codex home (CODEX_HOME, default ~/.codex) at the
relative rollout path it was discovered under, always as a plain .jsonl. Use
--output to write elsewhere (the same layout under that directory). Existing
files are kept unless you confirm overwriting them; a compressed .jsonl.zst twin
of a Codex rollout is never touched.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()
			return restoreVaultSession(cmd, st, args[0], output)
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "restore under this directory (default: the session's Claude projects dir, or the Codex home)")
	return cmd
}

// restoreVaultSession writes an archived session's files back to disk. Shared by
// the `restore` subcommand and the TUI's `r` action (which both resolve the
// session, pick a root, and report the result identically). An empty output uses
// the platform's default root (defaultRestoreRoot).
func restoreVaultSession(cmd *cobra.Command, st *vault.VaultStore, sessionID, output string) error {
	sess, err := st.GetSession(cmd.Context(), sessionID)
	if err != nil {
		return handleLookupError(sessionID, err)
	}
	files, err := st.GetFiles(cmd.Context(), sess.UUID)
	if err != nil {
		return err
	}

	root, mainRel, err := restoreTarget(sess, output)
	if err != nil {
		return err
	}
	res, err := vault.RestoreSessionAt(sess.UUID, mainRel, sess.RawJSONL, files, root, confirmOverwrite)
	if err != nil {
		return err
	}
	printRestoreResult(res)
	return nil
}

// restoreTarget decides where a session's main file lands: the root (output when
// given, else the platform's default — defaultRestoreRoot) and the root-relative
// path of the main file (restoreMainRel). The platform is resolved ONCE through
// vault.ResolveSessionPlatform, never read off the row: GetSession copies the
// platform column verbatim, and defaulting a corrupted Codex row to Claude would
// write its rollout under the Claude projects tree — precisely the pollution
// ADR-031's reader-version rule exists to prevent. An undetectable value is an
// error here, before anything is written.
func restoreTarget(sess *vault.Session, output string) (root, mainRel string, err error) {
	p, err := vault.ResolveSessionPlatform("vault restore", sess)
	if err != nil {
		return "", "", fmt.Errorf("session %s: %w (fix the stored platform before restoring)", sess.UUID, err)
	}
	root = output
	if root == "" {
		if root, err = defaultRestoreRoot(sess, p); err != nil {
			return "", "", err
		}
	}
	return root, restoreMainRel(sess, p), nil
}

// restoreMainRel is the root-relative path the session's main file restores to:
// the stored location hint (the relative rollout path) for Codex, <uuid>.jsonl
// for Claude — see vault.RestoreSessionAt. p is the resolved platform
// (restoreTarget), not the raw row value.
func restoreMainRel(sess *vault.Session, p vault.Platform) string {
	if p == vault.PlatformCodex {
		return sess.ClaudeProjectDir
	}
	return sess.UUID + ".jsonl"
}

func printRestoreResult(res *vault.RestoreResult) {
	for _, p := range res.Written {
		fmt.Printf("restored %s\n", p)
	}
	for _, p := range res.Skipped {
		fmt.Fprintf(os.Stderr, "kept existing (not overwritten): %s\n", p)
	}
	for _, p := range res.Unsafe {
		fmt.Fprintf(os.Stderr, "skipped unsafe path: %s\n", p)
	}
	fmt.Printf("\nrestored %d file(s) to %s\n", len(res.Written), res.Root)
	for _, n := range res.Notes {
		fmt.Fprintf(os.Stderr, "note: %s\n", n)
	}
}

// ---------------------------------------------------------------------------
// resume
// ---------------------------------------------------------------------------

func newVaultResumeCmd(env *vaultEnv) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Restore a session and launch `claude --resume` (partial UUID, 8+ chars)",
		Long: `Restore a session into its Claude Code project directory, then launch
Claude Code to resume it. The working directory is chosen from --dir, the
session's recorded project path, or the current directory (in that order).

Only Claude Code sessions can be resumed this way. For a Codex session the
command fails and points at 'capy vault restore' followed by 'codex resume'.`,
		Args: cobra.ExactArgs(1),
		// claude prints its own output; a non-zero claude exit must not dump
		// cobra's usage text on top of it.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close() // idempotent; resumeVaultSession Closes explicitly before exec
			return resumeVaultSession(cmd, st, args[0], dir)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "working directory to launch claude in (overrides the session's project path)")
	return cmd
}

// resumeVaultSession restores an archived session and launches `claude --resume`
// on it. Shared by the `resume` subcommand and the TUI's `R` action
// (performTUIAction), so both surfaces share one guard set. It closes st
// (flushing the WAL) before handing the terminal to claude, so callers must not
// use st afterwards (a deferred Close remains safe — Close is idempotent). dir
// overrides the launch directory; "" falls back to the session's project path
// then the cwd (see resolveResumeDir).
//
// The session is loaded BEFORE the `claude` lookup: a Codex session must fail
// with its own actionable error (resume is Claude-only for now — see
// implementation.md § Deferred #1) rather than "claude not found", and nothing
// is restored to disk for it.
func resumeVaultSession(cmd *cobra.Command, st *vault.VaultStore, sessionID, dir string) error {
	sess, err := st.GetSession(cmd.Context(), sessionID)
	if err != nil {
		return handleLookupError(sessionID, err)
	}
	if err := checkResumable(sess); err != nil {
		return err
	}

	// Fail fast before writing anything if Claude Code is not installed.
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("`claude` not found on PATH — install Claude Code to resume sessions")
	}

	files, err := st.GetFiles(cmd.Context(), sess.UUID)
	if err != nil {
		return err
	}

	// Restore to the platform's own location so `claude --resume` finds it.
	// checkResumable has already refused every non-Claude value, so the resolved
	// platform is Claude; going through restoreTarget keeps one dispatch site.
	root, mainRel, err := restoreTarget(sess, "")
	if err != nil {
		return err
	}
	if _, err := vault.RestoreSessionAt(sess.UUID, mainRel, sess.RawJSONL, files, root, confirmOverwrite); err != nil {
		return err
	}

	launchDir, err := resolveResumeDir(dir, sess.ProjectPath)
	if err != nil {
		return err
	}

	// Release the vault (flushes the WAL) before handing the terminal to claude.
	if err := st.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "capy vault resume: warning: closing vault: %v\n", err)
	}
	return runClaudeResume(claudeBin, sess.UUID, launchDir)
}

// checkResumable rejects a session `claude --resume` cannot resume: any
// non-Claude platform. `claude --resume <uuid>` would not find a Codex rollout
// (it is not a Claude session file), so the error names the two commands that
// do the job today. Launching `codex resume` from here is deferred
// (implementation.md § Deferred #1).
func checkResumable(sess *vault.Session) error {
	p := sess.Platform.OrClaude()
	if p == vault.PlatformClaudeCode {
		return nil
	}
	return fmt.Errorf("session %s is a %s session — 'capy vault resume' can only launch Claude Code; "+
		"run 'capy vault restore %s' to put the rollout back, then 'codex resume %s' to continue it",
		shortUUID(sess.UUID, p), p.DisplayName(), shortUUID(sess.UUID, p), sess.UUID)
}

// resolveResumeDir picks the directory to launch claude in, following the
// design's fallback chain: explicit --dir, then the session's project_path (if
// absolute and present), then the current working directory, and finally an
// interactive prompt as a last resort.
func resolveResumeDir(flagDir, projectPath string) (string, error) {
	if flagDir != "" {
		if !isExistingDir(flagDir) {
			return "", fmt.Errorf("--dir %q is not an existing directory", flagDir)
		}
		return flagDir, nil
	}
	if filepath.IsAbs(projectPath) && isExistingDir(projectPath) {
		return projectPath, nil
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd, nil
	} else {
		// Surface why we're falling through to a prompt — a broken cwd otherwise
		// shows up only as a confusing "not an existing directory" later.
		fmt.Fprintf(os.Stderr, "capy vault resume: cannot determine current directory (%v); prompting\n", err)
	}
	answer := strings.TrimSpace(promptLine(fmt.Sprintf("launch directory [%s]: ", displayPath(projectPath))))
	if answer == "" {
		answer = projectPath
	}
	if !isExistingDir(answer) {
		return "", fmt.Errorf("%q is not an existing directory", answer)
	}
	return answer, nil
}

// runClaudeResume launches `claude --resume <uuid>` in dir with inherited stdio
// and propagates claude's own exit code (via exitError) instead of the generic
// non-zero exit.
func runClaudeResume(bin, uuid, dir string) error {
	c := exec.Command(bin, "--resume", uuid) //nolint:gosec // bin is resolved via exec.LookPath; args are not shell-interpreted
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &exitError{code: ee.ExitCode(), err: fmt.Errorf("claude exited with status %d", ee.ExitCode())}
		}
		return fmt.Errorf("launching claude: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// delete
// ---------------------------------------------------------------------------

func newVaultDeleteCmd(env *vaultEnv) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete an archived session from the vault (partial UUID, 8+ chars)",
		Long: `Permanently remove a session (its transcript, sidecars, and search index)
from the vault. This does not touch any copy still on disk under the Claude
projects directory or the Codex home.

Only the addressed session is deleted. Its child sessions (Codex sub-agent
rollouts) are separate archived sessions and are kept — a warning lists them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sess, err := st.GetSession(cmd.Context(), args[0])
			if err != nil {
				return handleLookupError(args[0], err)
			}
			children, err := st.Children(cmd.Context(), sess.UUID)
			if err != nil {
				return err
			}

			short := shortUUID(sess.UUID, sess.Platform)
			printDeletePreview(os.Stderr, sess, children)
			if !yes && !promptYesNo(fmt.Sprintf("delete session %s?", short), false) {
				fmt.Println("aborted")
				return nil
			}

			ok, err := st.DeleteSession(cmd.Context(), sess.UUID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("session %s was not deleted (no longer in vault)", short)
			}
			fmt.Printf("deleted %s\n", short)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// printDeletePreview writes to w — stderr in production, so the "what you're
// about to delete" context stays attached to the confirmation prompt even when
// stdout is redirected. A non-empty children list ends the preview with a
// warning: capy deletes only the addressed row and never cascades (design §
// Sub-agent Model — Codex's own /delete does; the divergence is deliberate), so
// the children stay archived and, having a parent id, hidden from the default
// `list` — reachable via --include-children.
func printDeletePreview(w io.Writer, sess *vault.Session, children []vault.Session) {
	fmt.Fprintf(w, "UUID:     %s\n", sess.UUID)
	fmt.Fprintf(w, "Platform: %s\n", sess.Platform.OrClaude())
	fmt.Fprintf(w, "Title:    %s\n", orDash(sess.EffectiveTitle()))
	fmt.Fprintf(w, "Project:  %s\n", displaySessionProject(*sess))
	if sess.EffectiveProject() != sess.ProjectPath {
		fmt.Fprintf(w, "Original path: %s\n", displayPath(sess.ProjectPath))
	}
	fmt.Fprintf(w, "Messages: %d\n", sess.MessageCount)
	fmt.Fprintf(w, "Dates:    %s – %s\n", fmtDate(sess.StartTime), fmtDate(sess.EndTime))
	if len(children) == 0 {
		return
	}
	ids := make([]string, 0, len(children))
	for _, c := range children {
		ids = append(ids, shortUUID(c.UUID, c.Platform))
	}
	fmt.Fprintf(w, "warning: %d child session(s) name this session as parent and will NOT be deleted (no cascade): %s\n"+
		"         they stay archived; see them with 'capy vault list --include-children'\n",
		len(children), strings.Join(ids, ", "))
}

// ---------------------------------------------------------------------------
// rename
// ---------------------------------------------------------------------------

func newVaultRenameCmd(env *vaultEnv) *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "rename <session-id> [<name>]",
		Short: "Set or clear a custom name for an archived session (partial UUID, 8+ chars)",
		Long: `Assign a capy-owned name to an archived session, replacing its imported title
everywhere it is displayed (list, show, search, JSON, TUI). The archived
transcript is never modified, and the name survives re-import, reindex,
compact, rekey, and cross-machine merges.

Pass --clear instead of a name to remove the custom name and fall back to the
latest imported title. Quote names containing whitespace. Names matching a
recognized credential pattern are stored redacted.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			opts, err := renameOptions(args, clear)
			if err != nil {
				return err
			}

			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sess, err := st.RenameSession(cmd.Context(), args[0], opts)
			if err != nil {
				return handleLookupError(args[0], err)
			}
			if opts.Clear {
				fmt.Printf("cleared custom name for %s — title is now %q\n", shortUUID(sess.UUID, sess.Platform), sess.EffectiveTitle())
			} else {
				fmt.Printf("renamed %s to %q\n", shortUUID(sess.UUID, sess.Platform), sess.EffectiveTitle())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the custom name (restores the imported title)")
	return cmd
}

// renameOptions maps the rename command's argument forms onto the store
// contract: exactly one of a name argument or --clear must be present.
func renameOptions(args []string, clear bool) (vault.RenameOptions, error) {
	if clear {
		if len(args) > 1 {
			return vault.RenameOptions{}, errors.New("a name and --clear are mutually exclusive")
		}
		return vault.RenameOptions{Clear: true}, nil
	}
	if len(args) < 2 {
		return vault.RenameOptions{}, errors.New("provide a name, or pass --clear to remove the custom name")
	}
	return vault.RenameOptions{Name: args[1]}, nil
}

// ---------------------------------------------------------------------------
// JSON output DTOs
// ---------------------------------------------------------------------------

type sessionJSON struct {
	UUID        string `json:"uuid"`
	Title       string `json:"title,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	Project     string `json:"project"`
	GitBranch   string `json:"git_branch,omitempty"`
	StartTime   string `json:"start_time,omitempty"`
	EndTime     string `json:"end_time,omitempty"`
	Messages    int    `json:"message_count"`
	SizeBytes   int64  `json:"size_bytes"`
	// Platform is always present (every row stores one); ParentUUID only for a
	// child session (Codex sub-agent rollouts).
	Platform   string `json:"platform"`
	ParentUUID string `json:"parent_uuid,omitempty"`
}

func sessionsToJSON(sessions []vault.Session) []sessionJSON {
	out := make([]sessionJSON, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionJSON{
			UUID: s.UUID, Title: s.EffectiveTitle(), ProjectPath: s.ProjectPath, Project: s.EffectiveProject(), GitBranch: s.GitBranch,
			StartTime: rfc3339(s.StartTime), EndTime: rfc3339(s.EndTime),
			Messages: s.MessageCount, SizeBytes: s.SizeBytes,
			Platform: s.Platform.String(), ParentUUID: s.ParentUUID,
		})
	}
	return out
}

type searchJSON struct {
	UUID       string `json:"uuid"`
	SubagentID string `json:"subagent_id,omitempty"`
	LineIndex  int    `json:"line_index"`
	Role       string `json:"role"`
	Project    string `json:"project_path,omitempty"`
	EndTime    string `json:"end_time,omitempty"`
	Title      string `json:"title,omitempty"`
	Snippet    string `json:"snippet"`
	// Platform is always present; ParentUUID only for a hit from a child session
	// — the same contract as sessionJSON.
	Platform   string `json:"platform"`
	ParentUUID string `json:"parent_uuid,omitempty"`
}

func resultsToJSON(results []vault.SearchResult) []searchJSON {
	out := make([]searchJSON, 0, len(results))
	for _, r := range results {
		out = append(out, searchJSON{
			UUID: r.SessionUUID, SubagentID: r.SubagentID, LineIndex: r.LineIndex, Role: r.Role,
			Project: r.ProjectPath, EndTime: rfc3339(r.EndTime), Title: r.Title, Snippet: r.Snippet,
			Platform: r.Platform.OrClaude().String(), ParentUUID: r.ParentUUID,
		})
	}
	return out
}

type projectJSON struct {
	ProjectPath string `json:"project_path"`
	Count       int    `json:"count"`
}

type platformJSON struct {
	Platform string `json:"platform"`
	Sessions int    `json:"sessions"`
	Bytes    int64  `json:"bytes"`
}

type statsJSON struct {
	Sessions          int            `json:"sessions"`
	Children          int            `json:"children"`
	TotalContentBytes int64          `json:"total_content_bytes"`
	DBFileBytes       int64          `json:"db_file_bytes"`
	Oldest            string         `json:"oldest,omitempty"`
	Newest            string         `json:"newest,omitempty"`
	IndexVersion      int            `json:"index_version"`
	OutdatedSessions  int            `json:"outdated_sessions"`
	Platforms         []platformJSON `json:"platforms"`
	Projects          []projectJSON  `json:"projects"`
}

func statsToJSON(s *vault.VaultStats, dbBytes int64) statsJSON {
	projects := make([]projectJSON, 0, len(s.ByProject))
	for _, p := range s.ByProject {
		projects = append(projects, projectJSON{ProjectPath: p.ProjectPath, Count: p.Count})
	}
	platforms := make([]platformJSON, 0, len(s.ByPlatform))
	for _, p := range s.ByPlatform {
		platforms = append(platforms, platformJSON{Platform: p.Platform.String(), Sessions: p.Sessions, Bytes: p.Bytes})
	}
	return statsJSON{
		Sessions: s.Sessions, Children: s.Children, TotalContentBytes: s.TotalBytes, DBFileBytes: dbBytes,
		Oldest: rfc3339(s.Oldest), Newest: rfc3339(s.Newest),
		IndexVersion: s.IndexVersion, OutdatedSessions: s.OutdatedSessions,
		Platforms: platforms, Projects: projects,
	}
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// tuiRequested reports whether --tui was passed.
func tuiRequested(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("tui")
	return v
}

// launchTUI opens the vault store and runs the interactive bubbletea UI for the
// read/browse commands (list/search/show). The store is probed via Open() first
// so a wrong key or corrupt DB surfaces as a normal error rather than after the
// alt-screen has taken over the terminal. The TUI never closes the store — the
// CLI owns that lifecycle (deferred Close here).
func launchTUI(cmd *cobra.Command, env *vaultEnv, opts tui.Options) error {
	st := vault.NewVaultStore(env.dbPath)
	defer st.Close()
	if err := st.Open(cmd.Context()); err != nil {
		return err
	}
	action, err := tui.Run(cmd.Context(), st, opts)
	if err != nil {
		return err
	}
	// The TUI defers its destructive/exec keys (r/R) to here, after bubbletea has
	// torn down the alt-screen and restored the raw TTY — restore writes files and
	// resume hands the terminal to `claude --resume`, both of which need a normal
	// terminal. The store is still open (deferred Close above; resume closes it
	// explicitly before exec).
	return performTUIAction(cmd, st, action)
}

// performTUIAction runs the deferred restore/resume the TUI requested. ActionNone
// (the user just quit) is a no-op.
func performTUIAction(cmd *cobra.Command, st *vault.VaultStore, a tui.Action) error {
	switch a.Kind {
	case tui.ActionRestore:
		return restoreVaultSession(cmd, st, a.SessionUUID, "")
	case tui.ActionResume:
		return resumeVaultSession(cmd, st, a.SessionUUID, "")
	default:
		return nil
	}
}

// guardTUI rejects --tui for commands that have no interactive mode (the
// destructive/exec surface: restore, resume, delete). The read/browse commands
// launch the TUI via launchTUI instead.
func guardTUI(cmd *cobra.Command) error {
	if tuiRequested(cmd) {
		return errors.New("--tui is not supported for this command")
	}
	return nil
}

// exitError carries a specific process exit code up to main, so a wrapped child
// process (e.g. `claude` launched by `vault resume`) propagates its own exit
// status instead of the generic 1. main.go honors it via errors.As.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// defaultRestoreRoot is the directory a session restores under when no --output
// is given: for Codex the Codex home (CODEX_HOME-aware — the stored relative
// rollout path is joined beneath it by RestoreSessionAt), for Claude the
// session's project directory under the (CLAUDE_CONFIG_DIR-aware) projects dir —
// where each CLI expects to find the file to resume it. p is the resolved
// platform (restoreTarget), not the raw row value.
func defaultRestoreRoot(sess *vault.Session, p vault.Platform) (string, error) {
	if p == vault.PlatformCodex {
		home, err := config.CodexHome()
		if err != nil {
			return "", fmt.Errorf("resolving codex home: %w", err)
		}
		return home, nil
	}
	projects, err := config.ClaudeProjectsDir()
	if err != nil {
		return "", fmt.Errorf("resolving claude projects dir: %w", err)
	}
	return filepath.Join(projects, sess.ClaudeProjectDir), nil
}

func isExistingDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// confirmOverwrite is the OverwriteFunc the CLI passes to vault.RestoreSession:
// it prompts before clobbering an existing file, defaulting to "no".
func confirmOverwrite(absPath string) bool {
	return promptYesNo(fmt.Sprintf("overwrite existing %s?", absPath), false)
}

// stdinReader is a single shared buffered reader over os.Stdin. A fresh
// bufio.Reader per prompt could over-read and drop input meant for the next
// prompt, so all interactive prompts share this one.
var stdinReader = bufio.NewReader(os.Stdin)

// promptYesNo asks a yes/no question on stderr and reads a line from stdin.
// A blank line or any read error (EOF / non-interactive stdin) yields def, so
// piped and test runs never block and never take a destructive default.
func promptYesNo(question string, def bool) bool {
	suffix := " [y/N] "
	if def {
		suffix = " [Y/n] "
	}
	fmt.Fprint(os.Stderr, question+suffix)
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// promptLine asks a free-text question on stderr and returns the raw line
// (without the trailing newline); a read error yields "".
func promptLine(question string) string {
	fmt.Fprint(os.Stderr, question)
	line, _ := stdinReader.ReadString('\n')
	return strings.TrimRight(line, "\n")
}

// handleLookupError turns a GetSession error into a user-facing message, printing
// disambiguation candidates for an ambiguous partial UUID.
func handleLookupError(id string, err error) error {
	var amb *vault.AmbiguousUUIDError
	if errors.As(err, &amb) {
		writeLookupCandidates(os.Stderr, amb)
		return fmt.Errorf("ambiguous session id %q (%d matches) — use more characters", amb.Prefix, len(amb.Candidates))
	}
	if errors.Is(err, vault.ErrSessionNotFound) {
		return fmt.Errorf("no session matches %q", id)
	}
	return err
}

// writeLookupCandidates lists the sessions an ambiguous prefix matched, each
// with its platform-aware short id — so two Codex UUIDv7 ids sharing 8 leading
// hex digits (the very reason the prefix was ambiguous) are still told apart.
func writeLookupCandidates(w io.Writer, amb *vault.AmbiguousUUIDError) {
	fmt.Fprintf(w, "ambiguous session id %q matches %d sessions:\n", amb.Prefix, len(amb.Candidates))
	for _, c := range amb.Candidates {
		fmt.Fprintf(w, "  %-*s  %s  %-28s  %s\n",
			uuidColumnWidth, shortUUID(c.UUID, c.Platform), fmtDate(c.EndTime),
			truncate(displaySessionProject(c), 28), truncate(c.EffectiveTitle(), 50))
	}
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	fmt.Println(string(b))
	return nil
}

// writeRaw streams a raw blob to stdout, ensuring a trailing newline.
func writeRaw(b []byte) error {
	if _, err := os.Stdout.Write(b); err != nil {
		return err
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		fmt.Println()
	}
	return nil
}

// pageOrPrint pipes content through $PAGER when stdout is a terminal, else prints
// it directly (so redirects and tests get clean output). A missing/failing pager
// falls back to a direct print.
func pageOrPrint(content string) error {
	fi, err := os.Stdout.Stat()
	isTTY := err == nil && (fi.Mode()&os.ModeCharDevice) != 0
	if !isTTY {
		fmt.Print(content)
		return nil
	}
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less"
	}
	// Split on whitespace like git/man; quoted pager paths (spaces in binary name) are not supported.
	parts := strings.Fields(pager)
	c := exec.Command(parts[0], parts[1:]...) //nolint:gosec // PAGER is the user's own config, args split (no shell)
	c.Stdin = strings.NewReader(content)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		// A pager that can't even start (bad/missing $PAGER) is worth a heads-up;
		// a non-zero exit (e.g. quitting less) is not. Either way the user still
		// gets their content.
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			fmt.Fprintf(os.Stderr, "capy vault: pager %q failed to start (%v); printing directly\n", parts[0], err)
		}
		fmt.Print(content)
	}
	return nil
}

func dbFileSize(dbPath string) int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(dbPath + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

func validRole(role string) bool {
	switch role {
	case "user", "assistant", "tool", "system":
		return true
	}
	return false
}

// parseDateFlag accepts a date (YYYY-MM-DD) or full RFC3339 timestamp; "" yields
// the zero time (no filter). When endOfDay is set and a date-only value is given,
// it resolves to 23:59:59 of that day so a `--before <date>` filter is inclusive
// of the whole target day (matching the "on or before this date" help text). An
// explicit RFC3339 timestamp is always used verbatim regardless of endOfDay.
func parseDateFlag(s string, endOfDay bool) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Second)
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q (want YYYY-MM-DD or RFC3339)", s)
}

// shortUUID is the platform-aware display prefix of a session id: 12 characters
// for a Codex row, 8 otherwise. Codex ids are UUIDv7 — time-ordered, so ids
// minted close together share their first 8 hex digits (22 collisions among 164
// local rollouts at research time) — while 12 characters were collision-free.
// minUUIDPrefix for LOOKUP stays 8: an ambiguous prefix is already an error path
// that lists candidates (design § Identity and display). Every CLI surface that
// prints a session id goes through this function with the row's platform.
func shortUUID(u string, p vault.Platform) string {
	// The rule lives on Platform so the MCP hit meta line (internal/server)
	// and every CLI surface render the same prefix for the same row.
	return p.ShortID(u)
}

// displaySessionProject preserves custom labels literally, even when a label
// equals the imported path. Only fallback paths receive home shortening.
func displaySessionProject(s vault.Session) string {
	project := s.EffectiveProject()
	if s.ProjectOverride != nil && s.ProjectOverride.CustomProject != nil {
		return project
	}
	return displayPath(project)
}

// displayPath shortens a home-relative absolute path to ~/… for compact display.
func displayPath(p string) string {
	if p == "" {
		return "-"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fmtDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02")
}

func fmtDateTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04")
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// oneLine collapses internal whitespace (snippets may contain newlines) for
// single-row table display.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncate shortens s to max runes, appending an ellipsis when cut.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
