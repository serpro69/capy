package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// ClaudeCodeAdapter implements HookAdapter for the Claude Code platform.
type ClaudeCodeAdapter struct{}

// uuidInTranscriptRe extracts a UUID from a transcript path like ".../<uuid>.jsonl".
var uuidInTranscriptRe = regexp.MustCompile(`([a-f0-9-]{36})\.jsonl$`)

func (a *ClaudeCodeAdapter) ParsePreToolUse(input []byte) (*PreToolUseEvent, error) {
	var raw struct {
		ToolName       string         `json:"tool_name"`
		ToolInput      map[string]any `json:"tool_input"`
		SessionID      string         `json:"session_id"`
		TranscriptPath string         `json:"transcript_path"`
	}
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, err
	}
	return &PreToolUseEvent{
		ToolName:   raw.ToolName,
		ToolInput:  raw.ToolInput,
		SessionID:  extractSessionID(raw.SessionID, raw.TranscriptPath),
		ProjectDir: os.Getenv("CLAUDE_PROJECT_DIR"),
	}, nil
}

// extractSessionID resolves a session ID using a 4-tier priority:
// transcript_path UUID > session_id field > CLAUDE_SESSION_ID env > ppid fallback.
func extractSessionID(sessionID, transcriptPath string) string {
	if transcriptPath != "" {
		if m := uuidInTranscriptRe.FindStringSubmatch(transcriptPath); len(m) > 1 {
			return m[1]
		}
	}
	if sessionID != "" {
		return sessionID
	}
	if env := os.Getenv("CLAUDE_SESSION_ID"); env != "" {
		return env
	}
	return fmt.Sprintf("pid-%d", os.Getppid())
}

func (a *ClaudeCodeAdapter) FormatBlock(reason string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}

func (a *ClaudeCodeAdapter) FormatAllow(guidance string) ([]byte, error) {
	if guidance == "" {
		return nil, nil
	}
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PreToolUse",
			"additionalContext": guidance,
		},
	})
}

func (a *ClaudeCodeAdapter) FormatModify(updatedInput map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": "Routed to capy sandbox",
			"updatedInput":             updatedInput,
		},
	})
}

func (a *ClaudeCodeAdapter) FormatAsk() ([]byte, error) {
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":      "PreToolUse",
			"permissionDecision": "ask",
		},
	})
}

func (a *ClaudeCodeAdapter) FormatSessionStart(context string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": context,
		},
	})
}

func (a *ClaudeCodeAdapter) Capabilities() PlatformCapabilities {
	return PlatformCapabilities{
		PreToolUse:             true,
		PostToolUse:            true,
		PreCompact:             true,
		SessionStart:           true,
		CanModifyArgs:          true,
		CanModifyOutput:        true,
		CanInjectSessionContext: true,
	}
}
