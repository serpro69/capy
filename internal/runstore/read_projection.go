package runstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/sanitize"
)

type textCursor struct {
	Version   int    `json:"v"`
	Store     string `json:"store"`
	Run       string `json:"run"`
	View      string `json:"view"`
	Stream    string `json:"stream"`
	Redaction int    `json:"redaction"`
	Sequence  uint64 `json:"seq"`
	RawStart  int64  `json:"raw_start"`
	Offset    int    `json:"offset"`
}

func (s *Store) projectionCursor(run, stream string, seq uint64, start int64, offset int) string {
	b, _ := json.Marshal(textCursor{Version: 1, Store: s.id, Run: run, View: "projection", Stream: stream, Redaction: sanitize.StreamVersion, Sequence: seq, RawStart: start, Offset: offset})
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Store) decodeProjectionCursor(encoded, run, stream string) (textCursor, error) {
	c := textCursor{Version: 1, Store: s.id, Run: run, View: "projection", Stream: stream, Redaction: sanitize.StreamVersion, Sequence: 1}
	if encoded == "" {
		return c, nil
	}
	if len(encoded) > 1024 {
		return c, failure("cursor_invalid", "projection cursor exceeds its limit")
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return c, failure("cursor_invalid", "invalid projection cursor")
	}
	c = textCursor{}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.View != "projection" || c.Sequence == 0 || c.Sequence > 1<<62 || c.RawStart < 0 || c.Offset < 0 || c.Offset > 65536 {
		return c, failure("cursor_invalid", "invalid projection cursor")
	}
	if c.Store != s.id || c.Redaction != sanitize.StreamVersion {
		return c, failure("cursor_stale", "projection cursor belongs to another store or redaction version")
	}
	if c.Run != run || c.Stream != stream {
		return c, failure("cursor_invalid", "projection cursor selector does not match")
	}
	return c, nil
}

func (s *Store) ReadProjection(ctx context.Context, id, stream, encoded string, maxBytes int) (ReadPage, error) {
	page := ReadPage{Version: 1, Kind: "query", RunID: id, Encoding: "text", Fragments: []Fragment{}}
	if stream != "stdout" && stream != "stderr" && stream != "merged" {
		return page, failure("invalid_argument", "stream must be stdout, stderr or merged")
	}
	if maxBytes < 1 || maxBytes > 65536 {
		return page, failure("invalid_argument", "max_bytes must be between 1 and 65536")
	}
	cursor, err := s.decodeProjectionCursor(encoded, id, stream)
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
	if row.Result.Redaction.State == "unavailable" || row.Result.Redaction.Version != sanitize.StreamVersion {
		return page, failure("redaction_unavailable", "sanitized projection is unavailable; raw bytes require an explicit raw request")
	}
	var barrier sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(e.seq) FROM run_events e JOIN run_streams s ON s.run_id=e.run_id AND s.stream=e.stream
WHERE e.run_id=? AND e.byte_offset+length(e.data)>s.covered AND (?='merged' OR e.stream=?)`, id, stream, stream).Scan(&barrier); err != nil {
		return page, storageError()
	}
	limitSeq := int64(1 << 62)
	if barrier.Valid {
		limitSeq = barrier.Int64
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,phase,stream,raw_start,raw_end,start_line,text,redacted FROM run_projection
WHERE run_id=? AND (?='merged' OR stream=?) AND (seq>? OR (seq=? AND raw_end>?)) AND seq<=?
ORDER BY seq,raw_start LIMIT 64`, id, stream, stream, cursor.Sequence, cursor.Sequence, cursor.RawStart, limitSeq)
	if err != nil {
		return page, storageError()
	}
	defer rows.Close()
	remaining := maxBytes
	nextSeq, nextStart, nextOffset := cursor.Sequence, cursor.RawStart, cursor.Offset
	for rows.Next() {
		var f Fragment
		var end int64
		var text string
		if err := rows.Scan(&f.Sequence, &f.Phase, &f.Stream, &f.ByteOffset, &end, &f.StartLine, &text, &f.Redacted); err != nil {
			return page, storageError()
		}
		offset := 0
		if f.Sequence == cursor.Sequence && f.ByteOffset <= cursor.RawStart && end > cursor.RawStart {
			offset = cursor.Offset
			if offset == 0 && !f.Redacted {
				offset = int(cursor.RawStart - f.ByteOffset)
			}
		}
		if offset > len(text) || (offset < len(text) && !utf8.RuneStart(text[offset])) {
			return page, failure("cursor_invalid", "projection cursor offset is invalid")
		}
		n := min(remaining, len(text)-offset)
		for n > 0 && offset+n < len(text) && !utf8.RuneStart(text[offset+n]) {
			n--
		}
		if n == 0 && offset < len(text) {
			if len(page.Fragments) > 0 {
				break
			}
			// One Unicode code point is atomic even for a one-byte page request.
			_, n = utf8.DecodeRuneInString(text[offset:])
		}
		nextSeq, nextStart, nextOffset = f.Sequence, f.ByteOffset, offset+n
		if offset+n == len(text) {
			nextStart = end
			nextOffset = 0
		}
		if n > 0 {
			f.Text = text[offset : offset+n]
			f.RawEnd = end
			f.Continued = offset > 0 || offset+n < len(text) || !strings.HasSuffix(f.Text, "\n")
			if !f.Redacted {
				f.ByteOffset += int64(offset)
				f.StartLine += int64(strings.Count(text[:offset], "\n"))
			}
			page.Fragments = append(page.Fragments, f)
			remaining -= n
		}
		if remaining <= 0 || nextOffset > 0 {
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
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_projection WHERE run_id=? AND (?='merged' OR stream=?)
AND (seq>? OR (seq=? AND raw_end>?)) AND seq<=?)`, id, stream, stream, nextSeq, nextSeq, nextStart, limitSeq).Scan(&more); err != nil {
		return page, storageError()
	}
	page.EndOfAvailable = !more
	page.RedactionPending = barrier.Valid && !more
	page.EOF = !more && !barrier.Valid && row.Result.CompletedAt != nil
	if !page.EOF {
		next := s.projectionCursor(id, stream, nextSeq, nextStart, nextOffset)
		page.NextCursor = &next
	}
	return page, nil
}
