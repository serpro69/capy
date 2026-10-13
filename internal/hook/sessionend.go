package hook

import (
	"log/slog"

	"github.com/serpro69/capy/internal/adapter"
)

// handleSessionEnd runs when a host session ends.
//
// WAL checkpointing is NOT done here — the MCP server's own Close() handles
// that when the process exits (via lifecycle guard). Opening a second DB
// connection from the hook while the server is still running prevents SQLite
// from getting exclusive WAL access, resulting in incomplete checkpoints.
//
// Cleanup affects only this stable session's observation entries. It never
// removes either permanent lock or opens a knowledge/vault database (ADR-016).
func handleSessionEnd(projectDir string, event *adapter.PreToolUseEvent, platform adapter.Platform) {
	if !event.SessionIDStable || event.SessionID == "" {
		return
	}
	if platform == adapter.Codex {
		if event.AgentID != "" {
			return // a child's end cannot clear the root session's evidence
		}
		if err := newCodexMainObservationStore(projectDir).removeSession(event.SessionID); err != nil {
			slog.Warn("could not clear Codex main tool observations")
		}
	}
	// Independent transactions: a main-store failure must not skip child cleanup.
	if err := newObservationStore(projectDir).removeSession(event.SessionID); err != nil {
		slog.Warn("could not clear session tool observations", "error", err)
	}
}
