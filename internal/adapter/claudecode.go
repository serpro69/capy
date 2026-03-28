package adapter

import (
	"encoding/json"
)

// ClaudeCodeAdapter implements HookAdapter for the Claude Code platform.
// Full implementation in Task 13 — this provides the minimal interface needed
// for the hook system to compile and run.
type ClaudeCodeAdapter struct{}

func (a *ClaudeCodeAdapter) ParsePreToolUse(input []byte) (*PreToolUseEvent, error) {
	var raw struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
		SessionID string         `json:"session_id"`
	}
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, err
	}
	return &PreToolUseEvent{
		ToolName:  raw.ToolName,
		ToolInput: raw.ToolInput,
		SessionID: raw.SessionID,
	}, nil
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
		PostToolUse:            false,
		PreCompact:             false,
		SessionStart:           true,
		CanModifyArgs:          true,
		CanModifyOutput:        false,
		CanInjectSessionContext: true,
	}
}
