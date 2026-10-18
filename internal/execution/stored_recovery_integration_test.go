//go:build linux && integration

package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const storedFixtureKey = "synthetic-encrypted-recovery-key"

type storedIdentity struct {
	ID, Generation string
	Slot           int
}

type gatedStoredCapture struct {
	CaptureStore
	dir string
}

func (s *gatedStoredCapture) Close() error {
	if err := os.WriteFile(filepath.Join(s.dir, "closing"), nil, 0600); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(s.dir, "release-close")); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.CaptureStore.Close()
}

func openStoredFixture(ctx context.Context, start Startup, key string, leases Leases) (*Run, error) {
	run, err := OpenStoredRun(ctx, start, key, leases)
	if err != nil {
		return nil, err
	}
	if os.Getenv("CAPY_STORED_MODE") == "delayed cleanup" {
		run.Store = &gatedStoredCapture{CaptureStore: run.Store, dir: os.Getenv("CAPY_STORED_FIXTURE")}
	}
	return run, nil
}

func runStoredFixtureOwner() int {
	dir := os.Getenv("CAPY_STORED_FIXTURE")
	path, err := config.ResolveRunDBPath(dir)
	if err != nil {
		return 1
	}
	st, err := runstore.OpenWithPolicy(context.Background(), path, storedFixtureKey, true, runstore.PolicyConfig{TTLHours: 9, MaxRunBytes: 1 << 20, MaxRetainedBytes: 512 << 20})
	if err != nil {
		return 1
	}
	defer st.Close()
	owner, err := st.ClaimOwner(context.Background())
	if err != nil {
		return 1
	}
	defer owner.Lease.Close()
	deadline := time.Now().Add(time.Minute)
	row, err := st.Admit(context.Background(), owner, runstore.Program{Owner: dir, TTLHours: 9, MaxRunBytes: 1 << 20, Deadline: deadline,
		Request: executor.ExecRequest{Language: executor.Shell, Code: "printf early; echo $$ > leader.pid; sleep 60 & echo $! > child.pid; wait"}})
	if err != nil {
		return 1
	}
	identity, err := json.Marshal(storedIdentity{row.ID, owner.ID, owner.Slot})
	if err != nil || os.WriteFile(filepath.Join(dir, "identity.json"), identity, 0600) != nil {
		return 1
	}
	if os.Getenv("CAPY_STORED_MODE") == "before spawn" {
		for {
			time.Sleep(time.Second)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	helper, err := Launch(context.Background(), exe, Startup{Version: protocolVersion, RunID: row.ID, Generation: owner.ID, StorePath: path, Deadline: deadline}, storedFixtureKey,
		Leases{Owner: owner.Lease, Lifecycle: st.LifecycleLease()})
	if err != nil {
		if helper != nil {
			helper.Wait()
		}
		return 1
	}
	if err := os.WriteFile(filepath.Join(dir, "helper.pid"), []byte(strconv.Itoa(helper.cmd.Process.Pid)), 0600); err != nil {
		helper.Cancel()
		helper.Wait()
		return 1
	}
	_, err = helper.Wait()
	if err != nil {
		return 1
	}
	return 0
}

func startStoredOwner(t *testing.T, mode string) (string, *exec.Cmd, storedIdentity) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, dir)
	}
	t.Setenv("CAPY_DB_KEY", storedFixtureKey)
	t.Setenv("CAPY_VAULT_KEY", "synthetic-vault-key")
	exe, err := os.Executable()
	require.NoError(t, err)
	owner := exec.Command(exe, "stored-fixture-owner")
	owner.Env = append(os.Environ(), "CAPY_STORED_FIXTURE="+dir, "CAPY_STORED_MODE="+mode)
	owner.Dir = dir
	require.NoError(t, owner.Start())
	t.Cleanup(func() { owner.Process.Kill() })
	var identity storedIdentity
	require.NoError(t, json.Unmarshal(waitFile(t, filepath.Join(dir, "identity.json")), &identity))
	return dir, owner, identity
}

func storedPID(t *testing.T, dir, name string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(string(waitFile(t, filepath.Join(dir, name)))))
	require.NoError(t, err)
	return pid
}

func openStoredObserver(t *testing.T, dir string) *runstore.Store {
	t.Helper()
	path, err := config.ResolveRunDBPath(dir)
	require.NoError(t, err)
	st, err := runstore.Open(t.Context(), path, storedFixtureKey, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func TestStoredOwnerCrashBeforeSpawnRecovers(t *testing.T) {
	dir, owner, identity := startStoredOwner(t, "before spawn")
	require.NoError(t, owner.Process.Kill())
	require.Error(t, owner.Wait())
	st := openStoredObserver(t, dir)
	row, err := st.Load(t.Context(), identity.ID)
	require.NoError(t, err)
	require.Equal(t, "interrupted", row.Result.State)
	require.Equal(t, "not_started", row.Result.Capture.State)
	require.Equal(t, 9*time.Hour, row.Result.ExpiresAt.Sub(*row.Result.CompletedAt))
	require.Nil(t, row.Result.ExitCode)
	replacement, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer replacement.Lease.Close()
	require.Equal(t, identity.Slot, replacement.Slot)
	require.NotEqual(t, identity.Generation, replacement.ID)
	require.NoFileExists(t, filepath.Join(dir, "leader.pid"))
}

func TestStoredOwnerSIGKILLRetainsLeasesUntilHelperClose(t *testing.T) {
	dir, owner, identity := startStoredOwner(t, "delayed cleanup")
	helperPID := storedPID(t, dir, "helper.pid")
	childPID := storedPID(t, dir, "child.pid")
	// Stop an idle helper, not a SQLite writer mid-transaction. The command is
	// silent after this prefix, so its committed capture proves that append has
	// released the writer lock before the SIGSTOP below.
	ready := openStoredObserver(t, dir)
	require.Eventually(t, func() bool {
		row, err := ready.Load(t.Context(), identity.ID)
		return err == nil && row.Result.Capture.CapturedBytes == 5
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, ready.Close())
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			unix.Kill(helperPID, unix.SIGCONT)
			os.WriteFile(filepath.Join(dir, "release-close"), nil, 0600)
			reapOrphan(t, helperPID)
		}
	})
	require.NoError(t, unix.Kill(helperPID, unix.SIGSTOP))
	require.NoError(t, owner.Process.Kill())
	require.Error(t, owner.Wait())
	root := filepath.Join(dir, ".capy", "runs")
	probeLease(t, root, "lifecycle.lock", true)
	require.ErrorContains(t, runstore.Reset(t.Context(), filepath.Join(root, "runs.db")), "reset refused", "owner SIGKILL cannot release the helper's reset protection")
	changed := runstore.PolicyConfig{TTLHours: 1, MaxRunBytes: 1 << 20, MaxRetainedBytes: 256 << 20}
	_, err := runstore.OpenWithPolicy(t.Context(), filepath.Join(root, "runs.db"), storedFixtureKey, false, changed)
	require.ErrorContains(t, err, "stop", "the stopped helper's inherited lease blocks a policy update after coordinator death")
	st := openStoredObserver(t, dir)
	row, err := st.Load(t.Context(), identity.ID)
	require.NoError(t, err)
	require.Equal(t, identity.Generation, row.Generation)
	require.Equal(t, "pending", row.Cleanup)
	require.Nil(t, row.Result.Error, "a held helper lease is not an abandoned run")
	replacement, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, identity.Slot, replacement.Slot)
	require.NoError(t, replacement.Lease.Close())
	require.NoError(t, st.Close())
	require.NoError(t, unix.Kill(childPID, 0), "child remains live while cleanup is stopped")
	require.NoError(t, unix.Kill(helperPID, unix.SIGCONT))
	waitFile(t, filepath.Join(dir, "closing"))
	require.ErrorIs(t, unix.Kill(childPID, 0), unix.ESRCH)
	probeLease(t, root, "lifecycle.lock", true)
	probeLease(t, root, filepath.Join("locks", "owner-00.lock"), true)
	observer := openStoredObserver(t, dir)
	row, err = observer.Load(t.Context(), identity.ID)
	require.NoError(t, err)
	require.Equal(t, "retired", row.Cleanup)
	require.Equal(t, "interrupted", row.Result.State)
	require.True(t, row.Result.Capture.TerminalPersisted)
	require.NoError(t, observer.Close())
	require.ErrorContains(t, runstore.Reset(t.Context(), filepath.Join(root, "runs.db")), "reset refused", "retired metadata alone cannot permit reset before helper close")
	_, err = runstore.OpenWithPolicy(t.Context(), filepath.Join(root, "runs.db"), storedFixtureKey, false, changed)
	require.ErrorContains(t, err, "stop", "terminal persistence does not release helper policy protection before DB close")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "release-close"), nil, 0600))
	reapOrphan(t, helperPID)
	reaped = true
	probeLease(t, root, "lifecycle.lock", false)
	probeLease(t, root, filepath.Join("locks", "owner-00.lock"), false)
	final, err := runstore.OpenWithPolicy(t.Context(), filepath.Join(root, "runs.db"), storedFixtureKey, false, changed)
	require.NoError(t, err)
	defer final.Close()
	require.EqualValues(t, 2, final.Policy().Revision)
	recovered, err := final.Load(t.Context(), identity.ID)
	require.NoError(t, err)
	require.Equal(t, 9*time.Hour, recovered.Result.ExpiresAt.Sub(*recovered.Result.CompletedAt))
	last, err := final.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer last.Lease.Close()
	require.Equal(t, identity.Slot, last.Slot)
	require.NoError(t, last.Lease.Close())
	require.NoError(t, final.Close())
	require.NoError(t, runstore.Reset(t.Context(), filepath.Join(root, "runs.db")))
	require.NoFileExists(t, filepath.Join(root, "runs.db"))
}

func TestStoredHelperLossQuarantinesWithoutSignallingMetadata(t *testing.T) {
	dir, owner, identity := startStoredOwner(t, "helper loss")
	helperPID := storedPID(t, dir, "helper.pid")
	leaderPID := storedPID(t, dir, "leader.pid")
	childPID := storedPID(t, dir, "child.pid")
	// This test owns these live fixture processes. Recovery itself has no PID
	// metadata, no process adoption and no signalling operation.
	t.Cleanup(func() {
		unix.Kill(leaderPID, unix.SIGKILL)
		unix.Kill(childPID, unix.SIGKILL)
		reapOrphan(t, leaderPID)
		reapOrphan(t, childPID)
	})
	require.NoError(t, unix.Kill(helperPID, unix.SIGSTOP))
	require.NoError(t, owner.Process.Kill())
	require.Error(t, owner.Wait())
	require.NoError(t, unix.Kill(helperPID, unix.SIGKILL))
	reapOrphan(t, helperPID)
	st := openStoredObserver(t, dir)
	row, err := st.Load(t.Context(), identity.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", row.Cleanup)
	require.Equal(t, "cleanup_unverified", row.Result.Error.Code)
	require.Nil(t, row.Result.ExpiresAt)
	_, err = st.ClaimOwner(t.Context())
	require.ErrorContains(t, err, "unverified cleanup")
	require.NoError(t, unix.Kill(leaderPID, 0))
	require.NoError(t, unix.Kill(childPID, 0))
	page, err := st.ReadRaw(t.Context(), identity.ID, "stdout", "", 4096)
	require.NoError(t, err)
	require.Equal(t, "cleanup_unverified", page.Run.Error.Code)
}
