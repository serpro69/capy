package adapter

import "encoding/json"

// These response shapes are supported by both Claude Code and Codex.
func formatBlock(reason string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}

func formatAllow(guidance string) ([]byte, error) {
	if guidance == "" {
		return nil, nil
	}
	return formatContext("PreToolUse", guidance)
}

func formatContext(event, context string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": context,
		},
	})
}
