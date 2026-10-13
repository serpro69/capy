package hook

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/require"
)

type inputReaderFunc func([]byte) (int, error)

func (f inputReaderFunc) Read(p []byte) (int, error) { return f(p) }

func requireInputError(t *testing.T, err error, reason string) {
	t.Helper()
	var inputErr *InputError
	require.ErrorAs(t, err, &inputErr)
	require.Contains(t, inputErr.reason, reason)
	require.Contains(t, err.Error(), "inspect the hook launcher/input producer")
}

func TestCodexHookInputDescriptors(t *testing.T) {
	budgets := inputBudgets{30 * time.Millisecond, time.Second, 1024}
	for _, payload := range []string{"", "{", `{"tool_name":"Bash"}`} {
		t.Run("open pipe "+payload, func(t *testing.T) {
			r, w, err := os.Pipe()
			require.NoError(t, err)
			defer r.Close()
			defer w.Close()
			_, err = io.WriteString(w, payload)
			require.NoError(t, err)
			// Mimic inherited blocking stdin, not os.Pipe's pollable Go file.
			require.NoError(t, syscall.SetNonblock(int(r.Fd()), false))
			start := time.Now()
			got, err := readCodexInput(r, budgets)
			requireInputError(t, err, "idle")
			require.Nil(t, got)
			require.Less(t, time.Since(start), time.Second)
			// Closing the borrowed original leaves no reader if the owned
			// duplicate was closed. A leaked reader would accept this byte.
			require.NoError(t, r.Close())
			_, err = w.Write([]byte("x"))
			require.ErrorIs(t, err, syscall.EPIPE)
		})
	}
	t.Run("closed writer", func(t *testing.T) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		defer r.Close()
		_, err = io.WriteString(w, "complete")
		require.NoError(t, err)
		require.NoError(t, w.Close())
		got, err := readCodexInput(r, budgets)
		require.NoError(t, err)
		require.Equal(t, "complete", string(got))
		_, err = r.Stat()
		require.NoError(t, err, "borrowed stdin must remain owned by the caller")
	})
	t.Run("regular file", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "input")
		require.NoError(t, err)
		defer f.Close()
		_, err = f.WriteString("complete")
		require.NoError(t, err)
		_, err = f.Seek(0, io.SeekStart)
		require.NoError(t, err)
		got, err := readCodexInput(f, budgets)
		require.NoError(t, err)
		require.Equal(t, "complete", string(got))
	})
	for _, path := range []string{t.TempDir(), os.DevNull} {
		t.Run("unsupported "+filepath.Base(path), func(t *testing.T) {
			f, err := os.Open(path)
			require.NoError(t, err)
			defer f.Close()
			_, err = readCodexInput(f, budgets)
			requireInputError(t, err, "guard setup")
		})
	}
	t.Run("closed descriptor", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "closed")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		_, err = readCodexInput(f, budgets)
		requireInputError(t, err, "guard setup")
	})
	t.Run("unreadable regular descriptor", func(t *testing.T) {
		f, err := os.OpenFile(filepath.Join(t.TempDir(), "write-only"), os.O_CREATE|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		defer f.Close()
		_, err = readCodexInput(f, budgets)
		requireInputError(t, err, "read failed")
		require.ErrorIs(t, err, syscall.EBADF)
	})
}

func TestCodexHookInputBudgets(t *testing.T) {
	for _, event := range []string{"pretooluse", "posttooluse", "sessionstart", "subagentstart", "sessionend"} {
		b := codexInputBudgets(event)
		if event == "sessionend" {
			require.Equal(t, inputBudgets{100 * time.Millisecond, 250 * time.Millisecond, 64 * 1024}, b)
		} else {
			require.Equal(t, inputBudgets{2 * time.Second, 10 * time.Second, 8 * 1024 * 1024}, b)
		}
	}
	for _, tc := range []struct {
		name, payload string
		limit         int
		tooLarge      bool
	}{
		{"empty EOF", "", 16, false},
		{"exact cap", strings.Repeat("a", inputReadChunk+7), inputReadChunk + 7, false},
		{"cap plus one", strings.Repeat("a", inputReadChunk+8), inputReadChunk + 7, true},
		{"multibyte exact", `"é"`, 4, false},
		{"multibyte excess", `"é"`, 3, true},
		{"escaped exact", `"\u00e9"`, 8, false},
		{"escaped excess", `"\u00e9"`, 7, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.payload)
			got, err := readBoundedInput(r, nil, inputBudgets{time.Second, time.Second, tc.limit}, time.Now)
			if tc.tooLarge {
				requireInputError(t, err, "too large")
				require.Nil(t, got)
				require.Equal(t, len(tc.payload)-tc.limit-1, r.Len())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.payload, string(got))
			}
		})
	}
	for _, tc := range []struct {
		name  string
		bytes int
		step  time.Duration
		want  string
	}{
		{"zero reads do not renew idle", 0, 25 * time.Millisecond, "idle"},
		{"trickle cannot renew absolute", 1, 25 * time.Millisecond, "absolute"},
		{"late positive read", 1, 100 * time.Millisecond, "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Unix(0, 0)
			start := clock
			r := inputReaderFunc(func(p []byte) (int, error) {
				clock = clock.Add(tc.step)
				if tc.bytes > 0 {
					p[0] = 'x'
				}
				return tc.bytes, nil
			})
			got, err := readBoundedInput(r, nil, inputBudgets{100 * time.Millisecond, 250 * time.Millisecond, 100}, func() time.Time { return clock })
			requireInputError(t, err, tc.want)
			require.Nil(t, got)
			wantElapsed := 100 * time.Millisecond
			if tc.want == "absolute" {
				wantElapsed = 250 * time.Millisecond
			}
			require.Equal(t, wantElapsed, clock.Sub(start))
		})
	}
	t.Run("guard failure precedes read", func(t *testing.T) {
		r := inputReaderFunc(func([]byte) (int, error) { t.Fatal("unguarded read"); return 0, nil })
		_, err := readBoundedInput(r, func(time.Time) error { return os.ErrNoDeadline }, codexInputBudgets("pretooluse"), time.Now)
		requireInputError(t, err, "guard setup")
	})
	t.Run("read failure excludes payload and OS detail", func(t *testing.T) {
		secret := errors.New("secret input or path")
		r := inputReaderFunc(func(p []byte) (int, error) { return copy(p, "secret"), secret })
		got, err := readBoundedInput(r, nil, codexInputBudgets("pretooluse"), time.Now)
		requireInputError(t, err, "read failed")
		require.ErrorIs(t, err, secret)
		require.NotContains(t, err.Error(), "secret")
		require.Nil(t, got)
	})
}

// A transport failure must not reach even the adapter parser, which precedes
// context resolution, policy reads and all event/state/database operations.
type inputAdmissionAdapter struct {
	adapter.CodexAdapter
	parsed bool
}

func (a *inputAdmissionAdapter) ParsePreToolUse(b []byte) (*adapter.PreToolUseEvent, error) {
	a.parsed = true
	return a.CodexAdapter.ParsePreToolUse(b)
}

func TestCodexHookInputNoSideEffects(t *testing.T) {
	for _, event := range []string{"pretooluse", "posttooluse", "sessionstart", "subagentstart", "sessionend"} {
		t.Run(event, func(t *testing.T) {
			owner := t.TempDir()
			f, err := os.CreateTemp(t.TempDir(), "input")
			require.NoError(t, err)
			defer f.Close()
			_, err = f.WriteString(strings.Repeat("x", codexInputBudgets(event).maxBytes+1))
			require.NoError(t, err)
			_, err = f.Seek(0, io.SeekStart)
			require.NoError(t, err)
			previous := os.Stdin
			os.Stdin = f
			defer func() { os.Stdin = previous }()
			a := &inputAdmissionAdapter{}
			err = Run(event, a, &owner)
			requireInputError(t, err, "too large")
			require.False(t, a.parsed)
			entries, err := os.ReadDir(owner)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
