package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// DeleteOptions selects how to resolve input IDs and whether to record deletions.
type DeleteOptions struct {
	ExactIDs bool // TUI selections are full identities, never prefixes.
	Remember bool
}

// DeleteTarget is the metadata reviewed for one exact archive.
type DeleteTarget struct {
	UUID, ParentUUID, Title, Project, OriginalPath string
	CustomProject                                  bool
	Platform                                       Platform
	MessageCount                                   int
	StartTime, EndTime                             time.Time
}

func deleteTarget(s Session) DeleteTarget {
	return DeleteTarget{
		UUID: s.UUID, ParentUUID: s.ParentUUID, Platform: s.Platform,
		Title: s.EffectiveTitle(), Project: s.EffectiveProject(), OriginalPath: s.ProjectPath,
		CustomProject: s.ProjectOverride != nil && s.ProjectOverride.CustomProject != nil,
		MessageCount:  s.MessageCount, StartTime: s.StartTime, EndTime: s.EndTime,
	}
}

// DeletePlan binds the reviewed identities, projects and policy to their store.
type DeletePlan struct {
	store            *VaultStore
	targets          []DeleteTarget
	remember         bool
	RetainedChildren []Session
}

func (p *DeletePlan) Targets() []DeleteTarget  { return slices.Clone(p.targets) }
func (p *DeletePlan) RemembersDeletions() bool { return p.remember }

// DeleteInputError identifies a bad argument without losing its position or candidates.
type DeleteInputError struct {
	ID  string
	Err error
}

func (e DeleteInputError) Error() string { return fmt.Sprintf("%q: %v", e.ID, e.Err) }
func (e DeleteInputError) Unwrap() error { return e.Err }

// DeleteInputErrors contains all input-specific failures in the order supplied.
type DeleteInputErrors []DeleteInputError

func (e DeleteInputErrors) Error() string {
	return errors.Join(e.Unwrap()...).Error()
}
func (e DeleteInputErrors) Unwrap() []error {
	errs := make([]error, len(e))
	for i := range e {
		errs[i] = e[i]
	}
	return errs
}

// PrepareDelete resolves the selection in one read snapshot, released before confirmation.
func (s *VaultStore) PrepareDelete(ctx context.Context, ids []string, opts DeleteOptions) (*DeletePlan, error) {
	if len(ids) == 0 {
		return nil, errors.New("no sessions selected")
	}
	// Invalid prefixes alone must not open or initialize the vault.
	var tx *sql.Tx
	plan := &DeletePlan{store: s, remember: opts.Remember}
	selected := make(map[string]bool)
	var invalid DeleteInputErrors
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		where, arg := "WHERE s.uuid = ?", id
		var err error
		if !opts.ExactIDs {
			where = `WHERE s.uuid LIKE ? ESCAPE '\' ORDER BY s.end_time DESC, s.uuid`
			arg, err = sessionIDPrefixPattern(id)
			if err != nil {
				invalid = append(invalid, DeleteInputError{id, err})
				continue
			}
		}
		if tx == nil {
			db, err := s.getDB(ctx)
			if err != nil {
				return nil, err
			}
			tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
		}
		matches, err := querySessionMetadata(ctx, tx, where, arg)
		if err != nil {
			return nil, err
		}
		switch len(matches) {
		case 0:
			invalid = append(invalid, DeleteInputError{id, ErrSessionNotFound})
		case 1:
			row := matches[0]
			if !selected[row.UUID] {
				selected[row.UUID] = true
				plan.targets = append(plan.targets, deleteTarget(row))
			}
		default:
			invalid = append(invalid, DeleteInputError{id, &AmbiguousUUIDError{id, matches}})
		}
	}
	if len(invalid) > 0 {
		return nil, invalid
	}
	for _, target := range plan.targets {
		children, err := childMetadata(ctx, tx, target.UUID)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if !selected[child.UUID] {
				plan.RetainedChildren = append(plan.RetainedChildren, child)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return plan, nil
}

// ErrStaleDeletePlan requires a fresh review before retrying.
var ErrStaleDeletePlan = errors.New("deletion preview is stale")

func deletionProject(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var project string
	err := tx.QueryRowContext(ctx, "SELECT "+effectiveProjectSQL+sessionMetaJoin+" WHERE s.uuid = ?", id).Scan(&project)
	return project, err
}

// ApplyDelete rechecks only exact reviewed identities under the writer lock.
func (s *VaultStore) ApplyDelete(ctx context.Context, p *DeletePlan) error {
	if p == nil || p.store != s || len(p.targets) == 0 {
		return errors.New("invalid deletion plan for this vault")
	}
	db, err := s.getDB(ctx)
	if err != nil {
		return err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, target := range p.targets {
		project, err := deletionProject(ctx, tx, target.UUID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: session %q no longer in vault", ErrStaleDeletePlan, target.UUID)
		}
		if err != nil {
			return err
		}
		if project != target.Project {
			return fmt.Errorf("%w: project changed for session %q", ErrStaleDeletePlan, target.UUID)
		}
	}
	for group := range slices.Chunk(p.targets, maxBatchSessions) {
		n, err := deleteArchiveGroup(ctx, tx, group, p.remember)
		if err != nil {
			return err
		}
		if n != int64(len(group)) {
			return fmt.Errorf("deleted %d of %d reviewed sessions in group", n, len(group))
		}
	}
	return tx.Commit()
}

func deleteArchiveGroup(ctx context.Context, tx *sql.Tx, targets []DeleteTarget, remember bool) (int64, error) {
	ids := make([]any, len(targets))
	for i, target := range targets {
		ids[i] = target.UUID
		if remember {
			if _, err := tx.ExecContext(ctx, insertDeletionSQL, target.UUID, target.Project); err != nil {
				return 0, fmt.Errorf("recording session deletion: %w", err)
			}
		}
	}
	if remember {
		if err := markMinReaderVersion(ctx, tx, readerVersionSessionDeletions); err != nil {
			return 0, err
		}
	}
	if err := deleteSessionIndexes(ctx, tx, ids); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM vault_sessions WHERE uuid IN ("+strings.Repeat("?,", len(ids)-1)+"?)", ids...)
	if err != nil {
		return 0, fmt.Errorf("delete sessions: %w", err)
	}
	return res.RowsAffected()
}
