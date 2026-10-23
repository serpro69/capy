package runstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
)

type ListOptions struct {
	BatchID string
	States  []string
	Limit   int
	Cursor  string
}
type Summary struct {
	BatchID         string    `json:"batch_id,omitempty"`
	Ordinal         *int      `json:"ordinal,omitempty"`
	Label           string    `json:"label,omitempty"`
	Reason          *string   `json:"reason,omitempty"`
	Error           *Error    `json:"error,omitempty"`
	RunID           string    `json:"run_id"`
	OwnerID         string    `json:"owner_id"`
	OwnerType       string    `json:"owner_type"`
	State           string    `json:"state"`
	ExitCode        *int      `json:"exit_code"`
	Signal          *int      `json:"signal"`
	CaptureState    string    `json:"capture_state"`
	CapturedBytes   int64     `json:"captured_bytes"`
	CreatedAt       time.Time `json:"created_at"`
	OutputAvailable bool      `json:"output_available"`
}
type ListPage struct {
	BatchID    string    `json:"batch_id,omitempty"`
	Version    int       `json:"version"`
	Kind       string    `json:"kind"`
	Entries    []Summary `json:"entries"`
	NextCursor *string   `json:"next_cursor"`
}
type listCursor struct {
	Version   int      `json:"v"`
	Store     string   `json:"store"`
	Operation string   `json:"operation"`
	Cutoff    int64    `json:"cutoff"`
	Created   int64    `json:"created"`
	ID        string   `json:"id"`
	States    []string `json:"states"`
}

func validateStates(states []string) ([]string, error) {
	if len(states) > 11 {
		return nil, failure("invalid_argument", "too many state filters")
	}
	states = slices.Clone(states)
	slices.Sort(states)
	states = slices.Compact(states)
	for _, state := range states {
		switch state {
		case "not_started", "admitted", "queued", "running", "exited", "start_failed", "timed_out", "cancelled", "interrupted", "skipped":
		default:
			return nil, failure("invalid_argument", "unknown run state")
		}
	}
	return states, nil
}

func (s *Store) List(ctx context.Context, options ListOptions) (ListPage, error) {
	if options.BatchID != "" {
		return s.listBatch(ctx, options)
	}
	page := ListPage{Version: 1, Kind: "query", Entries: []Summary{}}
	if options.Limit < 1 || options.Limit > 64 {
		return page, failure("invalid_argument", "limit must be between 1 and 64")
	}
	states, err := validateStates(options.States)
	if err != nil {
		return page, err
	}
	var cutoff int64
	if err := s.db.QueryRowContext(ctx, "SELECT last_created FROM run_meta WHERE singleton=1").Scan(&cutoff); err != nil {
		return page, storageError()
	}
	cursor := listCursor{Version: 1, Store: s.id, Operation: "list", Cutoff: cutoff, States: states}
	if options.Cursor != "" {
		if len(options.Cursor) > 1024 {
			return page, failure("cursor_invalid", "list cursor exceeds its limit")
		}
		b, err := base64.RawURLEncoding.DecodeString(options.Cursor)
		if err != nil {
			return page, failure("cursor_invalid", "invalid list cursor")
		}
		cursor = listCursor{}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if d.Decode(&cursor) != nil || d.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Operation != "list" || cursor.Cutoff <= 0 || cursor.Created <= 0 || cursor.Created > cursor.Cutoff || uuid.Validate(cursor.ID) != nil {
			return page, failure("cursor_invalid", "invalid list cursor")
		}
		if cursor.Store != s.id {
			return page, failure("cursor_stale", "list cursor belongs to another store")
		}
		if cursor.Cutoff > cutoff {
			return page, failure("cursor_invalid", "list cursor cutoff exceeds this store's creation sequence")
		}
		embedded, err := validateStates(cursor.States)
		if err != nil || !slices.Equal(embedded, cursor.States) || len(states) > 0 && !slices.Equal(states, embedded) {
			return page, failure("cursor_invalid", "list cursor filters do not match")
		}
		states = embedded
	}
	query := `SELECT id,created_at,result,terminal,cleanup_scope,cleanup,
EXISTS(SELECT 1 FROM run_owners o WHERE o.slot=runs.slot AND o.generation=runs.generation AND o.cleanup_unverified=1),
(SELECT c.result FROM run_batch_children c WHERE c.run_id=runs.id),
(SELECT c.terminal FROM run_batch_children c WHERE c.run_id=runs.id)
FROM runs WHERE evicting=0 AND created_at<=? AND (created_at>? OR (created_at=? AND id>?))`
	args := []any{cursor.Cutoff, cursor.Created, cursor.Created, cursor.ID}
	if len(states) > 0 {
		query += ` AND json_extract(CAST(result AS TEXT),'$.state') IN (` + strings.TrimSuffix(strings.Repeat("?,", len(states)), ",") + `)`
		for _, state := range states {
			args = append(args, state)
		}
	}
	query += ` ORDER BY created_at,id LIMIT ?`
	args = append(args, options.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, storageError()
	}
	defer rows.Close()
	examined := 0
	more := false
	for rows.Next() {
		if examined == options.Limit {
			more = true
			break
		}
		var id string
		var created int64
		var encoded []byte
		var terminal, unverified bool
		var cleanup executor.CleanupEvidence
		var manifest []byte
		var manifestTerminal sql.NullBool
		if err := rows.Scan(&id, &created, &encoded, &terminal, &cleanup.Scope, &cleanup.State, &unverified, &manifest, &manifestTerminal); err != nil {
			return page, storageError()
		}
		var result Result
		if json.Unmarshal(encoded, &result) != nil {
			return page, storageError()
		}
		if err := validateTerminal(cleanup, result, terminal); err != nil {
			return page, err
		}
		if manifestTerminal.Valid {
			if err := validateManifestCopy(manifest, cleanup, terminal, manifestTerminal.Bool); err != nil {
				return page, err
			}
		}
		unverified = unverified || (!terminal && result.CompletedAt != nil)
		cursor.Created, cursor.ID = created, id
		examined++
		if unverified && !terminal {
			markUnverified(&result)
		}
		if result.ExpiresAt != nil && !time.Now().Before(*result.ExpiresAt) {
			continue
		}
		var recoveryError *Error
		if unverified && !terminal {
			recoveryError = result.Error
		}
		page.Entries = append(page.Entries, Summary{BatchID: result.BatchID, RunID: id, OwnerID: result.OwnerID, OwnerType: result.OwnerType, State: result.State, ExitCode: result.ExitCode, Signal: result.Signal, CaptureState: result.Capture.State, CapturedBytes: result.Capture.CapturedBytes, CreatedAt: time.Unix(0, created).UTC(), OutputAvailable: true, Error: recoveryError})
	}
	if err := rows.Err(); err != nil {
		return page, storageError()
	}
	if more {
		cursor.States = states
		b, err := json.Marshal(cursor)
		if err != nil {
			return page, storageError()
		}
		next := base64.RawURLEncoding.EncodeToString(b)
		page.NextCursor = &next
	}
	return page, nil
}
