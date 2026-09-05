package vault

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
)

const maxSessionNameRunes = 120

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
	name = strings.TrimSpace(name)
	name = sanitize.StripSecrets(name)
	if name == "" {
		return "", fmt.Errorf("session name must not be empty; use clear instead")
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("session name must be valid utf-8")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("session name must not contain control characters")
		}
	}
	if utf8.RuneCountInString(name) > maxSessionNameRunes {
		return "", fmt.Errorf("session name must not exceed %d characters", maxSessionNameRunes)
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
			return nil, fmt.Errorf("session name and clear are mutually exclusive")
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
			return nil, fmt.Errorf("session rename timestamp overflow")
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
