package hook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

const (
	codexInputIdle        = 2 * time.Second
	codexInputTotal       = 10 * time.Second
	codexInputMaxBytes    = 8 * 1024 * 1024
	codexEndInputIdle     = 100 * time.Millisecond
	codexEndInputTotal    = 250 * time.Millisecond
	codexEndInputMaxBytes = 64 * 1024
	inputReadChunk        = 32 * 1024
)

// InputError identifies a Codex transport failure, before payload parsing or
// event handling. Its diagnostic deliberately excludes input and OS paths.
type InputError struct {
	reason string
	cause  error
}

func (e *InputError) Error() string {
	return "Codex hook stdin: " + e.reason + "; inspect the hook launcher/input producer"
}

func (e *InputError) Unwrap() error { return e.cause }

type inputBudgets struct {
	idle, total time.Duration
	maxBytes    int
}

func codexInputBudgets(event string) inputBudgets {
	if event == "sessionend" {
		return inputBudgets{codexEndInputIdle, codexEndInputTotal, codexEndInputMaxBytes}
	}
	return inputBudgets{codexInputIdle, codexInputTotal, codexInputMaxBytes}
}

func readCodexInput(stdin *os.File, budgets inputBudgets) ([]byte, error) {
	f, pipe, err := ownHookInput(stdin)
	if err != nil {
		return nil, &InputError{reason: "input guard setup failed", cause: err}
	}
	defer f.Close()
	var deadline func(time.Time) error
	if pipe {
		deadline = f.SetReadDeadline
	}
	return readBoundedInput(f, deadline, budgets, time.Now)
}

// ownHookInput owns the duplicate only. The caller must use this as its sole
// stdin read path: O_NONBLOCK is shared with the original descriptor. Never call
// Fd on either file during reading, as it can restore blocking mode.
func ownHookInput(stdin *os.File) (*os.File, bool, error) {
	info, err := stdin.Stat()
	if err != nil {
		return nil, false, err
	}
	pipe := info.Mode()&os.ModeNamedPipe != 0
	if !pipe && !info.Mode().IsRegular() {
		return nil, false, errors.New("expected a pipe or regular file")
	}
	raw, err := stdin.SyscallConn()
	if err != nil {
		return nil, false, err
	}
	fd := -1
	var dupErr error
	err = raw.Control(func(original uintptr) {
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		fd, dupErr = syscall.Dup(int(original))
		if dupErr == nil {
			syscall.CloseOnExec(fd)
		}
	})
	if err != nil {
		return nil, false, err
	}
	if dupErr != nil {
		return nil, false, dupErr
	}
	if pipe {
		if err := syscall.SetNonblock(fd, true); err != nil {
			syscall.Close(fd)
			return nil, false, err
		}
	}
	// NewFile sees O_NONBLOCK and registers the duplicate with the Go poller.
	f := os.NewFile(uintptr(fd), "codex-hook-stdin")
	if pipe {
		if err := f.SetReadDeadline(time.Time{}); err != nil {
			f.Close()
			return nil, false, err
		}
	}
	return f, pipe, nil
}

// Regular files have no deadline setter: checks between bounded reads cannot
// interrupt filesystem/kernel I/O. Pipes must supply a working setter. The
// clock and budgets are internal test seams, never runtime configuration.
func readBoundedInput(r io.Reader, setDeadline func(time.Time) error, budgets inputBudgets, now func() time.Time) ([]byte, error) {
	start := now()
	absolute, idle := start.Add(budgets.total), start.Add(budgets.idle)
	var input []byte
	buf := make([]byte, min(inputReadChunk, budgets.maxBytes+1))
	for {
		deadline, reason := idle, "idle deadline exceeded"
		if !absolute.After(idle) {
			deadline, reason = absolute, "absolute read deadline exceeded"
		}
		if !now().Before(deadline) {
			return nil, &InputError{reason: reason}
		}
		if setDeadline != nil {
			if err := setDeadline(deadline); err != nil {
				return nil, &InputError{reason: "input guard setup failed", cause: err}
			}
		}
		n, err := r.Read(buf[:min(len(buf), budgets.maxBytes+1-len(input))])
		readAt := now()
		if !readAt.Before(deadline) || errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, &InputError{reason: reason, cause: err}
		}
		if n > 0 {
			input = append(input, buf[:n]...)
			if len(input) > budgets.maxBytes {
				return nil, &InputError{reason: fmt.Sprintf("input too large (limit %d bytes)", budgets.maxBytes)}
			}
			idle = readAt.Add(budgets.idle)
		}
		if errors.Is(err, io.EOF) {
			return input, nil
		}
		if err != nil {
			return nil, &InputError{reason: "read failed", cause: err}
		}
	}
}
