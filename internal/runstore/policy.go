package runstore

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
)

// PolicyConfig contains only store-wide settings. Response, deadline and owner
// capacity settings deliberately do not participate in policy compatibility.
type PolicyConfig struct {
	TTLHours         int   `json:"ttl_hours"`
	MaxRunBytes      int64 `json:"max_run_bytes"`
	MaxRetainedBytes int64 `json:"max_retained_bytes"`
}

// Policy is immutable for the lifetime of an attached Store. Its shared
// lifecycle lease prevents publication of a different revision until it closes.
type Policy struct {
	PolicyConfig
	Revision         int64  `json:"revision"`
	SchemaVersion    int    `json:"schema_version"`
	RedactionVersion int    `json:"redaction_version"`
	AccountingMode   string `json:"accounting_mode"`
}

func (s *Store) Policy() Policy { return s.policy }

func defaultPolicyConfig() PolicyConfig {
	return PolicyConfig{TTLHours: 24, MaxRunBytes: 64 << 20, MaxRetainedBytes: 512 << 20}
}

func (p PolicyConfig) validate() error {
	if p.TTLHours < 1 || int64(p.TTLHours) > math.MaxInt64/int64(time.Hour) ||
		p.MaxRunBytes < 1 || p.MaxRetainedBytes < p.MaxRunBytes {
		return failure("invalid_argument", "run policy requires positive bounded TTL and capture limits no larger than retained capacity")
	}
	return nil
}

// dbstatAvailable checks the pinned SQLite build and verifies the actual module
// before selecting it. An I/O/query failure is not permission to change modes.
func dbstatAvailable(ctx context.Context, db *sql.DB) (bool, error) {
	var enabled bool
	if err := db.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_DBSTAT_VTAB')").Scan(&enabled); err != nil {
		return false, storageError()
	}
	if enabled {
		rows, err := db.QueryContext(ctx, "SELECT pgsize FROM dbstat LIMIT 0")
		if err != nil {
			return false, storageError()
		}
		if err := rows.Close(); err != nil {
			return false, storageError()
		}
	}
	return enabled, nil
}

func readPolicy(ctx context.Context, db *sql.DB) (Policy, error) {
	var p Policy
	err := db.QueryRowContext(ctx, `SELECT revision,ttl_hours,max_run_bytes,max_retained_bytes,schema_version,redaction_version,accounting_mode FROM run_policy WHERE singleton=1`).Scan(
		&p.Revision, &p.TTLHours, &p.MaxRunBytes, &p.MaxRetainedBytes, &p.SchemaVersion, &p.RedactionVersion, &p.AccountingMode)
	if err != nil || p.Revision < 1 || p.PolicyConfig.validate() != nil {
		return Policy{}, storageError()
	}
	if p.SchemaVersion != schemaVersion || p.RedactionVersion != sanitize.StreamVersion || (p.AccountingMode != "dbstat" && p.AccountingMode != "conservative") {
		return Policy{}, failure("policy_mismatch", "stored run policy versions or accounting mode are unsupported; stop and reload owners with a compatible capy")
	}
	if p.AccountingMode == "dbstat" {
		enabled, err := dbstatAvailable(ctx, db)
		if err != nil {
			return Policy{}, err
		}
		if !enabled {
			return Policy{}, failure("policy_mismatch", "stored run policy requires dbstat accounting; stop and reload with a compatible capy build")
		}
	}
	return p, nil
}

func insertPolicy(ctx context.Context, tx *sql.Tx, settings PolicyConfig, mode string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_policy(singleton,revision,ttl_hours,max_run_bytes,max_retained_bytes,schema_version,redaction_version,accounting_mode) VALUES(1,1,?,?,?,?,?,?)`,
		settings.TTLHours, settings.MaxRunBytes, settings.MaxRetainedBytes, schemaVersion, sanitize.StreamVersion, mode)
	if err != nil {
		return storageError()
	}
	return nil
}

func policyMismatch(revision int64) error {
	if revision == math.MaxInt64 {
		return failure("policy_mismatch", "run policy revision is exhausted")
	}
	return failure("policy_mismatch", fmt.Sprintf("run store policy revision %d differs from requested revision %d; stop all run owners/readers and their supervisors, then reload with matching configuration", revision, revision+1))
}

// changePolicy is called only with exclusive lifecycle ownership, after
// reauthentication and rereading the current policy. It never converts a flock.
func (s *Store) changePolicy(ctx context.Context, settings PolicyConfig) error {
	if settings == s.policy.PolicyConfig {
		return nil
	}
	report, err := s.Reconcile(ctx)
	if err != nil {
		return err
	}
	if err := s.checkQuarantine(ctx); err != nil {
		return err
	}
	if report.DeferredSlots != 0 {
		return failure("policy_mismatch", "run policy change awaits bounded owner recovery; retry after recovery completes")
	}
	if s.policy.Revision == math.MaxInt64 {
		return failure("policy_mismatch", "run policy revision is exhausted")
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE run_policy SET revision=revision+1,ttl_hours=?,max_run_bytes=?,max_retained_bytes=? WHERE singleton=1 AND revision=?`,
		settings.TTLHours, settings.MaxRunBytes, settings.MaxRetainedBytes, s.policy.Revision)
	if err != nil {
		return storageError()
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return storageError()
	}
	if _, err := retain(ctx, tx, settings.MaxRetainedBytes, 0, true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError()
	}
	return s.reclaim(ctx)
}

const policySchema = `CREATE TABLE run_policy(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), revision INTEGER NOT NULL CHECK(revision>0),
 ttl_hours INTEGER NOT NULL CHECK(ttl_hours>0 AND ttl_hours<=2562047),
 max_run_bytes INTEGER NOT NULL CHECK(max_run_bytes>0),
 max_retained_bytes INTEGER NOT NULL CHECK(max_retained_bytes>=max_run_bytes),
 schema_version INTEGER NOT NULL, redaction_version INTEGER NOT NULL,
 accounting_mode TEXT NOT NULL CHECK(accounting_mode IN ('dbstat','conservative'))
);`
