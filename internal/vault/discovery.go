package vault

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/serpro69/capy/internal/config"
)

// maxSidecarBytes caps a single non-subagent sidecar file (tool-results, build
// logs, screenshots — all reproducible). Larger ones are skipped during
// discovery with a warning. The cap NEVER applies to the main session JSONL
// (always stored uncapped) nor to subagents/*.jsonl (irreproducible
// conversation content). See design.md DB Size Projection.
const maxSidecarBytes = 5 * 1024 * 1024

// SessionFile is one discovered session: its main JSONL plus, for Claude Code,
// every sidecar in the matching <uuid>/ directory. Platform says which
// discoverer produced it and therefore which decoder import must run; the
// per-platform location fields are documented on each field — a field the
// platform does not use is left at its zero value.
type SessionFile struct {
	Platform Platform // which platform's discoverer found this file (never empty)
	Path     string   // full path to the main session file on disk
	UUID     string   // the vault row key: <uuid>.jsonl for Claude, the thread uuid in the rollout filename for Codex

	// ProjectDir is Claude's mangled project-dir name (or, for loose imports,
	// the containing dir basename) — its location hint. Unused for Codex.
	ProjectDir string
	// RelativePath is Codex's location hint: the rollout's slash-separated path
	// relative to the Codex home (sessions/YYYY/MM/DD/rollout-…-<uuid>.jsonl)
	// with any .zst suffix stripped — the only way to restore a local-time
	// filename faithfully, since the UTC metadata cannot rebuild it. Unused for
	// Claude (whose main file is always <ProjectDir>/<uuid>.jsonl).
	RelativePath string
	// ProjectPath is Codex's project hint: session_meta.cwd from a bounded
	// first-line read, so project filtering needs no full scan. Empty when the
	// first line is not a session_meta record. Unused for Claude (import
	// unmangles ProjectDir instead).
	ProjectPath string
	// Compressed is true for a Codex .jsonl.zst rollout: import must
	// decompress before hashing (raw_jsonl, content_hash and size_bytes are
	// always the plain JSONL bytes). Always false for Claude.
	Compressed bool
	// OnDiskSize is the size of Path on disk (the compressed size for a .zst
	// rollout) — the sweep's skip-predicate input, from directory metadata.
	OnDiskSize int64

	AssociatedFiles []AssociatedFile // Claude: every file under <uuid>/ (subagents, tool-results, sidecars). Codex has none.
}

// Discoverer finds one platform's session files under a root directory. An
// implementation is a pure directory walker: it never reads a session beyond a
// bounded first line and never touches the vault. Two exist — claudeDiscoverer
// and codexDiscoverer — and a third agent CLI adds a third (design § Discoverer).
type Discoverer interface {
	Discover(rootDir string) ([]SessionFile, DiscoveryReport, error)
}

// DiscoveryReport carries what a discovery run skipped, so `capy vault import`
// and the server sweep can print or log the counts without re-walking the tree.
// Every skip it counts was also logged at the moment it happened.
type DiscoveryReport struct {
	// SkippedRevertVariants lists, root-relative and slash-separated, the Codex
	// `_<rollout_id>` rollouts (thread/revert variants) v1 does not archive
	// (design § Import — revert variants; implementation.md § Deferred #3).
	SkippedRevertVariants []string
	// SkippedByPredicate counts Codex rollouts CodexDiscoverOptions.Skip
	// dropped from directory metadata alone — they were never opened.
	SkippedByPredicate int
	// FirstLineReads counts Codex rollouts whose first line was read — the only
	// per-file cost discovery incurs (design § Server startup sweep).
	FirstLineReads int
}

// merge folds o into r (used by DiscoverAll to combine per-platform reports).
func (r *DiscoveryReport) merge(o DiscoveryReport) {
	r.SkippedRevertVariants = append(r.SkippedRevertVariants, o.SkippedRevertVariants...)
	r.SkippedByPredicate += o.SkippedByPredicate
	r.FirstLineReads += o.FirstLineReads
}

// AssociatedFile is one sidecar from a session directory.
type AssociatedFile struct {
	AbsPath      string // full path on disk
	RelativePath string // path relative to <uuid>/ (e.g. "subagents/agent-abc.jsonl")
}

// DiscoverSessions finds all session files reachable from rootDir. It is
// DiscoverSessionsReport without the report — kept for callers that only need
// the session list (the server sweep and its tests).
func DiscoverSessions(rootDir string) ([]SessionFile, error) {
	sessions, _, err := DiscoverSessionsReport(rootDir)
	return sessions, err
}

// DiscoverSessionsReport finds all session files reachable from rootDir and
// reports what it skipped. An empty rootDir resolves to Claude Code's projects
// directory (config.ClaudeProjectsDir, honoring CLAUDE_CONFIG_DIR). The layout
// is auto-detected:
//
//   - a Codex home (a sessions/ or archived_sessions/ child holding rollout-*
//     files)                                      → codexDiscoverer over both roots
//   - a Claude config dir (contains projects/)   → walk every project under projects/
//   - a projects/ root (subdirs hold *.jsonl)    → walk every project subdir
//   - a single project dir (holds *.jsonl)       → walk just that dir
//
// (Claude sub-cases: see detectProjectDirs.) Claude sidecars larger than
// maxSidecarBytes are skipped (subagent JSONLs and the main JSONL never are).
// Results are sorted by path for determinism — within each Codex root, see
// codexDiscoverer.
func DiscoverSessionsReport(rootDir string) ([]SessionFile, DiscoveryReport, error) {
	if rootDir == "" {
		resolved, err := config.ClaudeProjectsDir()
		if err != nil {
			return nil, DiscoveryReport{}, fmt.Errorf("resolving claude projects dir: %w", err)
		}
		rootDir = resolved
	}
	if isCodexHome(rootDir) {
		return codexDiscoverer{}.Discover(rootDir)
	}
	return claudeDiscoverer{}.Discover(rootDir)
}

// DiscoverAll discovers sessions from every platform root that exists on disk:
// Claude Code's projects dir first, then the Codex home (each honoring its env
// override — CLAUDE_CONFIG_DIR, CODEX_HOME). A root that is absent, or present
// but holding no sessions yet, is skipped at debug level; a root that fails to
// walk is logged at warn and skipped, so one platform never hides the other.
// codexOpts (nil ⇒ defaults) is handed to the Codex walker — the server sweep
// uses it for its skip predicate; `capy vault import` passes nil and reads every
// first line. only, when given, restricts discovery to those platforms (`capy
// vault import --platform`), so the other platform's root is not walked at all
// — not merely filtered afterwards. The returned error covers only root
// resolution (an unresolvable home directory), never a per-platform walk.
func DiscoverAll(codexOpts *CodexDiscoverOptions, only ...Platform) ([]SessionFile, DiscoveryReport, error) {
	claudeRoot, err := config.ClaudeProjectsDir()
	if err != nil {
		return nil, DiscoveryReport{}, fmt.Errorf("resolving claude projects dir: %w", err)
	}
	codexRoot, err := config.CodexHome()
	if err != nil {
		return nil, DiscoveryReport{}, fmt.Errorf("resolving codex home: %w", err)
	}

	var opts CodexDiscoverOptions
	if codexOpts != nil {
		opts = *codexOpts
	}
	roots := []struct {
		platform Platform
		root     string
		exists   bool
		d        Discoverer
	}{
		{PlatformClaudeCode, claudeRoot, isDir(claudeRoot), claudeDiscoverer{}},
		{PlatformCodex, codexRoot, HasCodexRolloutRoot(codexRoot), codexDiscoverer{opts: opts}},
	}

	var (
		all    []SessionFile
		report DiscoveryReport
	)
	for _, r := range roots {
		if len(only) > 0 && !slices.Contains(only, r.platform) {
			continue
		}
		if !r.exists {
			slog.Debug("vault discovery: platform root absent, skipping", "platform", r.platform, "root", r.root)
			continue
		}
		sessions, rep, err := r.d.Discover(r.root)
		switch {
		case errors.Is(err, errNoSessions):
			// A root that exists but holds no sessions yet is the common state
			// for a platform the user has installed but not used from here.
			slog.Debug("vault discovery: platform root holds no sessions", "platform", r.platform, "root", r.root)
			continue
		case err != nil:
			// The partial report of a failed walk is dropped with its sessions.
			slog.Warn("vault discovery: skipping platform root", "platform", r.platform, "root", r.root, "error", err)
			continue
		}
		report.merge(rep)
		if len(sessions) == 0 {
			// The Codex walker reports an existing-but-empty root as an empty
			// result (its report must still reach us); log it like the Claude
			// sentinel so both platforms are equally visible at debug.
			slog.Debug("vault discovery: platform root holds no sessions", "platform", r.platform, "root", r.root)
			continue
		}
		all = append(all, sessions...)
	}
	return all, report, nil
}

// claudeDiscoverer walks a Claude Code layout (see DiscoverSessionsReport's
// Claude sub-cases and detectProjectDirs). It is today's discovery logic
// unchanged behind the Discoverer seam; it never skips anything the report
// would count, so its report is always empty.
type claudeDiscoverer struct{}

var _ Discoverer = claudeDiscoverer{}

func (claudeDiscoverer) Discover(rootDir string) ([]SessionFile, DiscoveryReport, error) {
	projectDirs, err := detectProjectDirs(rootDir)
	if err != nil {
		return nil, DiscoveryReport{}, err
	}

	var sessions []SessionFile
	for _, projDir := range projectDirs {
		found, err := discoverProject(projDir)
		if err != nil {
			// Log and continue: one unreadable project dir must not abort discovery.
			slog.Warn("vault discovery: skipping project directory", "dir", projDir, "error", err)
			continue
		}
		sessions = append(sessions, found...)
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Path < sessions[j].Path })
	return sessions, DiscoveryReport{}, nil
}

// ProjectSessionDir resolves the Claude Code session directory for a real
// project path, scoping discovery to a single project (the server-startup
// sweep's "current project only" path). It honors CLAUDE_CONFIG_DIR via
// config.ClaudeProjectsDir(), as does session.SessionDir. When
// projectDir is already inside the projects root (i.e. it is itself a mangled
// session dir) it is returned unchanged; otherwise the absolute path is mangled
// (/ and . → -) to Claude Code's directory convention. The returned path is not
// stat-checked — DiscoverSessions reports a missing directory, and callers (the
// sweep) skip it gracefully.
func ProjectSessionDir(projectDir string) (string, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path: %w", err)
	}
	projects, err := config.ClaudeProjectsDir()
	if err != nil {
		return "", fmt.Errorf("resolving claude projects dir: %w", err)
	}
	if strings.HasPrefix(abs, projects+string(filepath.Separator)) {
		return abs, nil
	}
	mangled := strings.NewReplacer("/", "-", ".", "-").Replace(abs)
	return filepath.Join(projects, mangled), nil
}

// detectProjectDirs classifies rootDir and returns the list of project
// directories to walk. The checks are mutually exclusive in practice: a single
// project dir holds *.jsonl files directly, while a projects root holds only
// project subdirs (whose own children include <uuid>/ session dirs, not
// top-level *.jsonl).
func detectProjectDirs(rootDir string) ([]string, error) {
	info, err := os.Stat(rootDir)
	if err != nil {
		return nil, fmt.Errorf("reading input path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("input path is not a directory: %q", rootDir)
	}

	// Claude config dir: contains a projects/ subdirectory.
	if sub := filepath.Join(rootDir, "projects"); isDir(sub) {
		return childDirs(sub)
	}

	// Single project dir: holds *.jsonl files directly.
	if has, err := hasJSONLFiles(rootDir); err != nil {
		return nil, err
	} else if has {
		return []string{rootDir}, nil
	}

	// projects/ root: child dirs that directly hold *.jsonl files.
	dirs, err := childDirs(rootDir)
	if err != nil {
		return nil, err
	}
	var projectDirs []string
	for _, d := range dirs {
		has, err := hasJSONLFiles(d)
		if err != nil {
			// Log and continue: one unreadable child must not silently drop the
			// rest, nor surface only as a misleading "no session files" error.
			slog.Warn("vault discovery: skipping unreadable project directory", "dir", d, "error", err)
			continue
		}
		if has {
			projectDirs = append(projectDirs, d)
		}
	}
	if len(projectDirs) == 0 {
		return nil, fmt.Errorf("%w under %q", errNoSessions, rootDir)
	}
	return projectDirs, nil
}

// errNoSessions is the sentinel behind "a root that exists but holds no
// sessions" — DiscoverAll demotes it to debug (a platform installed but never
// used from this machine), while any other discovery error stays a warning.
var errNoSessions = errors.New("no session files found")

// discoverProject lists the main <uuid>.jsonl files in a single project dir and
// collects each one's sidecar files from its <uuid>/ directory.
func discoverProject(projectDir string) ([]SessionFile, error) {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil, fmt.Errorf("listing project dir: %w", err)
	}
	projectName := filepath.Base(projectDir)

	var sessions []SessionFile
	for _, e := range entries {
		// Only regular files are real session JSONLs. e.Type().IsRegular() is
		// false for directories AND symlinks — a symlink named "<uuid>.jsonl"
		// (whose target os.ReadFile would blindly follow) must not be treated as
		// a session file.
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		uuid := strings.TrimSuffix(e.Name(), ".jsonl")
		sf := SessionFile{
			Platform:   PlatformClaudeCode,
			Path:       filepath.Join(projectDir, e.Name()),
			UUID:       uuid,
			ProjectDir: projectName,
		}
		if info, err := e.Info(); err == nil {
			sf.OnDiskSize = info.Size()
		}
		sf.AssociatedFiles = collectAssociatedFiles(filepath.Join(projectDir, uuid))
		sessions = append(sessions, sf)
	}
	return sessions, nil
}

// collectAssociatedFiles walks the <uuid>/ session directory recursively and
// returns every file, relative to that directory. A non-subagent file larger
// than maxSidecarBytes is skipped with a warning; subagent JSONLs are always
// kept. A missing or unreadable directory yields no files (not an error) — many
// sessions have no sidecars.
func collectAssociatedFiles(sessionDir string) []AssociatedFile {
	var files []AssociatedFile
	err := filepath.WalkDir(sessionDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip non-regular entries (symlinks, devices, sockets, FIFOs). For a
		// symlink WalkDir reports d.Info().Size() as the link-target *path*
		// length, not the target's size — so the maxSidecarBytes cap below would
		// be silently bypassed, and os.ReadFile (readSidecars) would then follow
		// the link, reading an arbitrary or oversized target into the vault.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(sessionDir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		if !isSubagentJSONL(rel) {
			if info, statErr := d.Info(); statErr == nil && info.Size() > maxSidecarBytes {
				slog.Warn("vault discovery: skipping oversize sidecar file",
					"file", rel, "size", info.Size(), "max_bytes", maxSidecarBytes)
				return nil
			}
		}
		files = append(files, AssociatedFile{AbsPath: path, RelativePath: rel})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		slog.Warn("vault discovery: partial sidecar walk", "dir", sessionDir, "error", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return files
}

// isSubagentJSONL reports whether a session-relative path is a subagent
// transcript (subagents/*.jsonl) — irreproducible conversation content that is
// never size-capped.
func isSubagentJSONL(rel string) bool {
	return strings.HasPrefix(rel, "subagents/") && strings.HasSuffix(rel, ".jsonl")
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// childDirs returns the immediate subdirectories of dir, as full paths.
func childDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listing directory: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(dir, e.Name()))
		}
	}
	return dirs, nil
}

// hasJSONLFiles reports whether dir directly contains at least one *.jsonl file.
func hasJSONLFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("listing directory: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			return true, nil
		}
	}
	return false, nil
}

// ---- Codex ------------------------------------------------------------------

// codexRolloutRoots are the subdirectories of the Codex home that hold rollouts,
// in the FIXED order the walker visits them: sessions/ (live threads, nested
// YYYY/MM/DD/) strictly before archived_sessions/ (threads the user archived —
// Codex archive/unarchive is a move, so the same bytes can sit under either).
// Results are sorted within each root and never across roots, so an active copy
// of a thread always precedes an archived copy in the returned slice; import's
// same-run reconciliation relies on that to keep the sessions/ path as the
// location hint (design § Import).
var codexRolloutRoots = [...]string{"sessions", "archived_sessions"}

// CodexDiscoverOptions tunes the Codex walker.
type CodexDiscoverOptions struct {
	// Skip, when non-nil, is evaluated from directory metadata BEFORE a rollout
	// is opened; returning true drops the file from the result entirely (it is
	// neither read nor returned, only counted in DiscoveryReport.SkippedByPredicate).
	// relPath is the rollout's slash-separated path relative to the Codex home
	// with any .zst suffix stripped (SessionFile.RelativePath), onDiskSize its
	// size on disk (SessionFile.OnDiskSize), compressed whether it is a .zst
	// rollout. The server sweep passes its archived-rows predicate here so an
	// unchanged rollout costs nothing per start; nil skips nothing.
	Skip func(relPath string, onDiskSize int64, compressed bool) bool
}

// codexDiscoverer walks a Codex home's rollout roots (codexRolloutRoots). For
// every regular file whose name parses as a rollout it reads ONE bounded first
// line (session_meta) for the project cwd — the only per-file cost — unless
// opts.Skip drops the file first. Symlinks, unparseable names and `_<rollout_id>`
// revert variants are skipped with a warning (the variants are also reported).
type codexDiscoverer struct{ opts CodexDiscoverOptions }

var _ Discoverer = codexDiscoverer{}

// DiscoverCodexSessions is the exported entry to the Codex walker for callers
// that know they hold a Codex home and need the skip predicate (the server
// sweep). home is the Codex home directory (config.CodexHome for the default).
func DiscoverCodexSessions(home string, opts CodexDiscoverOptions) ([]SessionFile, DiscoveryReport, error) {
	return codexDiscoverer{opts: opts}.Discover(home)
}

func (d codexDiscoverer) Discover(home string) ([]SessionFile, DiscoveryReport, error) {
	var (
		all    []SessionFile
		report DiscoveryReport
		found  bool
	)
	for _, sub := range codexRolloutRoots {
		root := filepath.Join(home, sub)
		if !isDir(root) {
			continue
		}
		found = true
		sessions, err := d.walkRoot(home, root, &report)
		if err != nil {
			return nil, report, err
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].Path < sessions[j].Path })
		all = append(all, sessions...)
	}
	if !found {
		return nil, report, fmt.Errorf("no codex rollout roots (%s) under %q", strings.Join(codexRolloutRoots[:], ", "), home)
	}
	// An existing root that holds nothing (or only skipped files) is an empty
	// result, not an error: unlike a Claude dir the layout is unambiguous, and
	// the caller still needs the report (skipped variants, predicate counts).
	return all, report, nil
}

// walkRoot walks one rollout root. A rollout that survives the skip predicate
// gets its first line read; a first line that cannot be read or parsed skips the
// file with a warning (design § Observability). The walk itself fails only on a
// path-relativisation error (a programming error); unreadable subtrees are
// logged and skipped.
func (d codexDiscoverer) walkRoot(home, root string, report *DiscoveryReport) ([]SessionFile, error) {
	var sessions []SessionFile
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable subtree must not abort the root. WalkDir has already
			// skipped the entry's children when it reports a directory error.
			slog.Warn("vault discovery: skipping unreadable codex path", "path", path, "error", err)
			return nil
		}
		if e.IsDir() {
			return nil
		}
		// Only regular files are rollouts — a symlink is never followed, exactly
		// as for Claude session files (see discoverProject).
		if !e.Type().IsRegular() {
			return nil
		}
		// OS metadata files (.DS_Store, .directory, …) are never rollouts and
		// would otherwise warn on every server-startup sweep.
		if strings.HasPrefix(e.Name(), ".") {
			slog.Debug("vault discovery: skipping hidden file under a codex rollout root", "path", path)
			return nil
		}
		name, ok := parseRolloutFilename(e.Name())
		if !ok {
			slog.Warn("vault discovery: skipping file with an unrecognised codex rollout name", "path", path)
			return nil
		}
		rel, rerr := filepath.Rel(home, path)
		if rerr != nil {
			return fmt.Errorf("relativising %q to %q: %w", path, home, rerr)
		}
		rel = filepath.ToSlash(rel)
		if name.RolloutID != "" {
			// thread/revert writes a new immutable file for the same thread and
			// Codex appends to the new one; neither size nor hash establishes
			// containment against the base file, so v1 archives only the base
			// (design § Import — revert variants).
			slog.Warn("vault discovery: skipping codex revert rollout variant (not archived in v1)",
				"thread", name.UUID, "rollout_id", name.RolloutID, "path", path)
			report.SkippedRevertVariants = append(report.SkippedRevertVariants, rel)
			return nil
		}
		rel = strings.TrimSuffix(rel, ".zst")
		info, ierr := e.Info()
		if ierr != nil {
			slog.Warn("vault discovery: skipping unreadable codex rollout", "path", path, "error", ierr)
			return nil
		}
		if d.opts.Skip != nil && d.opts.Skip(rel, info.Size(), name.Compressed) {
			report.SkippedByPredicate++
			return nil
		}

		report.FirstLineReads++
		first, ferr := readRolloutFirstLine(path, name.Compressed)
		if ferr != nil {
			slog.Warn("vault discovery: skipping codex rollout with an unreadable first line", "path", path, "error", ferr)
			return nil
		}
		cwd, cerr := codexFirstLineCWD(first)
		if cerr != nil {
			slog.Warn("vault discovery: skipping codex rollout with an unparseable first line", "path", path, "error", cerr)
			return nil
		}
		if cwd == "" {
			slog.Debug("vault discovery: codex rollout does not open with session_meta; no project hint", "path", path)
		}
		sessions = append(sessions, SessionFile{
			Platform:     PlatformCodex,
			Path:         path,
			UUID:         name.UUID,
			RelativePath: rel,
			ProjectPath:  cwd,
			Compressed:   name.Compressed,
			OnDiskSize:   info.Size(),
		})
		return nil
	})
	return sessions, err
}

// rolloutFilenameRe matches Codex's rollout file name
// (codex-rs/rollout/src/rollout_file_name.rs):
//
//	rollout-<YYYY-MM-DDThh-mm-ss>-<thread uuid>[_<rollout_id>].jsonl[.zst]
//
// The timestamp is the thread's LOCAL creation time; the uuid is lowercase
// (UUIDv7); the optional _<rollout_id> marks a thread/revert variant.
var rolloutFilenameRe = regexp.MustCompile(
	`^rollout-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2})-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:_([^.]+))?\.jsonl(\.zst)?$`)

// rolloutName is a parsed Codex rollout file name.
type rolloutName struct {
	LocalTS    string // "2026-08-04T12-03-00": Codex's local-time creation stamp, kept only through RelativePath
	UUID       string // the thread uuid — the vault row key (design § Import: row identity is the filename uuid)
	RolloutID  string // non-empty for a thread/revert variant, which v1 skips
	Compressed bool   // .jsonl.zst
}

// parseRolloutFilename parses a Codex rollout file name (see rolloutFilenameRe).
// ok is false for any name that is not a rollout.
func parseRolloutFilename(name string) (rn rolloutName, ok bool) {
	m := rolloutFilenameRe.FindStringSubmatch(name)
	if m == nil {
		return rolloutName{}, false
	}
	return rolloutName{LocalTS: m[1], UUID: m[2], RolloutID: m[3], Compressed: m[4] != ""}, true
}

// isCodexHome reports whether rootDir has the Codex home layout: a sessions/ or
// archived_sessions/ child that holds at least one rollout-* file. Used by
// DiscoverSessionsReport to autodetect a `--source <codex home>` argument; a
// Claude layout never has these children.
func isCodexHome(rootDir string) bool {
	for _, sub := range codexRolloutRoots {
		if dirHoldsRollout(filepath.Join(rootDir, sub)) {
			return true
		}
	}
	return false
}

// HasCodexRolloutRoot reports whether home has at least one rollout root
// directory (codexRolloutRoots), regardless of contents — the "does the
// platform exist here" probe DiscoverAll and the server sweep run before
// touching anything else. It is cheaper and looser than isCodexHome: an
// installed-but-unused Codex is then reported as "holds no sessions" rather
// than "absent". The sweep needs it exported because it must decide whether
// to open the vault (for its skip predicate) BEFORE running Codex discovery,
// and must not create vault.db on a machine that has no Codex at all.
func HasCodexRolloutRoot(home string) bool {
	for _, sub := range codexRolloutRoots {
		if isDir(filepath.Join(home, sub)) {
			return true
		}
	}
	return false
}

// codexRolloutMaxDepth bounds the layout probe: Codex nests live rollouts as
// sessions/YYYY/MM/DD/<file> (three directory levels) and archived ones flat, so
// a rollout can never sit deeper than this below a root. The probe stops
// descending past it — without the bound a `--source` pointing at a large
// unrelated tree that happens to hold a sessions/ child would be walked whole.
const codexRolloutMaxDepth = 3

// dirHoldsRollout walks dir (no deeper than codexRolloutMaxDepth) until the
// first regular file whose name parses as a rollout (revert variants included —
// they still mark a Codex layout). A missing or unreadable dir holds none.
func dirHoldsRollout(dir string) bool {
	if !isDir(dir) {
		return false
	}
	found := false
	_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees; the probe is best-effort
		}
		if e.IsDir() {
			if path == dir {
				return nil
			}
			// Directory depth below dir: "2026" is 1, "2026/05/01" is 3 (the
			// deepest level Codex writes into); anything deeper is pruned.
			if rel, rerr := filepath.Rel(dir, path); rerr != nil ||
				strings.Count(rel, string(filepath.Separator))+1 > codexRolloutMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		if _, ok := parseRolloutFilename(e.Name()); ok {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// openRollout opens a rollout for readRolloutFirstLine. It is a variable purely
// so tests can count opens and bytes read: the sweep's skip predicate must leave
// a skipped rollout UNOPENED, and a .zst first-line read must stay far below the
// file size. Production code never reassigns it.
var openRollout = func(path string) (io.ReadCloser, error) { return os.Open(path) }

// readRolloutFirstLine returns the first line of the rollout at path (EOL
// stripped), bounded to maxScanLineBytes. For a .zst rollout it decompresses
// through a STREAMING, synchronous zstd decoder over the open file — never
// codec.go's whole-frame DecodeAll — so a compressed rollout costs its first
// block, not a full decompression (design § Discoverer).
func readRolloutFirstLine(path string, compressed bool) ([]byte, error) {
	f, err := openRollout(path)
	if err != nil {
		return nil, fmt.Errorf("opening rollout: %w", err)
	}
	defer f.Close()

	var r io.Reader = f
	if compressed {
		// Concurrency 1 ⇒ no async block decoding and no goroutines; lowmem
		// keeps the per-call allocation small for a reader that lives for one
		// line. This is a fresh decoder per file: the shared blobDecoder serves
		// stateless DecodeAll only (codec.go) and a stream decoder is
		// single-stream by contract.
		dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			return nil, fmt.Errorf("opening zstd stream: %w", err)
		}
		defer dec.Close()
		r = dec
	}
	return readFirstLine(r, maxScanLineBytes)
}

// readFirstLine returns r's first newline-delimited line with its EOL stripped,
// reading no further than that newline plus the reader's buffer. A first line
// longer than maxLineBytes, or an empty one, is an error — a rollout's line 0
// is session_meta and never legitimately either.
func readFirstLine(r io.Reader, maxLineBytes int) ([]byte, error) {
	// The limit is one byte past the cap so an over-cap line is detected as such
	// instead of being silently cut to exactly maxLineBytes.
	br := bufio.NewReaderSize(io.LimitReader(r, int64(maxLineBytes)+1), 64*1024)
	line, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading first line: %w", err)
	}
	line = trimEOL(line)
	if len(line) > maxLineBytes {
		return nil, fmt.Errorf("first line exceeds %d bytes", maxLineBytes)
	}
	if len(line) == 0 {
		return nil, errors.New("empty first line")
	}
	return line, nil
}

// codexFirstLineCWD extracts session_meta.cwd from a rollout's first line. A
// line that is not JSON is an error (the file is skipped with a warning); a JSON
// line that is not a session_meta record yields "" and no error — the decoder,
// not discovery, decides what such a rollout means (ADR-021).
func codexFirstLineCWD(first []byte) (string, error) {
	var line codexLine
	if err := json.Unmarshal(first, &line); err != nil {
		return "", fmt.Errorf("decoding first line: %w", err)
	}
	if line.Type != codexSessionMetaType {
		return "", nil
	}
	var meta codexSessionMeta
	if err := json.Unmarshal(line.Payload, &meta); err != nil {
		return "", fmt.Errorf("decoding session_meta payload: %w", err)
	}
	return meta.CWD, nil
}
