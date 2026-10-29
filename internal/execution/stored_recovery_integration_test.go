//go:build linux && integration

package execution

import (
	"context"
	"encoding/json"
	"errors"
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

type failedCloseStoredCapture struct{ CaptureStore }

type failedFinishStoredCapture struct {
	CaptureStore
	lost bool
}

func (s *failedFinishStoredCapture) Finish(context.Context, Outcome) error {
	if s.lost {
		// Cleanup completed locally, but no terminal write or notice survives.
		os.Exit(74)
	}
	return errors.New("synthetic helper finalization failure")
}

func (s *failedCloseStoredCapture) Close() error {
	return errors.Join(s.CaptureStore.Close(), errors.New("synthetic helper close failure"))
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
	if os.Getenv("CAPY_STORED_MODE") == "hold startup" {
		dir := os.Getenv("CAPY_STORED_FIXTURE")
		if err := os.WriteFile(filepath.Join(dir, "opening.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			run.Store.Close()
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "opening"), nil, 0600); err != nil {
			run.Store.Close()
			return nil, err
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "release-open")); err == nil {
				break
			}
			if err := ctx.Err(); err != nil {
				run.Store.Close()
				return nil, err
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if os.Getenv("CAPY_STORED_MODE") == "delayed cleanup" {
		run.Store = &gatedStoredCapture{CaptureStore: run.Store, dir: os.Getenv("CAPY_STORED_FIXTURE")}
	}
	if os.Getenv("CAPY_STORED_MODE") == "unstarted failed close" {
		run.Request.Language = executor.Language("missing-fixture-runtime")
		run.Store = &failedCloseStoredCapture{CaptureStore: run.Store}
	}
	if os.Getenv("CAPY_STORED_MODE") == "failed close" {
		run.Store = &failedCloseStoredCapture{CaptureStore: run.Store}
	}
	if mode := os.Getenv("CAPY_STORED_MODE"); mode == "failed finish" || mode == "helper loss" {
		run.Store = &failedFinishStoredCapture{CaptureStore: run.Store, lost: mode == "helper loss"}
	}
	return run, nil
}

func TestStoredFinalizationFailureQuarantinesLiveOwner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPY_STORED_FIXTURE", dir)
	t.Setenv("CAPY_STORED_MODE", "failed finish")
	s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	result := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "printf x >> once; exit 7"}, "", 30*time.Second)
	exit := 7
	require.Equal(t, &exit, result.ExitCode)
	require.Equal(t, executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupPending}, result.Cleanup)
	require.False(t, result.Capture.TerminalPersisted)
	assertRuntimeIncident(t, s, *result.RunID, "", "finalization_unpersisted", "finalization", "run")
	next := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}, "", time.Second)
	require.Nil(t, next.RunID)
	require.Equal(t, "cleanup_unverified", next.Error.Code)
	_, err := s.store.Admit(t.Context(), s.owner, executor.CleanupLinuxDescendants,
		runstore.Program{Owner: dir, Request: executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}})
	require.ErrorContains(t, err, "unverified cleanup")
	count, err := os.ReadFile(filepath.Join(dir, "once"))
	require.NoError(t, err)
	require.Equal(t, "x", string(count))
	require.NoFileExists(t, filepath.Join(dir, "forbidden"))
}

func TestStoredUnavailableRefreshNeverPublishesAdmissionProof(t *testing.T) {
	for _, mode := range []string{"single terminal", "single yielded", "batch terminal"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
				t.Setenv(name, dir)
			}
			t.Setenv("CAPY_STORED_FIXTURE", dir)
			fixture := "failed close"
			if mode == "single yielded" {
				fixture = "hold startup"
			}
			t.Setenv("CAPY_STORED_MODE", fixture)
			s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
			t.Cleanup(func() {
				// Release both fixture gates even after an assertion failure.
				require.NoError(t, os.WriteFile(filepath.Join(dir, "release-open"), nil, 0600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "release-command"), nil, 0600))
				require.NoError(t, s.Close())
			})
			const program = "printf x >> executed-once; touch running; while [ ! -e release-command ]; do sleep .01; done; exit 7"
			done := make(chan struct{})
			var result runstore.Result
			var batch runstore.BatchResult
			var executeErr error
			go func() {
				defer close(done)
				switch mode {
				case "single yielded":
					result, executeErr = s.ExecuteResponse(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: program}, "", 30*time.Second, 0, func(runstore.Result) error { return nil })
				case "batch terminal":
					batch, executeErr = s.ExecuteBatchResponse(t.Context(), runstore.BatchInput{Concurrency: 1, Commands: []runstore.BatchCommand{{Command: program}}}, 30*time.Second, 30*time.Second, func(runstore.BatchResult) error { return nil })
				default:
					result = s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: program}, "", 30*time.Second)
				}
			}()
			marker := "running"
			if mode == "single yielded" {
				marker = "opening"
			}
			waitFile(t, filepath.Join(dir, marker))
			s.mu.Lock()
			st := s.store
			s.mu.Unlock()
			// Fail the owner's refreshes while the inherited helper retains its
			// independent authenticated connection and acquired leases.
			require.NoError(t, st.Close())
			if mode == "single yielded" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "release-open"), nil, 0600))
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "release-command"), nil, 0600))
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("response did not return")
			}
			require.NoError(t, executeErr)
			proof, problem := result.Cleanup, result.Error
			if mode == "batch terminal" {
				proof, problem = batch.Cleanup, batch.Error
			}
			require.Equal(t, executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupPending}, proof)
			require.NotNil(t, problem)
			if mode == "single yielded" {
				waitFile(t, filepath.Join(dir, "running"))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "release-command"), nil, 0600))
			}
			s.wg.Wait()
			require.Equal(t, []byte("x"), waitFile(t, filepath.Join(dir, "executed-once")))
			reader, err := runstore.Open(t.Context(), st.Path(), storedFixtureKey, false)
			require.NoError(t, err)
			defer reader.Close()
			page, err := reader.List(t.Context(), runstore.ListOptions{Limit: 1})
			require.NoError(t, err)
			require.Len(t, page.Entries, 1)
			stored, err := reader.Load(t.Context(), page.Entries[0].RunID)
			require.NoError(t, err)
			require.Equal(t, executor.CleanupRetired, stored.Cleanup.State)
			require.Equal(t, 7, *stored.Result.ExitCode)
		})
	}
}

func TestStoredUnstartedTerminalSurvivesHelperCloseFailure(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, dir)
	}
	t.Setenv("CAPY_STORED_FIXTURE", dir)
	t.Setenv("CAPY_STORED_MODE", "unstarted failed close")
	s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	result := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}, "", 30*time.Second)
	require.Equal(t, "start_failed", result.State)
	require.NotNil(t, result.Error)
	require.Equal(t, "runtime_unavailable", result.Error.Code)
	require.True(t, result.Capture.TerminalPersisted)
	require.Equal(t, executor.CleanupNotSpawned, result.Cleanup.State)
	require.NotNil(t, result.CompletedAt)
	require.NoFileExists(t, filepath.Join(dir, "forbidden"))
	stored, err := s.Status(t.Context(), *result.RunID)
	require.NoError(t, err)
	require.Equal(t, stored, result)
	require.False(t, s.unverified)
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
	row, err := st.Admit(context.Background(), owner, executor.CleanupLinuxDescendants, runstore.Program{Owner: dir, TTLHours: 9, MaxRunBytes: 1 << 20, Deadline: deadline,
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
	refs, err := st.BorrowLeases(owner)
	if err != nil {
		return 1
	}
	helper, err := Launch(context.Background(), exe, Startup{Version: protocolVersion, RunID: row.ID, Generation: owner.ID, StorePath: path, Deadline: deadline, CleanupScope: linuxExchangeScope}, storedFixtureKey, refs)
	err = errors.Join(err, refs.Close())
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
	require.Equal(t, "pending", string(row.Cleanup.State))
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
	require.Equal(t, "retired", string(row.Cleanup.State))
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
	require.Equal(t, "pending", string(row.Cleanup.State))
	require.Equal(t, "cleanup_unverified", row.Result.Error.Code)
	require.Nil(t, row.Result.ExpiresAt)
	_, err = st.ClaimOwner(t.Context())
	require.ErrorContains(t, err, "unverified cleanup")
	require.NoError(t, unix.Kill(leaderPID, 0))
	require.NoError(t, unix.Kill(childPID, 0))
	page, err := st.ReadRaw(t.Context(), identity.ID, "stdout", "", 4096)
	require.NoError(t, err)
	require.Equal(t, "cleanup_unverified", page.Run.Error.Code)
	view, err := runstore.InspectRuns(t.Context(), st.Path(), storedFixtureKey, runstore.InspectionOptions{RunID: identity.ID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, view.Subjects, 1)
	require.Equal(t, "inferred", view.Subjects[0].Attribution)
	require.Equal(t, "reconciliation", view.Subjects[0].DetectionStage)
	require.Equal(t, "pending_cleanup", view.Subjects[0].Reason)
	require.Equal(t, "run", view.Subjects[0].Phase)
}

func TestStoredStoppedStartupRecoversWithoutRestart(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CAPY_STORED_FIXTURE", dir)
			t.Setenv("CAPY_STORED_MODE", "hold startup")
			s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan runstore.Result, 1)
			go func() {
				done <- s.Execute(ctx, executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}, "", 30*time.Second)
			}()
			pid, err := strconv.Atoi(string(waitFile(t, filepath.Join(dir, "opening.pid"))))
			require.NoError(t, err)
			require.NoError(t, unix.Kill(pid, unix.SIGSTOP))
			t.Cleanup(func() { unix.Kill(pid, unix.SIGCONT) })
			if mode == "cancel" {
				cancel()
			}
			var result runstore.Result
			select {
			case result = <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("pre-start teardown did not finish")
			}
			require.Equal(t, executor.CleanupNotSpawned, result.Cleanup.State)
			require.True(t, result.Capture.TerminalPersisted)
			require.ErrorIs(t, unix.Kill(pid, 0), unix.ESRCH)
			require.NoFileExists(t, filepath.Join(dir, "forbidden"))
			require.False(t, s.unverified)
			t.Setenv("CAPY_STORED_MODE", "")
			next := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "printf x >> executed-once"}, "", 30*time.Second)
			require.Equal(t, executor.CleanupRetired, next.Cleanup.State)
			require.Equal(t, []byte("x"), waitFile(t, filepath.Join(dir, "executed-once")))
		})
	}
}

func TestStoredGateDeadlineIsKnownUnstarted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPY_STORED_FIXTURE", dir)
	s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	st, owner, err := s.attached(t.Context(), true)
	require.NoError(t, err)
	refs, err := st.BorrowLeases(owner)
	require.NoError(t, err)
	defer refs.Close()
	gate, err := refs.AcquireExchange(t.Context())
	require.NoError(t, err)
	defer gate.Release()
	result := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}, "", 100*time.Millisecond)
	require.Equal(t, "timed_out", result.State)
	require.Equal(t, executor.CleanupNotSpawned, result.Cleanup.State)
	require.True(t, result.Capture.TerminalPersisted)
	require.False(t, s.unverified)
	require.NoFileExists(t, filepath.Join(dir, "forbidden"))
	require.NoError(t, gate.Release())
	next := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "true"}, "", 30*time.Second)
	require.Equal(t, executor.CleanupRetired, next.Cleanup.State)
}

func TestStoredCompilerRetirementSurvivesRuntimeSpawnFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPY_STORED_FIXTURE", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rustc"), []byte("#!/bin/sh\nprintf compiler-output\nexit 0\n"), 0700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	result := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Rust, Code: "synthetic compiler"}, "", 30*time.Second)
	require.Equal(t, "start_failed", result.State)
	require.Equal(t, "runtime_unavailable", result.Error.Code)
	require.Equal(t, executor.CleanupRetired, result.Cleanup.State)
	require.Equal(t, "complete", result.Capture.State)
	row, err := s.store.Load(t.Context(), *result.RunID)
	require.NoError(t, err)
	require.Equal(t, "compile", row.LastPhase)
	page, err := s.ReadRaw(t.Context(), *result.RunID, "stdout", "", 4096)
	require.NoError(t, err)
	require.Equal(t, []byte("compiler-output"), page.Fragments[0].Data)
}

func TestStoredUnverifiedReapRetainsReservation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPY_STORED_FIXTURE", dir)
	t.Setenv("CAPY_STORED_MODE", "hold startup")
	launcher := newLeaseLauncher()
	launcher.wait = func(*exec.Cmd) error { return errors.New("injected wait failure without reap") }
	s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
	s.launch = launcher.launchSupervisor
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan runstore.Result, 1)
	go func() {
		done <- s.Execute(ctx, executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}, "", 30*time.Second)
	}()
	waitFile(t, filepath.Join(dir, "opening"))
	cancel()
	var result runstore.Result
	select {
	case result = <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("unverified teardown response blocked")
	}
	launcher.mu.Lock()
	var actor *exchangeProcess
	for p, blocked := range launcher.reservations {
		if blocked {
			actor = p
		}
	}
	launcher.mu.Unlock()
	require.NotNil(t, actor)
	t.Cleanup(func() {
		// The injected waiter did not call Wait. Restore its sole reap explicitly;
		// production deliberately retains protection when that proof is unavailable.
		_ = actor.cmd.Wait()
		require.NotNil(t, actor.cmd.ProcessState)
		require.NoError(t, actor.gate.Release())
		require.NoError(t, actor.refs.Close())
		close(actor.reaped)
		require.NoError(t, s.Close())
	})
	require.Equal(t, "cleanup_unverified", result.Error.Code)
	require.False(t, result.Capture.TerminalPersisted)
	assertRuntimeIncident(t, s, *result.RunID, "", "startup_uncertain", "startup", "admitted")
	active, _ := s.WorkCounts()
	require.Equal(t, 1, active)
	require.Error(t, runstore.Reset(t.Context(), s.store.Path()))
	next := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch another"}, "", time.Second)
	require.Nil(t, next.RunID)
	require.Equal(t, "cleanup_unverified", next.Error.Code)
	require.NoFileExists(t, filepath.Join(dir, "forbidden"))
	require.NoFileExists(t, filepath.Join(dir, "another"))
}
