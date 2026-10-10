package hook

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/serpro69/capy/internal/adapter"
)

func handlePostToolUse(input []byte, event *adapter.PreToolUseEvent, ctx hookContext) ([]byte, error) {
	tool := observationToolName(event.ToolName)
	if !event.SessionIDStable || event.SessionID == "" || event.AgentID == "" || tool == "" || !successfulPostToolUse(input) {
		return nil, nil
	}
	if err := newObservationStore(ctx.projectDir).record(event.SessionID, event.AgentID, tool); err != nil {
		slog.Warn("could not record child tool observation", "error", err)
	}
	return nil, nil
}

func observationToolName(name string) string {
	for _, tool := range []string{observedExecute, observedFetch} {
		if name == tool || strings.HasSuffix(name, "__"+tool) || strings.HasSuffix(name, "/"+tool) {
			return tool
		}
	}
	return ""
}

// PostToolUse is the host's success event. Also honor explicit MCP/tool failure
// and interruption flags, including serialized MCP results. Do not infer failure
// from words in successful content (which may itself describe an error).
func successfulPostToolUse(input []byte) bool {
	var event map[string]any
	if err := json.Unmarshal(input, &event); err != nil || toolFailure(event) {
		return false
	}
	if name, exists := event["hook_event_name"]; exists && name != "PostToolUse" {
		return false
	}
	response := event["tool_response"]
	if response == nil {
		return false
	}
	if encoded, ok := response.(string); ok {
		trimmed := strings.TrimSpace(encoded)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
				return false
			}
		}
	}
	switch value := response.(type) {
	case map[string]any:
		return !toolFailure(value)
	case []any:
		for _, item := range value {
			if fields, ok := item.(map[string]any); ok && toolFailure(fields) {
				return false
			}
		}
	}
	return true
}

func toolFailure(fields map[string]any) bool {
	for _, key := range []string{"isError", "is_error", "is_interrupt", "canceled", "cancelled"} {
		if value, exists := fields[key]; exists {
			flag, ok := value.(bool)
			if !ok || flag {
				return true
			}
		}
	}
	if value, exists := fields["success"]; exists && value != true {
		return true
	}
	if value := fields["error"]; value != nil && value != "" {
		return true
	}
	return false
}
