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
	Started     bool   `json:"started"`
	ExitCode    *int   `json:"exit_code"`
	Signal      int    `json:"signal,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Retired     bool   `json:"retired"`
	CaptureLost bool   `json:"capture_lost"`
}

type eventWriter struct {
	mu       sync.Mutex
	sequence uint64
	sink     EventSink
	err      error
}

func (w *eventWriter) write(phase, stream string, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sequence++
	if w.err == nil {
		w.err = w.sink(OutputEvent{Sequence: w.sequence, Phase: phase, Stream: stream, Data: append([]byte(nil), data...)})
	}
}

// ExecuteStream is exclusively for a dedicated per-run supervisor process.
// PrepareSupervisor must succeed first. It does not detach jobs or impose the
// legacy output cap; the caller supplies execution cancellation and persistence.
func (e *PolyglotExecutor) ExecuteStream(ctx context.Context, req ExecRequest, sink EventSink) (StreamResult, error) {
	result := StreamResult{Retired: true}
	if req.Background || sink == nil {
		return result, errors.New("stream execution requires an attached sink")
	}
	if err := checkSupervisor(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	p, err := e.prepare(req)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(p.tmpDir)
	w := &eventWriter{sink: sink}
	compiled := false
	if req.Language == Rust {
		binary := filepath.Join(p.tmpDir, "main")
		result, err = runStreamPhase(ctx, p.runtime, []string{p.scriptPath, "-o", binary}, p.tmpDir, p.tmpDir, req, "compile", w)
		if err != nil || !result.Retired || result.ExitCode == nil || *result.ExitCode != 0 || result.Signal != 0 || ctx.Err() != nil {
			result.CaptureLost = w.err != nil
			return result, err
		}
		p.bin, p.args = binary, nil
		compiled = true
	}
	result, err = runStreamPhase(ctx, p.bin, p.args, p.workDir, p.tmpDir, req, "run", w)
	result.Started = result.Started || compiled
	result.CaptureLost = w.err != nil
	return result, err
}
