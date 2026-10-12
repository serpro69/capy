package server

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const maxFetchTTLMS = int64(math.MaxInt64) / int64(time.Millisecond)

// fetchTTLArg validates milliseconds before any storage or network work. The
// maximum is below 2^53, so every accepted integer is exactly representable by
// the JSON decoder's float64. Check bounds before multiplying into a duration.
func fetchTTLArg(args map[string]any, defaultHours int) (time.Duration, error) {
	value, present := args["ttl"]
	if !present {
		if defaultHours < 0 || int64(defaultHours) > math.MaxInt64/int64(time.Hour) {
			return 0, fmt.Errorf("invalid store.cache.fetch_ttl_hours: must fit a nonnegative duration")
		}
		return time.Duration(defaultHours) * time.Hour, nil
	}
	invalid := fmt.Errorf("invalid parameter %q: expected integer milliseconds from 0 to %d", "ttl", maxFetchTTLMS)
	var ms int64
	switch v := value.(type) {
	case int:
		ms = int64(v)
	case int64:
		ms = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > float64(maxFetchTTLMS) || math.Trunc(v) != v {
			return 0, invalid
		}
		ms = int64(v)
	case json.Number:
		var decoded any
		if err := json.Unmarshal([]byte(v), &decoded); err != nil {
			return 0, invalid
		}
		n, ok := decoded.(float64)
		if !ok {
			return 0, invalid
		}
		return fetchTTLArg(map[string]any{"ttl": n}, defaultHours)
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, invalid
		}
		for _, digit := range v {
			if digit < '0' || digit > '9' {
				return 0, invalid
			}
		}
		var err error
		ms, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, invalid
		}
	default:
		return 0, invalid
	}
	if ms < 0 || ms > maxFetchTTLMS {
		return 0, invalid
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// boolArg preserves omitted defaults but rejects malformed supplied values.
// Only literal true/false strings are accepted; numeric truthiness and the
// single-letter forms accepted by strconv.ParseBool are intentionally excluded.
func boolArg(args map[string]any, name string, defaultValue bool) (bool, error) {
	value, present := args[name]
	if !present {
		return defaultValue, nil
	}
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, fmt.Errorf("invalid parameter %q: expected a boolean or a true/false string", name)
}

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

// stringArrayArg preserves serialized-array compatibility without dropping
// malformed members or treating a supplied null as an omitted parameter.
func stringArrayArg(raw any, name string) ([]string, error) {
	invalid := fmt.Errorf("invalid parameter %q: expected an array of strings", name)
	if encoded, ok := raw.(string); ok {
		if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
			return nil, invalid
		}
	}
	switch values := raw.(type) {
	case []string:
		return values, nil
	case []any:
		valuesOut := make([]string, len(values))
		for i, value := range values {
			s, ok := value.(string)
			if !ok {
				return nil, invalid
			}
			valuesOut[i] = s
		}
		return valuesOut, nil
	default:
		return nil, invalid
	}
}

// searchLimitArg checks before conversion and applies the existing two-hit cap.
// Numeric strings remain supported, but invalid values cannot consume budget.
func searchLimitArg(args map[string]any) (int, error) {
	raw, present := args["limit"]
	if !present {
		return 2, nil
	}
	invalid := fmt.Errorf("invalid parameter %q: expected a positive integer", "limit")
	var n float64
	switch v := raw.(type) {
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case float64:
		n = v
	case json.Number:
		return searchLimitArg(map[string]any{"limit": string(v)})
	case string:
		var err error
		n, err = strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, invalid
		}
	default:
		return 0, invalid
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || math.Trunc(n) != n {
		return 0, invalid
	}
	return int(min(n, 2)), nil
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
