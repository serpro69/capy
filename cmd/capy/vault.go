package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/vault"
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
		Short: "Archive, search, and restore Claude Code sessions",
		Long: `Vault keeps a durable, cross-project, encrypted archive of every Claude
Code session — searchable and restorable after compaction, auto-cleanup, or
accidental deletion.

Requires CAPY_VAULT_KEY (the vault DB is encrypted at rest).`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if _, err := vault.RequireVaultKey(); err != nil {
				return err
			}
			env.dbPath = vault.VaultDBPath()
			return nil
		},
	}

	cmd.PersistentFlags().Bool("tui", false, "interactive terminal UI (not yet implemented)")

	cmd.AddCommand(
		newVaultImportCmd(env),
		newVaultListCmd(env),
		newVaultSearchCmd(env),
		newVaultShowCmd(env),
		newVaultStatsCmd(env),
		newVaultCheckpointCmd(env),
		newVaultRestoreCmd(env),
		newVaultResumeCmd(env),
		newVaultDeleteCmd(env),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// import
// ---------------------------------------------------------------------------

func newVaultImportCmd(env *vaultEnv) *cobra.Command {
	var (
		path    string
		project string
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Scan and archive Claude Code sessions into the vault",
		Long: `Discover Claude Code sessions and archive them into the vault.

The MCP server's startup sweep only archives the current project. Run
'capy vault import' periodically (e.g. via cron) to capture sessions across
all projects before Claude Code's 30-day cleanup removes them.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := vault.DiscoverSessions(path)
			if err != nil {
				return fmt.Errorf("discovering sessions: %w", err)
			}
			if len(sessions) == 0 {
				fmt.Println("capy vault import: no sessions found")
				return nil
			}

			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()
			// Fail fast on a wrong key / corrupt DB: Import has no error return and
			// would otherwise report the same open failure once per session.
			if err := st.Open(); err != nil {
				return err
			}

			res := vault.Import(st, sessions, vault.ImportOptions{Project: project, DryRun: dryRun})
			printImportResult(res, dryRun)
			if res.Errors > 0 {
				return fmt.Errorf("%d session(s) failed to import", res.Errors)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "source directory (default: Claude projects dir)")
	cmd.Flags().StringVar(&project, "project", "", "only import sessions whose project dir matches this substring")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview what would be imported without writing")
	return cmd
}

func printImportResult(res vault.ImportResult, dryRun bool) {
	if dryRun {
		fmt.Println("DRY RUN — no changes written")
	}
	if len(res.Sessions) == 0 {
		fmt.Println("no sessions matched")
		return
	}
	fmt.Printf("%-8s  %-8s  %-28s  %8s  %s\n", "UUID", "STATUS", "PROJECT", "SIZE", "TITLE")
	for _, s := range res.Sessions {
		if s.Status == vault.StatusError && s.Err != nil {
			fmt.Fprintf(os.Stderr, "  error %s: %v\n", shortUUID(s.UUID), s.Err)
		}
		fmt.Printf("%-8s  %-8s  %-28s  %8s  %s\n",
			shortUUID(s.UUID), s.Status, truncate(displayPath(s.ProjectPath), 28),
			formatSize(s.SizeBytes), truncate(s.Title, 50))
	}
	fmt.Printf("\nimported %d, updated %d, skipped %d, errors %d\n",
		res.Imported, res.Updated, res.Skipped, res.Errors)
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

func newVaultListCmd(env *vaultEnv) *cobra.Command {
	var (
		project string
		limit   int
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List archived sessions, newest first",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sessions, err := st.ListSessions(vault.ListOptions{Project: project, Limit: limit})
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
	cmd.Flags().StringVar(&project, "project", "", "filter by project path substring")
	cmd.Flags().IntVar(&limit, "limit", 50, "max sessions to list (0 = no limit)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON")
	return cmd
}

func printSessionTable(sessions []vault.Session) {
	if len(sessions) == 0 {
		fmt.Println("no sessions archived")
		return
	}
	fmt.Printf("%-8s  %-10s  %5s  %8s  %-28s  %s\n", "UUID", "DATE", "MSGS", "SIZE", "PROJECT", "TITLE")
	for _, s := range sessions {
		fmt.Printf("%-8s  %-10s  %5d  %8s  %-28s  %s\n",
			shortUUID(s.UUID), fmtDate(s.EndTime), s.MessageCount, formatSize(s.SizeBytes),
			truncate(displayPath(s.ProjectPath), 28), truncate(s.Title, 60))
	}
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
			if err := guardTUI(cmd); err != nil {
				return err
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

			results, err := st.Search(vault.SearchOptions{
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
	fmt.Printf("%-8s  %-10s  %-10s  %-24s  %s\n", "UUID", "DATE", "ROLE", "PROJECT", "SNIPPET")
	for _, r := range results {
		role := r.Role
		if r.SubagentID != "" {
			role += "*" // subagent match
		}
		fmt.Printf("%-8s  %-10s  %-10s  %-24s  %s\n",
			shortUUID(r.SessionUUID), fmtDate(r.EndTime), truncate(role, 10),
			truncate(displayPath(r.ProjectPath), 24), oneLine(r.Snippet))
	}
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
			if err := guardTUI(cmd); err != nil {
				return err
			}
			format = strings.ToLower(format)
			if format != "text" && format != "markdown" && format != "json" {
				return fmt.Errorf("invalid --format %q (want text|markdown|json)", format)
			}

			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sess, err := st.GetSession(args[0])
			if err != nil {
				return handleLookupError(args[0], err)
			}

			if format == "json" {
				return writeRaw(sess.RawJSONL)
			}

			files, err := st.GetFiles(sess.UUID)
			if err != nil {
				return err
			}
			markdown := format == "markdown"
			content := renderShow(sess, files, markdown)
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
// subagent rendering as spec-conformant.
func renderShow(sess *vault.Session, files []vault.File, markdown bool) string {
	render := vault.RenderText
	if markdown {
		render = vault.RenderMarkdown
	}

	var sb strings.Builder
	writeShowHeader(&sb, sess, markdown)
	sb.WriteString(render(sess.RawJSONL))

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
		sb.WriteString(render(f.RawContent))
	}
	return sb.String()
}

func writeShowHeader(sb *strings.Builder, sess *vault.Session, markdown bool) {
	title := sess.Title
	if title == "" {
		title = "(untitled)"
	}
	if markdown {
		fmt.Fprintf(sb, "# %s\n\n", title)
		fmt.Fprintf(sb, "- **UUID:** %s\n- **Project:** %s\n- **Branch:** %s\n- **Dates:** %s – %s\n\n",
			sess.UUID, displayPath(sess.ProjectPath), orDash(sess.GitBranch),
			fmtDateTime(sess.StartTime), fmtDateTime(sess.EndTime))
	} else {
		fmt.Fprintf(sb, "%s\n", title)
		fmt.Fprintf(sb, "uuid: %s  project: %s  branch: %s\n", sess.UUID, displayPath(sess.ProjectPath), orDash(sess.GitBranch))
		fmt.Fprintf(sb, "dates: %s – %s\n\n", fmtDateTime(sess.StartTime), fmtDateTime(sess.EndTime))
	}
}

// subagentDisplayID returns the agent id for a "subagents/agent-<id>.jsonl"
// relative path, or "" for any other (non-subagent or non-JSONL) sidecar.
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

			stats, err := st.Stats()
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
	fmt.Printf("Content size:  %s\n", formatSize(s.TotalBytes))
	fmt.Printf("DB file size:  %s\n", formatSize(dbBytes))
	fmt.Printf("Oldest:        %s\n", fmtDate(s.Oldest))
	fmt.Printf("Newest:        %s\n", fmtDate(s.Newest))
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
// restore
// ---------------------------------------------------------------------------

func newVaultRestoreCmd(env *vaultEnv) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "restore <session-id>",
		Short: "Restore an archived session's files to disk (partial UUID, 8+ chars)",
		Long: `Write a session's main JSONL and every preserved sidecar back to disk.

By default it restores into the session's Claude Code project directory
(honoring CLAUDE_CONFIG_DIR) so Claude Code can find it again; use --output to
write elsewhere. Existing files are kept unless you confirm overwriting them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sess, err := st.GetSession(args[0])
			if err != nil {
				return handleLookupError(args[0], err)
			}
			files, err := st.GetFiles(sess.UUID)
			if err != nil {
				return err
			}

			root := output
			if root == "" {
				if root, err = defaultRestoreRoot(sess); err != nil {
					return err
				}
			}
			res, err := vault.RestoreSession(sess.UUID, sess.RawJSONL, files, root, confirmOverwrite)
			if err != nil {
				return err
			}
			printRestoreResult(res)
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "restore into this directory (default: the session's Claude projects dir)")
	return cmd
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
session's recorded project path, or the current directory (in that order).`,
		Args: cobra.ExactArgs(1),
		// claude prints its own output; a non-zero claude exit must not dump
		// cobra's usage text on top of it.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			// Fail fast before touching the vault if Claude Code is not installed.
			claudeBin, err := exec.LookPath("claude")
			if err != nil {
				return fmt.Errorf("`claude` not found on PATH — install Claude Code to resume sessions")
			}

			st := vault.NewVaultStore(env.dbPath)
			defer st.Close() // idempotent; we Close explicitly before exec below

			sess, err := st.GetSession(args[0])
			if err != nil {
				return handleLookupError(args[0], err)
			}
			files, err := st.GetFiles(sess.UUID)
			if err != nil {
				return err
			}

			// Restore to the Claude Code location so `claude --resume` finds it.
			root, err := defaultRestoreRoot(sess)
			if err != nil {
				return err
			}
			if _, err := vault.RestoreSession(sess.UUID, sess.RawJSONL, files, root, confirmOverwrite); err != nil {
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
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "working directory to launch claude in (overrides the session's project path)")
	return cmd
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
projects directory.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sess, err := st.GetSession(args[0])
			if err != nil {
				return handleLookupError(args[0], err)
			}

			printDeletePreview(sess)
			if !yes && !promptYesNo(fmt.Sprintf("delete session %s?", shortUUID(sess.UUID)), false) {
				fmt.Println("aborted")
				return nil
			}

			ok, err := st.DeleteSession(sess.UUID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("session %s was not deleted (no longer in vault)", shortUUID(sess.UUID))
			}
			fmt.Printf("deleted %s\n", shortUUID(sess.UUID))
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// printDeletePreview writes to stderr (not stdout) so the "what you're about to
// delete" context stays attached to the confirmation prompt even when stdout is
// redirected.
func printDeletePreview(sess *vault.Session) {
	fmt.Fprintf(os.Stderr, "UUID:     %s\n", sess.UUID)
	fmt.Fprintf(os.Stderr, "Title:    %s\n", orDash(sess.Title))
	fmt.Fprintf(os.Stderr, "Project:  %s\n", displayPath(sess.ProjectPath))
	fmt.Fprintf(os.Stderr, "Messages: %d\n", sess.MessageCount)
	fmt.Fprintf(os.Stderr, "Dates:    %s – %s\n", fmtDate(sess.StartTime), fmtDate(sess.EndTime))
}

// ---------------------------------------------------------------------------
// JSON output DTOs
// ---------------------------------------------------------------------------

type sessionJSON struct {
	UUID      string `json:"uuid"`
	Title     string `json:"title,omitempty"`
	Project   string `json:"project_path,omitempty"`
	GitBranch string `json:"git_branch,omitempty"`
	StartTime string `json:"start_time,omitempty"`
	EndTime   string `json:"end_time,omitempty"`
	Messages  int    `json:"message_count"`
	SizeBytes int64  `json:"size_bytes"`
}

func sessionsToJSON(sessions []vault.Session) []sessionJSON {
	out := make([]sessionJSON, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionJSON{
			UUID: s.UUID, Title: s.Title, Project: s.ProjectPath, GitBranch: s.GitBranch,
			StartTime: rfc3339(s.StartTime), EndTime: rfc3339(s.EndTime),
			Messages: s.MessageCount, SizeBytes: s.SizeBytes,
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
}

func resultsToJSON(results []vault.SearchResult) []searchJSON {
	out := make([]searchJSON, 0, len(results))
	for _, r := range results {
		out = append(out, searchJSON{
			UUID: r.SessionUUID, SubagentID: r.SubagentID, LineIndex: r.LineIndex, Role: r.Role,
			Project: r.ProjectPath, EndTime: rfc3339(r.EndTime), Title: r.Title, Snippet: r.Snippet,
		})
	}
	return out
}

type projectJSON struct {
	ProjectPath string `json:"project_path"`
	Count       int    `json:"count"`
}

type statsJSON struct {
	Sessions          int           `json:"sessions"`
	TotalContentBytes int64         `json:"total_content_bytes"`
	DBFileBytes       int64         `json:"db_file_bytes"`
	Oldest            string        `json:"oldest,omitempty"`
	Newest            string        `json:"newest,omitempty"`
	Projects          []projectJSON `json:"projects"`
}

func statsToJSON(s *vault.VaultStats, dbBytes int64) statsJSON {
	projects := make([]projectJSON, 0, len(s.ByProject))
	for _, p := range s.ByProject {
		projects = append(projects, projectJSON{ProjectPath: p.ProjectPath, Count: p.Count})
	}
	return statsJSON{
		Sessions: s.Sessions, TotalContentBytes: s.TotalBytes, DBFileBytes: dbBytes,
		Oldest: rfc3339(s.Oldest), Newest: rfc3339(s.Newest), Projects: projects,
	}
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// guardTUI returns an error when --tui is requested. The interactive UI is wired
// in a later task; until then the read/browse commands fail loud rather than
// silently ignoring the flag.
func guardTUI(cmd *cobra.Command) error {
	if tui, _ := cmd.Flags().GetBool("tui"); tui {
		return errors.New("--tui mode is not yet implemented")
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

// defaultRestoreRoot is the session's Claude Code project directory under the
// (CLAUDE_CONFIG_DIR-aware) projects dir — where Claude Code expects to find the
// JSONL for `claude --resume`.
func defaultRestoreRoot(sess *vault.Session) (string, error) {
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
		fmt.Fprintf(os.Stderr, "ambiguous session id %q matches %d sessions:\n", amb.Prefix, len(amb.Candidates))
		for _, c := range amb.Candidates {
			fmt.Fprintf(os.Stderr, "  %s  %s  %-28s  %s\n",
				shortUUID(c.UUID), fmtDate(c.EndTime), truncate(displayPath(c.ProjectPath), 28), truncate(c.Title, 50))
		}
		return fmt.Errorf("ambiguous session id %q (%d matches) — use more characters", amb.Prefix, len(amb.Candidates))
	}
	if errors.Is(err, vault.ErrSessionNotFound) {
		return fmt.Errorf("no session matches %q", id)
	}
	return err
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

func shortUUID(u string) string {
	if len(u) >= 8 {
		return u[:8]
	}
	return u
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
