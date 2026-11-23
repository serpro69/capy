package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/require"
)

func TestCommittedDeleteReportDelivery(t *testing.T) {
	for _, delivery := range []string{"displayed", "coalesced", "write failed", "clipped"} {
		t.Run(delivery, func(t *testing.T) {
			m, st := deletionApp(t, true, deleteID)
			m = readyDeletion(t, press(t, m, "a"))
			m, apply := deleteStep(m, keyMsg("y"))
			st.listErr = errors.New("list unavailable")
			m, _ = deleteStep(m, apply())
			require.Equal(t, 1, st.applies)
			const status = "deleted 1 session(s) · list refresh failed: list unavailable"
			require.Equal(t, status, m.status)

			width := 90
			if delivery == "clipped" {
				width = 8
			}
			m, _ = deleteStep(m, tea.WindowSizeMsg{Width: width, Height: 1})
			frame := m.View()
			if delivery != "clipped" {
				require.Contains(t, frame, status)
			}
			if delivery == "coalesced" {
				m = press(t, m, "down")
				frame = m.View()
				require.NotContains(t, frame, status)
			}
			file, err := os.CreateTemp(t.TempDir(), "terminal")
			require.NoError(t, err)
			t.Cleanup(func() { _ = file.Close() })
			m.deleteOutput.File = file
			if delivery == "write failed" {
				require.NoError(t, file.Close())
			}
			_, writeErr := m.deleteOutput.Write([]byte(frame))
			var stderr bytes.Buffer
			err = m.deleteOutput.finishReports(&stderr)
			switch delivery {
			case "write failed":
				require.ErrorIs(t, writeErr, os.ErrClosed)
				require.ErrorIs(t, err, os.ErrClosed)
				require.ErrorContains(t, err, status)
			case "displayed":
				require.NoError(t, err)
				require.Empty(t, stderr.String())
			default:
				require.NoError(t, err)
				require.Equal(t, status+"\n", stderr.String())
			}
			require.Equal(t, 1, st.applies)
		})
	}
}

// Observe confirmation handling before Bubble Tea queues the next View. The
// renderer can hold its lock in Write while the event loop processes a key.
type observedDeleteModel struct {
	Model
	confirmations chan deletePhase
}

func (m observedDeleteModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.Model.Update(msg)
	m.Model = next.(Model)
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "y" {
		m.confirmations <- m.deletion.phase
	}
	if _, ok := msg.(deleteAppliedMsg); ok {
		return m, tea.Quit
	}
	return m, cmd
}

type frameWriterFunc func([]byte) (int, error)

func (f frameWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestDeleteDescendantsWaitsForRenderedPlan(t *testing.T) {
	m, st := descendantReviewApp(t, vault.ListOptions{})
	m = readyDeletion(t, press(t, m, "m"))
	oldFrame := m.View()
	m, expand := deleteStep(m, keyMsg("d"))
	next, _ := m.Update(expand()) // expanded model is ready; its frame is still buffered
	m = next.(Model)
	m.deleteOutput.didWrite([]byte(oldFrame))
	require.NotNil(t, m.deleteOutput.awaiting.Load(), "flushing the old frame cannot acknowledge its replacement")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	terminal, err := os.CreateTemp(t.TempDir(), "terminal")
	require.NoError(t, err)
	defer terminal.Close()
	m.deleteOutput.File = terminal
	held, release, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	pause := sync.OnceFunc(func() {
		close(held)
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	published := sync.OnceFunc(func() { close(written) })
	output := frameWriterFunc(func(p []byte) (int, error) {
		frame := bytes.Contains(p, []byte(m.deletion.frameTag))
		if frame {
			pause()
		}
		n, err := m.deleteOutput.Write(p)
		if frame && err == nil && n == len(p) {
			published()
		}
		return n, err
	})
	confirmations := make(chan deletePhase, 2)
	program := tea.NewProgram(observedDeleteModel{m, confirmations}, tea.WithInput(nil), tea.WithOutput(output), tea.WithContext(ctx), tea.WithoutSignalHandler())
	done := make(chan struct{})
	var runErr error
	go func() { _, runErr = program.Run(); close(done) }()
	defer func() { unblock(); cancel(); <-done }()
	wait := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	confirm := func(want deletePhase) {
		t.Helper()
		program.Send(keyMsg("y"))
		select {
		case phase := <-confirmations:
			require.Equal(t, want, phase)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	wait(held)
	confirm(deleteReady)
	unblock()
	wait(written)
	confirm(deleteApplying)
	wait(done)
	require.NoError(t, runErr)
	require.Equal(t, 1, st.applies)
	rows, err := st.ListSessions(t.Context(), vault.ListOptions{IncludeChildren: true})
	require.NoError(t, err)
	require.Empty(t, rows)
}
