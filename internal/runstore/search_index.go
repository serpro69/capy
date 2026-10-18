package runstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
)

const searchWindowBytes = 4096
const searchOverlapBytes = 512
const indexWindowsPerStream = 8

const searchSchema = `
CREATE TABLE run_search_progress(
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 stream TEXT NOT NULL CHECK(stream IN ('stdout','stderr')),
 next_start INTEGER NOT NULL DEFAULT 0, covered INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(run_id,stream)
);
CREATE TABLE run_search_windows(
 id INTEGER PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id),
 stream TEXT NOT NULL, raw_start INTEGER NOT NULL, raw_end INTEGER NOT NULL,
 seq INTEGER NOT NULL, phase TEXT NOT NULL, start_line INTEGER NOT NULL
);
CREATE UNIQUE INDEX run_search_window_start ON run_search_windows(run_id,stream,raw_start);
CREATE VIRTUAL TABLE run_search_fts USING fts5(title,content,tokenize='porter unicode61');
CREATE VIRTUAL TABLE run_search_trigram USING fts5(title,content,tokenize='trigram');
`

type indexWindow struct {
	Fragment
	next int64
}

// projectionWindow reads a bounded slice of finalized, immutable projection.
// Independent streams may advance while the merged read barrier is pending.
// Overlap crosses event/projection boundaries, retaining ordinary split tokens.
func projectionWindow(ctx context.Context, tx *sql.Tx, id, stream string, start int64) (indexWindow, error) {
	var window indexWindow
	rows, err := tx.QueryContext(ctx, `SELECT seq,phase,raw_start,raw_end,start_line,text,redacted FROM run_projection
WHERE run_id=? AND stream=? AND raw_start>=COALESCE(
 (SELECT raw_start FROM run_projection WHERE run_id=? AND stream=? AND raw_start<=? ORDER BY raw_start DESC LIMIT 1),?)
AND raw_end>? ORDER BY raw_start LIMIT 64`, id, stream, id, stream, start, start, start)
	if err != nil {
		return window, storageError()
	}
	// The predecessor lookup and lower bound both use the projection PK.
	// Filtering raw_end alone would rescan all previously indexed rows.
	defer rows.Close()
	var parts []Fragment
	var text strings.Builder
	for rows.Next() {
		part := Fragment{Stream: stream}
		if err := rows.Scan(&part.Sequence, &part.Phase, &part.ByteOffset, &part.RawEnd, &part.StartLine, &part.Text, &part.Redacted); err != nil {
			return window, storageError()
		}
		if len(parts) > 0 && (part.Phase != parts[0].Phase || part.ByteOffset != window.RawEnd) {
			break
		}
		if !part.Redacted && start > part.ByteOffset {
			offset := start - part.ByteOffset
			if offset >= int64(len(part.Text)) || !utf8.RuneStart(part.Text[offset]) {
				return window, storageError()
			}
			part.StartLine += int64(strings.Count(part.Text[:offset], "\n"))
			part.Text = part.Text[offset:]
			part.ByteOffset = start
		}
		remaining := searchWindowBytes - text.Len()
		if len(part.Text) > remaining {
			if part.Redacted {
				break
			}
			for remaining > 0 && !utf8.RuneStart(part.Text[remaining]) {
				remaining--
			}
			if remaining == 0 {
				break
			}
			part.Text = part.Text[:remaining]
			part.RawEnd = part.ByteOffset + int64(remaining)
		}
		if len(parts) == 0 {
			window.Fragment = part
		}
		window.RawEnd = part.RawEnd
		parts = append(parts, part)
		text.WriteString(part.Text)
		if text.Len() == searchWindowBytes {
			break
		}
	}
	if rows.Err() != nil {
		return window, storageError()
	}
	window.Text = text.String()
	window.next = window.ByteOffset
	// Retain at most 512 bytes of overlap, with redaction markers kept atomic.
	drop := len(window.Text) - searchOverlapBytes
	for _, part := range parts {
		if drop >= len(part.Text) {
			drop -= len(part.Text)
			window.next = part.RawEnd
			continue
		}
		if drop > 0 && !part.Redacted {
			for drop < len(part.Text) && !utf8.RuneStart(part.Text[drop]) {
				drop++
			}
			window.next = part.ByteOffset + int64(drop)
		}
		break
	}
	return window, nil
}

func writeWindow(ctx context.Context, tx *sql.Tx, id string, window indexWindow) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_search_windows(run_id,stream,raw_start,raw_end,seq,phase,start_line)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id,stream,raw_start) DO UPDATE SET raw_end=excluded.raw_end`,
		id, window.Stream, window.ByteOffset, window.RawEnd, window.Sequence, window.Phase, window.StartLine); err != nil {
		return storageError()
	}
	var rowID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM run_search_windows WHERE run_id=? AND stream=? AND raw_start=?", id, window.Stream, window.ByteOffset).Scan(&rowID); err != nil {
		return storageError()
	}
	// Fixed table names only; caller text is always bound. Matching rowids keep
	// both layers and their read anchors in the same atomic transaction.
	for _, table := range []string{"run_search_fts", "run_search_trigram"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid=?", rowID); err != nil {
			return storageError()
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+"(rowid,title,content) VALUES(?,?,?)", rowID, fmt.Sprintf("%s output %d", window.Stream, window.ByteOffset), window.Text); err != nil {
			return storageError()
		}
	}
	return nil
}

func (s *Store) indexBatch(ctx context.Context, owner *Owner, id string) error {
	ctx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	var claimed bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_work_claims WHERE run_id=? AND slot=? AND generation=?)", id, owner.Slot, owner.ID).Scan(&claimed); err != nil || !claimed {
		return storageError()
	}
	row, err := loadRun(ctx, tx, id)
	if err != nil {
		return err
	}
	if row.Result.Redaction.State == "unavailable" || row.Result.Redaction.Version != sanitize.StreamVersion {
		return failure("redaction_unavailable", "sanitized output is unavailable; explicit raw reads remain independent")
	}
	complete := row.Result.CompletedAt != nil && row.Result.Redaction.State == "ready"
	coverage := make(map[string]int64, 2)
	for _, stream := range []string{"stdout", "stderr"} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_search_progress(run_id,stream) VALUES(?,?) ON CONFLICT DO NOTHING", id, stream); err != nil {
			return storageError()
		}
		var next, covered, available int64
		if err := tx.QueryRowContext(ctx, "SELECT next_start,covered FROM run_search_progress WHERE run_id=? AND stream=?", id, stream).Scan(&next, &covered); err != nil {
			return storageError()
		}
		if err := tx.QueryRowContext(ctx, "SELECT covered FROM run_streams WHERE run_id=? AND stream=?", id, stream).Scan(&available); err != nil {
			return storageError()
		}
		for i := 0; i < indexWindowsPerStream && covered < available; i++ {
			window, err := projectionWindow(ctx, tx, id, stream, next)
			if err != nil {
				return err
			}
			if window.RawEnd <= covered {
				// Highly fragmented projection can exhaust the row budget within
				// the overlap. Drop overlap, never stall retained coverage.
				if next < covered {
					next = covered
					continue
				}
				return storageError()
			}
			if err := writeWindow(ctx, tx, id, window); err != nil {
				return err
			}
			covered, next = window.RawEnd, window.next
		}
		if _, err := tx.ExecContext(ctx, "UPDATE run_search_progress SET next_start=?,covered=? WHERE run_id=? AND stream=?", next, covered, id, stream); err != nil {
			return storageError()
		}
		coverage[stream] = covered
		complete = complete && covered == available
	}
	row.Result.Index = Index{State: "building"}
	if complete {
		row.Result.Index.State = "ready"
	}
	cursorBytes, err := json.Marshal(struct {
		Version          int `json:"v"`
		Store, Run, View string
		Coverage         map[string]int64
	}{1, s.id, id, "index", coverage})
	if err != nil {
		return storageError()
	}
	cursor := base64.RawURLEncoding.EncodeToString(cursorBytes)
	row.Result.Index.CoverageCursor = &cursor
	if err := saveIndexResult(ctx, tx, row); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE run_meta SET index_enabled=1 WHERE singleton=1"); err != nil {
		return storageError()
	}
	used, err := refreshIndexDebit(ctx, tx)
	if err != nil {
		return err
	}
	if used > s.policy.MaxRetainedBytes {
		return failure("index_quota_blocked", "index quota exhausted; retained raw and sanitized output can still be read")
	}
	if err := tx.Commit(); err != nil {
		return storageError()
	}
	return nil
}

func saveIndexResult(ctx context.Context, tx *sql.Tx, row Admitted) error {
	encoded, err := json.Marshal(row.Result)
	if err != nil {
		return storageError()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET result=? WHERE id=?", encoded, row.ID); err != nil {
		return storageError()
	}
	return nil
}
