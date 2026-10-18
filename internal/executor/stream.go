package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// EventBlockBytes bounds each raw output callback, including newline-free output.
const EventBlockBytes = 16 * 1024

// OutputEvent is raw output in capy's observation order. The sink may retain Data;
// each call owns its slice. Sequence spans both streams and compilation phases.
type OutputEvent struct {
	Sequence uint64 `json:"sequence"`
	Phase    string `json:"phase"`
	Stream   string `json:"stream"`
	Data     []byte `json:"data"`
}

// EventSink must bound its own latency. After its first error, execution keeps
// draining and discards subsequent output; a capture failure is not cancellation.
type EventSink func(OutputEvent) error

// StreamResult separates process retirement from command and capture outcomes.
// Retired is a local process proof, never an acknowledgement of durable storage.
type StreamResult struct {
	StopReason    string `json:"stop_reason,omitempty"`
	SpawnCode     int    `json:"spawn_code,omitempty"`
	Started       bool   `json:"started"`
	ExitCode      *int   `json:"exit_code"`
	Signal        int    `json:"signal,omitempty"`
	Phase         string `json:"phase,omitempty"`
	Retired       bool   `json:"retired"`
	CaptureReason string `json:"capture_reason,omitempty"`
	CaptureLost   bool   `json:"capture_lost"`
	ObservedBytes int64  `json:"observed_bytes"`
	ElapsedMS     int64  `json:"elapsed_ms"`
}

type eventWriter struct {
	mu       sync.Mutex
	sequence uint64
	sink     EventSink
	err      error
	observed int64
	started  func(string) error
}

func (w *eventWriter) write(phase, stream string, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sequence++
	w.observed += int64(len(data))
	if w.err == nil {
		w.err = w.sink(OutputEvent{Sequence: w.sequence, Phase: phase, Stream: stream, Data: append([]byte(nil), data...)})
	}
}

// ExecuteStream is exclusively for a dedicated per-run supervisor process.
// PrepareSupervisor must succeed first. It does not detach jobs or impose the
// legacy output cap; the caller supplies execution cancellation and persistence.
func (e *PolyglotExecutor) ExecuteStream(ctx context.Context, req ExecRequest, sink EventSink) (StreamResult, error) {
	return e.ExecuteStreamWithStart(ctx, req, sink, nil)
}

// ExecuteStreamWithStart reports the first real spawn, including a compiler,
// before its output. A failed persistence callback loses capture but still drains
// and executes the already-started command under its normal lifetime.
func (e *PolyglotExecutor) ExecuteStreamWithStart(ctx context.Context, req ExecRequest, sink EventSink, started func(string) error) (StreamResult, error) {
	result := StreamResult{Retired: true}
	if req.Background || sink == nil {
		return result, errors.New("stream execution requires an attached sink")
	}
	if err := checkSupervisor(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		result.StopReason = streamStopReason(ctx)
		return result, err
	}
	p, err := e.prepare(req)
	if err != nil {
		result.SpawnCode = 126
		if errors.Is(err, ErrRuntimeUnavailable) {
			result.SpawnCode = 127
		}
		return result, err
	}
	defer os.RemoveAll(p.tmpDir)
	w := &eventWriter{sink: sink, started: started}
	compiled := false
	if req.Language == Rust {
		binary := filepath.Join(p.tmpDir, "main")
		result, err = runStreamPhase(ctx, p.runtime, []string{p.scriptPath, "-o", binary}, p.tmpDir, p.tmpDir, req, "compile", w)
		if err != nil || !result.Retired || result.ExitCode == nil || *result.ExitCode != 0 || result.Signal != 0 || ctx.Err() != nil {
			if result.StopReason == "" && err == nil && result.ExitCode != nil && *result.ExitCode == 0 && ctx.Err() != nil {
				result.StopReason = streamStopReason(ctx)
			}
			result.CaptureLost = w.err != nil
			result.ObservedBytes = w.observed
			return result, err
		}
		p.bin, p.args = binary, nil
		compiled = true
	}
	result, err = runStreamPhase(ctx, p.bin, p.args, p.workDir, p.tmpDir, req, "run", w)
	result.Started = result.Started || compiled
	result.CaptureLost = w.err != nil
	result.ObservedBytes = w.observed
	return result, err
}

// Freeze the control outcome at the execution boundary. A later cancellation
// during pipe draining or persistence cannot rewrite an observed natural exit.
func streamStopReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "deadline"
	}
	if cause := context.Cause(ctx); cause != nil {
		switch cause.Error() {
		case "owner_lost", "control_failed":
			return cause.Error()
		}
	}
	return "cancelled"
}
