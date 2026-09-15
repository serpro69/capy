package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// preCommitHookBlockDBRepo returns the capy pre-commit block for a repository
// that *holds* knowledge databases (one subdirectory per project) rather than a
// project repo. Unlike the project block it needs no capy binary and no key: it
// only refuses an unsafe database, never repairs one. Repair happens in the
// owning project.
//
// SQLite removes the -wal/-shm sidecars on a clean last-connection close (and the
// capy MCP server checkpoints on shutdown, ADR-016), so "no sidecars beside the
// file" means the main file is complete and self-consistent. A present sidecar
// therefore means a live session or an unclean shutdown — both the user's call to
// resolve in the owning project.
//
// It reuses the same start/end markers as the project block so a re-run replaces
// the block in place and a stale hand-edited block is migrated. Shell conventions
// mirror preCommitHookBlock: runs under `#!/bin/sh` with no pipefail, so the
// `while` loop on the right of the pipe carries a trailing `|| exit 1` (an inner
// `exit 1` only sets the subshell/pipeline status). Do NOT remove those guards.
func preCommitHookBlockDBRepo() string {
	return fmt.Sprintf(`%s
# Installed by capy setup --db-repo — refuses unsafe databases; safe to remove if not needed.

staged_dbs=$(git diff --cached --name-only --diff-filter=AM | grep '\.db$' || true)
if [ -n "$staged_dbs" ]; then
  printf '%%s\n' "$staged_dbs" | while IFS= read -r f; do
    if head -c 15 "$f" 2>/dev/null | grep -q 'SQLite format 3'; then
      echo "capy: refusing to commit unencrypted $f." >&2
      echo "      Run 'capy encrypt' in the project that owns it, then re-stage." >&2
      exit 1
    fi
    if [ -s "$f-wal" ]; then
      echo "capy: refusing to commit $f with a pending WAL ($f-wal is non-empty)." >&2
      echo "      Stop active capy sessions for $(dirname "$f"), or run" >&2
      echo "      'capy checkpoint --project-dir <project>' (works from anywhere), then re-stage." >&2
      exit 1
    fi
    if [ -e "$f-shm" ]; then
      echo "capy: refusing to commit $f with an open connection ($f-shm present)." >&2
      echo "      Stop active capy sessions for $(dirname "$f"), or run" >&2
      echo "      'capy checkpoint --project-dir <project>' (works from anywhere), then re-stage." >&2
      exit 1
    fi
  done || exit 1
fi
%s
`, preCommitMarkerStart, preCommitMarkerEnd)
}

// SetupDBRepo configures a repository that holds knowledge databases (not a
// project). It does exactly two things: ignore the transient SQLite sidecars
// (committing them is the ADR-015 corruption vector) and install the pure-sh
// guard hook. No wrapper, no MCP config, no routing file, no .capy/ — the hook
// has no capy-binary dependency, so an outdated or missing binary can never
// weaken the guard.
//
// It returns hookInstalled=true only when the guard hook was actually written.
// The guard hook is the whole point of a DB repo, so the caller MUST NOT report
// success unconditionally: a real install error is returned (fail loud), and the
// one non-fatal case — no .git/hooks, i.e. not a normal checkout — reports
// hookInstalled=false so the caller can warn that commits are unguarded.
func SetupDBRepo(repoDir string) (hookInstalled bool, err error) {
	gitignorePath := filepath.Join(repoDir, ".gitignore")
	if err := ensureGitignoreEntry(gitignorePath, "*.db-wal"); err != nil {
		return false, fmt.Errorf("updating .gitignore: %w", err)
	}
	if err := ensureGitignoreEntry(gitignorePath, "*.db-shm"); err != nil {
		return false, fmt.Errorf("updating .gitignore: %w", err)
	}

	if _, err := os.Stat(filepath.Join(repoDir, ".git", "hooks")); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "capy: warning: no .git/hooks here; skipped the DB-repo guard hook (is this a git checkout?)")
		return false, nil
	}
	if err := installPreCommitHookBlock(repoDir, preCommitHookBlockDBRepo()); err != nil {
		return false, fmt.Errorf("installing pre-commit guard hook: %w", err)
	}
	return true, nil
}
