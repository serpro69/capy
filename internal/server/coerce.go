package server

import (
	"encoding/json"
	"fmt"
)

// coerceStringArray handles double-serialized JSON arrays and []any → []string.
func coerceStringArray(val any) []string {
	switch v := val.(type) {
	case []string:
		return v
	case string:
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err == nil {
			return arr
		}
		return nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// CommandInput represents a single command in a batch_execute request.
type CommandInput struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}

// fetchRequest is one entry in a capy_fetch_and_index batch.
type fetchRequest struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

// coerceFetchRequests parses the `requests` batch parameter — an array of
// {url, source?} objects. Mirrors coerceCommandsArray: it tolerates a
// double-serialized JSON string and silently skips entries that lack a url.
func coerceFetchRequests(val any) []fetchRequest {
	raw := val
	if s, ok := val.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			raw = parsed
		}
	}

	arr, ok := raw.([]any)
	if !ok {
		return nil
	}

	out := make([]fetchRequest, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		url, _ := m["url"].(string)
		if url == "" {
			continue
		}
		source, _ := m["source"].(string)
		out = append(out, fetchRequest{URL: url, Source: source})
	}
	return out
}

// coerceCommandsArray handles double-serialized JSON and plain command strings.
func coerceCommandsArray(val any) []CommandInput {
	// Try string → JSON parse
	raw := val
	if s, ok := val.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			raw = parsed
		}
	}

	arr, ok := raw.([]any)
	if !ok {
		return nil
	}

	out := make([]CommandInput, 0, len(arr))
	for i, item := range arr {
		switch v := item.(type) {
		case map[string]any:
			label, _ := v["label"].(string)
			command, _ := v["command"].(string)
			if label == "" {
				label = fmt.Sprintf("cmd_%d", i+1)
			}
			if command != "" {
				out = append(out, CommandInput{Label: label, Command: command})
			}
		case string:
			out = append(out, CommandInput{
				Label:   fmt.Sprintf("cmd_%d", i+1),
				Command: v,
			})
		}
	}
	return out
}
