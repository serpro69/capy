package runstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

const schemaVersion = 3
const OwnerSlots = 64
const WriteTimeout = 250 * time.Millisecond

type Store struct {
	db                   *sql.DB
	path, key, id, owner string
	lifecycle            *os.File
	closeOnce            sync.Once
	closeErr             error
}

func (s *Store) ID() string               { return s.id }
func (s *Store) Path() string             { return s.path }
func (s *Store) LifecycleLease() *os.File { return s.lifecycle }

// Open authenticates existing storage before any new file/slot is created.
// Missing credentials always fail before filesystem side effects. create=false
// is used by recovery queries and supervisors and never initializes a cache.
func Open(ctx context.Context, path, key string, create bool) (*Store, error) {
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
	if !exists {
		if err := initialize(ctx, path, key, owner); err != nil {
			return nil, err
		}
	}
	life, err := openRegular(filepath.Join(filepath.Dir(path), "lifecycle.lock"), false)
	if err != nil {
		return nil, storageError()
	}
	if err := unix.Flock(int(life.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		life.Close()
		return nil, failure("storage_unavailable", "run store lifecycle is busy")
	}
	s := &Store{path: path, key: key, owner: owner, lifecycle: life}
	// Read-only authentication avoids creating new locks/rows on a wrong key.
	probe, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+"&mode=ro&_busy_timeout=100", path, "selected run credential")
	if err != nil {
		life.Close()
		return nil, openError(err)
	}
	var version int
	var recordedOwner string
	err = probe.QueryRowContext(ctx, "SELECT version, store_id, owner FROM run_meta WHERE singleton=1").Scan(&version, &s.id, &recordedOwner)
	closeErr := probe.Close()
	// A moved tree may retain historical provenance; the current selected path
	// remains authoritative. The persisted owner never supplies a filesystem target.
	if err != nil || closeErr != nil || version != schemaVersion || uuid.Validate(s.id) != nil {
		life.Close()
		return nil, storageError()
	}
	s.db, err = sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+"&mode=rw&_busy_timeout=100&_foreign_keys=on", path, "selected run credential")
	if err != nil {
		life.Close()
		return nil, openError(err)
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	if _, err := inspectStore(path); err != nil {
		s.db.Close()
		life.Close()
		return nil, err
	}
	return s, nil
}

func storageError() error {
	return failure("storage_unavailable", "run storage is unavailable; check the selected owner's .capy/runs path and permissions")
}
func openError(err error) error {
	if sqliteutil.IsWrongPassphrase(err) || sqliteutil.IsSQLiteCorruption(err) {
		return failure("wrong_key", "run store cannot be authenticated with the selected credential; restore that credential; no data was discarded")
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

func initialize(ctx context.Context, path, key, owner string) error {
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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return storageError()
	}
	if err := f.Close(); err != nil {
		return storageError()
	}
	db, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(path, key)+"&mode=rw&_busy_timeout=100&_foreign_keys=on", path, "selected run credential")
	if err != nil {
		return openError(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return storageError()
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_meta(singleton,version,store_id,owner) VALUES(1,?,?,?)", schemaVersion, uuid.NewString(), owner); err != nil {
		return storageError()
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
		// The pool must close before opening the final checkpoint connection.
		s.closeErr = s.db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), WriteTimeout)
		defer cancel()
		db, err := sqliteutil.OpenWithCanary(ctx, sqliteutil.EncryptedDSN(s.path, s.key)+"&mode=rw&_busy_timeout=25", s.path, "selected run credential")
		if err == nil {
			var busy, log, checkpointed int
			err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
			// Other attached owners may legitimately keep WAL pages busy.
			err = errors.Join(err, db.Close())
		}
		if err != nil {
			s.closeErr = errors.Join(s.closeErr, storageError())
		}
		s.closeErr = errors.Join(s.closeErr, s.lifecycle.Close())
	})
	return s.closeErr
}

const schema = `
CREATE TABLE run_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL, store_id TEXT NOT NULL, owner TEXT NOT NULL, admission_blocked INTEGER NOT NULL DEFAULT 0 CHECK(admission_blocked IN (0,1)), last_created INTEGER NOT NULL DEFAULT 0);
CREATE TABLE run_owners(slot INTEGER PRIMARY KEY CHECK(slot>=0 AND slot<64), generation TEXT NOT NULL UNIQUE);
CREATE TABLE runs(
 id TEXT PRIMARY KEY, generation TEXT NOT NULL, slot INTEGER NOT NULL CHECK(slot>=0 AND slot<64),
 created_at INTEGER NOT NULL,
 program BLOB NOT NULL, result BLOB NOT NULL,
 cleanup TEXT NOT NULL CHECK(cleanup IN ('not_spawned','pending','retired')),
 terminal INTEGER NOT NULL DEFAULT 0 CHECK(terminal IN (0,1)),
 stdout_bytes INTEGER NOT NULL DEFAULT 0, stderr_bytes INTEGER NOT NULL DEFAULT 0,
 stdout_lines INTEGER NOT NULL DEFAULT 1, stderr_lines INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX runs_created ON runs(created_at,id);
CREATE INDEX runs_owner ON runs(slot,generation,terminal);
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
