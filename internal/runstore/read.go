package runstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
)

type rawCursor struct {
	Version  int    `json:"v"`
	Store    string `json:"store"`
	Run      string `json:"run"`
	View     string `json:"view"`
	Stream   string `json:"stream"`
	Sequence uint64 `json:"seq"`
	Offset   int    `json:"offset"`
}

func (s *Store) cursor(run, stream string, seq uint64, offset int) string {
	// This scalar-only struct cannot fail JSON encoding.
	b, _ := json.Marshal(rawCursor{1, s.id, run, "raw", stream, seq, offset})
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Store) decodeCursor(encoded, run, stream string) (rawCursor, error) {
	c := rawCursor{Version: 1, Store: s.id, Run: run, View: "raw", Stream: stream, Sequence: 1}
	if encoded == "" {
		return c, nil
	}
	if len(encoded) > 1024 {
		return c, failure("cursor_invalid", "run cursor exceeds its limit")
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return c, failure("cursor_invalid", "invalid run cursor")
	}
	c = rawCursor{}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Sequence == 0 || c.Sequence > 1<<62 || c.Offset < 0 || c.Offset >= 16384 || c.View != "raw" {
		return c, failure("cursor_invalid", "invalid run cursor")
	}
	if c.Store != s.id {
		return c, failure("cursor_stale", "run cursor belongs to a different store")
	}
	if c.Run != run || c.Stream != stream {
		return c, failure("cursor_invalid", "run cursor selector does not match")
	}
	return c, nil
}

func (s *Store) ReadRaw(ctx context.Context, id, stream, encoded string, maxBytes int) (ReadPage, error) {
	page := ReadPage{Version: 1, Kind: "query", RunID: id, Raw: true, Encoding: "base64", Fragments: []Fragment{}}
	if stream != "stdout" && stream != "stderr" && stream != "merged" {
		return page, failure("invalid_argument", "stream must be stdout, stderr or merged")
	}
	if maxBytes < 1 || maxBytes > 65536 {
		return page, failure("invalid_argument", "max_bytes must be between 1 and 65536")
	}
	cursor, err := s.decodeCursor(encoded, id, stream)
	if err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, storageError()
	}
	defer tx.Rollback()
	row, err := loadRun(ctx, tx, id)
	if err != nil {
		return page, err
	}
	page.Run = row.Result
	rows, err := tx.QueryContext(ctx, `SELECT seq,phase,stream,data,byte_offset,start_line FROM run_events
WHERE run_id=? AND seq>=? AND (?='merged' OR stream=?) ORDER BY seq LIMIT 64`, id, cursor.Sequence, stream, stream)
	if err != nil {
		return page, storageError()
	}
	defer rows.Close()
	remaining := maxBytes
	nextSeq, nextOffset := cursor.Sequence, cursor.Offset
	for rows.Next() {
		var f Fragment
		if err := rows.Scan(&f.Sequence, &f.Phase, &f.Stream, &f.Data, &f.ByteOffset, &f.StartLine); err != nil {
			return page, storageError()
		}
		offset := 0
		if f.Sequence == cursor.Sequence {
			offset = cursor.Offset
		}
		if offset > len(f.Data) {
			return page, failure("cursor_invalid", "run cursor offset exceeds its event")
		}
		f.StartLine += int64(bytes.Count(f.Data[:offset], []byte{'\n'}))
		f.ByteOffset += int64(offset)
		length := min(remaining, len(f.Data)-offset)
		fragment := f.Data[offset : offset+length]
		if offset+length < len(f.Data) {
			nextSeq, nextOffset = f.Sequence, offset+length
		} else {
			nextSeq, nextOffset = f.Sequence+1, 0
		}
		if length > 0 {
			f.Data = fragment
			page.Fragments = append(page.Fragments, f)
			remaining -= length
		}
		if remaining == 0 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return page, storageError()
	}
	if err := rows.Close(); err != nil {
		return page, storageError()
	}
	var more bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_events WHERE run_id=? AND seq>=? AND (?='merged' OR stream=?))", id, nextSeq, stream, stream).Scan(&more); err != nil {
		return page, storageError()
	}
	page.EndOfAvailable = !more
	page.EOF = !more && row.Result.CompletedAt != nil
	if !page.EOF {
		next := s.cursor(id, stream, nextSeq, nextOffset)
		page.NextCursor = &next
	}
	return page, nil
}

// CommittedCursor locates the byte immediately after an acknowledged prefix.
func (s *Store) CommittedCursor(run string, lastSequence uint64) *string {
	if lastSequence == 0 {
		return nil
	}
	cursor := s.cursor(run, "merged", lastSequence+1, 0)
	return &cursor
}
