// Package fileio provides bounded content reads shared by knowledge ingestion
// and the store, without coupling their orchestration.
package fileio

import (
	"fmt"
	"io"
	"math"
)

// TooLargeError rejects a file before any of its partial content is indexed.
// Size can be the descriptor's stat size or just the limit plus one probe byte.
type TooLargeError struct {
	Size  int64
	Limit int
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("file too large: %d bytes exceeds %d byte limit (configure store.max_source_bytes to adjust)", e.Size, e.Limit)
}

// ReadAll reads at most limit + 1 bytes, rejecting growth beyond the limit.
// Callers must admit a regular file and check its stat size before calling.
// Returned bytes on failure are only for accounting; never index partial content.
func ReadAll(r io.Reader, limit int) ([]byte, error) {
	if limit <= 0 || limit == math.MaxInt {
		return nil, fmt.Errorf("invalid file read limit: must be positive with room for a detection byte")
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return data, err
	}
	if len(data) > limit {
		return data, &TooLargeError{Size: int64(len(data)), Limit: limit}
	}
	return data, nil
}
