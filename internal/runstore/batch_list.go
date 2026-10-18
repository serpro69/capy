package runstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/sanitize"
)

type batchListCursor struct {
	Version   int      `json:"v"`
	Store     string   `json:"store"`
	Operation string   `json:"operation"`
	Batch     string   `json:"batch"`
	Ordinal   int      `json:"ordinal"`
	States    []string `json:"states"`
}

func (s *Store) listBatch(ctx context.Context, o ListOptions) (ListPage, error) {
	page := ListPage{Version: 1, Kind: "query", BatchID: o.BatchID, Entries: []Summary{}}
	batch, _, _, err := s.batchMetadata(ctx, o.BatchID)
	if err != nil {
		return page, err
	}
	if o.Limit < 1 || o.Limit > 64 {
		return page, failure("invalid_argument", "limit must be between 1 and 64")
	}
	states, err := validateStates(o.States)
	if err != nil {
		return page, err
	}
	c := batchListCursor{Version: 1, Store: s.id, Operation: "batch_list", Batch: o.BatchID, Ordinal: -1, States: states}
	if o.Cursor != "" {
		if len(o.Cursor) > 1024 {
			return page, failure("cursor_invalid", "invalid batch cursor")
		}
		data, err := base64.RawURLEncoding.DecodeString(o.Cursor)
		if err != nil {
			return page, failure("cursor_invalid", "invalid batch cursor")
		}
		c = batchListCursor{}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Operation != "batch_list" || c.Batch != o.BatchID || c.Ordinal < 0 || c.Ordinal >= 256 {
			return page, failure("cursor_invalid", "invalid batch cursor")
		}
		if c.Store != s.id {
			return page, failure("cursor_stale", "batch cursor belongs to another store")
		}
		embedded, err := validateStates(c.States)
		if err != nil || !slices.Equal(embedded, c.States) || len(states) > 0 && !slices.Equal(states, embedded) {
			return page, failure("cursor_invalid", "batch cursor filters do not match")
		}
		states = embedded
	}
	query := `SELECT c.ordinal,c.run_id,c.label,c.result,b.created_at,c.terminal,
 EXISTS(SELECT 1 FROM runs r WHERE r.id=c.run_id AND r.evicting=0 AND (r.expires_at IS NULL OR r.expires_at>?))
 FROM run_batch_children c JOIN run_batches b ON b.id=c.batch_id WHERE c.batch_id=? AND c.ordinal>?`
	args := []any{time.Now().UTC().UnixMilli(), o.BatchID, c.Ordinal}
	if len(states) > 0 {
		query += ` AND json_extract(CAST(c.result AS TEXT),'$.state') IN (` + strings.TrimSuffix(strings.Repeat("?,", len(states)), ",") + `)`
		for _, state := range states {
			args = append(args, state)
		}
	}
	query += " ORDER BY c.ordinal LIMIT ?"
	args = append(args, o.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, storageError()
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Entries) == o.Limit {
			data, err := json.Marshal(c)
			if err != nil {
				return page, storageError()
			}
			next := base64.RawURLEncoding.EncodeToString(data)
			page.NextCursor = &next
			break
		}
		var entry Summary
		var result Result
		var encoded []byte
		var created int64
		var ordinal int
		var terminal bool
		var label string
		if rows.Scan(&ordinal, &entry.RunID, &label, &encoded, &created, &terminal, &entry.OutputAvailable) != nil || json.Unmarshal(encoded, &result) != nil {
			return page, storageError()
		}
		if batch.Error != nil && !terminal {
			markUnverified(&result)
		}
		entry.BatchID = o.BatchID
		entry.Ordinal = &ordinal
		entry.Label = sanitize.StripSecrets(label)
		if len(entry.Label) > 160 {
			entry.Label = entry.Label[:160]
		} // shortened below on a UTF-8 boundary
		entry.Label = strings.ToValidUTF8(entry.Label, "")
		entry.OwnerID = result.OwnerID
		entry.OwnerType = result.OwnerType
		entry.State = result.State
		entry.ExitCode = result.ExitCode
		entry.Signal = result.Signal
		entry.Reason = result.Reason
		entry.CaptureState = result.Capture.State
		entry.CapturedBytes = result.Capture.CapturedBytes
		entry.CreatedAt = time.Unix(0, created).UTC()
		entry.Error = result.Error
		page.Entries = append(page.Entries, entry)
		c.Ordinal = ordinal
		c.States = states
	}
	if rows.Err() != nil {
		return page, storageError()
	}
	return page, nil
}
