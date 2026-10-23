package runstore

import (
	"context"
	"errors"
	"os"
	"sync"
)

// leaseAcquisition belongs to one authenticated shared lifecycle acquisition.
// All owner slots claimed through it share the same lifecycle description and
// therefore the same offset-exchange gate. mu protects every reference below.
type leaseAcquisition struct {
	mu        sync.Mutex
	lifecycle *os.File
	path, id  string
	refs      int
	closing   bool
	released  bool
	closeErr  error
	exchange  chan struct{}
}

type ownerAcquisition struct {
	context    *leaseAcquisition
	file       *os.File
	generation string
	slot       int
	refs       int
	closed     bool
	closeErr   error
}

// OwnerLease exposes lifetime and liveness, never a descriptor or lock mutation.
// Copies share closure state. Only ClaimOwner constructs a usable handle.
type OwnerLease struct{ acquired *ownerAcquisition }

func (l *OwnerLease) Close() error {
	if l == nil || l.acquired == nil {
		return os.ErrInvalid
	}
	o := l.acquired
	o.context.mu.Lock()
	defer o.context.mu.Unlock()
	if !o.closed {
		o.closed = true
		o.closeErr = o.releaseLocked()
	}
	return o.closeErr
}

func (l *OwnerLease) Stat() (os.FileInfo, error) {
	if l == nil || l.acquired == nil {
		return nil, os.ErrInvalid
	}
	o := l.acquired
	o.context.mu.Lock()
	defer o.context.mu.Unlock()
	if o.closed {
		return nil, os.ErrClosed
	}
	return o.file.Stat()
}

// LeaseRefs is an acquisition-proven pair, bound to one store, slot and owner
// generation. Its zero value is invalid; there is deliberately no FD constructor.
// Copies alias one borrower. Use Store.BorrowLeases for independent borrowers.
type LeaseRefs struct{ borrowed *leaseBorrow }

type leaseBorrow struct {
	owner    *ownerAcquisition
	closed   bool
	closeErr error
}

// BorrowLeases retains the exact successful EX owner and SH lifecycle
// acquisitions. Caller-edited metadata or handles from another attachment fail.
func (s *Store) BorrowLeases(owner *Owner) (*LeaseRefs, error) {
	if s.leases == nil || owner == nil || owner.Lease == nil || owner.Lease.acquired == nil {
		return nil, storageError()
	}
	a, o := s.leases, owner.Lease.acquired
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing || o.context != a || o.closed || owner.ID != o.generation || owner.Slot != o.slot || s.id != a.id || s.path != a.path {
		return nil, storageError()
	}
	if err := s.ValidateLeaseBinding(o.slot, o.file, a.lifecycle); err != nil {
		return nil, err
	}
	return o.borrowLocked(), nil
}

func (o *ownerAcquisition) borrowLocked() *LeaseRefs {
	o.refs++
	o.context.refs++
	return &LeaseRefs{borrowed: &leaseBorrow{owner: o}}
}

func (o *ownerAcquisition) releaseLocked() error {
	o.refs--
	var err error
	if o.refs == 0 {
		err = o.file.Close()
	}
	return errors.Join(err, o.context.releaseLocked())
}

func (a *leaseAcquisition) releaseLocked() error {
	a.refs--
	if a.refs == 0 {
		// Never LOCK_UN: inherited helper references share these lock descriptions.
		return a.lifecycle.Close()
	}
	return nil
}

func (r *LeaseRefs) retain() (*LeaseRefs, error) {
	if r == nil || r.borrowed == nil {
		return nil, storageError()
	}
	b := r.borrowed
	a := b.owner.context
	a.mu.Lock()
	defer a.mu.Unlock()
	if b.closed {
		return nil, os.ErrClosed
	}
	return b.owner.borrowLocked(), nil
}

// Retain creates an independent borrower of an existing live reference. Unlike
// Store.BorrowLeases it can extend an already borrowed lifetime during shutdown;
// it never creates a new acquisition or bypasses a closed reference.
func (r *LeaseRefs) Retain() (*LeaseRefs, error) { return r.retain() }

func (r *LeaseRefs) Close() error {
	if r == nil || r.borrowed == nil {
		return os.ErrInvalid
	}
	b := r.borrowed
	a := b.owner.context
	a.mu.Lock()
	defer a.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.closeErr = b.owner.releaseLocked()
	}
	return b.closeErr
}

// WithFiles pins both descriptions for the callback, even across concurrent
// Close. Only trusted launch/exchange code may use this escape hatch: it must not
// close, unlock, convert locks, retain the files, or seek outside an exchange.
// Inheritance by exec is allowed; the helper owns its resulting references.
func (r *LeaseRefs) WithFiles(path, generation string, use func(owner, lifecycle *os.File) error) (err error) {
	pin, err := r.retain()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, pin.Close()) }()
	o := pin.borrowed.owner
	a := o.context
	if use == nil || path != a.path || generation != o.generation {
		return storageError()
	}
	if err := (&Store{path: a.path}).ValidateLeaseBinding(o.slot, o.file, a.lifecycle); err != nil {
		return err
	}
	return use(o.file, a.lifecycle)
}

// ExchangeLease holds the shared gate and pins both descriptions until Release.
// Copies share a one-time release; a stale token cannot release a later exchange.
// It does not itself implement the Task 4 identity protocol or teardown policy.
type ExchangeLease struct{ state *exchangeLease }

type exchangeLease struct {
	once sync.Once
	refs *LeaseRefs
	err  error
}

func (r *LeaseRefs) AcquireExchange(ctx context.Context) (*ExchangeLease, error) {
	pin, err := r.retain()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, pin.Close())
	}
	gate := pin.borrowed.owner.context.exchange
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, errors.Join(err, pin.Close())
		}
		return &ExchangeLease{state: &exchangeLease{refs: pin}}, nil
	case <-ctx.Done():
		return nil, errors.Join(ctx.Err(), pin.Close())
	}
}

func (l *ExchangeLease) Release() error {
	if l == nil || l.state == nil {
		return os.ErrInvalid
	}
	s := l.state
	s.once.Do(func() {
		<-s.refs.borrowed.owner.context.exchange
		s.err = s.refs.Close()
	})
	return s.err
}

// stopLeaseBorrowing precedes database shutdown. Existing borrowers and owner
// handles retain their locks; new acquisition/borrowing through this Store fails.
func (s *Store) stopLeaseBorrowing() {
	if s.leases != nil {
		s.leases.mu.Lock()
		s.leases.closing = true
		s.leases.mu.Unlock()
	}
}

// retainLifecycle keeps attachment ownership continuous while ClaimOwner opens
// and publishes a slot. Store.Close may reject publication but cannot permit a
// reset in the middle of that acquisition.
func (s *Store) retainLifecycle() (func() error, error) {
	a := s.leases
	if a == nil {
		return nil, storageError()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return nil, os.ErrClosed
	}
	a.refs++
	return func() error {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.releaseLocked()
	}, nil
}

func (s *Store) closeLifecycle() error {
	if s.leases == nil { // Exclusive policy/reclamation attachments are not launch inputs.
		return s.lifecycle.Close()
	}
	a := s.leases
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closing = true
	if !a.released {
		a.released = true
		a.closeErr = a.releaseLocked()
	}
	return a.closeErr
}
