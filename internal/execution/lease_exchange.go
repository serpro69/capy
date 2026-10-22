package execution

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"time"
)

// exchangeIO owns the bounded pipe direction, never a lease. Both platforms
// execute this exact sequence; only their extra lock validation differs.
type exchangeIO struct {
	input, output *os.File
	id            string
	deadline      time.Time
}

func (x exchangeIO) send(m leaseMessage) error {
	m.Version, m.ExchangeID = exchangeProtocolVersion, x.id
	if err := x.output.SetWriteDeadline(minDeadline(x.deadline, time.Now().Add(writeTimeout))); err != nil {
		return err
	}
	return writeFrame(x.output, m)
}

func (x exchangeIO) receive(action, kind, role string, step int) (leaseMessage, error) {
	var m leaseMessage
	if err := x.input.SetReadDeadline(x.deadline); err != nil {
		return m, err
	}
	if err := readFrame(x.input, &m); err != nil {
		return m, err
	}
	if m.ExchangeID != x.id || m.Action != action || m.Type != kind || m.Lease != role || m.Step != step {
		return m, errors.New("out-of-order lease exchange message")
	}
	return m, nil
}

func minDeadline(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func freshLeaseMarker(exclude ...leaseMarker) (leaseMarker, error) {
	for range 16 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		// The wire allows 53 bits, but a filesystem can reject seeks well below
		// that ceiling. Use portable positive signed-32-bit offsets without
		// writing/growing the file; still validate the full wire range on reads.
		m := leaseMarker(binary.BigEndian.Uint64(b[:]) & (1<<31 - 1))
		valid := m != 0
		for _, old := range exclude {
			valid = valid && m != old
		}
		if valid {
			return m, nil
		}
	}
	return 0, errors.New("lease marker generation failed")
}

func setLeaseMarker(f *os.File, marker leaseMarker) error {
	if marker <= 0 || marker > maxLeaseMarker {
		return errors.New("invalid lease marker")
	}
	n, err := f.Seek(int64(marker), io.SeekStart)
	if err != nil {
		return err
	}
	if n != int64(marker) {
		return errors.New("lease marker seek mismatch")
	}
	return nil
}

func readLeaseMarker(f *os.File) (leaseMarker, error) {
	n, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if n <= 0 || n > maxLeaseMarker {
		return 0, errors.New("invalid observed lease marker")
	}
	return leaseMarker(n), nil
}

func coordinateLeaseExchange(x exchangeIO, leases Leases) error {
	for i, f := range []*os.File{leases.Owner, leases.Lifecycle} {
		role := []string{"owner", "lifecycle"}[i]
		a, err := freshLeaseMarker()
		if err != nil {
			return err
		}
		if err := setLeaseMarker(f, a); err != nil {
			return err
		}
		if err := x.send(leaseMessage{Action: "lease_observe", Lease: role, Step: 1}); err != nil {
			return err
		}
		m, err := x.receive("", "lease_observed", role, 1)
		if err != nil {
			return err
		}
		b, err := readLeaseMarker(f)
		if err != nil {
			return err
		}
		if m.Observed != a || m.Written == a || b != m.Written {
			return errors.New("lease description continuity not observed")
		}
		c, err := freshLeaseMarker(a, b)
		if err != nil {
			return err
		}
		if err := setLeaseMarker(f, c); err != nil {
			return err
		}
		if err := x.send(leaseMessage{Action: "lease_confirm", Lease: role, Step: 2, Expected: c}); err != nil {
			return err
		}
		m, err = x.receive("", "lease_confirmed", role, 2)
		if err != nil {
			return err
		}
		if m.Observed != c {
			return errors.New("lease confirmation mismatch")
		}
	}
	return x.send(leaseMessage{Action: "leases_confirmed"})
}

// observeLeaseExchange never imports lock ownership from inode/offset identity.
// The helper must validate files (and Linux fdinfo) before calling it; Task 3
// supplies trusted acquisition provenance on the coordinator side.
func observeLeaseExchange(x exchangeIO, leases Leases) error {
	for i, f := range []*os.File{leases.Owner, leases.Lifecycle} {
		role := []string{"owner", "lifecycle"}[i]
		if _, err := x.receive("lease_observe", "", role, 1); err != nil {
			return err
		}
		a, err := readLeaseMarker(f)
		if err != nil {
			return err
		}
		b, err := freshLeaseMarker(a)
		if err != nil {
			return err
		}
		if err := setLeaseMarker(f, b); err != nil {
			return err
		}
		if err := x.send(leaseMessage{Type: "lease_observed", Lease: role, Step: 1, Observed: a, Written: b}); err != nil {
			return err
		}
		m, err := x.receive("lease_confirm", "", role, 2)
		if err != nil {
			return err
		}
		c, err := readLeaseMarker(f)
		if err != nil {
			return err
		}
		if c == a || c == b || c != m.Expected {
			return errors.New("lease confirmation was not observed")
		}
		if err := x.send(leaseMessage{Type: "lease_confirmed", Lease: role, Step: 2, Observed: c}); err != nil {
			return err
		}
	}
	_, err := x.receive("leases_confirmed", "", "", 0)
	return err // the helper permanently leaves its offset-mutating state here
}
