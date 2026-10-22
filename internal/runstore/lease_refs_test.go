package runstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func leaseRefsFixture(t *testing.T) (*Store, *Owner) {
	t.Helper()
	path, err := config.ResolveRunDBPath(t.TempDir())
	require.NoError(t, err)
	s, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, o.Lease.Close()) })
	return s, o
}

func borrowForTest(t *testing.T, s *Store, o *Owner) *LeaseRefs {
	t.Helper()
	r, err := s.BorrowLeases(o)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	return r
}

func assertLeaseBusy(t *testing.T, path string, mode int, busy bool) {
	t.Helper()
	f, err := openRegular(path, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	err = unix.Flock(int(f.Fd()), mode|unix.LOCK_NB)
	if busy {
		require.ErrorIs(t, err, unix.EWOULDBLOCK)
	} else {
		require.NoError(t, err)
	}
}

func TestLeaseRefsRejectUnacquiredAndMismatchedOwners(t *testing.T) {
	s, owner := leaseRefsFixture(t)
	other, foreign := leaseRefsFixture(t)
	defer func() { require.NoError(t, other.Close()) }()
	for _, tc := range []struct {
		name  string
		owner *Owner
	}{
		{"nil", nil},
		{"zero", &Owner{}},
		{"fabricated metadata", &Owner{ID: owner.ID, Slot: owner.Slot}},
		{"unacquired handle", &Owner{ID: owner.ID, Slot: owner.Slot, Lease: &OwnerLease{}}},
		{"wrong generation", &Owner{ID: foreign.ID, Slot: owner.Slot, Lease: owner.Lease}},
		{"wrong slot", &Owner{ID: owner.ID, Slot: owner.Slot + 1, Lease: owner.Lease}},
		{"foreign acquisition", foreign},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := s.BorrowLeases(tc.owner)
			require.Error(t, err)
			require.Nil(t, refs)
		})
	}
	// Another authenticated attachment to the same store is still not the
	// acquisition that owns this lifecycle description and its exchange gate.
	attached, err := Open(t.Context(), s.Path(), testKey, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, attached.Close()) }()
	_, err = attached.BorrowLeases(owner)
	require.Error(t, err)
	refs := borrowForTest(t, s, owner)
	for _, tc := range []struct{ name, path, generation string }{
		{"path", other.Path(), owner.ID},
		{"generation", s.Path(), foreign.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := refs.WithFiles(tc.path, tc.generation, func(_, _ *os.File) error { called = true; return nil })
			require.Error(t, err)
			require.False(t, called)
		})
	}
}

func TestLeaseRefsRejectWrongModeAttachmentAndZeroValues(t *testing.T) {
	s, o := leaseRefsFixture(t)
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	exclusive, err := openAttached(t.Context(), s.Path(), testKey, s.owner, unix.LOCK_EX)
	require.NoError(t, err)
	defer func() { require.NoError(t, exclusive.closeHandles()) }()
	_, err = exclusive.ClaimOwner(t.Context())
	require.Error(t, err, "exclusive lifecycle acquisition cannot construct launch references")
	_, err = exclusive.BorrowLeases(o)
	require.Error(t, err)
	for _, r := range []*LeaseRefs{nil, {}} {
		require.Error(t, r.WithFiles(s.Path(), o.ID, func(_, _ *os.File) error { t.Fatal("invalid reference used"); return nil }))
		_, err := r.AcquireExchange(t.Context())
		require.Error(t, err)
		require.Error(t, r.Close())
	}
}

func TestLeaseRefsRetainLocksAcrossCloseAndAliases(t *testing.T) {
	s, o := leaseRefsFixture(t)
	lifePath := filepath.Join(filepath.Dir(s.Path()), "lifecycle.lock")
	ownerPath := s.OwnerLeasePath(o.Slot)
	first := borrowForTest(t, s, o)
	alias := *first
	second := borrowForTest(t, s, o)
	require.NoError(t, o.Lease.Close())
	_, err := o.Lease.Stat()
	require.ErrorIs(t, err, os.ErrClosed)
	_, err = s.BorrowLeases(o)
	require.Error(t, err)
	require.NoError(t, s.Close())
	_, err = s.ClaimOwner(t.Context())
	require.Error(t, err)
	_, err = s.BorrowLeases(o)
	require.Error(t, err)
	require.NoError(t, first.Close())
	require.NoError(t, alias.Close(), "copies release the same borrower only once")
	require.ErrorIs(t, alias.WithFiles(s.Path(), o.ID, func(_, _ *os.File) error { return nil }), os.ErrClosed)
	assertLeaseBusy(t, ownerPath, unix.LOCK_EX, true)
	assertLeaseBusy(t, ownerPath, unix.LOCK_SH, true)
	assertLeaseBusy(t, lifePath, unix.LOCK_EX, true)
	assertLeaseBusy(t, lifePath, unix.LOCK_SH, false)
	var resetErr *Error
	require.ErrorAs(t, Reset(t.Context(), s.Path()), &resetErr)
	require.Equal(t, "storage_busy", resetErr.Code)
	require.NoError(t, second.WithFiles(s.Path(), o.ID, func(owner, life *os.File) error {
		_, a := owner.Stat()
		_, b := life.Stat()
		return errors.Join(a, b)
	}))
	require.NoError(t, second.Close())
	assertLeaseBusy(t, ownerPath, unix.LOCK_EX, false)
	assertLeaseBusy(t, lifePath, unix.LOCK_EX, false)
	require.NoError(t, Reset(t.Context(), s.Path()))
}

func TestLeaseRefsOwnerAloneRetainsLifecycle(t *testing.T) {
	s, o := leaseRefsFixture(t)
	copy := *o.Lease
	require.NoError(t, s.Close())
	assertLeaseBusy(t, filepath.Join(filepath.Dir(s.Path()), "lifecycle.lock"), unix.LOCK_EX, true)
	require.NoError(t, copy.Close())
	require.NoError(t, o.Lease.Close())
	require.NoError(t, Reset(t.Context(), s.Path()))
}

func TestLeaseRefsGateSharedAcrossOwnersAndOneTimeRelease(t *testing.T) {
	s, o := leaseRefsFixture(t)
	otherOwner, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, otherOwner.Lease.Close()) }()
	a, b := borrowForTest(t, s, o), borrowForTest(t, s, otherOwner)
	first, err := a.AcquireExchange(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, first.Release()) }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = b.AcquireExchange(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "different owners share the lifecycle offset gate")
	require.NoError(t, first.Release())
	second, err := b.AcquireExchange(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, second.Release()) }()
	alias := *first
	require.NoError(t, alias.Release())
	ctx2, cancel2 := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel2()
	_, err = a.AcquireExchange(ctx2)
	require.ErrorIs(t, err, context.DeadlineExceeded, "stale token must not release another exchange")
	require.NoError(t, second.Release())
	// A cancelled acquisition must not take an available gate or leak its pin.
	ctx3, cancel3 := context.WithCancel(t.Context())
	cancel3()
	_, err = a.AcquireExchange(ctx3)
	require.ErrorIs(t, err, context.Canceled)
	third, err := a.AcquireExchange(t.Context())
	require.NoError(t, err)
	require.NoError(t, third.Release())
}

func TestLeaseRefsGateRetainsPairAndIndependentAttachments(t *testing.T) {
	s, o := leaseRefsFixture(t)
	refs := borrowForTest(t, s, o)
	gate, err := refs.AcquireExchange(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, gate.Release()) }()
	attached, err := Open(t.Context(), s.Path(), testKey, false)
	require.NoError(t, err)
	owner2, err := attached.ClaimOwner(t.Context())
	require.NoError(t, err)
	refs2 := borrowForTest(t, attached, owner2)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	gate2, err := refs2.AcquireExchange(ctx)
	require.NoError(t, err, "independently opened descriptions need no shared offset gate")
	require.NoError(t, gate2.Release())
	require.NoError(t, refs2.Close())
	require.NoError(t, owner2.Lease.Close())
	require.NoError(t, attached.Close())
	require.NoError(t, refs.Close())
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	assertLeaseBusy(t, s.OwnerLeasePath(o.Slot), unix.LOCK_EX, true)
	require.Error(t, Reset(t.Context(), s.Path()))
	require.NoError(t, gate.Release())
	require.NoError(t, Reset(t.Context(), s.Path()))
}

func TestLeaseRefsCallbackPinsDuringConcurrentClose(t *testing.T) {
	s, o := leaseRefsFixture(t)
	r := borrowForTest(t, s, o)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.WithFiles(s.Path(), o.ID, func(owner, life *os.File) error {
			close(entered)
			<-release
			_, a := owner.Stat()
			_, b := life.Stat()
			return errors.Join(a, b)
		})
	}()
	// Always join the callback, including on assertion failure.
	defer func() { close(release); require.NoError(t, <-done) }()
	<-entered
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := r.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	assertLeaseBusy(t, s.OwnerLeasePath(o.Slot), unix.LOCK_EX, true)
	require.Error(t, Reset(t.Context(), s.Path()))
}

func TestLeaseRefsConcurrentBorrowers(t *testing.T) {
	s, o := leaseRefsFixture(t)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			r, err := s.BorrowLeases(o)
			if err != nil {
				t.Error(err)
				return
			}
			if err := r.WithFiles(s.Path(), o.ID, func(_, _ *os.File) error { return nil }); err != nil {
				t.Error(err)
			}
			if err := r.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	assertLeaseBusy(t, s.OwnerLeasePath(o.Slot), unix.LOCK_EX, true)
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	require.NoError(t, Reset(t.Context(), s.Path()))
}
