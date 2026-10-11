package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"os"
	"syscall"
	"time"

	"github.com/serpro69/capy/internal/fileio"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
)

const (
	staleRefreshCooldown        = 5 * time.Second
	staleRefreshAdmission       = 100 * time.Millisecond
	staleRefreshBytes     int64 = 8 * 1024 * 1024
	staleRefreshSources         = 32
	staleSourcesSQL             = `SELECT id, label, file_path, content_hash, indexed_at, content_type, kind
		FROM sources WHERE file_path IS NOT NULL
		ORDER BY file_check_seq, id LIMIT 32`
	maxFileCheckSQL = `SELECT file_check_seq FROM sources WHERE file_path IS NOT NULL
		ORDER BY file_check_seq DESC, id DESC LIMIT 1`
)

type fileSource struct {
	id                                                   int64
	label, filePath, contentHash, indexedAt, contentType string
	kind                                                 SourceKind
}

type fileCheck struct {
	changed, current bool
	sanitized        string
	bytesRead        int64
}

// SetDenyChecker installs the prepared Read-policy check before concurrent use.
// A denied backing file is never opened for content. Nil disables filtering for
// standalone stores; MCP and CLI callers must install their policy snapshot.
func (s *ContentStore) SetDenyChecker(fn func(string) bool) { s.denyChecker = fn }

func (s *ContentStore) refreshTime() time.Time {
	if s.refreshNow != nil {
		return s.refreshNow()
	}
	return time.Now()
}

func (s *ContentStore) refreshStaleSources() int {
	return s.refreshStaleSourcesContext(s.ctx())
}

// refreshStaleSourcesContext bounds metadata, attempted files and actual reads.
// The time limit stops new admissions, not in-progress regular-file I/O. File
// failures retain cached content; scheduling failures are logged and stop the
// pass. Concurrent processes can duplicate a bounded check, but cannot advance
// or overwrite a different source incarnation.
func (s *ContentStore) refreshStaleSourcesContext(ctx context.Context) int {
	if ctx.Err() != nil || !s.refreshActive.CompareAndSwap(false, true) {
		return 0
	}
	defer s.refreshActive.Store(false)
	now := s.refreshTime().UnixNano()
	if now-s.lastRefreshTime.Load() < int64(staleRefreshCooldown) {
		return 0
	}
	s.lastRefreshTime.Store(now)

	if s.maxSourceBytes <= 0 || s.maxSourceBytes == math.MaxInt {
		slog.Warn("stale refresh: invalid source limit, skipping", "limit", s.maxSourceBytes)
		return 0
	}
	reservation := int64(s.maxSourceBytes) + 1
	remaining := max(staleRefreshBytes, reservation)
	sources, err := s.listStaleSources(ctx)
	if err != nil {
		slog.Warn("stale refresh: listing file-backed sources failed", "error", err)
		return 0
	}
	// Metadata latency must not consume the file-admission window. The first
	// selected source is attempted even if its operation exceeds this window.
	started := s.refreshTime()
	refreshed := 0
	for n, fs := range sources {
		if ctx.Err() != nil || (n > 0 && s.refreshTime().Sub(started) >= staleRefreshAdmission) {
			break
		}
		// Reserve room for a maximum-size file AND its growth-detection byte
		// before any open. Unused allowance is implicitly returned by charging
		// only actual bytes below, including partial/error/oversize reads.
		if remaining < reservation {
			break
		}
		var checked fileCheck
		if s.denyChecker == nil || !s.denyChecker(fs.filePath) {
			indexedAt, err := time.Parse("2006-01-02 15:04:05", fs.indexedAt)
			if err != nil {
				slog.Warn("stale refresh: unparseable indexed_at, skipping", "label", fs.label, "value", fs.indexedAt)
			} else if ctx.Err() == nil {
				checked = s.fileChangedSince(ctx, fs.filePath, fs.contentHash, indexedAt)
			}
		}
		remaining -= checked.bytesRead
		if ctx.Err() != nil {
			break
		}
		if checked.changed && len(checked.sanitized) > s.maxSourceBytes {
			slog.Warn("stale refresh: sanitized source exceeds limit, skipping", "path", fs.filePath)
			checked.changed = false
		}
		if checked.changed {
			chunks := chunkContent(checked.sanitized, fs.contentType)
			result, err := s.indexPreparedChunksChecked(ctx, checked.sanitized, fs.label, fs.contentType, fs.kind, fs.filePath, chunks, &fs)
			if err != nil {
				slog.Warn("stale refresh: re-index failed", "path", fs.filePath, "error", err)
			} else if result != nil {
				fs.id, fs.contentHash = result.SourceID, contentHash(checked.sanitized)
				refreshed++
			}
		}
		if err := s.completeFileCheck(ctx, fs, checked.current); err != nil {
			slog.Warn("stale refresh: scheduling failed, stopping pass", "path", fs.filePath, "error", err)
			break
		}
	}
	return refreshed
}

func (s *ContentStore) listStaleSources(ctx context.Context) ([]fileSource, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}
	rows, err := s.stmtListFileBackedSources.QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make([]fileSource, 0, staleRefreshSources)
	for rows.Next() {
		var fs fileSource
		var path, hash, indexedAt sql.NullString
		if err := rows.Scan(&fs.id, &fs.label, &path, &hash, &indexedAt, &fs.contentType, &fs.kind); err != nil {
			return nil, err
		}
		fs.filePath, fs.contentHash, fs.indexedAt = path.String, hash.String, indexedAt.String
		sources = append(sources, fs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return sources, nil
}

// completeFileCheck allocates and writes the next sequence in one short write
// transaction. The primary-key check rejects stale incarnations, including an
// in-place same-content backing-path or kind change. No filesystem I/O occurs
// while the write lock is held.
func (s *ContentStore) completeFileCheck(ctx context.Context, fs fileSource, current bool) error {
	db, err := s.getDB()
	if err != nil {
		return err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "sources")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sources
		WHERE id = ? AND label = ? AND file_path = ? AND COALESCE(content_hash, '') = ? AND kind = ?)`,
		fs.id, fs.label, fs.filePath, fs.contentHash, fs.kind).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check source incarnation: %w", err)
	}
	if !exists {
		return nil
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, maxFileCheckSQL).Scan(&seq); err != nil {
		return fmt.Errorf("allocate file check sequence: %w", err)
	}
	if seq == math.MaxInt64 {
		return fmt.Errorf("file check sequence exhausted")
	}
	_, err = tx.ExecContext(ctx, `UPDATE sources SET file_check_seq = ?,
		indexed_at = CASE WHEN ? THEN datetime('now') ELSE indexed_at END WHERE id = ?`, seq+1, current, fs.id)
	if err != nil {
		return fmt.Errorf("advance file check sequence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit file check sequence: %w", err)
	}
	return nil
}

// fileChangedSince binds stat/read to one nonblocking regular-file descriptor.
// Hashes are computed after secret stripping, like IndexWithFilePath. The raw
// read limit (including its probe byte) is applied first and always accounted.
// TODO(D2): atomically bind policy admission to open to close replacement
// between the earlier policy check and this descriptor open.
func (s *ContentStore) fileChangedSince(ctx context.Context, path, storedHash string, indexedAt time.Time) (out fileCheck) {
	if ctx.Err() != nil {
		return out
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return out
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return out
	}
	if !info.ModTime().UTC().After(indexedAt) {
		return out
	}
	if info.Size() > int64(s.maxSourceBytes) {
		slog.Warn("stale refresh: file exceeds max source size, skipping", "path", path, "size", info.Size(), "limit", s.maxSourceBytes)
		return out
	}
	if ctx.Err() != nil {
		return out
	}
	read := s.readFileContent
	if read == nil {
		read = fileio.ReadAll
	}
	raw, err := read(f, s.maxSourceBytes)
	out.bytesRead = int64(len(raw))
	if err != nil {
		slog.Warn("stale refresh: read failed, skipping", "path", path, "error", err)
		return out
	}
	if ctx.Err() != nil {
		return out
	}
	sanitized := sanitize.StripSecrets(string(raw))
	if contentHash(sanitized) == storedHash {
		out.current = true
	} else {
		out.changed, out.sanitized = true, sanitized
	}
	return out
}
