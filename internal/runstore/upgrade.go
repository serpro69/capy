package runstore

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
	"golang.org/x/sys/unix"
)

// UpgradeResult records separately committed policy/schema changes even when a
// later stage fails. It contains no program, output or raw storage error.
type UpgradeResult struct {
	Version                int     `json:"version"`
	Kind                   string  `json:"kind"`
	StoreID                string  `json:"store_id,omitempty"`
	FromSchema             int     `json:"from_schema,omitempty"`
	SchemaVersion          int     `json:"schema_version,omitempty"`
	Policy                 *Policy `json:"policy,omitempty"`
	PreviousPolicyRevision int64   `json:"previous_policy_revision,omitempty"`
	QuotaIncreased         bool    `json:"quota_increased"`
	AdmissionBlocked       *bool   `json:"admission_blocked,omitempty"`
	Error                  *Error  `json:"error,omitempty"`
}

// Upgrade is the explicit offline path. Unlike Open, it never initializes,
// reconciles, claims a generation, maintains, reclaims or submits execution.
func Upgrade(ctx context.Context, path, key string, settings PolicyConfig) (result UpgradeResult, retErr error) {
	result.Version, result.Kind = EnvelopeVersion, "upgrade"
	if err := settings.validate(); err != nil {
		return result, err
	}
	if key == "" {
		return result, failure("credential_unavailable", "restore the selected matching run credential; no fallback key was tried")
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != "runs.db" || filepath.Base(filepath.Dir(path)) != "runs" || filepath.Base(filepath.Dir(filepath.Dir(path))) != ".capy" {
		return result, storageError()
	}
	owner := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if physical, err := filepath.EvalSymlinks(owner); err != nil || physical != owner {
		return result, storageError()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	exists, err := inspectStore(path)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, failure("run_unavailable", "run store does not exist; upgrade creates nothing")
	}
	s, err := openAttachedSchema(ctx, path, key, owner, unix.LOCK_EX, 0, nil)
	if err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			err = upgradeBusy()
		}
		var typed *Error
		if errors.As(err, &typed) && typed.Code == "wrong_key" {
			err = failure("wrong_key", "run store cannot be authenticated; restore the selected matching credential and reload; history was not discarded")
		}
		return result, err
	}
	// Keep every acquired lock until DB handles close, including cancellation.
	var locks []*os.File
	defer func() {
		retErr = errors.Join(retErr, s.db.Close())
		for i := len(locks) - 1; i >= 0; i-- {
			retErr = errors.Join(retErr, locks[i].Close())
		}
		retErr = errors.Join(retErr, s.closeLifecycle())
	}()
	// Lifecycle ownership excludes cooperative clients. Verify the additional
	// permanent locks as well; never create or replace a missing inode.
	names := []string{filepath.Join(filepath.Dir(path), "recovery.lock")}
	for slot := 0; slot < OwnerSlots; slot++ {
		names = append(names, s.OwnerLeasePath(slot))
	}
	for _, name := range names {
		f, err := openRegular(name, false)
		if err != nil {
			return result, storageError()
		}
		locks = append(locks, f)
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return result, upgradeBusy()
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return result, storageError()
	}
	identity := schemaIdentity{id: s.id, file: info}
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := os.Lstat(path)
		if err != nil || !safeRegular(current) || !os.SameFile(info, current) {
			return storageError()
		}
		for _, f := range append([]*os.File{s.lifecycle}, locks...) {
			acquired, err := f.Stat()
			if err != nil {
				return storageError()
			}
			current, err := os.Lstat(f.Name())
			if err != nil || !safeRegular(current) || !os.SameFile(acquired, current) {
				return storageError()
			}
		}
		var id string
		if err := s.db.QueryRowContext(ctx, "SELECT store_id FROM run_meta WHERE singleton=1").Scan(&id); err != nil || id != identity.id {
			return storageError()
		}
		return nil
	}
	if err := verify(); err != nil {
		return result, err
	}
	result.StoreID, result.FromSchema, result.SchemaVersion = s.id, s.policy.SchemaVersion, s.policy.SchemaVersion
	policy := s.policy
	result.Policy, result.PreviousPolicyRevision = &policy, s.policy.Revision
	var blocked bool
	if err := s.db.QueryRowContext(ctx, "SELECT admission_blocked FROM run_meta WHERE singleton=1").Scan(&blocked); err != nil {
		return result, storageError()
	}
	result.AdmissionBlocked = &blocked
	if err := s.increaseUpgradeQuota(ctx, settings); err != nil {
		return result, err
	}
	policy = s.policy
	result.QuotaIncreased = s.policy.Revision != result.PreviousPolicyRevision
	if result.QuotaIncreased {
		// Keep the known committed result above even if this reload fails. Each
		// migration uses policy freshly read from the authenticated database.
		current, err := readPolicyVersion(ctx, s.db, result.SchemaVersion)
		if err != nil {
			return result, errors.Join(err, ctx.Err())
		}
		s.policy, policy = current, current
	}
	for result.SchemaVersion < schemaVersion {
		if err := verify(); err != nil {
			return result, err
		}
		if err := migrateStage(ctx, s, result.SchemaVersion); err != nil {
			return result, err
		}
		result.SchemaVersion++
		result.Policy.SchemaVersion = result.SchemaVersion
		s.policy, err = readPolicyVersion(ctx, s.db, result.SchemaVersion)
		if err != nil {
			return result, err
		}
		policy = s.policy
	}
	return result, nil
}

func (s *Store) increaseUpgradeQuota(ctx context.Context, settings PolicyConfig) error {
	p := s.policy
	if settings.TTLHours != p.TTLHours || settings.MaxRunBytes != p.MaxRunBytes || settings.MaxRetainedBytes < p.MaxRetainedBytes {
		return failure("policy_mismatch", "offline upgrade permits only a configured retained-quota increase; keep TTL and per-run limits unchanged and never decrease quota")
	}
	if settings.MaxRetainedBytes == p.MaxRetainedBytes {
		return nil
	}
	if p.Revision == math.MaxInt64 {
		return policyMismatch(p.Revision)
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, s.db, "runs")
	if err != nil {
		return upgradeError(err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE run_policy SET max_retained_bytes=?,revision=revision+1
 WHERE singleton=1 AND revision=? AND schema_version=? AND ttl_hours=? AND max_run_bytes=? AND max_retained_bytes=?`, settings.MaxRetainedBytes, p.Revision, p.SchemaVersion, p.TTLHours, p.MaxRunBytes, p.MaxRetainedBytes)
	if err != nil {
		return upgradeError(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return policyMismatch(p.Revision)
	}
	if err := tx.Commit(); err != nil {
		return upgradeError(err)
	}
	// Record the known committed policy before any subsequent read can fail.
	s.policy.MaxRetainedBytes = settings.MaxRetainedBytes
	s.policy.Revision++
	return nil
}
