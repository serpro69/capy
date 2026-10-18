package runstore

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/sanitize"
)

func (s *Store) project(ctx context.Context, tx *sql.Tx, row *Admitted, stream string, data []byte, eof bool) error {
	if row.Result.Redaction.State == "unavailable" {
		return nil
	}
	var checkpoint []byte
	if err := tx.QueryRowContext(ctx, "SELECT checkpoint FROM run_streams WHERE run_id=? AND stream=?", row.ID, stream).Scan(&checkpoint); err != nil {
		return storageError()
	}
	state, err := sanitize.RestoreStream(checkpoint)
	if err != nil {
		row.Result.Redaction.State = "unavailable"
		row.Result.Redaction.CoverageCursor = nil
		return nil
	}
	spans, err := state.Feed(data, eof)
	if err != nil {
		row.Result.Redaction.State = "unavailable"
		row.Result.Redaction.CoverageCursor = nil
		return nil
	}
	if err := s.persistSpans(ctx, tx, row, stream, spans); err != nil {
		return err
	}

	checkpoint, err = state.Checkpoint()
	if err != nil {
		return storageError()
	}
	_, err = tx.ExecContext(ctx, "UPDATE run_streams SET checkpoint=?,covered=?,observed=? WHERE run_id=? AND stream=?", checkpoint, state.Covered, state.Position, row.ID, stream)
	if err != nil {
		return storageError()
	}
	return nil
}

func (s *Store) projectionState(ctx context.Context, tx *sql.Tx, row *Admitted) error {
	if row.Result.Redaction.State == "unavailable" {
		return nil
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_streams WHERE run_id=? AND covered<observed)", row.ID).Scan(&pending); err != nil {
		return storageError()
	}
	row.Result.Redaction.State = "ready"
	if pending {
		row.Result.Redaction.State = "pending"
	}
	var seq uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(e.seq),(SELECT COALESCE(MAX(seq),0)+1 FROM run_events WHERE run_id=?))
FROM run_events e JOIN run_streams s ON s.run_id=e.run_id AND s.stream=e.stream
WHERE e.run_id=? AND e.byte_offset+length(e.data)>s.covered`, row.ID, row.ID).Scan(&seq); err != nil {
		return storageError()
	}
	cursor := s.projectionCursor(row.ID, "merged", seq, 0, 0)
	row.Result.Redaction.CoverageCursor = &cursor
	return nil
}

// A resolved prefix can start in an earlier raw event. Split its projection at
// the original event boundaries rather than moving later bytes ahead of another
// stream. A Unicode code point belongs atomically to the event of its first byte.
func (s *Store) persistSpans(ctx context.Context, tx *sql.Tx, row *Admitted, stream string, spans []sanitize.Span) error {
	id := row.ID
	if len(spans) == 0 {
		return nil
	}
	type anchor struct {
		seq          uint64
		phase        string
		offset, line int64
		data         []byte
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,phase,byte_offset,start_line,data FROM run_events
WHERE run_id=? AND stream=? AND byte_offset<? AND byte_offset+length(data)>? ORDER BY byte_offset LIMIT 65`, id, stream, spans[len(spans)-1].End, spans[0].Start)
	if err != nil {
		return storageError()
	}
	anchors := make([]anchor, 0, 2)
	for rows.Next() {
		var a anchor
		if err := rows.Scan(&a.seq, &a.phase, &a.offset, &a.line, &a.data); err != nil {
			rows.Close()
			return storageError()
		}
		anchors = append(anchors, a)
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil || closeErr != nil || len(anchors) > 64 {
		return storageError()
	}
	values := make([]any, 0, 32*9)
	flush := func() error {
		if len(values) == 0 {
			return nil
		}
		// Only the bounded row count shapes SQL; every stored value is a parameter.
		placeholders := strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?,?,?,?),", len(values)/9), ",")
		_, err := tx.ExecContext(ctx, `INSERT INTO run_projection(run_id,stream,raw_start,raw_end,seq,phase,start_line,text,redacted) VALUES `+placeholders, values...)
		if err != nil {
			return storageError()
		}
		clear(values)
		values = values[:0]
		return nil
	}
	for _, span := range spans {
		position := span.Start
		for _, a := range anchors {
			end := min(span.End, a.offset+int64(len(a.data)))
			if end <= position {
				continue
			}
			text := span.Text
			if !span.Redacted {
				for end < span.End && !utf8.RuneStart(span.Text[end-span.Start]) {
					end++
				}
				text = span.Text[position-span.Start : end-span.Start]
			}
			line := a.line + int64(bytes.Count(a.data[:position-a.offset], []byte{'\n'}))
			values = append(values, id, stream, position, end, a.seq, a.phase, line, text, span.Redacted)
			row.chargeBytes += int64(len(text)) + projectionOverhead
			if len(values) == cap(values) {
				if err := flush(); err != nil {
					return err
				}
			}
			position = end
		}
		if position != span.End {
			return storageError()
		}
	}
	return flush()
}
