package hook

import (
	"testing"
)

func TestHandleSessionEnd_NoOp(t *testing.T) {
	// handleSessionEnd is a no-op — WAL checkpointing is handled by the MCP
	// server's own Close(). This test verifies it doesn't panic on any input.
	handleSessionEnd("")
	handleSessionEnd("/nonexistent/path")
	handleSessionEnd(t.TempDir())
}
