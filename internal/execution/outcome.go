package execution

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
)

func (o Outcome) validate(scope executor.CleanupScope) error {
	r := o.Result
	if !r.Cleanup.Valid() || r.Cleanup.Scope != scope || o.CapturedBytes < 0 || r.ObservedBytes < o.CapturedBytes || r.ElapsedMS < 0 || r.Signal < 0 {
		return errors.New("invalid scoped supervisor outcome")
	}
	if r.Started && r.Cleanup.State == executor.CleanupNotSpawned || !r.Started && (r.Cleanup.State == executor.CleanupRetired || r.Cleanup.State == executor.CleanupObserved) {
		return errors.New("supervisor outcome contradicts phase cleanup")
	}
	return nil
}

func (o *Outcome) UnmarshalJSON(data []byte) error {
	fields, err := outcomeFields(data, []string{"captured_bytes", "last_sequence", "result", "terminal_persisted"}, []string{"reason"})
	if err != nil {
		return err
	}
	result, err := outcomeFields(fields["result"], []string{"cleanup", "started", "exit_code", "capture_lost", "observed_bytes", "elapsed_ms"}, []string{"stop_reason", "spawn_code", "signal", "phase", "capture_reason"})
	if err != nil {
		return err
	}
	if _, err := exactFields(result["cleanup"], "scope", "state"); err != nil {
		return err
	}
	type plain Outcome
	var wire plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*o = Outcome(wire)
	return o.validate(o.Result.Cleanup.Scope)
}

// Terminal fields are bounded and allow null only for the actual exit code.
// Detect duplicates before decoding structs, whose default decoder uses last wins.
func outcomeFields(data []byte, required, optional []string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid outcome object")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || fields[name] != nil {
			return nil, errors.New("duplicate outcome field")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(value, []byte("null")) && name != "exit_code" {
			return nil, errors.New("null outcome field")
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		if fields[name] == nil {
			return nil, errors.New("missing outcome field")
		}
		allowed[name] = true
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name := range fields {
		if !allowed[name] {
			return nil, errors.New("unknown outcome field")
		}
	}
	return fields, nil
}

func completionFromOutcome(scope executor.CleanupScope, out Outcome) (runstore.Completion, error) {
	if err := out.validate(scope); err != nil {
		return runstore.Completion{}, err
	}
	r := out.Result
	return runstore.Completion{Cleanup: r.Cleanup, SpawnCode: r.SpawnCode, Started: r.Started,
		CaptureLost: r.CaptureLost, CaptureReason: r.CaptureReason,
		ExitCode: r.ExitCode, Signal: r.Signal, Reason: out.Reason,
		ObservedBytes: r.ObservedBytes, ElapsedMS: r.ElapsedMS}, nil
}
