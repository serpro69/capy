//go:build (linux || darwin) && integration

package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const leaseFixtureKey = "synthetic-exchange-test-key-1234567890"

func exchangeFixtureScope() string {
	if runtime.GOOS == "darwin" {
		return darwinExchangeScope
	}
	return linuxExchangeScope
}

func TestLeaseExchangeHelper(t *testing.T) {
	mode := os.Getenv("CAPY_LEASE_EXCHANGE_FIXTURE")
	if mode == "" {
		return
	}
	files, err := inheritedExchangeFiles()
	require.NoError(t, err)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	root := os.Getenv("CAPY_LEASE_EXCHANGE_ROOT")
	if mode == "stopped" {
		require.NoError(t, os.WriteFile(filepath.Join(root, "stopped.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600))
		require.NoError(t, unix.Kill(os.Getpid(), unix.SIGSTOP))
	}
	if mode == "identity" || mode == "independent" || mode == "swapped" || mode == "reflected" || mode == "replayed" || mode == "bad-marker" || mode == "wrong-step" {
		runIdentityFixture(t, files, mode)
		return
	}
	_, err = acceptLeaseStartup(t.Context(), files, exchangeFixtureScope(), func(ctx context.Context, start exchangeStartup, key string) error {
		if key != leaseFixtureKey {
			return errors.New("wrong fixture credential")
		}
		if err := os.WriteFile(filepath.Join(root, "bound"), nil, 0o600); err != nil {
			return err
		}
		if mode == "hold-binding" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	if mode == "stopped" {
		// Only the signal-failure control resumes this stopped helper. Buffered
		// exchange frames may still make it seek before the closed pipe is seen.
		require.Error(t, err)
		return
	}
	if mode == "partial" || mode == "cancel-ready" {
		require.Error(t, err)
		if mode == "partial" {
			// Keep a possibly-authorized helper alive past the owner's teardown
			// observation budget. The parent must not kill it to force a result.
			require.NoError(t, awaitLeaseFile(t.Context(), filepath.Join(root, "release")))
		}
		return
	}
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "authorized"), nil, 0o600))
	if mode == "hold-running" {
		_, err = io.Copy(io.Discard, files[0])
		require.NoError(t, err)
	}
}

func awaitLeaseFile(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type leaseFixture struct {
	store *runstore.Store
	owner *runstore.Owner
	refs  *runstore.LeaseRefs
	start exchangeStartup
	root  string
}

func newExchangeFixture(t *testing.T) leaseFixture {
	t.Helper()
	path, err := config.ResolveRunDBPath(t.TempDir())
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, leaseFixtureKey, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, o.Lease.Close()) })
	refs, err := s.BorrowLeases(o)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, refs.Close()) })
	return leaseFixture{s, o, refs, exchangeStartup{RunID: uuid.NewString(), Generation: o.ID,
		StorePath: executor.ByteString(path), CleanupScope: exchangeFixtureScope()}, t.TempDir()}
}

func (f leaseFixture) command(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^TestLeaseExchangeHelper$")
	cmd.Env = append(os.Environ(), "CAPY_LEASE_EXCHANGE_FIXTURE="+mode, "CAPY_LEASE_EXCHANGE_ROOT="+f.root)
	// Direct files avoid an unrelated os/exec pipe copier in the reaping proof.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd
}

func assertExchangeLocksHeld(t *testing.T, f leaseFixture) {
	t.Helper()
	for i, path := range []string{f.store.OwnerLeasePath(f.owner.Slot), filepath.Join(filepath.Dir(f.store.Path()), "lifecycle.lock")} {
		probe, err := os.OpenFile(path, os.O_RDWR, 0)
		require.NoError(t, err)
		err = unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		require.ErrorIs(t, err, unix.EWOULDBLOCK)
		require.NoError(t, probe.Close())
		if i == 1 {
			probe, err = os.OpenFile(path, os.O_RDWR, 0)
			require.NoError(t, err)
			require.NoError(t, unix.Flock(int(probe.Fd()), unix.LOCK_SH|unix.LOCK_NB))
			require.NoError(t, probe.Close())
		}
	}
	require.Error(t, runstore.Reset(t.Context(), f.store.Path()))
}

func TestLeaseExchangeLaunchAndPermanentFiles(t *testing.T) {
	f := newExchangeFixture(t)
	l := newLeaseLauncher()
	paths := []string{f.store.OwnerLeasePath(f.owner.Slot), filepath.Join(filepath.Dir(f.store.Path()), "lifecycle.lock")}
	before := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		var err error
		before[i], err = os.Stat(path)
		require.NoError(t, err)
	}
	p, err := l.launch(t.Context(), f.command(t, "hold-running"), f.start, leaseFixtureKey, f.refs)
	require.NoError(t, err)
	require.NoError(t, awaitLeaseFile(t.Context(), filepath.Join(f.root, "authorized")))
	require.NoError(t, f.refs.Close())
	require.NoError(t, f.owner.Lease.Close())
	require.NoError(t, f.store.Close())
	assertExchangeLocksHeld(t, f)
	p.cancelStartup() // post-authorization: EOF is cooperative cancellation
	require.NoError(t, p.Wait(t.Context()))
	require.False(t, p.knownNotSpawned())
	for i, path := range paths {
		after, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, os.SameFile(before[i], after))
		require.Equal(t, before[i].Mode(), after.Mode())
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Empty(t, data)
	}
	require.NoError(t, runstore.Reset(t.Context(), f.store.Path()))
}

func TestLeaseExchangeGateWaitDoesNotSpawn(t *testing.T) {
	f := newExchangeFixture(t)
	gate, err := f.refs.AcquireExchange(t.Context())
	require.NoError(t, err)
	defer gate.Release()
	l := newLeaseLauncher()
	cmd := f.command(t, "normal")
	start := f.start
	start.Deadline = time.Now().Add(40 * time.Millisecond)
	p, err := l.launch(t.Context(), cmd, start, leaseFixtureKey, f.refs)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, p)
	require.Nil(t, cmd.Process)
	require.NoFileExists(t, filepath.Join(f.root, "bound"))
	require.NoError(t, gate.Release())
	p, err = l.launch(t.Context(), f.command(t, "normal"), f.start, leaseFixtureKey, f.refs)
	require.NoError(t, err)
	require.NoError(t, p.Wait(t.Context()))
}

func TestLeaseExchangeStoppedHelperRecoversOwner(t *testing.T) {
	f := newExchangeFixture(t)
	l := newLeaseLauncher()
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	began := time.Now()
	p, err := l.launch(ctx, f.command(t, "stopped"), f.start, leaseFixtureKey, f.refs)
	require.Error(t, err)
	require.NotNil(t, p)
	require.FileExists(t, filepath.Join(f.root, "stopped.pid"))
	require.True(t, p.knownNotSpawned())
	require.Less(t, time.Since(began), 4*time.Second)
	require.NoFileExists(t, filepath.Join(f.root, "authorized"))
	second, err := l.launch(t.Context(), f.command(t, "normal"), f.start, leaseFixtureKey, f.refs)
	require.NoError(t, err, "same owner must recover without restart")
	require.NoError(t, second.Wait(t.Context()))
}

func TestLeaseExchangeDelayedReapRetainsReservation(t *testing.T) {
	f := newExchangeFixture(t)
	l := newLeaseLauncher()
	release := make(chan struct{})
	l.wait = func(cmd *exec.Cmd) error { <-release; return cmd.Wait() }
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	p, err := l.launch(ctx, f.command(t, "stopped"), f.start, leaseFixtureKey, f.refs)
	require.Error(t, err)
	require.NotNil(t, p)
	defer func() { close(release); p.Wait(context.Background()) }()
	require.False(t, p.knownNotSpawned())
	secondCmd := f.command(t, "normal")
	_, err = l.launch(t.Context(), secondCmd, f.start, leaseFixtureKey, f.refs)
	require.ErrorContains(t, err, "unresolved")
	require.Nil(t, secondCmd.Process)
	require.NoError(t, f.refs.Close())
	require.NoError(t, f.owner.Lease.Close())
	require.NoError(t, f.store.Close())
	assertExchangeLocksHeld(t, f)
	gateCtx, gateCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer gateCancel()
	_, err = p.refs.AcquireExchange(gateCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestLeaseExchangePartialStartNeverKillsHelper(t *testing.T) {
	f := newExchangeFixture(t)
	l := newLeaseLauncher()
	var kills atomic.Int32
	l.kill = func(p *os.Process) error { kills.Add(1); return p.Kill() }
	l.startWrite = func(w *os.File, _ leaseMessage) error {
		_, err := w.Write([]byte{0, 0}) // partial length header counts as possible authorization
		return errors.Join(err, errors.New("injected partial write"))
	}
	p, err := l.launch(t.Context(), f.command(t, "partial"), f.start, leaseFixtureKey, f.refs)
	require.Error(t, err)
	require.NotNil(t, p)
	defer func() { os.WriteFile(filepath.Join(f.root, "release"), nil, 0o600); p.Wait(context.Background()) }()
	require.Zero(t, kills.Load())
	require.False(t, p.knownNotSpawned())
	require.NoError(t, unix.Kill(p.cmd.Process.Pid, 0), "helper is still alive after teardown observation expires")
	// The ready token was returned, but the owner's retained reservation still
	// prevents another launch when this possibly-authorized actor is unresolved.
	gate, err := f.refs.AcquireExchange(t.Context())
	require.NoError(t, err)
	require.NoError(t, gate.Release())
	cmd := f.command(t, "normal")
	_, err = l.launch(t.Context(), cmd, f.start, leaseFixtureKey, f.refs)
	require.ErrorContains(t, err, "unresolved")
	require.Nil(t, cmd.Process)
	require.NoError(t, f.refs.Close())
	require.NoError(t, f.owner.Lease.Close())
	require.NoError(t, f.store.Close())
	assertExchangeLocksHeld(t, f)
}

func runIdentityFixture(t *testing.T, files []*os.File, mode string) {
	t.Helper()
	var start exchangeStartup
	require.NoError(t, files[0].SetReadDeadline(time.Now().Add(3*time.Second)))
	require.NoError(t, readFrame(files[0], &start))
	var key []byte
	require.NoError(t, readFrameLimit(files[1], &key, maxCredentialFrameBytes))
	files[1].Close()
	x := exchangeIO{input: files[0], output: files[2], id: start.ExchangeID, deadline: time.Now().Add(3 * time.Second)}
	leases := Leases{Owner: files[3], Lifecycle: files[4]}
	if mode == "independent" {
		// A distinct SH holder of the same lifecycle inode passes file identity
		// and shared-lock availability, but cannot share the coordinator offset.
		independent, err := os.OpenFile(filepath.Join(filepath.Dir(string(start.StorePath)), "lifecycle.lock"), os.O_RDWR, 0)
		require.NoError(t, err)
		defer independent.Close()
		require.NoError(t, unix.Flock(int(independent.Fd()), unix.LOCK_SH|unix.LOCK_NB))
		leases.Lifecycle = independent
	}
	if mode == "swapped" {
		leases.Owner, leases.Lifecycle = leases.Lifecycle, leases.Owner
	}
	if mode == "reflected" || mode == "replayed" || mode == "bad-marker" || mode == "wrong-step" {
		_, err := x.receive("lease_observe", "", "owner", 1)
		require.NoError(t, err)
		a, err := readLeaseMarker(leases.Owner)
		require.NoError(t, err)
		b, err := freshLeaseMarker(a)
		require.NoError(t, err)
		m := leaseMessage{Type: "lease_observed", Lease: "owner", Step: 1, Observed: a, Written: b}
		switch mode {
		case "replayed":
			x.id = uuid.NewString()
		case "bad-marker":
			m.Written = maxLeaseMarker + 1
		case "wrong-step":
			m.Step = 2
		}
		// No local seek to B: an echo of A plus a claimed B cannot pass.
		require.NoError(t, x.send(m))
		return
	}
	err := observeLeaseExchange(x, leases)
	if mode != "identity" {
		return
	} // coordinator must reject negative controls
	require.NoError(t, err)
	require.NoError(t, x.send(leaseMessage{Type: "ready", CleanupScope: start.CleanupScope}))
	_, err = x.receive("start", "", "", 0)
	require.NoError(t, err)
}

func TestLeaseExchangeRejectsFalseIdentity(t *testing.T) {
	for _, mode := range []string{"independent", "swapped", "reflected", "replayed", "bad-marker", "wrong-step"} {
		t.Run(mode, func(t *testing.T) {
			f := newExchangeFixture(t)
			l := newLeaseLauncher()
			p, err := l.launch(t.Context(), f.command(t, mode), f.start, leaseFixtureKey, f.refs)
			require.Error(t, err)
			require.NotNil(t, p)
			require.True(t, p.knownNotSpawned())
			require.NoFileExists(t, filepath.Join(f.root, "bound"))
		})
	}
}

func TestLeaseExchangeIdentityAloneAcceptsUnlockedAliases(t *testing.T) {
	// Deliberately bypass Task 3 and Linux fdinfo to isolate the identity-only
	// claim. No production launch constructor accepts these arbitrary files.
	root := t.TempDir()
	owner, err := os.CreateTemp(root, "unlocked-owner-")
	require.NoError(t, err)
	defer owner.Close()
	lifecycle, err := os.CreateTemp(root, "unlocked-lifecycle-")
	require.NoError(t, err)
	defer lifecycle.Close()
	if runtime.GOOS == "linux" {
		require.Error(t, validateLeases(owner, lifecycle), "Linux fdinfo rejects these aliases despite matching identity")
	}
	f := leaseFixture{root: root}
	cmd := f.command(t, "identity")
	var pipes []*os.File
	for range 3 {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		pipes = append(pipes, r, w)
	}
	defer func() {
		for _, file := range pipes {
			file.Close()
		}
	}()
	cmd.ExtraFiles = []*os.File{pipes[0], pipes[2], pipes[5], owner, lifecycle}
	require.NoError(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	pipes[0].Close()
	pipes[2].Close()
	pipes[5].Close()
	start := exchangeStartup{Version: 2, RunID: uuid.NewString(), Generation: uuid.NewString(), ExchangeID: uuid.NewString(), StorePath: executor.ByteString(root), CleanupScope: exchangeFixtureScope()}
	deadline := time.Now().Add(3 * time.Second)
	require.NoError(t, pipes[1].SetWriteDeadline(deadline))
	require.NoError(t, pipes[3].SetWriteDeadline(deadline))
	require.NoError(t, writeFrame(pipes[1], start))
	require.NoError(t, writeFrameLimit(pipes[3], []byte(leaseFixtureKey), maxCredentialFrameBytes))
	x := exchangeIO{input: pipes[4], output: pipes[1], id: start.ExchangeID, deadline: deadline}
	require.NoError(t, coordinateLeaseExchange(x, Leases{Owner: owner, Lifecycle: lifecycle}))
	_, err = x.receive("", "ready", "", 0)
	require.NoError(t, err)
	require.NoError(t, x.send(leaseMessage{Action: "start", CleanupScope: start.CleanupScope}))
	require.NoError(t, cmd.Wait())
}

func TestLeaseExchangeCancellationAfterReadyKeepsNextGate(t *testing.T) {
	f := newExchangeFixture(t)
	l := newLeaseLauncher()
	var successor *runstore.ExchangeLease
	l.authorize = func(ctx context.Context, p *exchangeProcess, x exchangeIO, scope string) error {
		var err error
		successor, err = f.refs.AcquireExchange(ctx)
		if err != nil {
			return err
		}
		p.cancelStartup()
		return p.authorize(ctx, x, scope)
	}
	p, err := l.launch(t.Context(), f.command(t, "cancel-ready"), f.start, leaseFixtureKey, f.refs)
	require.Error(t, err)
	require.NotNil(t, successor)
	defer successor.Release()
	require.True(t, p.knownNotSpawned())
	require.NoFileExists(t, filepath.Join(f.root, "authorized"))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = f.refs.AcquireExchange(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "teardown must not release a subsequent exchange's token")
}

func TestLeaseExchangeSignalFailureRetainsUntilReap(t *testing.T) {
	f := newExchangeFixture(t)
	l := newLeaseLauncher()
	l.kill = func(*os.Process) error { return unix.EPERM }
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	p, err := l.launch(ctx, f.command(t, "stopped"), f.start, leaseFixtureKey, f.refs)
	require.ErrorIs(t, err, unix.EPERM)
	require.NotNil(t, p)
	resumed := false
	defer func() {
		if !resumed {
			p.cmd.Process.Kill()
			p.Wait(context.Background())
		}
	}()
	require.False(t, p.knownNotSpawned())
	require.NoError(t, f.refs.Close())
	require.NoError(t, f.owner.Lease.Close())
	require.NoError(t, f.store.Close())
	assertExchangeLocksHeld(t, f)
	cmd := f.command(t, "normal")
	_, err = l.launch(t.Context(), cmd, f.start, leaseFixtureKey, p.refs)
	require.ErrorContains(t, err, "unresolved")
	require.Nil(t, cmd.Process)
	latePin, err := p.refs.Retain()
	require.NoError(t, err)
	defer latePin.Close()
	var before, after leaseMarker
	require.NoError(t, latePin.WithFiles(f.store.Path(), f.owner.ID, func(owner, _ *os.File) error {
		var err error
		before, err = readLeaseMarker(owner)
		return err
	}))
	// This known-stopped actor can still consume a buffered observe frame and
	// change the shared offset after the response timeout. Retain the gate until
	// it exits, rather than treating closed coordinator pipes as a join.
	require.NoError(t, unix.Kill(p.cmd.Process.Pid, unix.SIGCONT))
	resumed = true
	require.NoError(t, p.Wait(t.Context()))
	require.True(t, p.knownNotSpawned())
	require.NoError(t, latePin.WithFiles(f.store.Path(), f.owner.ID, func(owner, _ *os.File) error {
		var err error
		after, err = readLeaseMarker(owner)
		return err
	}))
	require.NotEqual(t, before, after, "late helper consumed the buffered observation")
	require.NoError(t, latePin.Close())
	require.NoError(t, runstore.Reset(t.Context(), f.store.Path()))
}

func TestLeaseExchangeUnverifiedWaitKeepsProtection(t *testing.T) {
	f := newExchangeFixture(t)
	l := newLeaseLauncher()
	l.wait = func(*exec.Cmd) error { return errors.New("injected wait failure without reaping") }
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	p, err := l.launch(ctx, f.command(t, "stopped"), f.start, leaseFixtureKey, f.refs)
	require.ErrorContains(t, err, "helper reap is unverified")
	require.NotNil(t, p)
	defer func() {
		// Test-only recovery of the injected waiter failure: the production
		// actor deliberately keeps its reservation when no terminal proof exists.
		p.cmd.Wait()
		require.NotNil(t, p.cmd.ProcessState)
		require.NoError(t, p.gate.Release())
		require.NoError(t, p.refs.Close())
	}()
	require.False(t, p.knownNotSpawned())
	require.NoError(t, f.refs.Close())
	require.NoError(t, f.owner.Lease.Close())
	require.NoError(t, f.store.Close())
	assertExchangeLocksHeld(t, f)
	cmd := f.command(t, "normal")
	_, err = l.launch(t.Context(), cmd, f.start, leaseFixtureKey, p.refs)
	require.ErrorContains(t, err, "unresolved")
	require.Nil(t, cmd.Process)
}
