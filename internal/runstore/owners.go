package runstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type Owner struct {
	ID    string
	Slot  int
	Lease *os.File
}

func (s *Store) OwnerLeasePath(slot int) string {
	return filepath.Join(filepath.Dir(s.path), "locks", fmt.Sprintf("owner-%02d.lock", slot))
}

// ClaimOwner reserves one permanent slot. This initial admission check fails
// closed on abandoned unfinished rows. Task 7 adds proven-state reconciliation
// and periodic all-slot recovery; no old PID is used to make this decision.
func (s *Store) ClaimOwner(ctx context.Context) (*Owner, error) {
	var chosen *Owner
	success := false
	defer func() {
		if !success && chosen != nil {
			chosen.Lease.Close()
		}
	}()
	for slot := 0; slot < OwnerSlots; slot++ {
		f, err := openRegular(s.OwnerLeasePath(slot), true)
		if err != nil {
			return nil, storageError()
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == unix.EWOULDBLOCK {
			f.Close()
			continue
		}
		if err != nil {
			f.Close()
			return nil, storageError()
		}
		var unfinished int
		err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM runs WHERE slot=? AND terminal=0", slot).Scan(&unfinished)
		if err != nil {
			f.Close()
			return nil, storageError()
		}
		if unfinished != 0 {
			f.Close()
			return nil, failure("cleanup_unverified", "a previous run has unverified cleanup; stop its execution groups before operator recovery")
		}
		if chosen == nil {
			chosen = &Owner{ID: uuid.NewString(), Slot: slot, Lease: f}
		} else {
			f.Close()
		}
	}
	if chosen == nil {
		return nil, failure("owner_slots_exhausted", "all run owner slots are occupied")
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO run_owners(slot,generation) VALUES(?,?) ON CONFLICT(slot) DO UPDATE SET generation=excluded.generation", chosen.Slot, chosen.ID)
	if err != nil {
		return nil, storageError()
	}
	success = true
	return chosen, nil
}

// GuardAdmission is conservative until Task 7 implements reconciliation. A
// released owner lock does not make an unfinished generation safe to reuse.
func (s *Store) GuardAdmission(ctx context.Context) error {
	var blocked bool
	if err := s.db.QueryRowContext(ctx, "SELECT admission_blocked FROM run_meta WHERE singleton=1").Scan(&blocked); err != nil {
		return storageError()
	}
	if blocked {
		return failure("cleanup_unverified", "run store admission is blocked by unverified process cleanup")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT slot FROM runs WHERE terminal=0")
	if err != nil {
		return storageError()
	}
	var slots []int
	for rows.Next() {
		var slot int
		if err := rows.Scan(&slot); err != nil {
			rows.Close()
			return storageError()
		}
		slots = append(slots, slot)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return storageError()
	}
	for _, slot := range slots {
		f, err := openRegular(s.OwnerLeasePath(slot), false)
		if err != nil {
			return storageError()
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		f.Close()
		if err == nil {
			return failure("cleanup_unverified", "an unfinished run lost its owner; cleanup must be verified before more execution")
		}
		if err != unix.EWOULDBLOCK {
			return storageError()
		}
	}
	return nil
}

func (s *Store) Quarantine(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE run_meta SET admission_blocked=1 WHERE singleton=1"); err != nil {
		return storageError()
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
