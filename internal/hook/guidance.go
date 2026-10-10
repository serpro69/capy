package hook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/serpro69/capy/internal/adapter"
)

// sessionIDComponent preserves short ASCII IDs for compatibility when embedded
// in state filenames. Other nonempty IDs use a stable digest across hook
// processes; an empty ID continues to mean that state must not be persisted.
func sessionIDComponent(sessionID string) string {
	if len(sessionID) <= 128 {
		safe := true
		for i := 0; i < len(sessionID); i++ {
			c := sessionID[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
				c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				safe = false
				break
			}
		}
		if safe {
			return sessionID
		}
	}
	return fmt.Sprintf("sha256-%x", sha256.Sum256([]byte(sessionID)))
}

// guidanceOnce returns guidance context the first time a type is requested
// within a session, and nil on subsequent calls. Persists state to a file
// in .capy/ so it works across the short-lived process invocations that
// Claude Code spawns for each hook event.
func guidanceOnce(guidanceType, content string, a adapter.HookAdapter, projectDir, sessionID string) ([]byte, error) {
	if projectDir == "" || sessionID == "" {
		// Can't persist — fall back to always showing guidance
		return a.FormatAllow(content)
	}

	stateFile := filepath.Join(projectDir, ".capy", "guidance-"+sessionIDComponent(sessionID)+".json")

	shown := readGuidanceState(stateFile)
	if _, ok := shown[guidanceType]; ok {
		return nil, nil
	}

	shown[guidanceType] = true
	writeGuidanceState(stateFile, shown)

	return a.FormatAllow(content)
}

// readGuidanceState reads the guidance state file. Returns empty map on any error.
func readGuidanceState(path string) map[string]bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]bool)
	}
	var state map[string]bool
	if err := json.Unmarshal(data, &state); err != nil {
		return make(map[string]bool)
	}
	return state
}

// writeGuidanceState writes the guidance state file. Errors are silently
// ignored — worst case, guidance is shown again next invocation.
func writeGuidanceState(path string, state map[string]bool) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, data, 0o644)
}

// ResetGuidanceThrottle is a no-op kept for test compatibility.
// Tests should use ResetGuidanceFile instead.
func ResetGuidanceThrottle() {}

// ResetGuidanceFile removes a session's guidance state file. Used in tests.
func ResetGuidanceFile(projectDir, sessionID string) {
	if projectDir == "" || sessionID == "" {
		return
	}
	os.Remove(filepath.Join(projectDir, ".capy", "guidance-"+sessionIDComponent(sessionID)+".json"))
}
