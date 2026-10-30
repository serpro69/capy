package runstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

// recoveryAdmin owns fresh exclusive descriptors. It never attaches a Store,
// publishes generations, converts a shared flock, checkpoints or maintains.
type recoveryAdmin struct {
	db          *sql.DB
	files       []*os.File // lifecycle, registry, slots, database; close in reverse
	owner, path string
	id, binding string
	policy      Policy
}

func openRecoveryAdmin(ctx context.Context, path, key string) (_ *recoveryAdmin, retErr error) {
	if key == "" {
		return nil, failure("credential_unavailable", "restore the selected matching credential; offline recovery tried no fallback key")
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != "runs.db" || filepath.Base(filepath.Dir(path)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(path))) != ".capy" {
		return nil, storageError()
	}
	a := &recoveryAdmin{path: path, owner: filepath.Dir(filepath.Dir(filepath.Dir(path)))}
	if physical, err := filepath.EvalSymlinks(a.owner); err != nil || physical != a.owner {
		return nil, storageError()
	}
	if exists, err := inspectStore(path); err != nil || !exists {
		if err != nil {
			return nil, err
		}
		return nil, failure("run_unavailable", "run store does not exist; offline recovery creates nothing")
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, a.close())
		}
	}()
	root := filepath.Dir(path)
	names := []string{filepath.Join(root, "lifecycle.lock"), filepath.Join(root, "recovery.lock")}
	for slot := 0; slot < OwnerSlots; slot++ {
		names = append(names, filepath.Join(root, "locks", fmt.Sprintf("owner-%02d.lock", slot)))
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := openRegular(name, false)
		if err != nil {
			return nil, failure("recovery_inconsistent", "a permanent recovery lease is missing or unsafe; restore its provenance before offline recovery")
		}
		a.files = append(a.files, f)
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			if !errors.Is(err, unix.EWOULDBLOCK) {
				return nil, failure("storage_unavailable", "cannot acquire the permanent recovery leases; check filesystem locking support and permissions")
			}
			return nil, failure("storage_busy", "offline recovery requires exclusive leases; stop sessions, readers and helpers, then retry from an ordinary terminal; do not run recovery inside capy_execute")
		}
	}
	// Retain the selected database inode too. SQLite has its own descriptor;
	// revalidation before and after the snapshot detects replacement by path.
	f, err := openRegular(path, false)
	if err != nil {
		return nil, storageError()
	}
	a.files = append(a.files, f)
	a.binding, err = a.targetBinding()
	if err != nil {
		return nil, err
	}
	options, err := inspectionSQLiteOptions()
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path + "-wal"); errors.Is(err, os.ErrNotExist) {
		if journal, err := os.Lstat(path + "-journal"); err == nil && journal.Size() != 0 || err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, failure("recovery_inconsistent", "retained rollback journal prevents a nonmutating plan; preserve history and repair with a compatible reader")
		}
		options += "&immutable=1"
	} else if err != nil {
		return nil, storageError()
	}
	if exists, err := inspectStore(path); err != nil || !exists {
		return nil, storageError()
	}
	a.db, err = sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+options, path, "selected run credential")
	if err != nil {
		return nil, openError(err, path)
	}
	a.db.SetMaxOpenConns(1)
	a.db.SetMaxIdleConns(1)
	var version int
	if err := a.db.QueryRowContext(ctx, "SELECT version,CASE WHEN length(store_id)=36 THEN store_id ELSE NULL END FROM run_meta WHERE singleton=1").Scan(&version, &a.id); err != nil || uuid.Validate(a.id) != nil {
		return nil, recoveryInconsistent("store identity")
	}
	if version == 8 || version == 9 {
		return nil, failure("upgrade_required", "inspect this owner, then use capy runs upgrade from an ordinary terminal; legacy history cannot produce a current recovery plan")
	}
	if version != schemaVersion {
		return nil, failure("schema_mismatch", "use a compatible capy binary; recovery never upgrades an unsupported schema")
	}
	a.policy, err = readPolicy(ctx, a.db)
	if err != nil {
		return nil, err
	}
	if err := a.verifyTarget(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *recoveryAdmin) close() error {
	var err error
	if a.db != nil {
		err = a.db.Close()
	}
	for i := len(a.files) - 1; i >= 0; i-- {
		err = errors.Join(err, a.files[i].Close())
	}
	return err
}

// enableWrites retains all administrative leases while replacing only the
// read-only connection. mode=rw cannot initialize a missing store. No ordinary
// Store attachment, policy publication or close-time maintenance is involved.
func (a *recoveryAdmin) enableWrites(ctx context.Context, key string) error {
	if err := a.verifyTarget(); err != nil {
		return err
	}
	if err := a.db.Close(); err != nil {
		return storageError()
	}
	a.db = nil
	db, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(a.path, key)+"&mode=rw&_foreign_keys=on&_busy_timeout=100", a.path, "selected run credential")
	if err != nil {
		return openError(err, a.path)
	}
	a.db = db
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var id string
	var version int
	if err := db.QueryRowContext(ctx, "SELECT store_id,version FROM run_meta WHERE singleton=1").Scan(&id, &version); err != nil || id != a.id || version != schemaVersion {
		return staleRecoveryPlan()
	}
	return a.verifyTarget()
}

// Only path and device/inode identities participate, never mtimes, sizes or
// observation times. Cooperating clients remain excluded until handles close.
func (a *recoveryAdmin) targetBinding() (string, error) {
	if physical, err := filepath.EvalSymlinks(a.owner); err != nil || physical != a.owner {
		return "", staleRecoveryPlan()
	}
	h := sha256.New()
	encoder := json.NewEncoder(h)
	if err := encoder.Encode(a.owner); err != nil {
		return "", storageError()
	}
	for _, f := range a.files {
		got, err := f.Stat()
		if err != nil {
			return "", staleRecoveryPlan()
		}
		current, err := os.Lstat(f.Name())
		if err != nil || !safeRegular(current) || !os.SameFile(got, current) {
			return "", staleRecoveryPlan()
		}
		stat, ok := got.Sys().(*syscall.Stat_t)
		if !ok {
			return "", storageError()
		}
		if err := encoder.Encode([]string{f.Name(), fmt.Sprint(stat.Dev), fmt.Sprint(stat.Ino)}); err != nil {
			return "", storageError()
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *recoveryAdmin) verifyTarget() error {
	binding, err := a.targetBinding()
	if err != nil {
		return err
	}
	if binding != a.binding {
		return staleRecoveryPlan()
	}
	return nil
}

func staleRecoveryPlan() error {
	return failure("recovery_plan_stale", "recovery target or evidence changed; inspect and review a fresh capy runs recover plan without the old cursor")
}

// The caller supplies a fixed category, never an identifier or backend error.
func recoveryInconsistent(category string) error {
	return failure("recovery_inconsistent", "inconsistent "+category+"; preserve diagnostics and repair this evidence separately; confirmation cannot override it")
}
