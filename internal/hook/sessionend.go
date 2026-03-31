package hook

// handleSessionEnd runs when a Claude Code session ends.
//
// WAL checkpointing is NOT done here — the MCP server's own Close() handles
// that when the process exits (via lifecycle guard). Opening a second DB
// connection from the hook while the server is still running prevents SQLite
// from getting exclusive WAL access, resulting in incomplete checkpoints.
//
// This handler is a no-op placeholder for future session-end cleanup tasks
// (e.g., session event logging, stats persistence).
func handleSessionEnd(projectDir string) {
	// No-op. See ADR-016 for checkpoint strategy.
}
