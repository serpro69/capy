package runstore

import (
	"context"
	"database/sql"
	"errors"
)

type ReadOptions struct {
	Stream    string
	Cursor    string
	StartLine int64
	MaxBytes  int
	Raw       bool
}

func (s *Store) Read(ctx context.Context, id string, options ReadOptions) (ReadPage, error) {
	if options.StartLine < 0 || (options.StartLine > 0 && (options.Cursor != "" || options.Stream == "merged")) {
		return ReadPage{}, failure("invalid_argument", "start_line requires a positive raw line, an explicit single stream, and no cursor")
	}
	if options.StartLine > 0 {
		cursor, err := s.lineCursor(ctx, id, options)
		if err != nil {
			return ReadPage{}, err
		}
		options.Cursor = cursor
	}
	if options.Raw {
		return s.ReadRaw(ctx, id, options.Stream, options.Cursor, options.MaxBytes)
	}
	return s.ReadProjection(ctx, id, options.Stream, options.Cursor, options.MaxBytes)
}

func (s *Store) lineCursor(ctx context.Context, id string, options ReadOptions) (string, error) {
	if options.Stream != "stdout" && options.Stream != "stderr" {
		return "", failure("invalid_argument", "start_line requires stdout or stderr")
	}
	if _, err := s.Load(ctx, id); err != nil {
		return "", err
	}
	var seq uint64
	var offset, line int64
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT seq,byte_offset,start_line,data FROM run_events WHERE run_id=? AND stream=? AND start_line<? ORDER BY start_line DESC,seq DESC LIMIT 1`, id, options.Stream, options.StartLine).Scan(&seq, &offset, &line, &data)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `SELECT seq,byte_offset,start_line,data FROM run_events WHERE run_id=? AND stream=? ORDER BY seq LIMIT 1`, id, options.Stream).Scan(&seq, &offset, &line, &data)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", failure("run_unavailable", "requested line has no captured output")
	}
	if err != nil {
		return "", storageError()
	}
	pos := 0
	for line < options.StartLine && pos < len(data) {
		if data[pos] == '\n' {
			line++
		}
		pos++
	}
	if line != options.StartLine {
		return "", failure("invalid_argument", "start_line exceeds captured output")
	}
	if options.Raw {
		if pos == len(data) {
			return s.cursor(id, options.Stream, seq+1, 0), nil
		}
		return s.cursor(id, options.Stream, seq, pos), nil
	}
	rawStart := offset + int64(pos)
	var anchorSeq uint64
	err = s.db.QueryRowContext(ctx, `SELECT seq FROM run_projection WHERE run_id=? AND stream=? AND raw_end>? ORDER BY raw_start LIMIT 1`, id, options.Stream, rawStart).Scan(&anchorSeq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", storageError()
	}
	if anchorSeq != 0 {
		seq = anchorSeq
	}
	return s.projectionCursor(id, options.Stream, seq, rawStart, 0), nil
}
