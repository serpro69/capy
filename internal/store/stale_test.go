package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

// forceRefresh bypasses the cooldown and runs one stale-detection sweep.
func forceRefresh(s *ContentStore) int {
	s.lastRefreshTime.Store(0)
	return s.refreshStaleSources()
}

// writeAndIndexFile writes content to dir/name and indexes it file-backed.
func writeAndIndexFile(t *testing.T, s *ContentStore, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	_, err := s.IndexWithFilePath(content, "src:"+name, "", KindDurable, p)
	require.NoError(t, err)
	return p
}

// modifyFile rewrites a file and forces its mtime to a point clearly after the
// (second-granular) indexed_at timestamp so the mtime gate fires deterministically.
func modifyFile(t *testing.T, p, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	future := time.Now().Add(10 * time.Second)
	require.NoError(t, os.Chtimes(p, future, future))
}

// sourceContent concatenates the indexed chunk content for the source labeled label.
func sourceContent(t *testing.T, s *ContentStore, label string) string {
	t.Helper()
	sources, err := s.ListSources()
	require.NoError(t, err)
	var sb strings.Builder
	for _, src := range sources {
		if src.Label != label {
			continue
		}
		chunks, err := s.GetChunksBySource(src.ID)
		require.NoError(t, err)
		for _, c := range chunks {
			sb.WriteString(c.Content)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// filePathForLabel returns the stored file_path for the source labeled label.
func filePathForLabel(t *testing.T, s *ContentStore, label string) sql.NullString {
	t.Helper()
	db, err := s.getDB()
	require.NoError(t, err)
	var fp sql.NullString
	err = db.QueryRow(`SELECT file_path FROM sources WHERE label = ?`, label).Scan(&fp)
	require.NoError(t, err)
	return fp
}

// --- tests ---

func TestStaleDetection_FreshFileNoRefresh(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	writeAndIndexFile(t, s, dir, "doc.md", "the original content about authentication")

	// Unchanged on disk: even with a bumped mtime the hash matches, so no
	// re-index happens (touched-but-unchanged path).
	modifyFile(t, filepath.Join(dir, "doc.md"), "the original content about authentication")
	assert.Equal(t, 0, forceRefresh(s))
	assert.Contains(t, sourceContent(t, s, "src:doc.md"), "original content")
}

func TestStaleDetection_ModifiedFileAutoRefresh(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	p := writeAndIndexFile(t, s, dir, "doc.md", "original payload alpha")

	modifyFile(t, p, "replaced payload bravo")
	assert.Equal(t, 1, forceRefresh(s))

	got := sourceContent(t, s, "src:doc.md")
	assert.Contains(t, got, "bravo")
	assert.NotContains(t, got, "alpha")
}

func TestStaleDetection_ContentOnlySourceSkipped(t *testing.T) {
	s := newTestStore(t)
	// Inline content has no file_path → must not participate in stale detection.
	_, err := s.Index("inline content not backed by a file", "inline-src", "", KindDurable)
	require.NoError(t, err)

	assert.Equal(t, 0, forceRefresh(s))
	assert.False(t, filePathForLabel(t, s, "inline-src").Valid)
}

func TestStaleDetection_DeletedFileGraceful(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	p := writeAndIndexFile(t, s, dir, "doc.md", "content that will outlive its file")

	require.NoError(t, os.Remove(p))
	// Deleted file: skipped gracefully, cached content preserved.
	assert.Equal(t, 0, forceRefresh(s))
	assert.Contains(t, sourceContent(t, s, "src:doc.md"), "outlive its file")
}

func TestStaleDetection_DeniedFileSkipped(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	p := writeAndIndexFile(t, s, dir, "secret.md", "secret original")

	// Deny policy changed after indexing — the file must not be re-read.
	s.SetDenyChecker(func(path string) bool { return path == p })

	modifyFile(t, p, "secret leaked update")
	assert.Equal(t, 0, forceRefresh(s))

	got := sourceContent(t, s, "src:secret.md")
	assert.Contains(t, got, "original")
	assert.NotContains(t, got, "leaked")
}

func TestStaleDetection_SecondUpdate(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	p := writeAndIndexFile(t, s, dir, "doc.md", "version one")

	modifyFile(t, p, "version two")
	assert.Equal(t, 1, forceRefresh(s))
	assert.Contains(t, sourceContent(t, s, "src:doc.md"), "two")

	// Source must remain file-backed after the first refresh so a second
	// change is still detected.
	assert.True(t, filePathForLabel(t, s, "src:doc.md").Valid)

	modifyFile(t, p, "version three")
	assert.Equal(t, 1, forceRefresh(s))
	got := sourceContent(t, s, "src:doc.md")
	assert.Contains(t, got, "three")
	assert.NotContains(t, got, "two")
	assert.True(t, filePathForLabel(t, s, "src:doc.md").Valid)
}

func TestStaleDetection_SecretBearingFile(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	// AKIA... is redacted by sanitize.StripSecrets; the stored hash is of the
	// sanitized content, so re-reading the raw bytes must still hash-match.
	const secret = "deploy notes token AKIAIOSFODNN7EXAMPLE end"
	p := writeAndIndexFile(t, s, dir, "creds.md", secret)

	// Touched, same bytes: sanitized hash matches → no perpetual refresh.
	modifyFile(t, p, secret)
	assert.Equal(t, 0, forceRefresh(s))

	// A genuine change to the non-secret text is still detected.
	modifyFile(t, p, "deploy notes token AKIAIOSFODNN7EXAMPLE end CHANGED")
	assert.Equal(t, 1, forceRefresh(s))
	got := sourceContent(t, s, "src:creds.md")
	assert.Contains(t, got, "CHANGED")
	assert.NotContains(t, got, "AKIAIOSFODNN7EXAMPLE") // secret never stored
}

func TestStaleDetection_NullToNonNullFilePath(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()

	const content = "shared content indexed twice"
	// First indexed inline (file_path NULL)...
	_, err := s.Index(content, "dual-src", "", KindDurable)
	require.NoError(t, err)
	assert.False(t, filePathForLabel(t, s, "dual-src").Valid)

	// ...then re-indexed from a file with identical content. The dedup path
	// must attach file_path so stale detection activates.
	p := filepath.Join(dir, "dual.md")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	res, err := s.IndexWithFilePath(content, "dual-src", "", KindDurable, p)
	require.NoError(t, err)
	assert.True(t, res.AlreadyIndexed)

	fp := filePathForLabel(t, s, "dual-src")
	require.True(t, fp.Valid)
	assert.Equal(t, p, fp.String)

	// Now a file change is detected.
	modifyFile(t, p, "shared content edited on disk")
	assert.Equal(t, 1, forceRefresh(s))
	assert.Contains(t, sourceContent(t, s, "dual-src"), "edited on disk")
}

func TestStaleDetection_SymlinkEscapeBlocked(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()

	// A symlink whose real target lives under a "denied-target" dir.
	deniedDir := filepath.Join(dir, "denied-target")
	require.NoError(t, os.MkdirAll(deniedDir, 0o755))
	target := filepath.Join(deniedDir, "secret.md")
	require.NoError(t, os.WriteFile(target, []byte("symlink original"), 0o644))

	link := filepath.Join(dir, "link.md")
	require.NoError(t, os.Symlink(target, link))

	_, err := s.IndexWithFilePath("symlink original", "src:link.md", "", KindDurable, link)
	require.NoError(t, err)

	// Deny checker resolves symlinks before matching — mirrors EvaluateFilePath.
	s.SetDenyChecker(func(path string) bool {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return false
		}
		return strings.Contains(real, "denied-target")
	})

	modifyFile(t, target, "symlink leaked update")
	assert.Equal(t, 0, forceRefresh(s))
	got := sourceContent(t, s, "src:link.md")
	assert.Contains(t, got, "original")
	assert.NotContains(t, got, "leaked")
}

func TestStaleDetection_NonRegularFileSkipped(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()

	// A FIFO at the indexed path: O_NONBLOCK keeps the open from hanging and
	// the IsRegular guard rejects it (no re-index, no panic).
	fifo := filepath.Join(dir, "pipe")
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))

	_, err := s.IndexWithFilePath("placeholder", "src:pipe", "", KindDurable, fifo)
	require.NoError(t, err)

	future := time.Now().Add(10 * time.Second)
	require.NoError(t, os.Chtimes(fifo, future, future))

	assert.Equal(t, 0, forceRefresh(s))
}

func TestStaleDetection_Cooldown(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	p := writeAndIndexFile(t, s, dir, "doc.md", "cooldown one")

	// First sweep (cooldown reset) detects the change.
	modifyFile(t, p, "cooldown two")
	assert.Equal(t, 1, forceRefresh(s))

	// A second change within the cooldown window is throttled — refreshStaleSources
	// returns 0 without re-indexing.
	modifyFile(t, p, "cooldown three")
	assert.Equal(t, 0, s.refreshStaleSources())
	assert.NotContains(t, sourceContent(t, s, "src:doc.md"), "three")
}

func TestStaleDetection_TouchedUnchangedAdvancesIndexedAt(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	p := writeAndIndexFile(t, s, dir, "doc.md", "stable content")

	// Backdate indexed_at so the advance is observable without a 1s sleep, and
	// bump mtime so the gate fires and the file is read + hash-matched.
	db, err := s.getDB()
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sources SET indexed_at = '2020-01-01 00:00:00' WHERE label = ?`, "src:doc.md")
	require.NoError(t, err)
	modifyFile(t, p, "stable content") // identical content, future mtime

	// Touched-but-unchanged: no re-index, but indexed_at must advance so the
	// mtime gate stops re-reading this file on every future search.
	assert.Equal(t, 0, forceRefresh(s))

	var indexedAt string
	require.NoError(t, db.QueryRow(`SELECT indexed_at FROM sources WHERE label = ?`, "src:doc.md").Scan(&indexedAt))
	assert.NotEqual(t, "2020-01-01 00:00:00", indexedAt, "indexed_at should advance on a touched-but-unchanged file")
}

func TestStaleDetection_OversizedFileSkipped(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	// 64-byte source limit so the test doesn't need a multi-MB file.
	s := NewContentStore(filepath.Join(dir, "test.db"), dir, 0, 64)
	t.Cleanup(func() { s.Close() })

	fileDir := t.TempDir()
	p := filepath.Join(fileDir, "doc.md")
	require.NoError(t, os.WriteFile(p, []byte("small original"), 0o644))
	_, err := s.IndexWithFilePath("small original", "src:doc.md", "", KindDurable, p)
	require.NoError(t, err)

	// Grow the file past the limit and bump mtime so the gate fires.
	modifyFile(t, p, strings.Repeat("x", 200))

	// Oversized file is skipped without reading — no re-index, no unbounded read.
	assert.Equal(t, 0, forceRefresh(s))
	assert.Contains(t, sourceContent(t, s, "src:doc.md"), "small original")
}

func TestMigrate019_FilePathPersistsAcrossReopen(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	fileDir := t.TempDir()
	p := filepath.Join(fileDir, "doc.md")
	require.NoError(t, os.WriteFile(p, []byte("persisted content"), 0o644))

	s := NewContentStore(dbPath, dir, 0, 0)
	_, err := s.IndexWithFilePath("persisted content", "persist-src", "", KindDurable, p)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// Reopen: the migration is idempotent and the column round-trips.
	s2 := NewContentStore(dbPath, dir, 0, 0)
	t.Cleanup(func() { s2.Close() })
	fp := filePathForLabel(t, s2, "persist-src")
	require.True(t, fp.Valid)
	assert.Equal(t, p, fp.String)
}
