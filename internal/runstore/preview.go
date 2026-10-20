package runstore

import (
	"context"
	"unicode/utf8"
)

// ReadProjectionTail selects a bounded suffix of finalized, sanitized coverage
// in one stream. It never reads raw text or creates a search index.
func (s *Store) ReadProjectionTail(ctx context.Context, id, stream string, maxBytes int) (ReadPage, error) {
	if (stream != "stdout" && stream != "stderr") || maxBytes < 1 || maxBytes > 65536 {
		return ReadPage{}, failure("invalid_argument", "tail preview requires a single stream and a bounded size")
	}
	// The primary key (run_id,stream,raw_start) supports this reverse scan.
	rows, err := s.db.QueryContext(ctx, `SELECT seq,raw_start,text FROM run_projection
WHERE run_id=? AND stream=? ORDER BY raw_start DESC LIMIT 64`, id, stream)
	if err != nil {
		return ReadPage{}, storageError()
	}
	defer rows.Close()
	var sequence uint64 = 1
	var start int64
	offset, available := 0, 0
	for rows.Next() {
		var text string
		if err := rows.Scan(&sequence, &start, &text); err != nil {
			return ReadPage{}, storageError()
		}
		offset = max(0, len(text)-(maxBytes-available))
		for offset < len(text) && !utf8.RuneStart(text[offset]) {
			offset++
		}
		available += len(text)
		if available >= maxBytes {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return ReadPage{}, storageError()
	}
	if err := rows.Close(); err != nil {
		return ReadPage{}, storageError()
	}
	// Delegate expiry, projection-version and pending-coverage checks to the
	// ordinary reader; a suffix selection never overrides those boundaries.
	cursor := s.projectionCursor(id, stream, sequence, start, offset)
	return s.ReadProjection(ctx, id, stream, cursor, maxBytes)
}
