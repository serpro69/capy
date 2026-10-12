package adapter

import (
	"encoding/json"
	"fmt"
)

// CodexAdapter implements the main-session Codex CLI hook transport.
// Wire baseline: Codex CLI 0.162.1, https://learn.chatgpt.com/docs/hooks.
type CodexAdapter struct{}

var _ HookAdapter = (*CodexAdapter)(nil)

func (a *CodexAdapter) Platform() Platform { return Codex }

func (a *CodexAdapter) ParsePreToolUse(input []byte) (*PreToolUseEvent, error) {
	// The dispatcher also uses this parser for common SessionStart metadata.
	// Required pre-tool fields are checked before routing that event.
	var raw *struct {
		ToolName  string          `json:"tool_name"`
		ToolInput map[string]any  `json:"tool_input"`
		SessionID string          `json:"session_id"`
		AgentID   string          `json:"agent_id"`
		AgentType string          `json:"agent_type"`
		Cwd       json.RawMessage `json:"cwd"`
	}
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, fmt.Errorf("decode Codex hook input: %w", err)
	}
	if raw == nil {
		return nil, fmt.Errorf("decode Codex hook input: expected JSON object")
	}
	event := &PreToolUseEvent{
		ToolName: raw.ToolName, ToolInput: raw.ToolInput,
		AgentID: raw.AgentID, AgentType: raw.AgentType,
	}
	// Never derive identity from transcript_path (which may be null), Claude
	// environment variables or the process parent. Empty means no persisted state.
	if raw.SessionID != "" {
		event.SessionID = "codex:" + raw.SessionID
		event.SessionIDStable = true
	}
	if len(raw.Cwd) != 0 {
		if err := json.Unmarshal(raw.Cwd, &event.Cwd); err != nil || event.Cwd == "" {
			event.InvalidCwd = true
		}
	}
	return event, nil
}

func (a *CodexAdapter) FormatBlock(reason string) ([]byte, error) {
	return formatBlock(reason)
}

func (a *CodexAdapter) FormatAllow(guidance string) ([]byte, error) {
	return formatAllow(guidance)
}

func (a *CodexAdapter) FormatAsk(reason string) ([]byte, error) {
	return formatBlock(reason + "; this rule requires approval, which capy's Codex PreToolUse hook cannot request")
}

func (a *CodexAdapter) FormatModify(_ map[string]any) ([]byte, error) {
	return nil, fmt.Errorf("capy's Codex adapter deliberately does not implement input rewriting")
}

func (a *CodexAdapter) FormatSessionStart(context string) ([]byte, error) {
	return formatContext("SessionStart", context)
}

func (a *CodexAdapter) Capabilities() PlatformCapabilities {
	return PlatformCapabilities{
		PreToolUse: true, SessionStart: true, CanInjectSessionContext: true,
		// Input rewriting is host-supported but deliberately unused by capy.
		// TODO(codex-hooks Task 4): enable the child observation event path.
	}
}
