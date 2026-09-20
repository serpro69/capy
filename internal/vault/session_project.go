package vault

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// SessionProject is capy-owned project state. A nil *SessionProject means never
// assigned; a non-nil state with CustomProject == nil is a clear tombstone.
type SessionProject struct {
	CustomProject *string
	UpdatedAtNS   int64
	MachineID     string
}

// ProjectOptions describes one local project edit. Name and Clear are mutually
// exclusive; an empty Name without Clear is invalid.
type ProjectOptions struct {
	Name  string
	Clear bool
}

// EffectiveProject returns the custom label when set, otherwise the latest
// imported project path. It never normalizes a label as a filesystem path.
func (s Session) EffectiveProject() string {
	if s.ProjectOverride != nil && s.ProjectOverride.CustomProject != nil {
		return *s.ProjectOverride.CustomProject
	}
	return s.ProjectPath
}

// NormalizeSessionProject applies the same normalization as session names:
// trim, redact secrets, reject empty/invalid/control-bearing values, then check
// the 120-code-point limit. Path-looking labels remain literal.
func NormalizeSessionProject(name string) (string, error) {
	return normalizeSessionLabel(name, "session project")
}

// SetSessionProject sets or explicitly clears the capy-owned project for the session
// matching prefix. Prefix resolution and the upsert share one immediate
// transaction, so concurrent delete/project-edit operations cannot leave an orphan.
func (s *VaultStore) SetSessionProject(ctx context.Context, prefix string, opts ProjectOptions) (*Session, error) {
	return s.setSessionProjectAt(ctx, prefix, opts, time.Now().UTC(), MachineID())
}

// setSessionProjectAt is the deterministic seam used by tests for clock rollback
// and concurrent monotonicity. Production callers use SetSessionProject.
func (s *VaultStore) setSessionProjectAt(
	ctx context.Context,
	prefix string,
	opts ProjectOptions,
	now time.Time,
	machineID string,
) (*Session, error) {
	pattern, err := sessionIDPrefixPattern(prefix)
	if err != nil {
		return nil, err
	}

	var customProject *string
	if opts.Clear {
		if opts.Name != "" {
			return nil, errors.New("session project and clear are mutually exclusive")
		}
	} else {
		normalized, err := NormalizeSessionProject(opts.Name)
		if err != nil {
			return nil, err
		}
		customProject = &normalized
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
	updatedAtNS := now.UnixNano()
	if sess.ProjectOverride != nil && updatedAtNS <= sess.ProjectOverride.UpdatedAtNS {
		if sess.ProjectOverride.UpdatedAtNS == math.MaxInt64 {
			return nil, errors.New("session project update timestamp overflow")
		}
		updatedAtNS = sess.ProjectOverride.UpdatedAtNS + 1
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO vault_session_projects (session_uuid, custom_project, updated_at_ns, machine_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_uuid) DO UPDATE SET
			custom_project = excluded.custom_project,
			updated_at_ns = excluded.updated_at_ns,
			machine_id = excluded.machine_id`,
		sess.UUID, pointerValue(customProject), updatedAtNS, machineID,
	); err != nil {
		return nil, fmt.Errorf("writing session project: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing session project update: %w", err)
	}

	sess.ProjectOverride = &SessionProject{CustomProject: customProject, UpdatedAtNS: updatedAtNS, MachineID: machineID}
	return &sess, nil
}
