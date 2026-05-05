package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/store"
)

// SessionDir returns the Claude Code session directory for the given project.
// Claude Code mangles the absolute project path by replacing "/" and "." with "-"
// and stores sessions under ~/.claude/projects/<mangled>/.
func SessionDir(projectDir string) (string, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path: %w", err)
	}

	mangled := manglePath(abs)

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}

	dir := filepath.Join(home, ".claude", "projects", mangled)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("session directory not found: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("session path is not a directory: %s", dir)
	}
	return dir, nil
}

// manglePath replaces "/" and "." with "-" to match Claude Code's directory naming.
func manglePath(absPath string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(absPath)
}

// Sweep discovers, parses, and indexes Claude Code session files for the given
// project. It checks ctx.Err() between files for cooperative cancellation.
//
// Returns counts of indexed, skipped, and errored sessions.
func Sweep(ctx context.Context, cs *store.ContentStore, projectDir string) (indexed, skipped, errors int) {
	dir, err := SessionDir(projectDir)
	if err != nil {
		slog.Debug("session sweep: directory not found, skipping", "project", projectDir, "error", err)
		return 0, 0, 0
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("session sweep: cannot list directory", "dir", dir, "error", err)
		return 0, 0, 1
	}

	var jsonlFiles []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			jsonlFiles = append(jsonlFiles, e)
		}
	}

	if len(jsonlFiles) == 0 {
		return 0, 0, 0
	}

	indexedMap, err := buildIndexedAtMap(cs)
	if err != nil {
		slog.Warn("session sweep: cannot query existing sources", "error", err)
		return 0, 0, 1
	}

	for _, entry := range jsonlFiles {
		if ctx.Err() != nil {
			slog.Info("session sweep: cancelled", "indexed", indexed, "skipped", skipped, "errors", errors)
			return indexed, skipped, errors
		}

		uuid := strings.TrimSuffix(entry.Name(), ".jsonl")

		if shouldSkip(dir, uuid, entry, indexedMap) {
			skipped++
			continue
		}

		ok, err := indexSession(ctx, cs, dir, uuid)
		if err != nil {
			slog.Warn("session sweep: index failed", "file", entry.Name(), "error", err)
			errors++
			continue
		}
		if ok {
			indexed++
		} else {
			skipped++
		}
	}

	return indexed, skipped, errors
}

// buildIndexedAtMap queries existing session sources and returns uuid → indexed_at.
func buildIndexedAtMap(cs *store.ContentStore) (map[string]time.Time, error) {
	sources, err := cs.ListSources()
	if err != nil {
		return nil, err
	}

	m := make(map[string]time.Time, len(sources))
	for _, src := range sources {
		if src.Kind != store.KindSession {
			continue
		}
		uuid := extractUUIDFromLabel(src.Label)
		if uuid != "" {
			m[uuid] = src.IndexedAt
		}
	}
	return m, nil
}

// extractUUIDFromLabel extracts the UUID from a "session:<ISO-datetime>:<uuid>" label.
// The UUID is always the last colon-delimited segment. Using LastIndex avoids coupling
// to the specific datetime format (which contains internal colons).
func extractUUIDFromLabel(label string) string {
	const prefix = "session:"
	if !strings.HasPrefix(label, prefix) {
		return ""
	}
	idx := strings.LastIndex(label, ":")
	// The last ":" must be past the prefix (i.e., there's a datetime segment between).
	if idx <= len(prefix)-1 || idx == len(label)-1 {
		return ""
	}
	return label[idx+1:]
}

// shouldSkip returns true if the session file has not changed since last indexing.
// Compares max(file.mtime, subagents_dir.mtime) against the stored indexed_at time.
func shouldSkip(dir, uuid string, entry os.DirEntry, indexedMap map[string]time.Time) bool {
	indexedAt, exists := indexedMap[uuid]
	if !exists {
		return false
	}

	effectiveMtime := fileMtime(entry)

	subagentsDir := filepath.Join(dir, uuid, "subagents")
	if info, err := os.Stat(subagentsDir); err == nil {
		if info.ModTime().After(effectiveMtime) {
			effectiveMtime = info.ModTime()
		}
	}

	return !effectiveMtime.After(indexedAt)
}

// fileMtime extracts the modification time from a DirEntry.
func fileMtime(entry os.DirEntry) time.Time {
	info, err := entry.Info()
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// indexSession parses, validates, and indexes a single session file.
// Returns (true, nil) if the session was indexed, (false, nil) if it was
// skipped (not indexable / empty), or (false, err) on failure.
func indexSession(ctx context.Context, cs *store.ContentStore, dir, uuid string) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	jsonlPath := filepath.Join(dir, uuid+".jsonl")
	parsed, err := ParseSession(jsonlPath)
	if err != nil {
		return false, fmt.Errorf("parsing: %w", err)
	}

	// Log-and-continue: partial sub-agent failure does not fail the session.
	sessionBareDir := filepath.Join(dir, uuid)
	subPairs, err := ParseSubagents(sessionBareDir)
	if err != nil {
		slog.Warn("session sweep: sub-agent parse partial failure", "session", uuid, "error", err)
	}
	if len(subPairs) > 0 {
		parsed.TurnPairs = append(parsed.TurnPairs, subPairs...)
		for _, sp := range subPairs {
			parsed.TotalAssistantChars += len(sp.AssistantText)
		}
	}

	if !parsed.IsIndexable() {
		fi, _ := os.Stat(jsonlPath)
		if fi != nil && fi.Size() > 1024 && len(parsed.TurnPairs) == 0 {
			slog.Warn("session parsed to 0 turns",
				"file", filepath.Base(jsonlPath),
				"size", fi.Size(),
			)
		}
		return false, nil
	}

	tr := BuildTranscript(parsed)
	tr.Text = sanitize.StripSecrets(tr.Text)
	chunks := ChunkSession(parsed, tr, 0)
	if len(chunks) == 0 {
		return false, nil
	}

	label := buildLabel(parsed)
	_, err = cs.IndexChunked(tr.Text, label, "session", store.KindSession, chunks)
	if err != nil {
		return false, fmt.Errorf("indexing: %w", err)
	}

	return true, nil
}

// buildLabel creates a machine-agnostic label: "session:<ISO-datetime>:<UUID>".
func buildLabel(s *ParsedSession) string {
	ts := s.StartTime.UTC().Format("2006-01-02T15:04:05Z")
	return fmt.Sprintf("session:%s:%s", ts, s.SessionID)
}
