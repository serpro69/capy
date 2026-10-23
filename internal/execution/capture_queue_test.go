package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

type gatedCapture struct {
	entered chan struct{}
	release chan struct{}
	fail    bool
	events  []executor.OutputEvent
}

func (s *gatedCapture) Pending(context.Context) error         { return nil }
func (s *gatedCapture) Finish(context.Context, Outcome) error { return nil }
func (s *gatedCapture) Close() error                          { return nil }
func (s *gatedCapture) Append(e executor.OutputEvent) error {
	if len(s.events) == 0 {
		close(s.entered)
		<-s.release
	}
	if s.fail {
		return errors.New("injected write failure")
	}
	s.events = append(s.events, e)
	return nil
}
func TestCaptureQueueRetainsPrefixWithoutBlocking(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "saturation", true: "write failure"}[fail], func(t *testing.T) {
			st := &gatedCapture{entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
			q := newCaptureQueue(st)
			event := func(n int) executor.OutputEvent {
				return executor.OutputEvent{Sequence: uint64(n), Stream: "stdout", Phase: "run", Data: make([]byte, executor.EventBlockBytes)}
			}
			require.NoError(t, q.append(event(1)))
			<-st.entered
			for i := 2; i <= captureQueueBlocks+1; i++ {
				require.NoError(t, q.append(event(i)))
			}
			require.Error(t, q.append(event(captureQueueBlocks+2)))
			require.Error(t, q.append(event(captureQueueBlocks+3)))
			close(st.release)
			require.Equal(t, "queue_saturated", q.close())
			if fail {
				require.Empty(t, st.events)
				return
			}
			require.Len(t, st.events, captureQueueBlocks+1)
			for i, e := range st.events {
				require.Equal(t, uint64(i+1), e.Sequence)
			}
		})
	}
}

func TestKnownOutcomeSeparatesRefreshAndFinalizationFailure(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		t.Run(map[bool]string{false: "finalization failed", true: "refresh failed"}[persisted], func(t *testing.T) {
			code := 7
			cursor := "acknowledged-prefix"
			result := knownOutcome(runstore.Result{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupPending}}, Outcome{Result: executor.StreamResult{Started: true, Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, ExitCode: &code, ObservedBytes: 12}, CapturedBytes: 12, TerminalPersisted: persisted}, &cursor)
			require.Equal(t, "exited", result.State)
			require.Equal(t, 7, *result.ExitCode)
			require.Equal(t, persisted, result.Capture.TerminalPersisted)
			require.EqualValues(t, 12, result.Capture.CapturedBytes)
			if persisted {
				require.Equal(t, "complete", result.Capture.State)
				require.Nil(t, result.Capture.Reason)
			} else {
				require.Equal(t, "incomplete", result.Capture.State)
				require.Equal(t, "finalization_failed", *result.Capture.Reason)
			}
		})
	}
}

// Many short writes must consume byte capacity, not 64 whole-block slots.
func TestCaptureQueueSmallEventsUseByteBudget(t *testing.T) {
	st := &gatedCapture{entered: make(chan struct{}), release: make(chan struct{})}
	q := newCaptureQueue(st)
	require.NoError(t, q.append(executor.OutputEvent{Sequence: 1, Stream: "stdout", Phase: "run", Data: []byte("first")}))
	<-st.entered
	for i := 2; i <= 1000; i++ {
		require.NoError(t, q.append(executor.OutputEvent{Sequence: uint64(i), Stream: "stdout", Phase: "run", Data: []byte("line\n")}))
	}
	close(st.release)
	require.Empty(t, q.close())
	require.Len(t, st.events, 1000)
	require.Zero(t, q.queuedBytes.Load())
	for i, event := range st.events {
		require.EqualValues(t, i+1, event.Sequence)
	}
}

type acknowledgedCapture struct{ through chan uint64 }

func (s *acknowledgedCapture) Pending(context.Context) error         { return nil }
func (s *acknowledgedCapture) Finish(context.Context, Outcome) error { return nil }
func (s *acknowledgedCapture) Close() error                          { return nil }
func (s *acknowledgedCapture) Append(e executor.OutputEvent) error {
	s.through <- e.Sequence
	return nil
}
func (s *acknowledgedCapture) AppendBatch(events []executor.OutputEvent) error {
	s.through <- events[len(events)-1].Sequence
	return nil
}
func TestCaptureQueueReleasesBytesAcrossBatches(t *testing.T) {
	st := &acknowledgedCapture{through: make(chan uint64, 32)}
	q := newCaptureQueue(st)
	var sequence uint64
	for range 16 {
		for range 32 {
			sequence++
			require.NoError(t, q.append(executor.OutputEvent{Sequence: sequence, Phase: "run", Stream: "stdout", Data: make([]byte, executor.EventBlockBytes)}))
		}
		for {
			if <-st.through == sequence {
				break
			}
		}
	}
	require.Empty(t, q.close())
	require.EqualValues(t, 8<<20, q.captured)
	require.Zero(t, q.queuedBytes.Load())
}
