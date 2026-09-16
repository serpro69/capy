package vault

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// OverwriteFunc decides whether an existing file at absPath may be overwritten.
// RestoreSession calls it only when a target already exists; returning false
// leaves the existing file untouched and records it as skipped. A nil
// OverwriteFunc is treated as "never overwrite".
type OverwriteFunc func(absPath string) bool

// RestoreResult reports what a restore wrote, skipped, or rejected.
type RestoreResult struct {
	Root    string   // the resolved (symlink-evaluated) restore root
	Written []string // absolute paths written
	Skipped []string // existing absolute paths the OverwriteFunc declined
	Unsafe  []string // session-relative paths rejected by path-safety validation
	// Notes are human-readable observations the CLI prints after the summary —
	// today only "a compressed twin of the main file was left untouched at …"
	// (see RestoreSessionAt). Empty for every Claude restore.
	Notes []string
}

// RestoreSession writes an archived Claude Code session's main JSONL and every
// preserved sidecar back to disk under root:
//
//	root/<uuid>.jsonl                  (from rawJSONL)
//	root/<uuid>/<relative_path>        (one per vault_files entry)
//
// It is RestoreSessionAt with the Claude main-file name; see there for the path
// safety contract.
func RestoreSession(uuid string, rawJSONL []byte, files []File, root string, overwrite OverwriteFunc) (*RestoreResult, error) {
	return RestoreSessionAt(uuid, uuid+".jsonl", rawJSONL, files, root, overwrite)
}

// RestoreSessionAt writes an archived session back to disk under root, with the
// main file at root/<mainRel> and every preserved sidecar at
// root/<uuid>/<relative_path>. mainRel is the platform's location for the main
// file: "<uuid>.jsonl" for Claude (RestoreSession), and for Codex the stored
// relative rollout path — sessions/YYYY/MM/DD/rollout-…-<uuid>.jsonl, always
// the plain .jsonl name even when the archived rollout was discovered as .zst
// (design § CLI — restore). Codex rollouts have no sidecars, so files is empty
// for them and the root/<uuid>/ convention is Claude-only in practice.
//
// Path safety: root is created then resolved with filepath.EvalSymlinks so a
// symlinked root component cannot redirect writes outside it; mainRel and every
// sidecar path are validated to reject absolute paths and ".." components and
// to stay within the resolved root (containment via filepath.Rel). An unsafe
// mainRel is fatal (it is the session's own location — there is nothing safe to
// fall back to); an unsafe sidecar is skipped with a warning rather than
// aborting the whole restore, so the main file (the critical artifact) is
// always restored. Existing files are written only if overwrite approves them.
//
// A compressed twin — root/<mainRel>.zst — is never touched: Codex itself
// materialises the plain .jsonl beside a compressed rollout when it appends, so
// the pair is a state Codex produces. Its path is appended to
// RestoreResult.Notes so the CLI can tell the user (design § Open Questions 5).
func RestoreSessionAt(uuid, mainRel string, rawJSONL []byte, files []File, root string, overwrite OverwriteFunc) (*RestoreResult, error) {
	if overwrite == nil {
		overwrite = func(string) bool { return false }
	}
	// Validate the raw relative path BEFORE it is joined under root: the join
	// cleans, so an interior ".." would otherwise be collapsed and masked.
	if err := validateSidecarRel(mainRel); err != nil {
		return nil, fmt.Errorf("session %q main file path: %w", uuid, err)
	}

	// 0o700: restored transcripts can carry secrets/credentials — keep them
	// owner-only (the same user runs `claude --resume` / `codex resume`, so
	// this is sufficient).
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("creating restore root %q: %w", root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolving restore root %q: %w", root, err)
	}
	res := &RestoreResult{Root: resolvedRoot}

	// Main file. A traversal-bearing mainRel is fatal (see above); a
	// symlink-escaping root is likewise fatal (it would taint every sidecar too).
	mainTarget, err := safeChildPath(resolvedRoot, filepath.FromSlash(mainRel))
	if err != nil {
		return nil, fmt.Errorf("session %q main file path: %w", uuid, err)
	}
	if unsafe, err := writeRestoreFile(resolvedRoot, mainTarget, rawJSONL, overwrite, res); err != nil {
		return nil, err
	} else if unsafe {
		return nil, fmt.Errorf("restore root %q escapes via a symlinked directory", resolvedRoot)
	}
	// Lstat, not Stat: a dangling symlink named like the twin is still "something
	// there we did not touch" and worth the note. Worded for both outcomes of
	// the main write — a declined overwrite means a plain file already sat there.
	if twin := mainTarget + ".zst"; fileExistsNoFollow(twin) {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"left a compressed copy of this session untouched at %s; a plain .jsonl sits beside it", twin))
	}

	// Sidecars under <uuid>/. relative_path is stored slash-separated.
	for _, f := range files {
		if err := validateSidecarRel(f.RelativePath); err != nil {
			slog.Warn("vault restore: skipping unsafe file path", "path", f.RelativePath, "error", err)
			res.Unsafe = append(res.Unsafe, f.RelativePath)
			continue
		}
		// Belt-and-suspenders: even after the rule check, confirm the joined
		// target stays within the resolved root.
		rel := filepath.Join(uuid, filepath.FromSlash(f.RelativePath))
		target, err := safeChildPath(resolvedRoot, rel)
		if err != nil {
			slog.Warn("vault restore: skipping unsafe file path", "path", f.RelativePath, "error", err)
			res.Unsafe = append(res.Unsafe, f.RelativePath)
			continue
		}
		unsafe, err := writeRestoreFile(resolvedRoot, target, f.RawContent, overwrite, res)
		if err != nil {
			return nil, err
		}
		if unsafe {
			slog.Warn("vault restore: skipping file whose directory escapes the root via a symlink", "path", f.RelativePath)
			res.Unsafe = append(res.Unsafe, f.RelativePath)
		}
	}
	return res, nil
}

// fileExistsNoFollow reports whether something (file, dir or symlink — dangling
// included) exists at path, without following a symlink.
func fileExistsNoFollow(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// validateSidecarRel enforces the restore path-safety rules on a stored
// (slash-separated) relative path — a sidecar's relative_path, or the main
// file's mainRel (RestoreSessionAt): it must not be absolute and must not
// contain a ".." component. These rules apply to the raw path before it is
// joined under the root, so an absolute path or interior ".." cannot be masked
// by the join (which would otherwise re-anchor or collapse it inside the root).
func validateSidecarRel(rel string) error {
	if rel == "" {
		return fmt.Errorf("empty relative path")
	}
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(filepath.FromSlash(rel)) {
		return fmt.Errorf("absolute path not allowed: %q", rel)
	}
	if slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), "..") {
		return fmt.Errorf("path contains a .. component: %q", rel)
	}
	return nil
}

// safeChildPath joins rel under root and verifies the result stays within root.
// It rejects absolute paths and ".." escapes. root must already be resolved
// (filepath.EvalSymlinks) by the caller so a symlinked root component cannot
// redirect writes. filepath.Join cleans the result, so a "../escape" rel
// collapses to a path the containment check then rejects. This is a *lexical*
// check only — symlinked path components are caught later by writeRestoreFile.
func safeChildPath(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path not allowed: %q", rel)
	}
	target := filepath.Join(root, rel)
	if !withinRoot(root, target) {
		return "", fmt.Errorf("path escapes restore root: %q", rel)
	}
	return target, nil
}

// withinRoot reports whether p is root itself or lies beneath it, using a
// lexical filepath.Rel comparison. Both paths should already be cleaned (and,
// for symlink safety, resolved) by the caller.
func withinRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// writeRestoreFile writes content to target (under the resolved root), creating
// parent directories. It returns unsafe=true (without writing) when a symlinked
// directory component redirects the parent outside root — the lexical
// safeChildPath check cannot catch that because os.WriteFile follows symlinks.
//
// Symlink safety, two layers beyond the resolved-root guard:
//   - After MkdirAll, the parent dir is re-resolved with EvalSymlinks and
//     re-checked for containment (defeats a pre-planted symlinked *directory*).
//   - The leaf is stat'd with os.Lstat (no-follow); an existing non-dir is
//     removed before writing so os.WriteFile creates a regular file in place
//     rather than following a *leaf* symlink to an arbitrary target. A dangling
//     symlink — which os.Stat would have missed, silently bypassing the
//     overwrite prompt and writing through the link — is handled here too.
//
// An existing entry is overwritten only if overwrite approves; otherwise it is
// recorded as skipped and left intact.
func writeRestoreFile(root, target string, content []byte, overwrite OverwriteFunc, res *RestoreResult) (unsafe bool, err error) {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("creating directory for %q: %w", target, err)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, fmt.Errorf("resolving directory for %q: %w", target, err)
	}
	if !withinRoot(root, resolvedDir) {
		return true, nil
	}

	if info, lerr := os.Lstat(target); lerr == nil {
		if !overwrite(target) {
			res.Skipped = append(res.Skipped, target)
			return false, nil
		}
		// Remove an existing non-directory (file or symlink) so the write lands
		// on a fresh regular file and never follows a leaf symlink.
		if !info.IsDir() {
			if err := os.Remove(target); err != nil {
				return false, fmt.Errorf("removing existing %q: %w", target, err)
			}
		}
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		return false, fmt.Errorf("writing %q: %w", target, err)
	}
	res.Written = append(res.Written, target)
	return false, nil
}
