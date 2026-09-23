package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
)

const maxSessionNameRunes = 120 // Shared by custom session names and project labels.

// RenameOptions describes one explicit local name operation. Clear and Name are
// mutually exclusive; an empty Name without Clear is invalid.
type RenameOptions struct {
	Name  string
	Clear bool
}

// EffectiveTitle resolves the one display title for a session without losing
// the imported title or capy-owned name state. Clear tombstones deliberately
// fall back to the latest imported title.
func (s Session) EffectiveTitle() string {
	return effectiveTitle(s.Title, s.Name)
}

func effectiveTitle(imported string, name *SessionName) string {
	if name != nil && name.CustomTitle != nil {
		return *name.CustomTitle
	}
	return imported
}

// ContainsFold reports whether s contains substr under Unicode simple
// lowercasing (strings.ToLower on both sides — NOT full Unicode case folding:
// e.g. Turkish dotless-i and Greek final sigma keep their simple mappings).
// It is the shared session-name matcher behind `vault list --name` and the TUI
// session filter — SQLite's lower()/NOCASE folds ASCII only, which would
// silently mismatch the non-ASCII names the rename contract allows. Both
// operands are literals: no wildcard or FTS syntax is interpreted.
func ContainsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// NormalizeSessionName applies the shared CLI/TUI storage contract in order:
// trim, redact recognized secrets, reject empty/invalid/control-bearing values,
// then enforce the Unicode code-point limit.
func NormalizeSessionName(name string) (string, error) {
	return normalizeSessionLabel(name, "session name")
}

// normalizeSessionLabel keeps title and project normalization identical while
// preserving field-specific validation errors.
func normalizeSessionLabel(name, field string) (string, error) {
	name = strings.TrimSpace(name)
	name = sanitize.StripSecrets(name)
	if name == "" {
		return "", fmt.Errorf("%s must not be empty; use clear instead", field)
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("%s must be valid utf-8", field)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%s must not contain control characters", field)
		}
	}
	if utf8.RuneCountInString(name) > maxSessionNameRunes {
		return "", fmt.Errorf("%s must not exceed %d characters", field, maxSessionNameRunes)
	}
	return name, nil
}

// RenameSession sets or explicitly clears the capy-owned name for the session
// matching prefix. Prefix resolution and the upsert share one immediate
// transaction, so concurrent delete/rename operations cannot leave an orphan.
func (s *VaultStore) RenameSession(ctx context.Context, prefix string, opts RenameOptions) (*Session, error) {
	return s.renameSessionAt(ctx, prefix, opts, time.Now().UTC(), MachineID())
}

// renameSessionAt is the deterministic seam used by tests for clock rollback
// and concurrent monotonicity. Production callers use RenameSession.
func (s *VaultStore) renameSessionAt(
	ctx context.Context,
	prefix string,
	opts RenameOptions,
	now time.Time,
	machineID string,
) (*Session, error) {
	pattern, err := sessionIDPrefixPattern(prefix)
	if err != nil {
		return nil, err
	}

	var customTitle *string
	if opts.Clear {
		if opts.Name != "" {
			return nil, errors.New("session name and clear are mutually exclusive")
		}
	} else {
		normalized, err := NormalizeSessionName(opts.Name)
		if err != nil {
			return nil, err
		}
		customTitle = &normalized
	}

	db, err := s.getDB(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx,
		`SELECT `+sessionMetaColumns+sessionMetaJoin+` WHERE s.uuid LIKE ? ESCAPE '\' ORDER BY s.end_time DESC`,
		pattern,
	)
	if err != nil {
		return nil, fmt.Errorf("querying sessions: %w", err)
	}

	// rows is closed explicitly, not deferred: the cursor must be released
	// before the upsert below runs on the same transaction (an open *sql.Rows
	// pins the tx's connection), and a deferred Close would only run after it.
	// The read-only paths elsewhere in this package use `defer rows.Close()`.
	var matches []Session
	for rows.Next() {
		var sess Session
		if err := scanSessionMeta(rows, &sess, nil); err != nil {
			_ = rows.Close()
			return nil, err
		}
		matches = append(matches, sess)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterating sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("closing session rows: %w", err)
	}

	switch len(matches) {
	case 0:
		return nil, ErrSessionNotFound
	case 1:
		// Continue below.
	default:
		return nil, &AmbiguousUUIDError{Prefix: prefix, Candidates: matches}
	}

	sess := matches[0]
	renamedAtNS := now.UnixNano()
	if sess.Name != nil && renamedAtNS <= sess.Name.RenamedAtNS {
		if sess.Name.RenamedAtNS == math.MaxInt64 {
			return nil, errors.New("session rename timestamp overflow")
		}
		renamedAtNS = sess.Name.RenamedAtNS + 1
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO vault_session_names (session_uuid, custom_title, renamed_at_ns, machine_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_uuid) DO UPDATE SET
			custom_title = excluded.custom_title,
			renamed_at_ns = excluded.renamed_at_ns,
			machine_id = excluded.machine_id`,
		sess.UUID, pointerValue(customTitle), renamedAtNS, machineID,
	); err != nil {
		return nil, fmt.Errorf("writing session name: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing session rename: %w", err)
	}

	sess.Name = &SessionName{CustomTitle: customTitle, RenamedAtNS: renamedAtNS, MachineID: machineID}
	return &sess, nil
}

func pointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// sessionNameSupersedes reports whether merged source name state src replaces
// the destination's stored state dest, under cross-vault reconciliation's total
// order: compare (renamed_at_ns, machine_id) lexicographically; equal tuples —
// possible because machine identity can be duplicated via CAPY_MACHINE_ID or a
// dotfile-synced machine-id file — fall to the deterministic value tie-break,
// where a non-null title beats a null clear tombstone and two non-null titles
// compare bytewise with the greater winning. A nil dest (the session was never
// named there) loses to any source state. Identical states never supersede, so
// re-merging is a no-op and every vault presented with the same states
// converges on the same winner regardless of merge direction.
func sessionNameSupersedes(src SessionName, dest *SessionName) bool {
	if dest == nil {
		return true
	}
	if src.RenamedAtNS != dest.RenamedAtNS {
		return src.RenamedAtNS > dest.RenamedAtNS
	}
	if src.MachineID != dest.MachineID {
		return src.MachineID > dest.MachineID
	}
	switch {
	case src.CustomTitle == nil:
		// Equal tuple: a source tombstone never beats a destination non-null
		// title, and against a destination tombstone the states are identical.
		return false
	case dest.CustomTitle == nil:
		return true
	default:
		return *src.CustomTitle > *dest.CustomTitle
	}
}

// reconcileSessionNameTx upserts src for uuid iff it supersedes the currently
// stored state, re-reading that state inside tx so the decision and the write
// are atomic — a local rename committing between a caller's snapshot read and
// this write cannot be clobbered by a stale decision. A winning tuple is
// written VERBATIM: the monotonic max(now, stored+1) bump belongs to local
// rename/clear operations only, and re-stamping here would break idempotence
// and cross-vault convergence. Returns whether a write occurred.
func reconcileSessionNameTx(ctx context.Context, tx *sql.Tx, uuid string, src SessionName) (bool, error) {
	var (
		customTitle sql.NullString
		renamedAtNS int64
		machineID   string
		dest        *SessionName
	)
	err := tx.QueryRowContext(ctx,
		`SELECT custom_title, renamed_at_ns, machine_id FROM vault_session_names WHERE session_uuid = ?`,
		uuid).Scan(&customTitle, &renamedAtNS, &machineID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No row: dest stays nil and any source state supersedes.
	case err != nil:
		return false, fmt.Errorf("reading session name state: %w", err)
	default:
		dest = &SessionName{
			CustomTitle: nullStringPointer(customTitle),
			RenamedAtNS: renamedAtNS,
			MachineID:   machineID,
		}
	}
	if !sessionNameSupersedes(src, dest) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO vault_session_names (session_uuid, custom_title, renamed_at_ns, machine_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_uuid) DO UPDATE SET
			custom_title = excluded.custom_title,
			renamed_at_ns = excluded.renamed_at_ns,
			machine_id = excluded.machine_id`,
		uuid, pointerValue(src.CustomTitle), src.RenamedAtNS, src.MachineID,
	); err != nil {
		return false, fmt.Errorf("writing session name state: %w", err)
	}
	return true, nil
}
