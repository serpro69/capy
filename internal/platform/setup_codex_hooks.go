package platform

import (
	"encoding/json"
	"fmt"
	"os"
)

const codexHooksRelPath = ".codex/hooks.json"

type codexHookEvent struct {
	Event, CLIArg, Matcher string
}

// Match verified native Bash payloads and known capy tools under any server
// alias. Do not subscribe to unrelated tools just because their server is capy.
var codexHookEvents = []codexHookEvent{
	{Event: "SessionStart", CLIArg: "sessionstart", Matcher: "^.*$"},
	{Event: "PreToolUse", CLIArg: "pretooluse", Matcher: "^(Bash|mcp__[^[:space:]]+__capy_(" +
		"execute|execute_file|batch_execute|fetch_and_index|index|search|vault_search|stats|doctor|cleanup" +
		"))$"},
	{Event: "SubagentStart", CLIArg: "subagentstart", Matcher: "^.*$"},
	{Event: "PostToolUse", CLIArg: "posttooluse", Matcher: "^mcp__[^[:space:]]+__capy_(execute|fetch_and_index)$"},
	{Event: "SessionEnd", CLIArg: "sessionend", Matcher: "^.*$"},
}

// mergeCodexHooks only rewrites positively recognized commands for this ID.
// Setup has already validated its adjacent marker and all ancestor bindings.
// Keep this helper parameterized so drift tests never create/rotate an owner.
func mergeCodexHooks(path, id string) error {
	if !validCodexID(id) {
		return fmt.Errorf("%s: invalid owner ID; restore committed configuration", path)
	}
	data, exists, err := readCodexFile(path, codexConfigLimit)
	if err != nil {
		return err
	}
	root := make(map[string]any)
	if exists {
		root, err = decodeCodexJSON(data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	bindings, err := codexHookBindings(root, path)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.ID != id {
			return fmt.Errorf("%s: hook binding disagrees with owner marker; restore matching configuration", path)
		}
	}
	hooks, _ := root["hooks"].(map[string]any) // validated above
	if hooks == nil {
		hooks = make(map[string]any)
		root["hooks"] = hooks
	}
	for _, event := range codexHookEvents {
		groups, _ := hooks[event.Event].([]any)
		kept, err := withoutOwnedCodexHooks(groups, id)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		hooks[event.Event] = append(kept, map[string]any{
			"matcher": event.Matcher,
			"hooks": []any{map[string]any{
				"type": "command", "command": codexHookCommand(id, event.CLIArg),
			}},
		})
	}
	// additionalContextLimit is deliberately absent from generated handlers.
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	out = append(out, '\n')
	if len(out) > codexConfigLimit {
		return fmt.Errorf("%s would exceed %d bytes after merging; reduce the configuration before setup", path, codexConfigLimit)
	}
	return os.WriteFile(path, out, 0o644)
}

// All type assertions here follow codexHookBindings' structural validation.
// Preserve every external handler and its group metadata. Empty groups carrying
// unknown metadata remain intact; only capy's recognized handlers are removed.
func withoutOwnedCodexHooks(groups []any, id string) ([]any, error) {
	kept := make([]any, 0, len(groups))
	for _, rawGroup := range groups {
		group := rawGroup.(map[string]any)
		handlers := group["hooks"].([]any)
		remaining := make([]any, 0, len(handlers))
		removed := false
		for _, rawHandler := range handlers {
			handler := rawHandler.(map[string]any)
			command, _ := handler["command"].(string)
			binding, owned, err := recognizeCodexHook(command)
			if err != nil {
				return nil, err
			}
			if owned && binding.ID == id {
				removed = true
				continue
			}
			remaining = append(remaining, rawHandler)
		}
		metadata := false
		for key := range group {
			metadata = metadata || (key != "matcher" && key != "hooks")
		}
		if !removed || len(remaining) > 0 || metadata {
			group["hooks"] = remaining
			kept = append(kept, group)
		}
	}
	return kept, nil
}
