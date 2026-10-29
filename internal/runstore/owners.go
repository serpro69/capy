package runstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

type Owner struct {
	ID    string
	Slot  int
	Lease *OwnerLease
}

func (s *Store) OwnerLeasePath(slot int) string {
	return filepath.Join(filepath.Dir(s.path), "locks", fmt.Sprintf("owner-%02d.lock", slot))
}

// ClaimOwner scans every slot before selecting one, then reconciles the selected
// slot again under its retained exclusive lease before publishing a generation.
func (s *Store) ClaimOwner(ctx context.Context) (_ *Owner, err error) {
	defer func() {
		if err != nil {
			err = AdmissionError(err, s.owner)
		}
	}()
	release, err := s.retainLifecycle()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, release()) }()
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	registry, err := s.recoveryLease(ctx)
	if err != nil {
		return nil, err
	}
	defer registry.Close()
	if _, err := s.reconcile(ctx); err != nil {
		return nil, err
	}
	if err := s.checkQuarantine(ctx); err != nil {
		return nil, err
	}
	for slot := 0; slot < OwnerSlots; slot++ {
		f, err := s.tryOwnerLease(slot)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		owner, err := s.claimSlot(ctx, slot, f)
		if err != nil || owner == nil {
			f.Close()
		}
		if err != nil || owner != nil {
			return owner, err
		}
	}
	return nil, failure("owner_slots_exhausted", "all run owner slots are occupied or awaiting bounded recovery")
}

func (s *Store) claimSlot(ctx context.Context, slot int, lease *os.File) (*Owner, error) {
	report, err := s.reconcileSlot(ctx, slot)
	if err != nil {
		return nil, err
	}
	if report.DeferredSlots != 0 {
		return nil, nil
	}
	if err := s.checkQuarantine(ctx); err != nil {
		return nil, err
	}
	owner := &Owner{ID: uuid.NewString(), Slot: slot}
	// The registry lease stays held through publication. Admit rechecks recovery
	// and quarantine when this generation later requests execution.
	_, err = s.db.ExecContext(ctx, "INSERT INTO run_owners(slot,generation) VALUES(?,?) ON CONFLICT(slot) DO UPDATE SET generation=excluded.generation", slot, owner.ID)
	if err != nil {
		return nil, storageError()
	}
	a := s.leases
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return nil, os.ErrClosed
	}
	a.refs++
	owner.Lease = &OwnerLease{acquired: &ownerAcquisition{context: a, file: lease, generation: owner.ID, slot: slot, refs: 1}}
	return owner, nil
}

// GuardAdmission reconciles all free slots, including slots never selected by
// later low-concurrency owners. A free lock alone is never a cleanup proof.
func (s *Store) GuardAdmission(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			err = AdmissionError(err, s.owner)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	registry, err := s.recoveryLease(ctx)
	if err != nil {
		return err
	}
	defer registry.Close()
	if _, err := s.reconcile(ctx); err != nil {
		return err
	}
	return s.checkQuarantine(ctx)
}

func (s *Store) checkQuarantine(ctx context.Context) error { return s.checkQuarantineQuery(ctx, s.db) }

// Transactional admission rechecks use their existing connection. Exhausted
// recovery epochs also refuse new work, including while a live slot is held.
func (s *Store) checkQuarantineQuery(ctx context.Context, q runQuerier) error {
	var blocked bool
	if s.policy.SchemaVersion < 10 {
		if err := q.QueryRowContext(ctx, "SELECT admission_blocked FROM run_meta WHERE singleton=1").Scan(&blocked); err != nil {
			return storageError()
		}
	} else {
		var epoch int64
		if err := q.QueryRowContext(ctx, "SELECT admission_blocked OR EXISTS(SELECT 1 FROM run_incidents WHERE receipt_id IS NULL),recovery_revision FROM run_meta WHERE singleton=1").Scan(&blocked, &epoch); err != nil {
			return storageError()
		}
		if epoch == math.MaxInt64 {
			return s.admissionFailure(ctx, q, "recovery_revision_exhausted", "recovery revision exhausted")
		}
	}
	if blocked {
		return s.admissionFailure(ctx, q, "cleanup_unverified", "unverified cleanup")
	}
	return nil
}

func (s *Store) tryOwnerLease(slot int) (*os.File, error) {
	f, err := openRegular(s.OwnerLeasePath(slot), false)
	if err != nil {
		return nil, storageError()
	}
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return nil, nil
		}
		return nil, storageError()
	}
	return f, nil
}

// Quarantine preserves the existing live-owner safety latch without depending
// on incident quota or an index recount. Callers with admitted identity use
// QuarantineRun; reconciliation attributes legacy markers when necessary.
func (s *Store) Quarantine(ctx context.Context) error {
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	changed, err := quarantineMarkers(ctx, tx, nil, "")
	if err != nil {
		return err
	}
	if changed {
		if err := advanceRecoveryRevision(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return metadataFailure(ctx, "persisting quarantine marker")
	}
	return nil
}

// ValidateLeaseBinding binds inherited kernel locks to this admitted row/store,
// rather than accepting valid locks on unrelated inodes supplied by a caller.
func (s *Store) ValidateLeaseBinding(slot int, owner, lifecycle *os.File) error {
	if slot < 0 || slot >= OwnerSlots || owner == nil || lifecycle == nil {
		return storageError()
	}
	for _, pair := range []struct {
		file *os.File
		path string
	}{
		{owner, s.OwnerLeasePath(slot)}, {lifecycle, filepath.Join(filepath.Dir(s.path), "lifecycle.lock")},
	} {
		want, err := os.Lstat(pair.path)
		if err != nil || !safeRegular(want) {
			return storageError()
		}
		got, err := pair.file.Stat()
		if err != nil || !os.SameFile(want, got) {
			return failure("storage_unavailable", "supervisor leases do not belong to the admitted run store")
		}
	}
	return nil
}
