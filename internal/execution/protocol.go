// Package execution owns captured-run admission, supervision and recovery.
package execution

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/executor"
)

const (
	SupervisorCommand = "execute-supervisor"
	protocolVersion   = 2
	maxFrameBytes     = 16 * 1024
	// Base64 preserves non-UTF-8 credential bytes. This exceeds Linux's maximum
	// single environment string, including JSON expansion, without changing the
	// resolver's environment-key contract or weakening the control frame limit.
	maxCredentialFrameBytes = 1 << 20
	startupTimeout          = 3 * time.Second
	writeTimeout            = 250 * time.Millisecond
)

// Startup contains locators only, never source code or credentials. OpenRun must
// validate the admitted run's generation and recover its program from storage.
type Startup struct {
	Version      int       `json:"version"`
	RunID        string    `json:"run_id"`
	Generation   string    `json:"generation"`
	StorePath    string    `json:"store_path"`
	Deadline     time.Time `json:"deadline"`
	ExchangeID   string    `json:"exchange_id"`
	CleanupScope string    `json:"cleanup_scope"`
}

func (s Startup) validate() error {
	if s.Version != protocolVersion || !validID(s.RunID) || !validID(s.Generation) || s.StorePath == "" || len(s.StorePath) > 4096 || !validID(s.ExchangeID) || !exchangeScope(s.CleanupScope) {
		return errors.New("invalid supervisor startup")
	}
	return nil
}

func validID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil
}

// Outcome's retirement proof is independent of command status and capture loss.
// A missing terminal message always means an unknown outcome to the coordinator.
type Outcome struct {
	CapturedBytes     int64                 `json:"captured_bytes"`
	LastSequence      uint64                `json:"last_sequence"`
	Result            executor.StreamResult `json:"result"`
	Reason            string                `json:"reason,omitempty"`
	TerminalPersisted bool                  `json:"terminal_persisted"`
}

// CaptureStore is the deliberately small persistence seam for the risk proof.
// Pending must durably record cleanup=pending before returning success. Finish
// may finalize only valid local cleanup evidence with its admitted scope. All operations
// must be bounded; Append failure must preserve the already committed prefix.
type CaptureStore interface {
	Pending(context.Context) error
	Append(executor.OutputEvent) error
	Finish(context.Context, Outcome) error
	Close() error
}

type Run struct {
	Executor *executor.PolyglotExecutor
	Request  executor.ExecRequest
	Store    CaptureStore
	Started  func(string) error
}

type Leases struct{ Owner, Lifecycle *os.File }

type OpenRun func(context.Context, Startup, string, Leases) (*Run, error)

type notice = leaseMessage
type control = leaseMessage

func readFrame(r io.Reader, value any) error {
	return readFrameLimit(r, value, maxFrameBytes)
}

func readFrameLimit(r io.Reader, value any, limit uint32) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return errors.New("supervisor channel closed")
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > limit {
		return errors.New("invalid supervisor frame length")
	}
	b := make([]byte, int(size))
	if _, err := io.ReadFull(r, b); err != nil {
		return errors.New("incomplete supervisor frame")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid supervisor frame")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing supervisor frame data")
	}
	return nil
}

func writeFrame(w io.Writer, value any) error {
	return writeFrameLimit(w, value, maxFrameBytes)
}

func writeFrameLimit(w io.Writer, value any, limit int) error {
	b, err := json.Marshal(value)
	if err != nil || len(b) > limit {
		return errors.New("invalid supervisor response")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(b)))
	for _, part := range [][]byte{header[:], b} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if err != nil || n == 0 {
				return errors.New("supervisor channel write failed")
			}
			part = part[n:]
		}
	}
	return nil
}
