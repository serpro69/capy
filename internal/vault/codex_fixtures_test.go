package vault

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// codex_fixtures_test.go builds Codex rollout lines the way fixtures_test.go /
// golden_test.go build Claude lines. Shapes follow research.md Appendix A (one
// redacted real line per record type). Every builder exists in a legacy
// (< 0.147: no ordinal, `user_message` events) and a paginated (≥ 0.147:
// ordinal, `item_completed` items) form, selected by codexMode. Builders take
// testing.TB so fuzz targets can seed from codexCases.

// codexMode selects the history mode a fixture mimics.
type codexMode int

const (
	codexLegacy codexMode = iota + 1
	codexPaginated
)

func (m codexMode) cliVersion() string {
	if m == codexPaginated {
		return "0.152.1"
	}
	return "0.130.0"
}

// Fixture identities (real UUIDv7 shapes from Appendix A; safe to keep).
const (
	codexFixtureID       = "019e22ba-4c15-7ae0-a903-255537a6a1b3"
	codexFixtureParentID = "019dc606-f552-7f93-b9a1-c5620b23b8dd"
	codexFixtureChildID  = "019dc608-0061-7d90-a4e9-fca142504625"
	codexFixtureChild2ID = "01a0714f-f6ea-7bb1-bdeb-dc80fab2f1bf"
	// codexEncryptedMessage is a spawn_agent `message` as CLI ≥ 0.147 writes it:
	// an opaque Fernet-style token that must never surface as a label.
	codexEncryptedMessage = "gAAAAABqm_vfQ2xhdWRlIGlzIGEgZ29vZCBib3k" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

// codexEnv wraps a payload in the rollout envelope.
func codexEnv(ts, typ string, payload map[string]any) map[string]any {
	return map[string]any{"timestamp": ts, "type": typ, "payload": payload}
}

// codexRollout renders lines as a rollout; paginated files carry `ordinal`
// (the 0-based history position, which the decoder ignores).
func codexRollout(t testing.TB, mode codexMode, lines ...map[string]any) []byte {
	t.Helper()
	if mode == codexPaginated {
		for i, l := range lines {
			l["ordinal"] = i
		}
	}
	return jsonlBytes(t, lines...)
}

// codexMetaOpts tunes codexSessionMetaLine. Zero values pick the Appendix A
// legacy/paginated sample values.
type codexMetaOpts struct {
	id, cwd, branch string
	payloadTS       string // session_meta.payload.timestamp (creation time); "" omits it
	source          any    // string or object; nil → "cli"
	noGit           bool
	// child fields; lifted places them at the payload top level (CLI ≥ 0.137)
	parent, nickname, role, agentPath string
	lifted                            bool
}

// codexSessionMetaLine builds line 0 of a rollout.
func codexSessionMetaLine(ts string, mode codexMode, o codexMetaOpts) map[string]any {
	if o.id == "" {
		o.id = codexFixtureID
	}
	if o.cwd == "" {
		o.cwd = "/home/user/Projects/proj"
	}
	if o.branch == "" {
		o.branch = "master"
	}
	p := map[string]any{
		"id": o.id, "cwd": o.cwd, "originator": "codex-tui", "cli_version": mode.cliVersion(),
		"thread_source": "user", "model_provider": "openai",
		"base_instructions": map[string]any{"text": "You are Codex, a coding agent…"},
	}
	if o.payloadTS != "" {
		p["timestamp"] = o.payloadTS
	}
	if !o.noGit {
		p["git"] = map[string]any{"commit_hash": "17985d49e752c3f3c5aeaedd55364f12cc1a43bc", "branch": o.branch, "repository_url": "git@github.com:org/repo.git"}
	}
	if mode == codexPaginated {
		p["session_id"] = o.id
		p["history_mode"] = "paginated"
		p["context_window"] = map[string]any{"window_id": "01a06224-f7d9-71d3-b6bb-4eb9b471a48a"}
	}
	switch {
	case o.source != nil:
		p["source"] = o.source
	case o.parent != "":
		p["source"] = map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{
			"parent_thread_id": o.parent, "depth": 1, "agent_path": nilIfEmpty(o.agentPath),
			"agent_nickname": nilIfEmpty(o.nickname), "agent_role": nilIfEmpty(o.role),
		}}}
		if o.lifted {
			p["parent_thread_id"] = o.parent
			p["agent_path"] = nilIfEmpty(o.agentPath)
			p["multi_agent_version"] = "v2"
		}
		// Every observed child (0.125 → 0.147) also carries nickname/role at the top level.
		p["agent_nickname"] = nilIfEmpty(o.nickname)
		p["agent_role"] = nilIfEmpty(o.role)
	default:
		p["source"] = "cli"
	}
	return codexEnv(ts, "session_meta", p)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- human turns -----------------------------------------------------------

// codexUserEvent is the legacy human prompt: event_msg / user_message.
func codexUserEvent(ts, text string) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{
		"type": "user_message", "message": text, "images": []any{}, "local_images": []any{}, "text_elements": []any{},
	})
}

// codexUserItem is the paginated human prompt: item_completed / UserMessage.
func codexUserItem(ts, text string) map[string]any {
	return codexItemCompletedLine(ts, map[string]any{
		"type": "UserMessage", "id": "01a0714f-37aa-7f92-869a-f8c1130b8fdb",
		"content": []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}},
	})
}

// codexHuman picks the human-turn event shape for the mode.
func codexHuman(mode codexMode, ts, text string) map[string]any {
	if mode == codexPaginated {
		return codexUserItem(ts, text)
	}
	return codexUserEvent(ts, text)
}

// codexUserResponseItem is a response_item / message with role user — the
// prompt's twin in legacy files, or injected context. Each text is one
// input_text part.
func codexUserResponseItem(ts string, texts ...string) map[string]any {
	return codexMessageItem(ts, "user", "input_text", texts...)
}

// codexDeveloperItem is a developer-role message (system-prompt fragments).
func codexDeveloperItem(ts, text string) map[string]any {
	return codexMessageItem(ts, "developer", "input_text", text)
}

// codexAssistant is the canonical assistant text: response_item / message with
// role assistant and output_text parts.
func codexAssistant(ts string, texts ...string) map[string]any {
	l := codexMessageItem(ts, "assistant", "output_text", texts...)
	l["payload"].(map[string]any)["phase"] = "commentary"
	return l
}

func codexMessageItem(ts, role, partType string, texts ...string) map[string]any {
	parts := make([]map[string]any, 0, len(texts))
	for _, t := range texts {
		parts = append(parts, map[string]any{"type": partType, "text": t})
	}
	return codexEnv(ts, "response_item", map[string]any{"type": "message", "role": role, "content": parts})
}

// codexEnvContext / codexRecommendedPlugins / codexAgentsMD are the observed
// injected-context user messages (never human turns).
func codexEnvContext(ts, cwd string) map[string]any {
	return codexUserResponseItem(ts, "<environment_context>\n  <cwd>"+cwd+"</cwd>\n  <shell>zsh</shell>\n</environment_context>")
}

func codexRecommendedPlugins(ts string) map[string]any {
	return codexUserResponseItem(ts, "<recommended_plugins>\nHere is a list of plugins that may help.\n</recommended_plugins>")
}

func codexAgentsMD(ts string) map[string]any {
	return codexUserResponseItem(ts, "# AGENTS.md instructions for /home/user/Projects/proj\n\n<INSTRUCTIONS>\nBe nice.\n</INSTRUCTIONS>")
}

// --- assistant duplicates in the event stream (skipped) ----------------------

func codexAgentMessageEvent(ts, text string) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{"type": "agent_message", "message": text, "phase": "commentary", "memory_citation": nil})
}

func codexAgentMessageItem(ts, text string) map[string]any {
	return codexItemCompletedLine(ts, map[string]any{
		"type": "AgentMessage", "id": "msg_085a52", "content": []map[string]any{{"type": "Text", "text": text}}, "phase": "commentary",
	})
}

// codexItemCompletedLine wraps a TurnItem in a paginated item_completed event.
func codexItemCompletedLine(ts string, item map[string]any) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{
		"type": "item_completed", "thread_id": codexFixtureID, "turn_id": "01a0714f-3474-7ef1-b960-bdad01a46df8",
		"item": item, "started_at_ms": 1788607412138, "completed_at_ms": 1788607412138,
	})
}

// --- tool calls and results ---------------------------------------------------

// codexFunctionCallLine builds a function_call whose arguments object is serialised
// as a JSON STRING (the wire form). args may be nil for an empty object.
func codexFunctionCallLine(t testing.TB, ts, callID, name string, args map[string]any) map[string]any {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	b, err := json.Marshal(args)
	require.NoError(t, err)
	return codexEnv(ts, "response_item", map[string]any{"type": "function_call", "name": name, "arguments": string(b), "call_id": callID})
}

// codexFunctionCallRaw builds a function_call with a verbatim arguments string
// (for the malformed-arguments path).
func codexFunctionCallRaw(ts, callID, name, arguments string) map[string]any {
	return codexEnv(ts, "response_item", map[string]any{"type": "function_call", "name": name, "arguments": arguments, "call_id": callID})
}

// codexFunctionOutput builds a function_call_output; output is a string or a
// content array ([]map[string]any).
func codexFunctionOutput(ts, callID string, output any) map[string]any {
	return codexEnv(ts, "response_item", map[string]any{"type": "function_call_output", "call_id": callID, "output": output})
}

// codexExecOutput wraps a shell body in the exec_command header.
func codexExecOutput(exitCode int, body string) string {
	return fmt.Sprintf("Chunk ID: 5e6cdb\nWall time: 0.1310 seconds\nProcess exited with code %d\nOriginal token count: 5\nOutput:\n%s", exitCode, body)
}

// codexExecPair is an exec_command call plus its header-wrapped result.
func codexExecPair(t testing.TB, ts1, ts2, callID, cmd string, exitCode int, body string) []map[string]any {
	t.Helper()
	return []map[string]any{
		codexFunctionCallLine(t, ts1, callID, "exec_command", map[string]any{"cmd": cmd, "workdir": "/home/user/Projects/proj", "yield_time_ms": 1000, "max_output_tokens": 4000}),
		codexFunctionOutput(ts2, callID, codexExecOutput(exitCode, body)),
	}
}

// codexCustomToolCallLine builds a custom_tool_call (apply_patch / exec) whose input
// is free text.
func codexCustomToolCallLine(ts, callID, name, input string) map[string]any {
	return codexEnv(ts, "response_item", map[string]any{"type": "custom_tool_call", "status": "completed", "call_id": callID, "name": name, "input": input})
}

// codexCustomToolOutput builds a custom_tool_call_output with a verbatim output
// string (normally the JSON codexCustomOutputJSON produces).
func codexCustomToolOutput(ts, callID, output string) map[string]any {
	return codexEnv(ts, "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": callID, "output": output})
}

// codexCustomToolOutputParts builds the content-ARRAY form of a
// custom_tool_call_output (the hosted exec tool: 539 of 598 local results),
// one input_text part per text plus a trailing image part.
func codexCustomToolOutputParts(ts, callID string, texts ...string) map[string]any {
	parts := make([]map[string]any, 0, len(texts)+1)
	for _, t := range texts {
		parts = append(parts, map[string]any{"type": "input_text", "text": t})
	}
	parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"})
	return codexEnv(ts, "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": callID, "output": parts})
}

// codexCustomOutputJSON is the JSON string inside custom_tool_call_output.output.
// exitCode < 0 omits metadata.exit_code.
func codexCustomOutputJSON(t testing.TB, output string, exitCode int) string {
	t.Helper()
	body := map[string]any{"output": output}
	if exitCode >= 0 {
		body["metadata"] = map[string]any{"exit_code": exitCode, "duration_seconds": 0.1}
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return string(b)
}

// codexAddPatch is a one-file apply_patch input adding script.py.
const codexAddPatch = "*** Begin Patch\n*** Add File: /tmp/proj/script.py\n+#!/usr/bin/env python3\n+import json\n*** End Patch\n"

// codexApplyPatchPair is an apply_patch call plus its result: success (exit 0,
// `Success. Updated the following files:` body) or failure (exit 1).
func codexApplyPatchPair(t testing.TB, ts1, ts2, callID, patch string, success bool) []map[string]any {
	t.Helper()
	files := codexPatchFiles(patch)
	var out string
	if success {
		var sb strings.Builder
		sb.WriteString("Success. Updated the following files:\n")
		for _, f := range files {
			sb.WriteString("A " + f + "\n")
		}
		out = codexCustomOutputJSON(t, sb.String(), 0)
	} else {
		out = codexCustomOutputJSON(t, "Failed to apply patch: context mismatch in "+strings.Join(files, ", ")+"\n", 1)
	}
	return []map[string]any{
		codexCustomToolCallLine(ts1, callID, "apply_patch", patch),
		codexCustomToolOutput(ts2, callID, out),
	}
}

// codexWebSearchCallLine builds a web_search_call with the given action.
func codexWebSearchCallLine(ts string, action map[string]any) map[string]any {
	return codexEnv(ts, "response_item", map[string]any{"type": "web_search_call", "status": "completed", "action": action})
}

// codexSpawnEnd is the legacy parent-side spawn record linking call_id → child.
func codexSpawnEnd(ts, callID, childID string) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{
		"type": "collab_agent_spawn_end", "call_id": callID, "sender_thread_id": codexFixtureParentID,
		"new_thread_id": childID, "new_agent_nickname": "Boole", "new_agent_role": "code-reviewer",
		"prompt": "Review the latest commit…", "model": "gpt-5.5", "reasoning_effort": "high", "status": "pending_init",
	})
}

// codexSubAgentActivity is the paginated parent-side spawn record.
func codexSubAgentActivity(ts, callID, childID, agentPath, kind string) map[string]any {
	return codexItemCompletedLine(ts, map[string]any{
		"type": "SubAgentActivity", "id": callID, "kind": kind, "agent_thread_id": childID, "agent_path": agentPath,
	})
}

// codexSpawnPair is a spawn_agent call, the mode's parent-side spawn event and
// the call's output. childID "" omits the spawn event (an unresolved launch).
func codexSpawnPair(t testing.TB, mode codexMode, ts1, ts2, ts3, callID string, args map[string]any, childID, agentPath string) []map[string]any {
	t.Helper()
	lines := []map[string]any{codexFunctionCallLine(t, ts1, callID, "spawn_agent", args)}
	if childID != "" {
		if mode == codexPaginated {
			lines = append(lines, codexSubAgentActivity(ts2, callID, childID, agentPath, "started"))
		} else {
			lines = append(lines, codexSpawnEnd(ts2, callID, childID))
		}
	}
	return append(lines, codexFunctionOutput(ts3, callID, `{"agent_id":"`+childID+`","status":"pending_init"}`))
}

// --- noise and bookkeeping (all skipped) ------------------------------------

func codexReasoning(ts string) map[string]any {
	return codexEnv(ts, "response_item", map[string]any{"type": "reasoning", "id": "rs_018ab236", "summary": []any{}, "encrypted_content": "gAAAAABqmBn9…"})
}

func codexReasoningItem(ts string) map[string]any {
	return codexItemCompletedLine(ts, map[string]any{"type": "Reasoning", "id": "rs_1", "summary": []any{}})
}

func codexTurnContext(ts, cwd string) map[string]any {
	return codexEnv(ts, "turn_context", map[string]any{"turn_id": "01a06225-0462-7b61-807f-4c5175c52572", "cwd": cwd, "model": "gpt-5.6-sol", "effort": "xhigh", "summary": "auto"})
}

func codexTaskStartedLine(ts string) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{"type": "task_started", "turn_id": "01a06225-0462-7b61-807f-4c5175c52572", "started_at": 1788352988, "model_context_window": 258400})
}

func codexTaskComplete(ts, last string) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{"type": "task_complete", "turn_id": "01a06225-0462-7b61-807f-4c5175c52572", "last_agent_message": last, "duration_ms": 2318})
}

func codexTurnAborted(ts string) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "019dbf8b-6f5b-70d1-af82-c61c535777eb", "reason": "interrupted", "duration_ms": 789})
}

func codexTokenCount(ts string) map[string]any {
	return codexEnv(ts, "event_msg", map[string]any{"type": "token_count", "info": map[string]any{"model_context_window": 258400}, "rate_limits": nil})
}

func codexWorldState(ts string) map[string]any {
	return codexEnv(ts, "world_state", map[string]any{"full": true, "state": `{"agents_md":{}}`})
}

func codexCompacted(ts string) map[string]any {
	return codexEnv(ts, "compacted", map[string]any{"message": "", "replacement_history": []map[string]any{
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "What about user-facing docs?"}}},
		{"type": "compaction", "encrypted_content": "gAAAAABp74mL…"},
	}})
}

func codexInterAgentMeta(ts string) map[string]any {
	return codexEnv(ts, "inter_agent_communication_metadata", map[string]any{"trigger_turn": false})
}

func codexTokenUsageRecord(ts string) map[string]any {
	return codexEnv(ts, "token_usage_record", map[string]any{"thread_id": codexFixtureID, "usage": map[string]any{"total_tokens": 15473}})
}

// codexAgentMessageResponseItem is the inter-agent `agent_message` response
// item a child receives (the task hand-off); skipped by the decoder.
func codexAgentMessageResponseItem(ts, text string) map[string]any {
	return codexEnv(ts, "response_item", map[string]any{"type": "agent_message", "author": "parent", "recipient": "child", "content": []map[string]any{{"type": "input_text", "text": text}}})
}

func codexCommandExecutionItem(ts, cmd string) map[string]any {
	return codexItemCompletedLine(ts, map[string]any{
		"type": "CommandExecution", "id": "exec-e21167b2", "command": []string{"/usr/bin/zsh", "-lc", cmd},
		"cwd": "file:///home/user/Projects/proj", "status": "completed", "aggregated_output": "# proj…", "exit_code": 0,
	})
}

func codexMcpToolCallItem(ts, tool string) map[string]any {
	return codexItemCompletedLine(ts, map[string]any{
		"type": "McpToolCall", "id": "exec-1377d957", "server": "capy", "tool": tool, "arguments": map[string]any{"queries": []string{"vault"}},
		"status": "completed", "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "## vault\n…"}}},
	})
}

// writeCodexRollout writes lines to <home>/<relPath> (slash-separated), zstd
// compressed with the vault's own encoder when compressed (relPath then ends in
// .zst), creating parent directories. It returns the absolute path. Used by the
// Task 7 discovery/import tests; codex_decoder_test.go round-trips it here.
func writeCodexRollout(t testing.TB, home, relPath string, lines []byte, compressed bool) string {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(relPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data := lines
	if compressed {
		require.True(t, strings.HasSuffix(relPath, ".zst"), "a compressed rollout path must end in .zst: %s", relPath)
		data = blobEncoder.EncodeAll(lines, nil)
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

// codexCase is one Codex fixture: name, rollout bytes and an optional lowered
// per-line cap (0 = production) for the oversize-line case.
type codexCase struct {
	name    string
	raw     []byte
	lineCap int
}

// Timestamps: cases use at(n) (n seconds after 2026-05-01T10:00:00Z). The
// session_meta payload timestamp is deliberately EARLIER than line 0's envelope
// time (Codex writes the envelope when the first turn starts).
const codexPayloadTS = "2026-05-01T09:58:30Z"

// codexCases is the Codex fixture table (the analogue of goldenCases). Names are
// stable identifiers referenced by codex_decoder_test.go's expectation table,
// which must cover every case and vice versa.
func codexCases(t testing.TB) []codexCase {
	t.Helper()
	var cases []codexCase
	add := func(name string, raw []byte) { cases = append(cases, codexCase{name: name, raw: raw}) }
	prompt := "Please review the design documents under docs/ and report inconsistencies."
	twoHunks := "*** Begin Patch\n*** Update File: /p/config.toml\n@@ [db]\n-retries = 1\n+retries = 3\n@@ [http]\n timeout = 30\n+keepalive = true\n*** End Patch\n"

	// legacy_basic: the canonical legacy turn — twin response_item + user_message
	// event, agent_message duplicate, exec_command pair, bookkeeping noise.
	add("legacy_basic", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexTaskStartedLine(at(0)), // 1
		codexDeveloperItem(at(1), "<permissions instructions>\nFilesystem sandboxing…"),                         // 2
		codexEnvContext(at(1), "/home/user/Projects/proj"),                                                      // 3
		codexUserResponseItem(at(1), prompt),                                                                    // 4 twin (not used: events win)
		codexUserEvent(at(1), prompt),                                                                           // 5 HUMAN
		codexReasoning(at(2)),                                                                                   // 6
		codexAssistant(at(3), "I'll review the three documents and trace each claim against the code."),         // 7
		codexAgentMessageEvent(at(3), "I'll review the three documents and trace each claim against the code."), // 8 dup
		codexExecPair(t, at(4), at(5), "call_1", "sed -n '1,240p' README.md", 0, "# proj\n\nREADME body…")[0],   // 9
		codexExecPair(t, at(4), at(5), "call_1", "sed -n '1,240p' README.md", 0, "# proj\n\nREADME body…")[1],   // 10
		codexAssistant(at(6), "The README matches the design."),                                                 // 11
		codexTaskComplete(at(6), "The README matches the design."),                                              // 12
		codexTokenCount(at(6)), // 13
	))

	// paginated_basic: the ≥ 0.147 shape — UserMessage item, Reasoning /
	// AgentMessage / CommandExecution / McpToolCall duplicates, an MCP call with
	// a content-array result, no git (non-git cwd).
	add("paginated_basic", codexRollout(t, codexPaginated,
		codexSessionMetaLine(at(0), codexPaginated, codexMetaOpts{cwd: "/tmp/proj", noGit: true, payloadTS: codexPayloadTS}), // 0
		codexTaskStartedLine(at(0)),                                                             // 1
		codexDeveloperItem(at(1), "<skills_instructions>\n## Skills"),                           // 2
		codexEnvContext(at(1), "/tmp/proj"),                                                     // 3
		codexUserResponseItem(at(1), "Let's work on issue #81."),                                // 4 twin
		codexUserItem(at(1), "Let's work on issue #81."),                                        // 5 HUMAN
		codexReasoningItem(at(2)),                                                               // 6
		codexReasoning(at(2)),                                                                   // 7
		codexAgentMessageItem(at(3), "I'm turning issue #81 into a reviewed design direction."), // 8 dup
		codexAssistant(at(3), "I'm turning issue #81 into a reviewed design direction."),        // 9
		codexExecPair(t, at(4), at(5), "call_2", "sed -n '1,260p' README.md", 0, "# proj…")[0],  // 10
		codexExecPair(t, at(4), at(5), "call_2", "sed -n '1,260p' README.md", 0, "# proj…")[1],  // 11
		codexCommandExecutionItem(at(5), "sed -n '1,260p' README.md"),                           // 12 dup
		codexFunctionCallLine(t, at(6), "call_3", "capy_search", map[string]any{"queries": []string{"vault scanner conventions"}, "source": "kk:project-conventions", "limit": 3}), // 13
		codexFunctionOutput(at(7), "call_3", []map[string]any{{"type": "input_text", "text": "## vault scanner conventions\n…"}, {"type": "input_image", "image_url": "data:…"}}),  // 14
		codexMcpToolCallItem(at(7), "capy_search"),             // 15 dup
		codexAssistant(at(8), "Found the convention note."),    // 16
		codexTaskComplete(at(8), "Found the convention note."), // 17
	))

	// apply_patch_success / failure / malformed / no_exit_code: the Diff rules.
	patchLines := func(patch string, success bool) []map[string]any {
		lines := []map[string]any{
			codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
			codexUserEvent(at(1), "add a script"),                                              // 1
			codexAssistant(at(2), "Adding it."),                                                // 2
		}
		return append(lines, codexApplyPatchPair(t, at(3), at(4), "call_p", patch, success)...) // 3, 4
	}
	add("apply_patch_success", codexRollout(t, codexLegacy, patchLines(codexAddPatch, true)...))
	add("apply_patch_failure", codexRollout(t, codexLegacy, patchLines(codexAddPatch, false)...))
	add("apply_patch_malformed", codexRollout(t, codexLegacy, patchLines("*** Begin Patch\n*** Add File: /tmp/proj/script.py\n+import json\n", true)...))
	add("apply_patch_two_hunks", codexRollout(t, codexLegacy, patchLines(twoHunks, true)...))
	add("apply_patch_no_exit_code", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}),                                                     // 0
		codexUserEvent(at(1), "add a script"),                                                                                                  // 1
		codexCustomToolCallLine(at(2), "call_p", "apply_patch", codexAddPatch),                                                                 // 2 (no assistant text → text-less assistant)
		codexCustomToolOutput(at(3), "call_p", codexCustomOutputJSON(t, "Success. Updated the following files:\nA /tmp/proj/script.py\n", -1)), // 3
		codexCustomToolCallLine(at(4), "call_q", "apply_patch", codexAddPatch),                                                                 // 4
		codexCustomToolOutput(at(5), "call_q", codexCustomOutputJSON(t, "Error: patch rejected\n", -1)),                                        // 5 no exit code, not Success. → no Diff
	))

	// custom_exec: the hosted `exec` custom tool — multi-line input, structured
	// output with exit code; plus an output string that is NOT JSON (verbatim).
	add("custom_exec", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}),                                             // 0
		codexUserEvent(at(1), "compute it"),                                                                                            // 1
		codexAssistant(at(2), "Running a script."),                                                                                     // 2
		codexCustomToolCallLine(at(3), "call_e", "exec", "const x = 1;\nconsole.log(x);\n"),                                            // 3
		codexCustomToolOutput(at(4), "call_e", codexCustomOutputJSON(t, "1\n", 0)),                                                     // 4
		codexCustomToolCallLine(at(5), "call_f", "exec", "   \n"),                                                                      // 5 blank input → bare name
		codexCustomToolOutput(at(6), "call_f", "plain text, not json {"),                                                               // 6 verbatim body, no exit line
		codexCustomToolCallLine(at(7), "call_g", "exec", "throw 1"),                                                                    // 7
		codexCustomToolOutput(at(8), "call_g", `{"output": 42, "metadata": {"exit_code": 0}}`),                                         // 8 JSON but .output not a string → verbatim
		codexCustomToolCallLine(at(9), "call_h", "exec", "await fetch('https://a.example')"),                                           // 9
		codexCustomToolOutputParts(at(10), "call_h", "Script completed\nWall time 1.9 seconds\nOutput:\n", "Airbnb: Vacation Rentals"), // 10 content-array form → header stripped
	))

	// spawn_legacy: agent_type + plain message; ChildUUID via collab_agent_spawn_end.
	add("spawn_legacy", codexRollout(t, codexLegacy, append([]map[string]any{
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{id: codexFixtureParentID, payloadTS: codexPayloadTS}), // 0
		codexUserEvent(at(1), "Review the latest commit"),                                                            // 1
		codexAssistant(at(2), "Spawning a reviewer."),                                                                // 2
	}, codexSpawnPair(t, codexLegacy, at(3), at(4), at(5), "call_s", map[string]any{"agent_type": "code-reviewer", "fork_context": true, "message": "Review the latest commit on the current branch…"}, codexFixtureChildID, "")...)..., // 3, 4, 5
	))

	// spawn_paginated: task_name + agent_type + ENCRYPTED message resolved via
	// SubAgentActivity; a message-only spawn resolved via SubAgentActivity
	// (label = agent_path); a message-only spawn with no event (unresolved).
	add("spawn_paginated", codexRollout(t, codexPaginated, append(append(append([]map[string]any{
		codexSessionMetaLine(at(0), codexPaginated, codexMetaOpts{id: codexFixtureParentID, payloadTS: codexPayloadTS}), // 0
		codexUserItem(at(1), "Detect the profiles"), // 1
		codexAssistant(at(2), "Delegating."),        // 2
	},
		codexSpawnPair(t, codexPaginated, at(3), at(4), at(5), "call_a", map[string]any{"task_name": "detect_profiles", "agent_type": "profile-resolver", "fork_turns": 0, "message": codexEncryptedMessage}, codexFixtureChild2ID, "/root/detect_profiles")...), // 3, 4, 5
		codexSpawnPair(t, codexPaginated, at(6), at(7), at(8), "call_b", map[string]any{"message": codexEncryptedMessage}, codexFixtureChildID, "/root/task1_review")...), // 6, 7, 8
		codexSpawnPair(t, codexPaginated, at(9), at(10), at(11), "call_c", map[string]any{"message": codexEncryptedMessage}, "", "")...)..., // 9, 10 (no event → 2 lines: 9, 10)
	))

	// child_legacy_137: a ≤ 0.137 child — parent only inside source.subagent,
	// nickname + role at the top level, spawn prompt as a user_message event.
	add("child_legacy_137", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{id: codexFixtureChildID, parent: codexFixtureParentID, nickname: "Boole", role: "code-reviewer", payloadTS: codexPayloadTS}), // 0
		codexTaskStartedLine(at(0)), // 1
		codexUserEvent(at(1), "Review the latest commit on the current branch…"), // 2 HUMAN (spawn prompt)
		codexAssistant(at(2), "Reviewing."),                                      // 3
	))

	// child_paginated_147: a ≥ 0.147 child — everything lifted, NO human turn by
	// any path (its only user-role item is <recommended_plugins>), the hand-off
	// arrives as an agent_message response item.
	add("child_paginated_147", codexRollout(t, codexPaginated,
		codexSessionMetaLine(at(0), codexPaginated, codexMetaOpts{id: codexFixtureChild2ID, parent: codexFixtureParentID, nickname: "Chandrasekhar", role: "code-reviewer", agentPath: "/root/task1_isolated_review", lifted: true, payloadTS: codexPayloadTS}), // 0
		codexTaskStartedLine(at(0)), // 1
		codexDeveloperItem(at(1), "## Plugin root\n\n`<kk-plugin-root>` = …"), // 2
		codexDeveloperItem(at(1), "You are an agent in a team of agents…"),    // 3
		codexRecommendedPlugins(at(1)),                                        // 4 noise, never a Human
		codexWorldState(at(1)),                                                // 5
		codexTurnContext(at(1), "/home/user/Projects/proj"),                   // 6
		codexInterAgentMeta(at(1)),                                            // 7
		codexAgentMessageResponseItem(at(1), "Message Type: NEW_TASK\nTask name: /root/task1_isolated_review"), // 8
		codexReasoningItem(at(2)), // 9
		codexReasoning(at(2)),     // 10
		codexAgentMessageItem(at(3), "I'm loading the review instructions."), // 11
		codexAssistant(at(3), "I'm loading the review instructions."),        // 12
	))

	// child_nickname_only: a 0.137 child with a lifted parent but no role and no
	// path — the agent title degrades to the nickname alone.
	add("child_nickname_only", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{id: codexFixtureChildID, parent: codexFixtureParentID, nickname: "Raman", lifted: true, payloadTS: codexPayloadTS}), // 0
		codexRecommendedPlugins(at(1)),  // 1
		codexAssistant(at(2), "On it."), // 2
	))

	// aborted_shell: a session aborted at startup — nothing the model said,
	// nothing the human said that reached the event stream. Zero entries; debug.
	add("aborted_shell", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexTaskStartedLine(at(0)), // 1
		codexDeveloperItem(at(1), "<permissions instructions>\nFilesystem sandboxing…"), // 2
		codexEnvContext(at(1), "/home/user/Projects/proj"),                              // 3
		codexTurnContext(at(1), "/home/user/Projects/proj"),                             // 4
		codexTokenCount(at(2)), // 5
		codexUserResponseItem(at(2), "<turn_aborted>\nThe user interrupted the turn.\n</turn_aborted>"), // 6
		codexTurnAborted(at(2)), // 7
	))

	// assistant_without_human: the model answered but no prompt was found → the
	// zero-human WARNING (non-subagent, ≥ 1 assistant, 0 human).
	add("assistant_without_human", codexRollout(t, codexPaginated,
		codexSessionMetaLine(at(0), codexPaginated, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexTaskStartedLine(at(0)),                                     // 1
		codexAssistant(at(1), "Hi! What would you like to work on?"),    // 2
		codexTaskComplete(at(1), "Hi! What would you like to work on?"), // 3
	))

	// fallback_response_items: NO event-derived human turn at all → the
	// noise-filtered response_item user messages become the human turns.
	add("fallback_response_items", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexDeveloperItem(at(1), "<permissions instructions>"),                            // 1 developer → never
		codexEnvContext(at(1), "/home/user/Projects/proj"),                                 // 2 `<` → dropped
		codexAgentsMD(at(1)), // 3 AGENTS.md → dropped
		codexUserResponseItem(at(1), "  What about user-facing docs?  ", "<skill>\n<name>x</name>\n</skill>"), // 4 HUMAN (first part; tag part dropped)
		codexRecommendedPlugins(at(1)),                                               // 5 dropped
		codexAssistant(at(2), "Documented in README."),                               // 6
		codexUserResponseItem(at(3), "<turn_aborted>\ninterrupted\n</turn_aborted>"), // 7 dropped
		codexUserResponseItem(at(3), "", "   "),                                      // 8 empty → nothing
	))

	// events_win_over_response_items: when even ONE event human exists, no
	// response_item user message is used — even a plain one with no twin.
	add("events_win_over_response_items", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexUserResponseItem(at(1), "plain response item without an event twin"),          // 1 not used
		codexUserEvent(at(2), "the real prompt"),                                           // 2 HUMAN
		codexAssistant(at(3), "ok"),                                                        // 3
	))

	// tool_only_assistant: calls with no preceding assistant text open a
	// text-less assistant at their own line; a result closes the attach window.
	add("tool_only_assistant", codexRollout(t, codexLegacy, append(append([]map[string]any{
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexUserEvent(at(1), "list files"),                                                // 1
	},
		codexExecPair(t, at(2), at(3), "call_1", "ls", 0, "a.go\nb.go")...), // 2 (new assistant), 3
		codexFunctionCallLine(t, at(4), "call_2", "exec_command", map[string]any{"cmd": "wc -l a.go"}), // 4 (new assistant: previous closed by the result)
		codexFunctionCallLine(t, at(4), "call_3", "update_plan", map[string]any{"plan": []any{}}),      // 5 attaches to 4's assistant
		codexFunctionOutput(at(5), "call_2", codexExecOutput(0, "12 a.go")),                            // 6
		codexFunctionOutput(at(5), "call_3", "Plan updated"),                                           // 7 no header → verbatim
		codexFunctionOutput(at(5), "call_9", "orphan output"),                                          // 8 unmatched id
		codexFunctionOutput(at(6), "call_2", ""),                                                       // 9 empty body still emitted (D18 parity)
	)...))

	// human_closes_attach_window: a call after a human turn (with no assistant
	// text in between) must not attach to the pre-human assistant.
	add("human_closes_attach_window", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexUserEvent(at(1), "first"),  // 1
		codexAssistant(at(2), "Sure."),  // 2
		codexUserEvent(at(3), "second"), // 3
		codexFunctionCallLine(t, at(4), "call_1", "exec_command", map[string]any{"cmd": "true"}), // 4 → new assistant
	))

	// exec_variants: header grammar edge cases.
	add("exec_variants", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}),       // 0
		codexUserEvent(at(1), "run things"),                                                      // 1
		codexAssistant(at(2), "Running."),                                                        // 2
		codexFunctionCallLine(t, at(3), "c1", "exec_command", map[string]any{"cmd": "sleep 30"}), // 3
		codexFunctionOutput(at(4), "c1", "Chunk ID: ab12cd\nWall time: 1.0 seconds\nProcess running with session ID 7\nOriginal token count: 0\nOutput:\n"),   // 4 running variant, empty tail
		codexFunctionCallLine(t, at(5), "c2", "exec", map[string]any{}),                                                                                       // 5 no cmd → bare
		codexFunctionOutput(at(6), "c2", []map[string]any{{"type": "input_text", "text": "Script completed\nWall time 0.0 seconds\nOutput:\nhello"}}),         // 6 array + no-colon Wall time
		codexFunctionCallLine(t, at(7), "c3", "exec_command", map[string]any{"cmd": "cat x"}),                                                                 // 7
		codexFunctionOutput(at(8), "c3", "Output:\nnot a header, kept whole"),                                                                                 // 8 no header line → unchanged
		codexFunctionCallRaw(at(9), "c4", "exec_command", "{not json"),                                                                                        // 9 malformed args → bare, nil Input
		codexFunctionOutput(at(10), "c4", "Chunk ID: x\nunexpected line\nOutput:\nbody"),                                                                      // 10 unexpected line → unchanged
		codexFunctionOutput(at(11), "c3", "Chunk ID: x\nWall time: 0.1 seconds\nProcess exited with code 2\nOriginal token count: 1\nOutput:\nerr\nline two"), // 11
	))

	// web_search_variants: query › queries[0] › url › pattern › bare.
	add("web_search_variants", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexUserEvent(at(1), "search the docs"),                                           // 1
		codexAssistant(at(2), "Searching."),                                                // 2
		codexWebSearchCallLine(at(3), map[string]any{"type": "search", "query": "site:developers.openai.com/codex hooks", "queries": []string{"other"}}), // 3
		codexWebSearchCallLine(at(3), map[string]any{"type": "search", "queries": []string{"first query", "second"}}),                                    // 4
		codexWebSearchCallLine(at(3), map[string]any{"type": "open_page", "url": "https://example.com/doc"}),                                             // 5
		codexWebSearchCallLine(at(3), map[string]any{"type": "find_in_page", "pattern": "needle"}),                                                       // 6
		codexWebSearchCallLine(at(3), map[string]any{"type": "search"}),                                                                                  // 7
		codexAssistant(at(4), "Found it."), // 8
	))

	// noise_and_unknown_types: every known skip type plus one unknown per level
	// (envelope, response_item, event_msg, turn item) → zero entries, fingerprint.
	add("noise_and_unknown_types", codexRollout(t, codexPaginated,
		codexSessionMetaLine(at(0), codexPaginated, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
		codexCompacted(at(1)),        // 1
		codexWorldState(at(1)),       // 2
		codexTokenUsageRecord(at(1)), // 3
		codexInterAgentMeta(at(1)),   // 4
		codexEnv(at(1), "security_risk_score", map[string]any{"score": 0}),                                     // 5 known
		codexEnv(at(2), "hologram_record", map[string]any{"type": "x"}),                                        // 6 unknown envelope
		codexEnv(at(2), "response_item", map[string]any{"type": "teleport_call", "name": "x"}),                 // 7 unknown response item
		codexEnv(at(2), "event_msg", map[string]any{"type": "collab_agent_wait_end"}),                          // 8 collab_* prefix: known family
		codexEnv(at(2), "event_msg", map[string]any{"type": "quantum_flux"}),                                   // 9 unknown event
		codexItemCompletedLine(at(3), map[string]any{"type": "HoloDeck", "id": "h1"}),                          // 10 unknown turn item
		codexItemCompletedLine(at(3), map[string]any{"type": "Extension", "id": "e1"}),                         // 11 known
		codexEnv(at(3), "response_item", map[string]any{"type": "tool_search_call", "queries": []string{"x"}}), // 12 known
	))

	// malformed_and_oversize: bad lines advance LineIndex; needs a lowered cap.
	oversize := codexUserEvent(at(2), strings.Repeat("y", 3000))
	cases = append(cases, codexCase{name: "malformed_and_oversize", lineCap: 2048, raw: []byte(
		string(codexRollout(t, codexLegacy,
			codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}), // 0
			codexUserEvent(at(1), "hello"), // 1
		)) +
			"not json at all\n" + // 2 malformed
			string(jsonlBytes(t, oversize)) + // 3 oversize → skipped, counted
			`{"timestamp":"` + at(3) + `","type":"session_meta","payload":"not an object"}` + "\n" + // 4 malformed session_meta payload (first wins anyway)
			`{"timestamp":"` + at(3) + `","type":"response_item","payload":[1,2]}` + "\n" + // 5 malformed response_item payload
			`{"timestamp":"` + at(3) + `","type":"event_msg","payload":"nope"}` + "\n" + // 6 malformed event payload
			`{"timestamp":"` + at(4) + `","type":"response_item","payload":{"type":"message","role":"assistant","content":"not an array"}}` + "\n" + // 7 message content wrong type → skipped
			string(jsonlBytes(t, codexAssistant(at(5), "after the bad lines"))) + // 8
			`{"timestamp":"` + at(6) + `","type":"event_msg","payload":{"type":"user_message","message":"trailing without newline"}}`, // 9 no trailing newline
	)})

	// no_session_meta: a rollout whose first line is not session_meta (corrupt
	// head) still decodes; StartTime falls back to the first envelope time.
	add("no_session_meta", codexRollout(t, codexLegacy,
		codexTurnContext(at(1), "/elsewhere"),    // 0
		codexUserEvent(at(2), "still a prompt"),  // 1
		codexAssistant(at(3), "still an answer"), // 2
	))

	// second_session_meta_ignored: only the first session_meta counts.
	add("second_session_meta_ignored", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}),  // 0
		codexSessionMetaLine(at(1), codexLegacy, codexMetaOpts{id: "other", cwd: "/other"}), // 1
		codexUserEvent(at(2), "hi"), // 2
	))

	// source_variants: an `exec` source string, and a unit-variant sub-agent
	// source ({"subagent":"review"}) with no parent → Source "subagent", no
	// ParentUUID, and no human → agent title empty (nothing to build it from).
	add("source_exec", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{source: "exec", payloadTS: codexPayloadTS}), // 0
		codexUserEvent(at(1), "non-interactive prompt"),                                                    // 1
		codexAssistant(at(2), "done"),                                                                      // 2
	))
	add("source_subagent_review", codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{source: map[string]any{"subagent": "review"}, payloadTS: codexPayloadTS}), // 0
		codexAssistant(at(1), "Reviewing the diff."), // 1 — no human, but subagent-sourced → no warning
	))

	// empty: zero bytes.
	add("empty", nil)

	return cases
}
