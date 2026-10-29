package runstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	sqlite3 "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

type schemaIdentity struct {
	id   string
	file os.FileInfo
}

func (s schemaIdentity) matches(other schemaIdentity) bool {
	return s.id == other.id && s.file != nil && other.file != nil && os.SameFile(s.file, other.file)
}

type schemaMismatch struct {
	found    int
	identity schemaIdentity
	cause    *Error
}

func (e *schemaMismatch) Error() string { return e.cause.Error() }
func (e *schemaMismatch) Unwrap() error { return e.cause }

func newSchemaMismatch(found int, identity schemaIdentity) *schemaMismatch {
	message := fmt.Sprintf("run store schema %d is incompatible with supported schema %d; use a compatible capy binary", found, schemaVersion)
	if found == 8 || found == 9 {
		message = fmt.Sprintf("run store schema %d requires upgrade to schema %d; stop owners, readers and supervisors, then use capy runs upgrade from an ordinary terminal; readers and helpers cannot upgrade", found, schemaVersion)
	}
	return &schemaMismatch{found: found, identity: identity, cause: &Error{Code: "schema_mismatch", Message: message}}
}

const legacyRunColumns = "id,generation,slot,created_at,program,result,cleanup,terminal,accounted_bytes,evicting,expires_at,completed_at,stdout_bytes,stderr_bytes,stdout_lines,stderr_lines"

// migrateSchema8 uses the documented SQLite rebuild order. The dedicated
// connection is pinned while foreign keys are off and is never lent to readers.
func migrateSchema8(ctx context.Context, s *Store) (retErr error) {
	defer func() {
		// database/sql can report ErrTxDone after its cancellation watcher has
		// already rolled back. Preserve the causal cancellation for the caller.
		if retErr != nil {
			retErr = errors.Join(retErr, ctx.Err())
		}
	}()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return upgradeError(err)
	}
	defer func() { retErr = errors.Join(retErr, conn.Close()) }()
	if err := checkMigrationSpace(ctx, conn, s.path, filesystemAvailable); err != nil {
		return err
	}
	// Temporary sort data must not spill plaintext outside the encrypted database.
	if _, err := conn.ExecContext(ctx, "PRAGMA temp_store=MEMORY"); err != nil {
		return upgradeError(err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return upgradeError(err)
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), WriteTimeout)
		defer cancel()
		if _, err := conn.ExecContext(restoreCtx, "PRAGMA foreign_keys=ON"); err != nil {
			retErr = errors.Join(retErr, upgradeError(err))
		}
	}()
	var foreignKeys bool
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys {
		return failure("schema_upgrade_failed", "could not prepare foreign-key-safe run schema rebuild")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return upgradeError(err)
	}
	defer tx.Rollback()
	// Acquire the writer reservation before validating metadata/copying rows.
	if _, err := tx.ExecContext(ctx, "DELETE FROM runs WHERE 0"); err != nil {
		return upgradeError(err)
	}
	var version int
	var id string
	if err := tx.QueryRowContext(ctx, "SELECT version,store_id FROM run_meta WHERE singleton=1").Scan(&version, &id); err != nil {
		return upgradeError(err)
	}
	if version != 8 || id != s.id {
		return failure("schema_upgrade_failed", "run store identity or version changed before migration")
	}
	indexes, err := legacyRunIndexes(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateLegacyRuns(ctx, tx); err != nil {
		return err
	}
	newTable := strings.Replace(runTableSchemaV9, "CREATE TABLE runs(", "CREATE TABLE runs_v9(", 1)
	statements := []string{
		newTable,
		"INSERT INTO runs_v9(rowid," + legacyRunColumns + ",cleanup_scope) SELECT rowid," + legacyRunColumns + ",'linux_descendants_v1' FROM runs",
		"DROP TABLE runs",
		"ALTER TABLE runs_v9 RENAME TO runs",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return upgradeError(err)
		}
	}
	for _, statement := range indexes {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return upgradeError(err)
		}
	}
	if err := migrateBatchProofs(ctx, tx); err != nil {
		return err
	}
	var inconsistentBatch bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_batches b WHERE
(SELECT count(*) FROM run_batch_children c WHERE c.batch_id=b.id) NOT BETWEEN 1 AND 256
OR (b.completed_at IS NOT NULL) <> NOT EXISTS(SELECT 1 FROM run_batch_children c WHERE c.batch_id=b.id AND c.terminal=0)
OR (b.completed_at IS NULL) <> (b.expires_at IS NULL))`).Scan(&inconsistentBatch); err != nil {
		return upgradeError(err)
	}
	if inconsistentBatch {
		return failure("schema_upgrade_failed", "legacy batch completion disagrees with its child inventory")
	}
	if err := checkMigrationForeignKeys(ctx, tx); err != nil {
		return err
	}
	// Metadata fits the existing run/child reservations. Recount allocated index
	// pages after dropping the old table and renaming its replacement, so payload is not charged
	// twice in conservative mode. No retention or quota-driven deletion is used.
	used, err := refreshIndexDebit(ctx, tx)
	if err != nil {
		return err
	}
	if used > s.policy.MaxRetainedBytes {
		return failure("schema_upgrade_capacity", "preserved history exceeds the stored retained quota; raise runs.max_retained_bytes and use capy runs upgrade offline, then retry; no logs were evicted")
	}
	for _, statement := range []string{
		"UPDATE run_policy SET schema_version=9 WHERE singleton=1 AND schema_version=8",
		"UPDATE run_meta SET version=9 WHERE singleton=1 AND version=8",
	} {
		result, err := tx.ExecContext(ctx, statement)
		if err != nil {
			return upgradeError(err)
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return failure("schema_upgrade_failed", "run schema markers changed during migration")
		}
	}
	if err := tx.Commit(); err != nil {
		return upgradeError(err)
	}
	return nil
}

func legacyRunIndexes(ctx context.Context, tx *sql.Tx) ([]string, error) {
	return migrationRunIndexes(ctx, tx, legacyRunColumns)
}

func migrationRunIndexes(ctx context.Context, tx *sql.Tx, expectedColumns string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_info('runs') ORDER BY cid")
	if err != nil {
		return nil, upgradeError(err)
	}
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			rows.Close()
			return nil, upgradeError(err)
		}
		columns = append(columns, column)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, upgradeError(err)
	}
	if strings.Join(columns, ",") != expectedColumns {
		return nil, failure("schema_upgrade_failed", "run migration source table has unexpected columns; history was not changed")
	}
	rows, err = tx.QueryContext(ctx, "SELECT type,sql FROM sqlite_schema WHERE tbl_name='runs' AND type IN ('index','trigger') AND sql IS NOT NULL LIMIT 65")
	if err != nil {
		return nil, upgradeError(err)
	}
	defer rows.Close()
	indexes := []string{}
	for rows.Next() {
		var kind, statement string
		if err := rows.Scan(&kind, &statement); err != nil {
			return nil, upgradeError(err)
		}
		if kind != "index" || len(statement) > 16384 || len(indexes) == 64 {
			return nil, failure("schema_upgrade_failed", "run migration source table has unsupported dependent objects")
		}
		indexes = append(indexes, statement)
	}
	return indexes, upgradeError(rows.Err())
}

func checkMigrationForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return upgradeError(err)
	}
	defer rows.Close()
	if rows.Next() {
		return failure("schema_upgrade_failed", "run schema upgrade found inconsistent foreign-key references; history was not changed")
	}
	return upgradeError(rows.Err())
}

func filesystemAvailable(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 || uint64(stat.Bavail) > math.MaxUint64/uint64(stat.Bsize) {
		return 0, errors.New("invalid filesystem capacity")
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func checkMigrationSpace(ctx context.Context, conn *sql.Conn, path string, available func(string) (uint64, error)) error {
	var pages, size uint64
	if err := conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return upgradeError(err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return upgradeError(err)
	}
	// Conservative filesystem headroom: a full replacement image plus its WAL
	// and checkpoint growth, with metadata slack. This is not retained quota or
	// a reservation; real write/commit failures still roll back the transaction.
	const slack = 2 << 20
	if size == 0 || pages > (math.MaxUint64-slack)/3/size {
		return failure("schema_upgrade_capacity", "run schema migration size is unsupported")
	}
	needed := 3*pages*size + slack
	free, err := available(path)
	if err != nil {
		return failure("schema_upgrade_capacity", "cannot determine filesystem headroom for encrypted run migration")
	}
	if free < needed {
		return failure("schema_upgrade_capacity", fmt.Sprintf("run schema upgrade needs at least %d bytes of free filesystem headroom for encrypted table-copy/WAL work; %d bytes available; free disk space and retry; no logs were evicted", needed, free))
	}
	return nil
}

func upgradeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrFull || errors.Is(err, unix.ENOSPC) {
		return failure("schema_upgrade_capacity", "run schema upgrade exhausted database/filesystem capacity; free disk space and retry; no logs were evicted")
	}
	return failure("schema_upgrade_failed", "encrypted run schema upgrade failed; history was not discarded; check disk availability and retry with a compatible binary")
}
