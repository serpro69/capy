package hook

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/executor"
)

func handlePostToolUse(input []byte, event *adapter.PreToolUseEvent, ctx hookContext, platform adapter.Platform) ([]byte, error) {
	tool := observationToolName(event.ToolName)
	if !event.SessionIDStable || event.SessionID == "" || tool == "" {
		return nil, nil
	}
	response, success := postToolResponse(input)
	if !success || tool == observedExecute && !healthyExecuteObservation(response, 0) {
		return nil, nil
	}
	if event.AgentID == "" {
		if platform == adapter.Codex && tool == observedExecute {
			if err := newCodexMainObservationStore(ctx.projectDir).record(event.SessionID, "", tool); err != nil {
				slog.Warn("could not record Codex main tool observation")
			}
		}
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
	_, ok := postToolResponse(input)
	return ok
}

func postToolResponse(input []byte) (any, bool) {
	var event map[string]any
	if err := json.Unmarshal(input, &event); err != nil || toolFailure(event) {
		return nil, false
	}
	if name, exists := event["hook_event_name"]; exists && name != "PostToolUse" {
		return nil, false
	}
	response := event["tool_response"]
	if response == nil {
		return nil, false
	}
	if encoded, ok := response.(string); ok {
		trimmed := strings.TrimSpace(encoded)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
				return nil, false
			}
		}
	}
	switch value := response.(type) {
	case map[string]any:
		return response, !toolFailure(value)
	case []any:
		for _, item := range value {
			if fields, ok := item.(map[string]any); ok && toolFailure(fields) {
				return nil, false
			}
		}
	}
	return response, true
}

// Captured execute results have explicit lifetime/capture states. A successful
// host event alone cannot certify queued, failed or incomplete executions. Keep
// legacy text/content-only responses separate, and never read state from output
// text: a command may print arbitrary JSON. Presence of modern envelope fields
// disables legacy fallback, including malformed/unsupported structured content.
func healthyExecuteObservation(response any, depth int) bool {
	if depth > 16 {
		return false
	}
	switch value := response.(type) {
	case map[string]any:
		if toolFailure(value) {
			return false
		}
		structuredSeen := false
		for _, field := range []string{"structuredContent", "structured_content"} {
			if structured, exists := value[field]; exists {
				structuredSeen = true
				envelope, ok := structured.(map[string]any)
				if !ok || !healthyRunEnvelope(envelope) {
					return false
				}
			}
		}
		if structuredSeen {
			return true
		}
		for _, field := range []string{"version", "kind", "run_id", "state", "capture", "cleanup", "recovery"} {
			if _, exists := value[field]; exists {
				return healthyRunEnvelope(value)
			}
		}
	case []any:
		for _, item := range value {
			if !healthyExecuteObservation(item, depth+1) {
				return false
			}
		}
	}
	return true // Successful legacy execute response; fetch uses its own path.
}

func healthyRunEnvelope(value map[string]any) bool {
	if _, present := value["recovery"]; present {
		return false
	}
	id, ok := value["run_id"].(string)
	if !ok || strings.TrimSpace(id) == "" || value["kind"] != "run" || toolFailure(value) || value["signal"] != nil {
		return false
	}
	switch value["version"] {
	case float64(1): // Preserve the Linux-era envelope interpretation.
	case float64(2):
		if !healthyScopedCleanup(value) {
			return false
		}
	default:
		return false
	}
	capture, ok := value["capture"].(map[string]any)
	if !ok {
		return false
	}
	switch value["state"] {
	case "running":
		return capture["state"] == "recording" && value["exit_code"] == nil
	case "exited":
		return value["exit_code"] == float64(0) && capture["state"] == "complete" && capture["terminal_persisted"] == true
	default:
		return false
	}
}

func healthyScopedCleanup(value map[string]any) bool {
	cleanup, ok := value["cleanup"].(map[string]any)
	if !ok || len(cleanup) != 2 || value["error"] != nil {
		return false
	}
	if _, exists := value["exit_code"]; !exists {
		return false
	}
	if _, exists := value["signal"]; !exists {
		return false
	}
	var completed string
	switch cleanup["scope"] {
	case string(executor.CleanupLinuxDescendants):
		completed = string(executor.CleanupRetired)
	case string(executor.CleanupDarwinObservation):
		completed = string(executor.CleanupObserved)
	default:
		return false
	}
	switch value["state"] {
	case "running":
		return cleanup["state"] == string(executor.CleanupPending)
	case "exited":
		return cleanup["state"] == completed
	default:
		return false
	}
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
