package runstore

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func policyPath(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, root)
	}
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	return path
}

func TestPolicyRejectsMismatchWhileAnyHandleIsAttached(t *testing.T) {
	path := policyPath(t)
	settings := defaultPolicyConfig()
	st, err := OpenWithPolicy(t.Context(), path, testKey, true, settings)
	require.NoError(t, err)
	defer st.Close()
	require.EqualValues(t, 1, st.Policy().Revision)
	reader, err := Open(t.Context(), path, testKey, false)
	require.NoError(t, err)
	defer reader.Close()
	require.Equal(t, st.Policy(), reader.Policy())
	for _, field := range []string{"ttl", "capture", "retained"} {
		t.Run(field, func(t *testing.T) {
			changed := settings
			switch field {
			case "ttl":
				changed.TTLHours++
			case "capture":
				changed.MaxRunBytes--
			case "retained":
				changed.MaxRetainedBytes--
			}
			other, err := OpenWithPolicy(t.Context(), path, testKey, true, changed)
			require.Nil(t, other)
			var typed *Error
			require.ErrorAs(t, err, &typed)
			require.Equal(t, "policy_mismatch", typed.Code)
			require.Contains(t, typed.Message, "revision 1")
			require.Contains(t, typed.Message, "revision 2")
			require.Contains(t, typed.Message, "stop")
		})
	}
	require.NoError(t, st.Close())
	changed := settings
	changed.TTLHours = 1
	_, err = OpenWithPolicy(t.Context(), path, testKey, false, changed)
	require.ErrorContains(t, err, "stop", "a query-only handle also prevents policy change")
	require.NoError(t, reader.Close())
	updated, err := OpenWithPolicy(t.Context(), path, testKey, false, changed)
	require.NoError(t, err)
	defer updated.Close()
	require.EqualValues(t, 2, updated.Policy().Revision)
	require.Equal(t, changed, updated.Policy().PolicyConfig)
	require.Equal(t, st.ID(), updated.ID())
	require.Equal(t, st.Policy().AccountingMode, updated.Policy().AccountingMode)
}

func TestPolicySnapshotsAdmissionAndPreservesTerminalExpiry(t *testing.T) {
	path := policyPath(t)
	settings := PolicyConfig{TTLHours: 9, MaxRunBytes: 32, MaxRetainedBytes: 1 << 20}
	st, err := OpenWithPolicy(t.Context(), path, testKey, true, settings)
	require.NoError(t, err)
	defer st.Close()
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer owner.Lease.Close()
	row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{PolicyRevision: 99, TTLHours: 1, MaxRunBytes: math.MaxInt64})
	require.NoError(t, err)
	require.Equal(t, settings.TTLHours, row.Program.TTLHours)
	require.Equal(t, settings.MaxRunBytes, row.Program.MaxRunBytes)
	require.EqualValues(t, 1, row.Program.PolicyRevision)
	require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, executor.CleanupLinuxDescendants))
	require.NoError(t, st.Started(t.Context(), row.ID, "run"))
	require.ErrorContains(t, st.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: make([]byte, 33)}), "capture limit")
	require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true}))
	completed, err := st.Load(t.Context(), row.ID)
	require.NoError(t, err)
	require.Equal(t, 9*time.Hour, completed.Result.ExpiresAt.Sub(*completed.Result.CompletedAt))
	unfinished, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	// Model an owner loss without clean shutdown/recovery so the policy-changing
	// opener itself must reconcile the admitted run using its original TTL.
	st.maintenanceCancel()
	<-st.maintenanceDone
	require.NoError(t, owner.Lease.Close())
	require.NoError(t, st.closeHandles())
	st.closeOnce.Do(func() {})
	settings.TTLHours, settings.MaxRunBytes, settings.MaxRetainedBytes = 1, 16, 1<<19
	updated, err := OpenWithPolicy(t.Context(), path, testKey, false, settings)
	require.NoError(t, err)
	defer updated.Close()
	recovered, err := updated.Load(t.Context(), unfinished.ID)
	require.NoError(t, err)
	require.Equal(t, "interrupted", recovered.Result.State)
	require.EqualValues(t, 1, recovered.Program.PolicyRevision)
	require.Equal(t, 9*time.Hour, recovered.Result.ExpiresAt.Sub(*recovered.Result.CompletedAt))
	still, err := updated.Load(t.Context(), row.ID)
	require.NoError(t, err)
	require.Equal(t, completed.Result, still.Result)
	newOwner, err := updated.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer newOwner.Lease.Close()
	newRun, err := updated.Admit(t.Context(), newOwner, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	require.EqualValues(t, 2, newRun.Program.PolicyRevision)
	require.NoError(t, updated.Finish(t.Context(), newRun.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupNotSpawned}}))
	newRun, err = updated.Load(t.Context(), newRun.ID)
	require.NoError(t, err)
	require.Equal(t, time.Hour, newRun.Result.ExpiresAt.Sub(*newRun.Result.CompletedAt))
	require.Error(t, updated.Finish(t.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupNotSpawned}}), "terminal expiry cannot be overwritten")
}

func TestPolicyInvalidConfigHasNoFilesystemEffects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings PolicyConfig
	}{
		{"zero", PolicyConfig{}},
		{"duration overflow", PolicyConfig{TTLHours: int(math.MaxInt64/int64(time.Hour)) + 1, MaxRunBytes: 1, MaxRetainedBytes: 1}},
		{"negative limit", PolicyConfig{TTLHours: 1, MaxRunBytes: -1, MaxRetainedBytes: 1}},
		{"inverted limits", PolicyConfig{TTLHours: 1, MaxRunBytes: 2, MaxRetainedBytes: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := policyPath(t)
			_, err := OpenWithPolicy(t.Context(), path, testKey, true, tc.settings)
			require.Error(t, err)
			require.NoDirExists(t, filepath.Dir(filepath.Dir(path)))
		})
	}
}

func TestPolicyAccountingModeCannotSilentlyChange(t *testing.T) {
	path := policyPath(t)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	available, err := dbstatAvailable(t.Context(), st.db)
	require.NoError(t, err)
	want := "conservative"
	if available {
		want = "dbstat"
	}
	require.Equal(t, want, st.Policy().AccountingMode)
	// Simulate a persisted policy from a build with dbstat. Builds unable to
	// honor it must refuse both configured writers and helper/read attachments.
	_, err = st.db.ExecContext(t.Context(), "UPDATE run_policy SET accounting_mode='dbstat' WHERE singleton=1")
	require.NoError(t, err)
	require.NoError(t, st.Close())
	for _, configured := range []bool{false, true} {
		var reopened *Store
		if configured {
			reopened, err = OpenWithPolicy(t.Context(), path, testKey, false, defaultPolicyConfig())
		} else {
			reopened, err = Open(t.Context(), path, testKey, false)
		}
		if available {
			require.NoError(t, err)
			require.Equal(t, "dbstat", reopened.Policy().AccountingMode)
			require.NoError(t, reopened.Close())
		} else {
			require.Nil(t, reopened)
			require.ErrorContains(t, err, "requires dbstat")
		}
	}
}

func TestCompetingPolicyOpenersNeverAttachDifferentRevisions(t *testing.T) {
	path := policyPath(t)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	for range 5 {
		start, release := make(chan struct{}), make(chan struct{})
		type answer struct {
			store *Store
			err   error
		}
		answers := make(chan answer, 2)
		var wg sync.WaitGroup
		for _, ttl := range []int{2, 3} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				settings := defaultPolicyConfig()
				settings.TTLHours = ttl
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				attached, err := OpenWithPolicy(ctx, path, testKey, false, settings)
				answers <- answer{attached, err}
				<-release
				if attached != nil {
					attached.Close()
				}
			}()
		}
		close(start)
		first, second := <-answers, <-answers
		close(release)
		wg.Wait()
		require.False(t, first.err == nil && second.err == nil, "conflicting policies cannot attach together")
	}
}

func TestPolicyChangePreservesPermanentLocksAndResetExclusion(t *testing.T) {
	path := policyPath(t)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	before, err := st.lifecycle.Stat()
	require.NoError(t, err)
	require.NoError(t, st.Close())
	reset, err := openRegular(filepath.Join(filepath.Dir(path), "lifecycle.lock"), false)
	require.NoError(t, err)
	defer reset.Close()
	require.NoError(t, unix.Flock(int(reset.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	settings := defaultPolicyConfig()
	settings.TTLHours++
	_, err = OpenWithPolicy(t.Context(), path, testKey, false, settings)
	require.ErrorContains(t, err, "lifecycle is busy")
	require.NoError(t, reset.Close())
	st, err = OpenWithPolicy(t.Context(), path, testKey, false, settings)
	require.NoError(t, err)
	defer st.Close()
	after, err := st.lifecycle.Stat()
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	probe, err := openRegular(filepath.Join(filepath.Dir(path), "lifecycle.lock"), false)
	require.NoError(t, err)
	defer probe.Close()
	require.True(t, errors.Is(unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB), unix.EWOULDBLOCK))
}

func TestPolicyUpdateFailureLeavesRevisionAndReleasesLeases(t *testing.T) {
	for _, mode := range []string{"unverified cleanup", "write failure", "cancelled", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			path := policyPath(t)
			st, err := Open(t.Context(), path, testKey, true)
			require.NoError(t, err)
			defer st.Close()
			revision := int64(1)
			switch mode {
			case "unverified cleanup":
				owner, err := st.ClaimOwner(t.Context())
				require.NoError(t, err)
				row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{})
				require.NoError(t, err)
				require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, executor.CleanupLinuxDescendants))
				require.NoError(t, owner.Lease.Close())
			case "write failure":
				_, err = st.db.ExecContext(t.Context(), `CREATE TRIGGER test_policy_failure BEFORE UPDATE ON run_policy BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END`)
				require.NoError(t, err)
			case "exhausted":
				revision = math.MaxInt64
				_, err = st.db.ExecContext(t.Context(), "UPDATE run_policy SET revision=?", revision)
				require.NoError(t, err)
			}
			require.NoError(t, st.Close())
			settings := defaultPolicyConfig()
			settings.TTLHours = 1
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			changed, err := OpenWithPolicy(ctx, path, testKey, false, settings)
			require.Error(t, err)
			require.Nil(t, changed)
			probe, err := openRegular(filepath.Join(filepath.Dir(path), "lifecycle.lock"), false)
			require.NoError(t, err)
			require.NoError(t, unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB))
			require.NoError(t, probe.Close())
			original, err := Open(t.Context(), path, testKey, false)
			require.NoError(t, err)
			defer original.Close()
			require.Equal(t, revision, original.Policy().Revision)
			require.Equal(t, defaultPolicyConfig(), original.Policy().PolicyConfig)
		})
	}
}

func TestUnsupportedPolicyVersionsFailWithoutRepair(t *testing.T) {
	for _, column := range []string{"schema_version", "redaction_version"} {
		t.Run(column, func(t *testing.T) {
			path := policyPath(t)
			st, err := Open(t.Context(), path, testKey, true)
			require.NoError(t, err)
			// Column comes only from this fixed test table.
			_, err = st.db.ExecContext(t.Context(), "UPDATE run_policy SET "+column+"=999")
			require.NoError(t, err)
			require.NoError(t, st.Close())
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			_, err = OpenWithPolicy(t.Context(), path, testKey, false, defaultPolicyConfig())
			require.ErrorContains(t, err, "unsupported")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
