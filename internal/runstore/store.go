package runstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

const schemaVersion = 9
const OwnerSlots = 64
const WriteTimeout = 250 * time.Millisecond

type Store struct {
	db                   *sql.DB
	path, key, id, owner string
	lifecycle            *os.File
	leases               *leaseAcquisition
	policy               Policy
	closeOnce            sync.Once
	closeErr             error
	maintenanceCancel    context.CancelFunc
	maintenanceDone      chan struct{}
	maintenanceErr       error // worker-owned; Close reads only after joining it
	indexReleaseMu       sync.Mutex
	pendingIndexReleases map[string]Owner // only workers that have already returned
}

func (s *Store) ID() string   { return s.id }
func (s *Store) Path() string { return s.path }

// Open authenticates existing storage before any new file/slot is created.
// Missing credentials always fail before filesystem side effects. create=false
// is used by recovery queries and supervisors and never initializes a cache.
func Open(ctx context.Context, path, key string, create bool) (*Store, error) {
	return open(ctx, path, key, create, nil)
}

// OpenWithPolicy attaches only to matching configured store-wide settings.
// A mismatch can publish a revision only after all handles/helpers are gone.
// Open without configuration (used by supervisors) consumes the stored policy.
// create=true is the normal execution-owner path and may upgrade schema 8;
// readers (create=false), Open and supervisors never migrate.
func OpenWithPolicy(ctx context.Context, path, key string, create bool, settings PolicyConfig) (*Store, error) {
	if err := settings.validate(); err != nil {
		return nil, err
	}
	return open(ctx, path, key, create, &settings)
}

func open(ctx context.Context, path, key string, create bool, desired *PolicyConfig) (*Store, error) {
	if key == "" {
		return nil, failure("storage_unavailable", "selected run credential is empty")
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != "runs.db" || filepath.Base(filepath.Dir(path)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(path))) != ".capy" {
		return nil, failure("storage_unavailable", "invalid owner-local run store path")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	physical, err := filepath.EvalSymlinks(owner)
	if err != nil || physical != owner {
		return nil, failure("storage_unavailable", "run owner must be a physical existing directory")
	}
	exists, err := inspectStore(path)
	if err != nil {
		return nil, err
	}
	if !exists && !create {
		return nil, failure("run_unavailable", "run store does not exist for this owner")
	}
	settings := defaultPolicyConfig()
	if desired != nil {
		settings = *desired
	}
	// Every retry starts with a new shared lease and revalidates the identity
	// and policy. A reset or another opener may win either unlocked interval.
	var upgraded *schemaIdentity
	for range 3 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		exists, err = inspectStore(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			if upgraded != nil {
				return nil, failure("run_unavailable", "run store disappeared after schema upgrade; retry attachment; no store was recreated")
			}
			if !create {
				return nil, failure("run_unavailable", "run store does not exist for this owner")
			}
			if err := initialize(ctx, path, key, owner, settings); err != nil {
				return nil, err
			}
		}
		s, err := openAttachedSchema(ctx, path, key, owner, unix.LOCK_SH, schemaVersion, upgraded)
		if err != nil {
			var mismatch *schemaMismatch
			if create && desired != nil && errors.As(err, &mismatch) && mismatch.found == 8 {
				if err := upgradeSchema8(ctx, path, key, owner, mismatch.identity); err != nil {
					return nil, err
				}
				upgraded = &mismatch.identity
				continue // close exclusive handles, then authenticate a fresh shared attachment
			}
			return nil, err
		}
		if desired == nil || *desired == s.policy.PolicyConfig {
			if _, err := s.Reconcile(ctx); err != nil {
				s.closeHandles()
				return nil, err
			}
			if _, err := s.Maintain(ctx); err != nil {
				s.closeHandles()
				return nil, err
			}
			s.startMaintenance()
			return s, nil
		}
		revision := s.policy.Revision
		if err := s.closeHandles(); err != nil {
			return nil, err
		}
		// No shared DB handle or lease survives this point. Nonblocking exclusive
		// acquisition refuses live readers, owners and inherited helper leases.
		s, err = openAttachedSchema(ctx, path, key, owner, unix.LOCK_EX, schemaVersion, upgraded)
		if err != nil {
			if errors.Is(err, unix.EWOULDBLOCK) {
				return nil, policyMismatch(revision)
			}
			return nil, err
		}
		err = s.changePolicy(ctx, *desired)
		closeErr := s.closeHandles()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
	}
	return nil, failure("policy_mismatch", "run store policy changed repeatedly during attachment; stop competing owners and reload with matching configuration")
}

// openAttached returns an authenticated, policy-validated handle without a
// maintenance worker. The caller closes all handles before releasing its lease.
func openAttached(ctx context.Context, path, key, owner string, lock int) (*Store, error) {
	return openAttachedSchema(ctx, path, key, owner, lock, schemaVersion, nil)
}

func openAttachedSchema(ctx context.Context, path, key, owner string, lock, supported int, expected *schemaIdentity) (*Store, error) {
	life, err := openRegular(filepath.Join(filepath.Dir(path), "lifecycle.lock"), false)
	if err != nil {
		return nil, storageError()
	}
	if err := unix.Flock(int(life.Fd()), lock|unix.LOCK_NB); err != nil {
		life.Close()
		if lock == unix.LOCK_EX && errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		return nil, failure("storage_unavailable", "run store lifecycle is busy")
	}
	// Recheck after locking: a reset may have removed/replaced storage between
	// the initial inspection and lease acquisition. Never recreate it with rw.
	if exists, err := inspectStore(path); err != nil || !exists {
		life.Close()
		if err != nil {
			return nil, err
		}
		return nil, failure("run_unavailable", "run store changed during attachment; retry")
	}
	s := &Store{path: path, key: key, owner: owner, lifecycle: life}
	info, err := os.Lstat(path)
	if err != nil || !safeRegular(info) {
		life.Close()
		return nil, storageError()
	}
	// Read-only authentication avoids creating new locks/rows on a wrong key.
	probe, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+"&mode=ro&_busy_timeout=100", path, "selected run credential")
	if err != nil {
		life.Close()
		return nil, openError(err, path)
	}
	var version int
	var recordedOwner string
	err = probe.QueryRowContext(ctx, "SELECT version, store_id, owner FROM run_meta WHERE singleton=1").Scan(&version, &s.id, &recordedOwner)
	if err == nil && version == supported && uuid.Validate(s.id) == nil {
		s.policy, err = readPolicyVersion(ctx, probe, supported)
	}
	closeErr := probe.Close()
	identity := schemaIdentity{id: s.id, file: info}
	if err == nil && closeErr == nil && uuid.Validate(s.id) == nil && expected != nil && !expected.matches(identity) {
		life.Close()
		return nil, failure("run_unavailable", "run store changed during schema upgrade; retry with the selected owner's current store")
	}
	if err == nil && closeErr == nil && uuid.Validate(s.id) == nil && version != supported {
		life.Close()
		return nil, newSchemaMismatch(version, identity)
	}
	// A moved tree may retain historical provenance; the current selected path
	// remains authoritative. The persisted owner never supplies a filesystem target.
	if err != nil || closeErr != nil || version != supported || uuid.Validate(s.id) != nil {
		life.Close()
		if err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				return nil, err
			}
		}
		return nil, storageError()
	}
	s.db, err = sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+"&mode=rw&_busy_timeout=100&_foreign_keys=on", path, "selected run credential")
	if err != nil {
		life.Close()
		return nil, openError(err, path)
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	if _, err := inspectStore(path); err != nil {
		s.db.Close()
		life.Close()
		return nil, err
	}
	if lock == unix.LOCK_SH {
		s.leases = &leaseAcquisition{lifecycle: life, path: path, id: s.id, refs: 1, exchange: make(chan struct{}, 1)}
	}
	return s, nil
}

func (s *Store) closeHandles() error {
	s.stopLeaseBorrowing()
	// Keep lifecycle ownership until database/sql has closed every connection.
	err := s.db.Close()
	return errors.Join(err, s.closeLifecycle())
}

func (s *Store) startMaintenance() {
	maintenanceCtx, stop := context.WithCancel(context.Background())
	s.maintenanceCancel, s.maintenanceDone = stop, make(chan struct{})
	go func() {
		ticker := time.NewTicker(maintenanceInterval)
		defer ticker.Stop()
		s.maintenance(maintenanceCtx, ticker.C)
	}()
}

func storageError() error {
	return failure("storage_unavailable", "run storage is unavailable; check the selected owner's .capy/runs path and permissions")
}
func openError(err error, path string) error {
	if sqliteutil.IsWrongPassphrase(err) || sqliteutil.IsSQLiteCorruption(err) {
		return failure("wrong_key", fmt.Sprintf("run store %s cannot be authenticated; restore the selected credential or, after stopping owners and old execution groups, explicitly run capy runs reset --discard for this project (permanently deletes its run logs); no data was discarded", path))
	}
	return storageError()
}

func inspectStore(path string) (bool, error) {
	root := filepath.Dir(path)
	for _, dir := range []string{filepath.Dir(root), root, filepath.Join(root, "locks")} {
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, storageError()
		}
		if dir != filepath.Dir(root) && info.Mode().Perm() != 0o700 {
			return false, storageError()
		}
	}
	exists := false
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !safeRegular(info) {
			return false, storageError()
		}
		if suffix == "" {
			exists = true
		}
	}
	return exists, nil
}

func safeRegular(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func openRegular(path string, create bool) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !safeRegular(info) {
		f.Close()
		return nil, storageError()
	}
	return f, nil
}

func initialize(ctx context.Context, path, key, owner string, settings PolicyConfig) error {
	root := filepath.Dir(path)
	for _, dir := range []string{filepath.Dir(root), root, filepath.Join(root, "locks")} {
		if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
			return storageError()
		}
		if _, err := inspectStore(path); err != nil {
			return err
		}
	}
	life, err := openRegular(filepath.Join(root, "lifecycle.lock"), true)
	if err != nil {
		return storageError()
	}
	defer life.Close()
	if err := unix.Flock(int(life.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return failure("storage_unavailable", "run store initialization is busy")
	}
	if exists, err := inspectStore(path); err != nil {
		return err
	} else if exists {
		return nil
	}
	registry, err := openRegular(filepath.Join(root, "recovery.lock"), true)
	if err != nil {
		return storageError()
	}
	if err := registry.Close(); err != nil {
		return storageError()
	}
	for slot := 0; slot < OwnerSlots; slot++ {
		registry := &Store{path: path}
		lock, err := openRegular(registry.OwnerLeasePath(slot), true)
		if err != nil {
			return storageError()
		}
		if err := lock.Close(); err != nil {
			return storageError()
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return storageError()
	}
	if err := f.Close(); err != nil {
		return storageError()
	}
	db, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+"&mode=rw&_busy_timeout=100&_foreign_keys=on", path, "selected run credential")
	if err != nil {
		return openError(err, path)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
		return storageError()
	}
	available, err := dbstatAvailable(ctx, db)
	if err != nil {
		return err
	}
	mode := "conservative"
	if available {
		mode = "dbstat"
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema+policySchema+searchSchema+batchSchema); err != nil {
		return storageError()
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_meta(singleton,version,store_id,owner) VALUES(1,?,?,?)", schemaVersion, uuid.NewString(), owner); err != nil {
		return storageError()
	}
	if err := insertPolicy(ctx, tx, settings, mode); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError()
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return storageError()
	}
	return nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.stopLeaseBorrowing()
		s.maintenanceCancel()
		<-s.maintenanceDone
		ctx, cancel := context.WithTimeout(context.Background(), recoveryTimeout)
		_, recoveryErr := s.Reconcile(ctx)
		_, retentionErr := s.Maintain(ctx)
		if recoveryErr != nil {
			recoveryErr = fmt.Errorf("reconcile runs on close: %w", recoveryErr)
		}
		if retentionErr != nil {
			retentionErr = fmt.Errorf("maintain runs on close: %w", retentionErr)
		}
		var freePages int64
		reclaimCheckErr := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freePages)
		cancel()
		// The pool must close before opening the final checkpoint connection.
		s.closeErr = errors.Join(s.maintenanceErr, recoveryErr, retentionErr, reclaimCheckErr, s.db.Close())
		// Closing authenticates a fresh encrypted connection as well as running
		// the checkpoint. It is not an append/finalization write; allow the same
		// bounded budget as recovery so CPU contention during key derivation does
		// not turn a successful capture into a spurious close failure.
		ctx, cancel = context.WithTimeout(context.Background(), recoveryTimeout)
		defer cancel()
		db, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(s.path, s.key)+"&mode=rw&_busy_timeout=25", s.path, "selected run credential")
		if err == nil {
			var busy, log, checkpointed int
			err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
			// Other attached owners may legitimately keep WAL pages busy.
			err = errors.Join(err, db.Close())
		}
		if err != nil {
			s.closeErr = errors.Join(s.closeErr, fmt.Errorf("checkpoint runs on close: %w", storageError()))
		}
		s.closeErr = errors.Join(s.closeErr, s.closeLifecycle())
		if freePages == 0 {
			return
		}
		// No lock conversion: another opener/reset may win this interval. Only
		// reclaim the same authenticated store if a fresh exclusive lease wins.
		probe, openErr := openAttached(ctx, s.path, s.key, s.owner, unix.LOCK_EX)
		if openErr == nil {
			if probe.id == s.id {
				s.closeErr = errors.Join(s.closeErr, probe.reclaim(ctx))
			}
			s.closeErr = errors.Join(s.closeErr, probe.closeHandles())
		} else if !errors.Is(openErr, unix.EWOULDBLOCK) {
			var typed *Error
			// A replacement or now-incompatible policy is outside this closed
			// handle's lifetime; skip its optional reclamation without touching it.
			if !errors.As(openErr, &typed) || (typed.Code != "run_unavailable" && typed.Code != "wrong_key" && typed.Code != "policy_mismatch") {
				s.closeErr = errors.Join(s.closeErr, openErr)
			}
		}
	})
	return s.closeErr
}

const schema = `
CREATE TABLE run_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL, store_id TEXT NOT NULL, owner TEXT NOT NULL, admission_blocked INTEGER NOT NULL DEFAULT 0 CHECK(admission_blocked IN (0,1)), last_created INTEGER NOT NULL DEFAULT 0, accounted_bytes INTEGER NOT NULL DEFAULT 0 CHECK(accounted_bytes>=0), index_bytes INTEGER NOT NULL DEFAULT 0 CHECK(index_bytes>=0), index_enabled INTEGER NOT NULL DEFAULT 0 CHECK(index_enabled IN (0,1)));
CREATE TABLE run_owners(slot INTEGER PRIMARY KEY CHECK(slot>=0 AND slot<64), generation TEXT NOT NULL UNIQUE, cleanup_unverified INTEGER NOT NULL DEFAULT 0 CHECK(cleanup_unverified IN (0,1)));
` + runTableSchema + runIndexesSchema + `
CREATE TABLE run_work_claims(
 run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 slot INTEGER NOT NULL CHECK(slot>=0 AND slot<64), generation TEXT NOT NULL
);
CREATE INDEX run_work_owner ON run_work_claims(slot,generation,run_id);
CREATE TABLE run_events(
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 seq INTEGER NOT NULL CHECK(seq>0), phase TEXT NOT NULL CHECK(phase IN ('compile','run')),
 stream TEXT NOT NULL CHECK(stream IN ('stdout','stderr')), data BLOB NOT NULL CHECK(length(data)>0 AND length(data)<=16384),
 byte_offset INTEGER NOT NULL CHECK(byte_offset>=0), start_line INTEGER NOT NULL CHECK(start_line>0),
 PRIMARY KEY(run_id,seq)
);
CREATE INDEX run_events_stream ON run_events(run_id,stream,seq);
CREATE INDEX run_events_offsets ON run_events(run_id,stream,byte_offset);
CREATE INDEX run_events_lines ON run_events(run_id,stream,start_line,seq);
CREATE TABLE run_streams(
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 stream TEXT NOT NULL CHECK(stream IN ('stdout','stderr')),
 checkpoint BLOB NOT NULL CHECK(length(checkpoint)<=4096),
 covered INTEGER NOT NULL DEFAULT 0, observed INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(run_id,stream)
);
CREATE TABLE run_projection(
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 stream TEXT NOT NULL, raw_start INTEGER NOT NULL, raw_end INTEGER NOT NULL,
 seq INTEGER NOT NULL, phase TEXT NOT NULL, start_line INTEGER NOT NULL,
 text TEXT NOT NULL, redacted INTEGER NOT NULL,
 PRIMARY KEY(run_id,stream,raw_start), CHECK(raw_end>raw_start)
);
CREATE INDEX run_projection_order ON run_projection(run_id,seq,raw_start);

`

// Shared by fresh stores and the transactional schema-8 rebuild.
const runTableSchema = `
CREATE TABLE runs(
 id TEXT PRIMARY KEY, generation TEXT NOT NULL, slot INTEGER NOT NULL CHECK(slot>=0 AND slot<64),
 created_at INTEGER NOT NULL,
 program BLOB NOT NULL, result BLOB NOT NULL,
 cleanup_scope TEXT NOT NULL CHECK(cleanup_scope IN ('linux_descendants_v1','darwin_process_group_observation_v1')),
 cleanup TEXT NOT NULL CHECK(cleanup IN ('not_spawned','pending','retired','observed')),
 terminal INTEGER NOT NULL DEFAULT 0 CHECK(terminal IN (0,1)),
 accounted_bytes INTEGER NOT NULL DEFAULT 0 CHECK(accounted_bytes>=0),
 evicting INTEGER NOT NULL DEFAULT 0 CHECK(evicting IN (0,1)),
 expires_at INTEGER, completed_at INTEGER,
 stdout_bytes INTEGER NOT NULL DEFAULT 0, stderr_bytes INTEGER NOT NULL DEFAULT 0,
 stdout_lines INTEGER NOT NULL DEFAULT 1, stderr_lines INTEGER NOT NULL DEFAULT 1,
 CHECK((cleanup_scope='linux_descendants_v1' AND cleanup IN ('not_spawned','pending','retired')) OR
       (cleanup_scope='darwin_process_group_observation_v1' AND cleanup IN ('not_spawned','pending','observed')))
);
`

const runIndexesSchema = `
CREATE INDEX runs_created ON runs(created_at,id);
CREATE INDEX runs_owner ON runs(slot,generation,terminal,id);
CREATE INDEX runs_unfinished ON runs(slot,generation,id) WHERE terminal=0;
CREATE INDEX runs_expiry ON runs(expires_at,id) WHERE terminal=1;
CREATE INDEX runs_completed ON runs(completed_at,id) WHERE terminal=1;
CREATE INDEX runs_evicting ON runs(id) WHERE evicting=1;
`
