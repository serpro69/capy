package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCanary_RealSessionFiles is a canary test that parses actual Claude Code
// session files from the developer's machine. It catches JSONL format drift
// that synthetic fixtures cannot detect (see ADR-021).
//
// The test is CI-safe: it skips if no session directory exists.
func TestCanary_RealSessionFiles(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	projectsDir := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		t.Skip("no ~/.claude/projects/ directory — CI or non-Claude-Code machine")
	}

	projectEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		t.Skipf("cannot read projects directory: %v", err)
	}

	var parsed int
	for _, projEntry := range projectEntries {
		if !projEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projEntry.Name())
		entries, err := os.ReadDir(projDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}

			info, err := e.Info()
			if err != nil || info.Size() < 100 {
				continue
			}

			jsonlPath := filepath.Join(projDir, e.Name())
			sess, err := ParseSession(jsonlPath)
			if err != nil {
				t.Errorf("ParseSession(%s) failed: %v", e.Name(), err)
				continue
			}

			if sess.SessionID == "" {
				t.Errorf("%s: session ID is empty", e.Name())
			}

			if len(sess.TurnPairs) > 0 {
				if sess.TotalAssistantChars == 0 {
					t.Errorf("%s: has %d turn pairs but 0 assistant chars", e.Name(), len(sess.TurnPairs))
				}
				if sess.StartTime.IsZero() {
					t.Errorf("%s: has turn pairs but zero StartTime", e.Name())
				}
			}

			parsed++
			if parsed >= 20 {
				break
			}
		}
		if parsed >= 20 {
			break
		}
	}

	if parsed == 0 {
		t.Skip("no session files found to parse")
	}
	t.Logf("canary: successfully parsed %d real session files", parsed)
}
