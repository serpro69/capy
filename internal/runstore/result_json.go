package runstore

import (
	"encoding/json"

	"github.com/serpro69/capy/internal/executor"
)

// Keep the internal evidence value typed while representing an unadmitted
// rejection with JSON null. A loaded run always carries its durable proof.
func (r Result) MarshalJSON() ([]byte, error) {
	type plain Result
	var cleanup *executor.CleanupEvidence
	if r.RunID != nil {
		cleanup = &r.Cleanup
	}
	return json.Marshal(struct {
		plain
		Cleanup *executor.CleanupEvidence `json:"cleanup"`
	}{plain(r), cleanup})
}

func (b BatchResult) MarshalJSON() ([]byte, error) {
	type plain BatchResult
	var cleanup *executor.CleanupEvidence
	if b.BatchID != "" {
		cleanup = &b.Cleanup
	}
	return json.Marshal(struct {
		plain
		Cleanup *executor.CleanupEvidence `json:"cleanup"`
	}{plain(b), cleanup})
}

// Row columns, not a historical response blob, are the authoritative proof.
// Ignore even malformed cached cleanup and normalize at the read boundary.
// Manifest copies instead use decodeManifest, which requires valid evidence.
func decodeRunResult(encoded []byte, cleanup executor.CleanupEvidence, result *Result) error {
	var cached struct {
		*Result
		Cleanup  json.RawMessage `json:"cleanup"`
		Recovery json.RawMessage `json:"recovery"`
	}
	cached.Result = result
	if err := json.Unmarshal(encoded, &cached); err != nil {
		return storageError()
	}
	if result.Version != 1 && result.Version != EnvelopeVersion {
		return storageError()
	}
	if err := ValidateCleanup(cleanup); err != nil {
		return err
	}
	result.Version = EnvelopeVersion
	result.Cleanup = cleanup
	// Administrative authority comes only from validated audit records, never
	// cached/public JSON persisted in the original execution result.
	result.Recovery = nil
	return nil
}
