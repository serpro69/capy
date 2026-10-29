package runstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const schema9RunColumns = "id,generation,slot,created_at,program,result,cleanup_scope,cleanup,terminal,accounted_bytes,evicting,expires_at,completed_at,stdout_bytes,stderr_bytes,stdout_lines,stderr_lines"

// upgradeSupportedSchema performs separately committed stages. A schema-9
// intermediate is valid and retryable; no stage clears quarantine or maintains.
func upgradeSupportedSchema(ctx context.Context, path, key, owner string, version int, expected schemaIdentity) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for version < schemaVersion {
		s, err := openAttachedSchema(ctx, path, key, owner, unix.LOCK_EX, version, &expected)
		if err != nil {
			if errors.Is(err, unix.EWOULDBLOCK) {
				return upgradeBusy()
			}
			var mismatch *schemaMismatch
			if errors.As(err, &mismatch) && expected.matches(mismatch.identity) && mismatch.found > version && mismatch.found <= schemaVersion {
				version = mismatch.found
				continue
			}
			return err
		}
		err = migrateStage(ctx, s, version)
		if closeErr := s.closeHandles(); err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		version++
	}
	return nil
}

func upgradeBusy() error {
	return failure("schema_upgrade_busy", "run schema upgrade requires exclusive lifecycle ownership; stop all run owners, readers and supervisors, then use capy runs upgrade from an ordinary terminal")
}

func migrateStage(ctx context.Context, s *Store, version int) error {
	switch version {
	case 8:
		return migrateSchema8(ctx, s)
	case 9:
		return migrateSchema9(ctx, s)
	default:
		return newSchemaMismatch(version, schemaIdentity{id: s.id})
	}
}

func migrateSchema9(ctx context.Context, s *Store) (retErr error) {
	defer func() {
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
	if _, err := conn.ExecContext(ctx, "PRAGMA temp_store=MEMORY; PRAGMA foreign_keys=OFF"); err != nil {
		return upgradeError(err)
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), WriteTimeout)
		defer cancel()
		if _, err := conn.ExecContext(restoreCtx, "PRAGMA foreign_keys=ON"); err != nil {
			retErr = errors.Join(retErr, upgradeError(err))
		}
	}()
	var fk bool
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk {
		return failure("schema_upgrade_failed", "could not prepare foreign-key-safe run schema rebuild")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return upgradeError(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM runs WHERE 0"); err != nil {
		return upgradeError(err)
	}
	var version int
	var id string
	var used int64
	if err := tx.QueryRowContext(ctx, "SELECT version,store_id,accounted_bytes FROM run_meta WHERE singleton=1").Scan(&version, &id, &used); err != nil {
		return upgradeError(err)
	}
	if version != 9 || id != s.id {
		return failure("schema_upgrade_failed", "run store identity or version changed before migration")
	}
	if used > s.policy.MaxRetainedBytes {
		return migrationCapacity()
	}
	indexes, err := migrationRunIndexes(ctx, tx, schema9RunColumns)
	if err != nil {
		return err
	}
	for _, statement := range []string{
		strings.Replace(runTableSchema, "CREATE TABLE runs(", "CREATE TABLE runs_v10(", 1),
		"INSERT INTO runs_v10(rowid," + schema9RunColumns + ",last_phase) SELECT rowid," + schema9RunColumns + ",'unknown' FROM runs",
		"DROP TABLE runs", "ALTER TABLE runs_v10 RENAME TO runs",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return upgradeError(err)
		}
	}
	for _, statement := range indexes {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return upgradeError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, recoverySchema+"ALTER TABLE run_meta ADD COLUMN recovery_revision INTEGER NOT NULL DEFAULT 0 CHECK(recovery_revision>=0);"); err != nil {
		return upgradeError(err)
	}
	if err := checkMigrationForeignKeys(ctx, tx); err != nil {
		return err
	}
	used, err = refreshIndexDebit(ctx, tx)
	if err != nil {
		return err
	}
	if used > s.policy.MaxRetainedBytes {
		return migrationCapacity()
	}
	for _, statement := range []string{"UPDATE run_policy SET schema_version=10 WHERE singleton=1 AND schema_version=9", "UPDATE run_meta SET version=10 WHERE singleton=1 AND version=9"} {
		result, err := tx.ExecContext(ctx, statement)
		if err != nil {
			return upgradeError(err)
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return failure("schema_upgrade_failed", "run schema markers changed during migration")
		}
	}
	return upgradeError(tx.Commit())
}

func migrationCapacity() error {
	return failure("schema_upgrade_capacity", "preserved history and schema metadata exceed stored retained capacity; raise runs.max_retained_bytes and use capy runs upgrade offline; no logs were evicted")
}
