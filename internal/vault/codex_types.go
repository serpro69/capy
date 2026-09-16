package vault

import (
	"encoding/json"
	"sort"
	"strings"
)

// codex_types.go holds the Codex CLI rollout wire types the Codex decoder
// (codex_decoder.go) unmarshals into. Shapes follow openai/codex `main`
// (codex-rs/history/src/lib.rs RolloutLine, codex-rs/protocol/src/{protocol,
// models,items}.rs) as observed over the local corpus — see
// docs/feat/done/codex-vault-sessions/research.md § 3 and Appendix A (one
// redacted sample line per record type; codex_types_test.go unmarshals each).
//
// Only the fields the decoder reads are declared. Every struct tolerates
// absent fields (the session_meta payload alone has nine distinct key sets
// across CLI 0.124 → 0.154), and json.RawMessage is used wherever the wire
// value is polymorphic (`source` string|object, `output` string|array).

// codexLine is the rollout envelope: every physical line is
// {timestamp, ordinal?, type, payload}. `ordinal` (paginated files only) is a
// Codex history position, not a line index, and is deliberately not read —
// the decoder keeps its own physical LineIndex.
type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// Envelope `type` values the decoder switches on. Every other value (compacted,
// turn_context, world_state, token_usage_record, inter_agent_communication*,
// security_risk_score, retained_context, realtime_item, …) is skipped; ones
// outside codexKnownEnvelopeTypes are also collected into the per-file drift
// fingerprint.
const (
	codexTypeResponseItem = "response_item"
	codexTypeEventMsg     = "event_msg"
)

// codexPayloadType probes the discriminator shared by response_item payloads
// (`ResponseItem`, snake_case) and event_msg payloads (`EventMsg`, snake_case).
type codexPayloadType struct {
	Type string `json:"type"`
}

// codexSessionMeta is the `session_meta` payload — always line 0 of a rollout.
// `source` is a string for interactive sessions (cli | exec | vscode | mcp | …)
// and an object for sub-agents ({"subagent": {"thread_spawn": {…}}} or
// {"subagent": "review"} for the unit variants). CLI ≥ 0.137 also lifts
// parent_thread_id / agent_nickname / agent_role / agent_path to the payload
// top level; older children carry them only inside source.subagent.thread_spawn.
type codexSessionMeta struct {
	ID             string          `json:"id"`
	Timestamp      string          `json:"timestamp"`
	CWD            string          `json:"cwd"`
	CLIVersion     string          `json:"cli_version"`
	HistoryMode    string          `json:"history_mode"`
	Source         json.RawMessage `json:"source"`
	ThreadSource   string          `json:"thread_source"`
	ParentThreadID string          `json:"parent_thread_id"`
	AgentNickname  string          `json:"agent_nickname"`
	AgentRole      string          `json:"agent_role"`
	AgentPath      string          `json:"agent_path"`
	Git            *codexGit       `json:"git"`
}

// codexGit is session_meta.git; absent for a non-git cwd.
type codexGit struct {
	Branch string `json:"branch"`
}

// codexSubagentSource is the object form of session_meta.source. Subagent is
// raw because the SubAgentSource enum serialises its thread_spawn variant as an
// object and its unit variants (review, compact, memory_consolidation) as bare
// strings.
type codexSubagentSource struct {
	Subagent json.RawMessage `json:"subagent"`
}

// codexThreadSpawn is source.subagent.thread_spawn — the only sub-agent source
// that names a parent.
type codexThreadSpawn struct {
	ThreadSpawn *struct {
		ParentThreadID string `json:"parent_thread_id"`
		AgentPath      string `json:"agent_path"`
		AgentNickname  string `json:"agent_nickname"`
		AgentRole      string `json:"agent_role"`
	} `json:"thread_spawn"`
}

// codexSourceLabel returns Meta.Source for a session_meta `source` value: the
// string itself, "subagent" for the sub-agent object, the object's (sorted)
// first key for any other object, and "" when absent or unrecognised.
func codexSourceLabel(raw json.RawMessage) string {
	if s, ok := asJSONString(raw); ok {
		return s
	}
	if !isJSONObject(raw) {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}

// codexThreadSpawnOf returns the thread_spawn block of a sub-agent `source`, or
// nil when the source is a string, a unit-variant sub-agent, or malformed.
func codexThreadSpawnOf(raw json.RawMessage) *codexThreadSpawn {
	if !isJSONObject(raw) {
		return nil
	}
	var src codexSubagentSource
	if err := json.Unmarshal(raw, &src); err != nil || !isJSONObject(src.Subagent) {
		return nil
	}
	var ts codexThreadSpawn
	if err := json.Unmarshal(src.Subagent, &ts); err != nil || ts.ThreadSpawn == nil {
		return nil
	}
	return &ts
}

// --- response_item payloads (codex-rs/protocol/src/models.rs ResponseItem) ---

// codexMessage is a `message` response item. Role is user | assistant |
// developer; Content is a block array of {type: input_text | output_text |
// input_image | input_audio, text}. `phase` (commentary | final_answer) and
// `id` are not read.
type codexMessage struct {
	Role    string             `json:"role"`
	Content []codexContentPart `json:"content"`
}

// codexContentPart is one block of a message's content array. Type is
// input_text / output_text for text; anything else (input_image, input_audio,
// encrypted_content) carries no searchable text and is skipped.
type codexContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexFunctionCall is a `function_call` response item: a tool invocation whose
// arguments are a JSON object serialised as a STRING (exec_command,
// spawn_agent, update_plan, bare MCP tool names such as capy_search, …).
type codexFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	CallID    string `json:"call_id"`
}

// codexFunctionCallOutput is a `function_call_output` response item. Output is
// a plain string (5,500 of 5,533 locally) or a content array of
// {type: input_text | input_image | input_audio | encrypted_content, text}.
type codexFunctionCallOutput struct {
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

// codexCustomToolCall is a `custom_tool_call` response item (apply_patch, exec):
// its input is free text, not JSON — for apply_patch it is the
// `*** Begin Patch … *** End Patch` text itself.
type codexCustomToolCall struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	CallID string `json:"call_id"`
}

// codexCustomToolCallOutput is a `custom_tool_call_output` response item. Output
// is polymorphic: for apply_patch (24/24 locally) and some exec results (59) it
// is a STRING that itself holds JSON of shape codexCustomOutputBody — the
// decoder performs the second decode and keeps the raw string verbatim when it
// fails; for most hosted `exec` results (539/598) it is a content ARRAY of
// {type: input_text | input_image, text} parts whose joined text carries the
// exec header (`Script completed` / `Wall time …` / `Output:`), handled exactly
// like a function_call_output array. research.md § 3.3 describes only the
// string form; the array form was found by the corpus canary.
type codexCustomToolCallOutput struct {
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

// codexCustomOutputBody is the JSON carried inside
// custom_tool_call_output.output: {output, metadata{exit_code, duration_seconds}}.
// ExitCode is a pointer so "absent" and 0 are distinguishable (the success rule
// falls back to the body prefix when it is absent).
type codexCustomOutputBody struct {
	Output   string `json:"output"`
	Metadata struct {
		ExitCode *int `json:"exit_code"`
	} `json:"metadata"`
}

// codexWebSearchCall is a `web_search_call` response item. It carries no
// call_id; its action is one of {type: search, query, queries[]},
// {type: open_page, url}, {type: find_in_page, url, pattern}, or a bare
// {type} — the summary prefers query › queries[0] › url › pattern.
type codexWebSearchCall struct {
	Action json.RawMessage `json:"action"`
}

// codexWebSearchAction is the decoded web_search_call.action.
type codexWebSearchAction struct {
	Type    string   `json:"type"`
	Query   string   `json:"query"`
	Queries []string `json:"queries"`
	URL     string   `json:"url"`
	Pattern string   `json:"pattern"`
}

// --- event_msg payloads (codex-rs/protocol/src/protocol.rs EventMsg) ---

// codexUserMessageEvent is a legacy (< 0.147) `user_message` event — THE human
// prompt in legacy files (images / local_images / text_elements not read).
type codexUserMessageEvent struct {
	Message string `json:"message"`
}

// codexItemCompleted is a paginated (≥ 0.147) `item_completed` event wrapping a
// TurnItem. `thread_id` / `turn_id` / timings are not read.
type codexItemCompleted struct {
	Item codexTurnItem `json:"item"`
}

// codexTurnItem is a paginated TurnItem (codex-rs/protocol/src/items.rs). Type
// is PascalCase on the wire (UserMessage, AgentMessage, SubAgentActivity, …).
// Content is read for UserMessage ({type: text, text}); ID (= the spawn call's
// call_id), AgentThreadID and AgentPath for SubAgentActivity. Every other item
// type duplicates a response_item and is skipped.
type codexTurnItem struct {
	Type          string             `json:"type"`
	ID            string             `json:"id"`
	Content       []codexContentPart `json:"content"`
	AgentThreadID string             `json:"agent_thread_id"`
	AgentPath     string             `json:"agent_path"`
}

// codexCollabSpawnEnd is a legacy `collab_agent_spawn_end` event: the parent-side
// record linking a spawn_agent call_id to the child rollout's thread id.
type codexCollabSpawnEnd struct {
	CallID           string `json:"call_id"`
	NewThreadID      string `json:"new_thread_id"`
	NewAgentNickname string `json:"new_agent_nickname"`
	NewAgentRole     string `json:"new_agent_role"`
}

// codexTaskStarted is a `task_started` event (both history modes) — a turn
// boundary. The decoder does not use it (turns follow the shared "a human
// entry starts a turn" heuristic); the canary reads it to characterise
// aborted-at-startup shells.
type codexTaskStarted struct {
	TurnID string `json:"turn_id"`
}

// codexSpawnArgs is the decoded `arguments` of a spawn_agent function_call.
// Message is ENCRYPTED from CLI 0.147 (a `gAAAAAB…` token), so it is only ever
// used as the last-resort label.
type codexSpawnArgs struct {
	TaskName  string `json:"task_name"`
	AgentType string `json:"agent_type"`
	Message   string `json:"message"`
}

// codexKnownEnvelopeTypes / codexKnownResponseItemTypes / codexKnownEventTypes /
// codexKnownTurnItemTypes enumerate every record type the decoder either handles
// or knowingly skips. A type outside its set is skipped too (ADR-021) but
// recorded in the per-file drift fingerprint the decoder logs at debug level, so
// a Codex upgrade that introduces a new record shape is visible in the logs
// before it is silently lost from search.
var (
	codexKnownEnvelopeTypes = map[string]bool{
		"session_meta": true, codexTypeResponseItem: true, codexTypeEventMsg: true,
		"compacted": true, "turn_context": true, "world_state": true, "token_usage_record": true,
		"inter_agent_communication": true, "inter_agent_communication_metadata": true,
		"security_risk_score": true, "retained_context": true, "realtime_item": true,
	}
	codexKnownResponseItemTypes = map[string]bool{
		"message": true, "function_call": true, "function_call_output": true,
		"custom_tool_call": true, "custom_tool_call_output": true, "web_search_call": true,
		"reasoning": true, "agent_message": true, "local_shell_call": true,
		"tool_search_call": true, "tool_search_output": true, "image_generation_call": true,
		"compaction": true, "configuration_update": true, "context_compaction": true,
	}
	codexKnownEventTypes = map[string]bool{
		"user_message": true, "item_completed": true, "collab_agent_spawn_end": true,
		"agent_message": true, "task_started": true, "task_complete": true, "turn_aborted": true,
		"token_count": true, "thread_settings_applied": true, "exec_command_end": true,
		"mcp_tool_call_end": true, "web_search_end": true, "patch_apply_end": true,
		"context_compacted": true, "entered_review_mode": true, "exited_review_mode": true,
	}
	codexKnownTurnItemTypes = map[string]bool{
		"UserMessage": true, "AgentMessage": true, "Reasoning": true, "CommandExecution": true,
		"McpToolCall": true, "FileChange": true, "WebSearch": true, "ImageView": true,
		"SubAgentActivity": true, "ContextCompaction": true, "Extension": true, "Plan": true,
		"FunctionCallOutput": true, "CollabAgentToolCall": true,
	}
)

// codexTextParts joins the text of the text-bearing parts (input_text,
// output_text, and the paginated item form `text`) with "\n", TrimSpace'd per
// part and overall; non-text parts are skipped. Returns "" when nothing remains.
func codexTextParts(parts []codexContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text":
			t := strings.TrimSpace(p.Text)
			if t == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(t)
		}
	}
	return b.String()
}
