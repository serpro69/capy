package vault

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codex_types_test.go unmarshals the redacted real lines of research.md
// Appendix A into the wire types, one per record shape, so a struct that
// drifts from the corpus fails here before it fails on the decoder.

// unmarshalCodexLine parses an Appendix A line into the envelope and asserts
// its type; the payload is returned for the typed decode.
func unmarshalCodexLine(t *testing.T, raw, wantType string) json.RawMessage {
	t.Helper()
	var line codexLine
	require.NoError(t, json.Unmarshal([]byte(raw), &line))
	assert.Equal(t, wantType, line.Type)
	assert.NotEmpty(t, line.Timestamp, "every rollout line is timestamped")
	assert.False(t, parseJSONLTime(line.Timestamp).IsZero(), "RFC3339 with ms")
	return line.Payload
}

func TestCodexTypes_SessionMeta(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-05-13T19:11:47.906Z","type":"session_meta","payload":{"id":"019e22ba-4c15-7ae0-a903-255537a6a1b3","timestamp":"2026-05-13T19:04:55.061Z","cwd":"/home/user/Projects/proj","originator":"codex-tui","cli_version":"0.130.0","source":"cli","thread_source":"user","model_provider":"openai","base_instructions":{"text":"You are Codex, a coding agent based on G…"},"git":{"commit_hash":"17985d49e752c3f3c5aeaedd55364f12cc1a43bc","branch":"master","repository_url":"git@github.com:org/repo.git"}}}`, "session_meta")
		var m codexSessionMeta
		require.NoError(t, json.Unmarshal(p, &m))
		assert.Equal(t, "019e22ba-4c15-7ae0-a903-255537a6a1b3", m.ID)
		assert.Equal(t, "2026-05-13T19:04:55.061Z", m.Timestamp)
		assert.Equal(t, "/home/user/Projects/proj", m.CWD)
		assert.Equal(t, "0.130.0", m.CLIVersion)
		assert.Empty(t, m.HistoryMode, "absent ⇒ legacy")
		assert.Equal(t, "cli", codexSourceLabel(m.Source))
		assert.Nil(t, codexThreadSpawnOf(m.Source))
		require.NotNil(t, m.Git)
		assert.Equal(t, "master", m.Git.Branch)
		assert.Empty(t, m.ParentThreadID)
	})
	t.Run("paginated, non-git cwd", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-09-02T12:43:08.280Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a06224-f7d9-71d3-b6bb-4ea07576fdb3","id":"01a06224-f7d9-71d3-b6bb-4ea07576fdb3","timestamp":"2026-09-02T12:43:05.050Z","cwd":"/tmp/proj","originator":"codex-tui","cli_version":"0.152.1","source":"cli","thread_source":"user","model_provider":"openai","base_instructions":{"text":"You are Codex, an agent based on GPT-5. …","provenance":{"type":"model","model":"gpt-5.6-sol"}},"history_mode":"paginated","context_window":{"window_id":"01a06224-f7d9-71d3-b6bb-4eb9b471a48a"}}}`, "session_meta")
		var m codexSessionMeta
		require.NoError(t, json.Unmarshal(p, &m))
		assert.Equal(t, "paginated", m.HistoryMode)
		assert.Nil(t, m.Git)
		assert.Equal(t, "/tmp/proj", m.CWD)
	})
	t.Run("sub-agent child 0.125", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-04-25T19:05:08.569Z","type":"session_meta","payload":{"id":"019dc608-0061-7d90-a4e9-fca142504625","timestamp":"2026-04-25T19:05:06.466Z","cwd":"/home/user/Projects/proj","originator":"codex-tui","cli_version":"0.125.0","source":{"subagent":{"thread_spawn":{"parent_thread_id":"019dc606-f552-7f93-b9a1-c5620b23b8dd","depth":1,"agent_path":null,"agent_nickname":"Boole","agent_role":"code-reviewer"}}},"agent_nickname":"Boole","agent_role":"code-reviewer","model_provider":"openai","base_instructions":{"text":"…"},"git":{"commit_hash":"cb1ad9a7c33419f424fad1f5c6c2a499edc7ced4","branch":"feat/x","repository_url":"git@github.com:org/repo.git"}}}`, "session_meta")
		var m codexSessionMeta
		require.NoError(t, json.Unmarshal(p, &m))
		assert.Equal(t, "subagent", codexSourceLabel(m.Source))
		spawn := codexThreadSpawnOf(m.Source)
		require.NotNil(t, spawn)
		assert.Equal(t, "019dc606-f552-7f93-b9a1-c5620b23b8dd", spawn.ThreadSpawn.ParentThreadID)
		assert.Equal(t, "Boole", spawn.ThreadSpawn.AgentNickname)
		assert.Equal(t, "code-reviewer", spawn.ThreadSpawn.AgentRole)
		assert.Empty(t, spawn.ThreadSpawn.AgentPath, "null → empty")
		assert.Empty(t, m.ParentThreadID, "not lifted in 0.125")
		assert.Equal(t, "Boole", m.AgentNickname)
	})
	t.Run("source variants", func(t *testing.T) {
		assert.Equal(t, "exec", codexSourceLabel(json.RawMessage(`"exec"`)))
		assert.Equal(t, "subagent", codexSourceLabel(json.RawMessage(`{"subagent":"review"}`)), "unit-variant sub-agent")
		assert.Nil(t, codexThreadSpawnOf(json.RawMessage(`{"subagent":"review"}`)), "no parent for a unit variant")
		assert.Equal(t, "alpha", codexSourceLabel(json.RawMessage(`{"zeta":1,"alpha":2}`)), "unknown object → sorted first key")
		assert.Empty(t, codexSourceLabel(nil))
		assert.Empty(t, codexSourceLabel(json.RawMessage(`42`)))
		assert.Empty(t, codexSourceLabel(json.RawMessage(`{}`)))
		assert.Nil(t, codexThreadSpawnOf(json.RawMessage(`{"subagent":{"other":{}}}`)))
		assert.Nil(t, codexThreadSpawnOf(json.RawMessage(`{"subagent":[1]}`)))
	})
}

func TestCodexTypes_Messages(t *testing.T) {
	for name, tc := range map[string]struct {
		raw, role, text string
	}{
		"developer":     {`{"timestamp":"2026-09-02T12:43:08.704Z","ordinal":2,"type":"response_item","payload":{"type":"message","id":"msg_01a06225-0620-75b2-a7a4-9667890893d7","role":"developer","content":[{"type":"input_text","text":"<skills_instructions>\n## Skills\nA skill is a set of local instructions…"}],"internal_chat_message_metadata_passthrough":{"turn_id":"01a06225-0462-7b61-807f-4c5175c52572","create_time":1788352988.7040403,"content_item_kinds":["host_skills.instructions","permissions.instructions"]}}}`, "developer", "<skills_instructions>\n## Skills\nA skill is a set of local instructions…"},
		"user injected": {`{"timestamp":"2026-09-02T12:43:08.704Z","ordinal":5,"type":"response_item","payload":{"type":"message","id":"msg_01a06225-0620-75b2-a7a4-96948e8943db","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/tmp/proj</cwd>\n  <shell>zsh</shell>\n  <current_date>…"}],"internal_chat_message_metadata_passthrough":{"turn_id":"01a06225-0462-7b61-807f-4c5175c52572","create_time":1788352988.7040434,"content_item_kinds":["environments.environment_context"]}}}`, "user", "<environment_context>\n  <cwd>/tmp/proj</cwd>\n  <shell>zsh</shell>\n  <current_date>…"},
		"user prompt":   {`{"timestamp":"2026-05-13T19:11:48.041Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Please review the design documents under docs/ and report inconsistencies."}]}}`, "user", "Please review the design documents under docs/ and report inconsistencies."},
		"assistant":     {`{"timestamp":"2026-05-13T19:12:00.963Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll review the three documents and trace each claim against the code."}],"phase":"commentary"}}`, "assistant", "I'll review the three documents and trace each claim against the code."},
	} {
		t.Run(name, func(t *testing.T) {
			p := unmarshalCodexLine(t, tc.raw, "response_item")
			var probe codexPayloadType
			require.NoError(t, json.Unmarshal(p, &probe))
			assert.Equal(t, "message", probe.Type)
			var m codexMessage
			require.NoError(t, json.Unmarshal(p, &m))
			assert.Equal(t, tc.role, m.Role)
			assert.Equal(t, tc.text, codexTextParts(m.Content))
		})
	}
	assert.Equal(t, "a\nb", codexTextParts([]codexContentPart{{Type: "input_text", Text: " a "}, {Type: "input_image", Text: "zzz"}, {Type: "text", Text: "b"}, {Type: "output_text", Text: "  "}}))
	assert.Empty(t, codexTextParts(nil))
}

func TestCodexTypes_Events(t *testing.T) {
	t.Run("legacy user_message", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-05-13T19:11:48.042Z","type":"event_msg","payload":{"type":"user_message","message":"Please review the design documents under docs/ and report inconsistencies.","images":[],"local_images":[],"text_elements":[]}}`, "event_msg")
		var ev codexUserMessageEvent
		require.NoError(t, json.Unmarshal(p, &ev))
		assert.Equal(t, "Please review the design documents under docs/ and report inconsistencies.", ev.Message)
	})
	t.Run("paginated UserMessage item", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-09-05T11:23:32.138Z","ordinal":10,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-cbc4-7d23-9f9d-02d710c07ed7","turn_id":"01a0714f-3474-7ef1-b960-bdad01a46df8","item":{"type":"UserMessage","id":"01a0714f-37aa-7f92-869a-f8c1130b8fdb","content":[{"type":"text","text":"Let's work on issue #81. Use kk:design to discuss and design the feature.","text_elements":[]}]},"started_at_ms":1788607412138,"completed_at_ms":1788607412138}}`, "event_msg")
		var ev codexItemCompleted
		require.NoError(t, json.Unmarshal(p, &ev))
		assert.Equal(t, "UserMessage", ev.Item.Type)
		assert.Equal(t, "Let's work on issue #81. Use kk:design to discuss and design the feature.", codexTextParts(ev.Item.Content))
	})
	t.Run("paginated AgentMessage item", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-09-05T11:23:36.321Z","ordinal":13,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-cbc4-7d23-9f9d-02d710c07ed7","turn_id":"01a0714f-3474-7ef1-b960-bdad01a46df8","item":{"type":"AgentMessage","id":"msg_085a52…","content":[{"type":"Text","text":"I'm turning issue #81 into a reviewed design direction."}],"phase":"commentary"},"started_at_ms":1788607415164,"completed_at_ms":1788607416321}}`, "event_msg")
		var ev codexItemCompleted
		require.NoError(t, json.Unmarshal(p, &ev))
		assert.Equal(t, "AgentMessage", ev.Item.Type)
		assert.True(t, codexKnownTurnItemTypes[ev.Item.Type], "skipped, not fingerprinted")
	})
	t.Run("task_started / task_complete / turn_aborted / token_count", func(t *testing.T) {
		for _, raw := range []string{
			`{"timestamp":"2026-09-02T12:43:08.280Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"01a06225-0462-7b61-807f-4c5175c52572","started_at":1788352988,"model_context_window":258400,"collaboration_mode_kind":"default"}}`,
			`{"timestamp":"2026-09-02T12:43:10.581Z","ordinal":13,"type":"event_msg","payload":{"type":"task_complete","turn_id":"01a06225-0462-7b61-807f-4c5175c52572","last_agent_message":"Hi! What would you like to work on?","started_at":1788352988,"completed_at":1788352990,"duration_ms":2318,"time_to_first_token_ms":1925}}`,
			`{"timestamp":"2026-04-24T12:51:20.306Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"019dbf8b-6f5b-70d1-af82-c61c535777eb","reason":"interrupted","completed_at":1777035080,"duration_ms":789}}`,
			`{"timestamp":"2026-09-02T12:43:10.577Z","ordinal":12,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":"…","model_context_window":258400,"total_token_usage":"…"},"rate_limits":"…"}}`,
		} {
			p := unmarshalCodexLine(t, raw, "event_msg")
			var probe codexPayloadType
			require.NoError(t, json.Unmarshal(p, &probe))
			assert.True(t, codexKnownEventTypes[probe.Type], probe.Type)
			if probe.Type == "task_started" {
				var ts codexTaskStarted
				require.NoError(t, json.Unmarshal(p, &ts))
				assert.Equal(t, "01a06225-0462-7b61-807f-4c5175c52572", ts.TurnID)
			}
		}
	})
	t.Run("legacy collab_agent_spawn_end", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-04-25T19:05:06.724Z","type":"event_msg","payload":{"type":"collab_agent_spawn_end","call_id":"call_YOG7fKaiPd6iRKbLKV5JJxf8","sender_thread_id":"019dc606-f552-7f93-b9a1-c5620b23b8dd","new_thread_id":"019dc608-0061-7d90-a4e9-fca142504625","new_agent_nickname":"Boole","new_agent_role":"code-reviewer","prompt":"Review the latest commit on the current branch…","model":"gpt-5.5","reasoning_effort":"high","status":"pending_init"}}`, "event_msg")
		var ev codexCollabSpawnEnd
		require.NoError(t, json.Unmarshal(p, &ev))
		assert.Equal(t, "call_YOG7fKaiPd6iRKbLKV5JJxf8", ev.CallID)
		assert.Equal(t, "019dc608-0061-7d90-a4e9-fca142504625", ev.NewThreadID)
		assert.Equal(t, "Boole", ev.NewAgentNickname)
	})
	t.Run("paginated SubAgentActivity", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-09-05T11:24:21.149Z","ordinal":57,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-cbc4-7d23-9f9d-02d710c07ed7","turn_id":"01a0714f-3474-7ef1-b960-bdad01a46df8","item":{"type":"SubAgentActivity","id":"call_Ttit3KpsQjMgX6QwLug6dzXa","kind":"started","agent_thread_id":"01a0714f-f6ea-7bb1-bdeb-dc80fab2f1bf","agent_path":"/root/detect_profiles"},"started_at_ms":1788607461149,"completed_at_ms":1788607461149}}`, "event_msg")
		var ev codexItemCompleted
		require.NoError(t, json.Unmarshal(p, &ev))
		assert.Equal(t, "SubAgentActivity", ev.Item.Type)
		assert.Equal(t, "call_Ttit3KpsQjMgX6QwLug6dzXa", ev.Item.ID, "item.id is the spawn call_id")
		assert.Equal(t, "01a0714f-f6ea-7bb1-bdeb-dc80fab2f1bf", ev.Item.AgentThreadID)
		assert.Equal(t, "/root/detect_profiles", ev.Item.AgentPath)
	})
}

func TestCodexTypes_ToolCalls(t *testing.T) {
	t.Run("exec_command function_call", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-05-13T19:12:06.002Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n '1,240p' /home/user/Projects/proj/README.md\",\"workdir\":\"/home/user/Projects/proj\",\"yield_time_ms\":1000,\"max_output_tokens\":4000}","call_id":"call_2NEhPnK4XuycKlDAmpVx6QWd"}}`, "response_item")
		var fc codexFunctionCall
		require.NoError(t, json.Unmarshal(p, &fc))
		assert.Equal(t, "exec_command", fc.Name)
		assert.Equal(t, "call_2NEhPnK4XuycKlDAmpVx6QWd", fc.CallID)
		assert.True(t, json.Valid([]byte(fc.Arguments)), "arguments is a JSON object serialised as a string")
		assert.Equal(t, "exec_command sed -n '1,240p' /home/user/Projects/proj/README.md", codexFunctionCallSummary(fc))
	})
	t.Run("exec_command output with header", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-05-13T19:12:06.140Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_2NEhPnK4XuycKlDAmpVx6QWd","output":"Chunk ID: 5e6cdb\nWall time: 0.1310 seconds\nProcess exited with code 0\nOriginal token count: 5\nOutput:\n# proj\n\nREADME body…"}}`, "response_item")
		var out codexFunctionCallOutput
		require.NoError(t, json.Unmarshal(p, &out))
		assert.Equal(t, "Process exited with code 0\n# proj\n\nREADME body…", stripExecHeader(codexOutputText(out.Output)))
	})
	t.Run("MCP function_call is a bare name", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-09-05T11:31:29.960Z","ordinal":241,"type":"response_item","payload":{"type":"function_call","name":"capy_search","arguments":"{\"queries\":[\"vault scanner conventions\"],\"source\":\"kk:project-conventions\",\"limit\":3}","call_id":"call_XXXXXXXXXXXXXXXXXXXXXXXX"}}`, "response_item")
		var fc codexFunctionCall
		require.NoError(t, json.Unmarshal(p, &fc))
		assert.Equal(t, "capy_search", codexFunctionCallSummary(fc))
	})
	t.Run("content-array output", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-08-28T13:11:09.248Z","ordinal":178,"type":"response_item","payload":{"type":"function_call_output","id":"fco_01a0487e-dec0-7643-9647-4a03ca597336","call_id":"call_6uqdeRXUesJmNplXDPt8wcfK","output":[{"type":"input_text","text":"Script completed\nWall time 0.0 seconds\nOutput:\n"}],"internal_chat_message_metadata_passthrough":{"turn_id":"01a04876-de72-7420-8734-9068acba0efe","create_time":1787922669.2481296}}}`, "response_item")
		var out codexFunctionCallOutput
		require.NoError(t, json.Unmarshal(p, &out))
		assert.Equal(t, "Script completed\nWall time 0.0 seconds\nOutput:", codexOutputText(out.Output), "parts are TrimSpace'd")
		assert.Equal(t, "Script completed", stripExecHeader(codexOutputText(out.Output)))
	})
	t.Run("spawn_agent", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-04-25T19:05:00.552Z","type":"response_item","payload":{"type":"function_call","name":"spawn_agent","arguments":"{\"agent_type\":\"code-reviewer\",\"fork_context\":true,\"message\":\"Review the latest commit on the current branch…\"}","call_id":"call_6SXk8LhIEcye4gXl5reZ8gb9"}}`, "response_item")
		var fc codexFunctionCall
		require.NoError(t, json.Unmarshal(p, &fc))
		var args codexSpawnArgs
		require.NoError(t, json.Unmarshal([]byte(fc.Arguments), &args))
		assert.Equal(t, "code-reviewer", args.AgentType)
		assert.Empty(t, args.TaskName)
		assert.Equal(t, "spawn_agent code-reviewer", codexFunctionCallSummary(fc))
	})
	t.Run("apply_patch pair", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-04-24T13:04:38.070Z","type":"response_item","payload":{"type":"custom_tool_call","status":"completed","call_id":"call_8DaKfXFwQ5Gij3AqBTbNnodD","name":"apply_patch","input":"*** Begin Patch\n*** Add File: /tmp/proj/script.py\n+#!/usr/bin/env python3\n+import json\n*** End Patch\n"}}`, "response_item")
		var cc codexCustomToolCall
		require.NoError(t, json.Unmarshal(p, &cc))
		assert.Equal(t, "apply_patch", cc.Name)
		assert.Equal(t, "apply_patch /tmp/proj/script.py", codexCustomCallSummary(cc))
		_, added, _, ok := codexPatchToDiff(cc.Input)
		assert.True(t, ok)
		assert.Equal(t, 2, added)

		p = unmarshalCodexLine(t, `{"timestamp":"2026-04-24T13:04:38.117Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_8DaKfXFwQ5Gij3AqBTbNnodD","output":"{\"output\":\"Success. Updated the following files:\\nA /tmp/proj/script.py\\n\",\"metadata\":{\"exit_code\":0,\"duration_seconds\":0.1}}"}}`, "response_item")
		var out codexCustomToolCallOutput
		require.NoError(t, json.Unmarshal(p, &out))
		inner, ok := asJSONString(out.Output)
		require.True(t, ok, "apply_patch output is the JSON-string form")
		body, structured, success := codexCustomOutputText(inner)
		assert.True(t, structured)
		assert.True(t, success)
		assert.Equal(t, "Process exited with code 0\nSuccess. Updated the following files:\nA /tmp/proj/script.py\n", body)
	})
	t.Run("reasoning is known noise", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-09-02T12:43:42.011Z","ordinal":20,"type":"response_item","payload":{"type":"reasoning","id":"rs_018ab236…","summary":[],"encrypted_content":"gAAAAABqmBn9…","internal_chat_message_metadata_passthrough":{"turn_id":"01a06225-7cd9-7362-8cf1-84fc4391b3d1"}}}`, "response_item")
		var probe codexPayloadType
		require.NoError(t, json.Unmarshal(p, &probe))
		assert.True(t, codexKnownResponseItemTypes[probe.Type])
	})
	t.Run("web_search_call", func(t *testing.T) {
		p := unmarshalCodexLine(t, `{"timestamp":"2026-04-24T13:00:03.106Z","type":"response_item","payload":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"site:developers.openai.com/codex hooks","queries":["site:developers.openai.com/codex hooks"]}}}`, "response_item")
		var ws codexWebSearchCall
		require.NoError(t, json.Unmarshal(p, &ws))
		assert.Equal(t, "web_search site:developers.openai.com/codex hooks", codexWebSearchSummary(ws.Action))
	})
	t.Run("paginated CommandExecution / McpToolCall / FileChange items are known", func(t *testing.T) {
		for _, raw := range []string{
			`{"timestamp":"2026-09-05T11:23:37.973Z","ordinal":16,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-…","turn_id":"01a0714f-…","item":{"type":"CommandExecution","id":"exec-e21167b2-4f0f-44b1-a6f9-5eb82ddec091","process_id":"45532","command":["/usr/bin/zsh","-lc","sed -n '1,260p' README.md"],"cwd":"file:///home/user/Projects/proj","parsed_cmd":[{"type":"read","cmd":"sed -n '1,260p' README.md","name":"README.md","path":"/home/user/Projects/proj/README.md"}],"source":"unified_exec_startup","status":"completed","stdout":"# proj…","stderr":"","aggregated_output":"# proj…","exit_code":0,"duration":{"secs":0,"nanos":109277969},"formatted_output":"# proj…"},"started_at_ms":1788607417863,"completed_at_ms":1788607417972}}`,
			`{"timestamp":"2026-09-05T11:31:30.267Z","ordinal":242,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-…","turn_id":"01a07153-…","item":{"type":"McpToolCall","id":"exec-1377d957-…","server":"capy","tool":"capy_search","arguments":{"queries":["vault scanner conventions"],"source":"kk:project-conventions","limit":3},"readOnlyHint":true,"status":"completed","result":{"content":[{"type":"text","text":"## vault scanner conventions\n…"}]},"duration":{"secs":0,"nanos":304582142}},"started_at_ms":1788607889962,"completed_at_ms":1788607890266}}`,
			`{"timestamp":"2026-09-05T12:42:04.387Z","ordinal":649,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-…","turn_id":"01a0718e-…","item":{"type":"FileChange","id":"exec-0db07141-…","changes":{"/home/user/Projects/proj/docs/design.md":{"type":"add","content":"# Design\n…"}},"status":"failed","stdout":"","stderr":"Failed to create parent directories for /home/user/Projects/proj/docs/…"},"started_at_ms":1788611847513,"completed_at_ms":1788612124387}}`,
		} {
			p := unmarshalCodexLine(t, raw, "event_msg")
			var ev codexItemCompleted
			require.NoError(t, json.Unmarshal(p, &ev))
			assert.True(t, codexKnownTurnItemTypes[ev.Item.Type], ev.Item.Type)
		}
	})
}

func TestCodexTypes_Bookkeeping(t *testing.T) {
	for _, raw := range []string{
		`{"timestamp":"2026-09-02T12:43:08.710Z","ordinal":7,"type":"turn_context","payload":{"turn_id":"01a06225-0462-7b61-807f-4c5175c52572","cwd":"/tmp/proj","workspace_roots":["/tmp/proj"],"current_date":"2026-09-02","timezone":"Europe/Oslo","approval_policy":"on-request","sandbox_policy":{"type":"workspace-write","network_access":false},"model":"gpt-5.6-sol","effort":"xhigh","summary":"auto"}}`,
		`{"timestamp":"2026-04-26T15:30:21.505Z","type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>…"}]},{"type":"compaction","encrypted_content":"gAAAAABp74mL…"}]}}`,
		`{"timestamp":"2026-09-05T11:25:34.037Z","ordinal":72,"type":"inter_agent_communication_metadata","payload":{"trigger_turn":false}}`,
		`{"timestamp":"2026-09-10T14:45:02.192Z","ordinal":12,"type":"token_usage_record","payload":{"thread_id":"01a08bc7-4014-7980-9b35-951772b7ddfd","usage":{"input_tokens":15460,"total_tokens":15473}}}`,
		`{"timestamp":"2026-08-04T10:04:52.370Z","type":"world_state","payload":{"full":true,"state":"{\"agents_md\":{},…}"}}`,
	} {
		var line codexLine
		require.NoError(t, json.Unmarshal([]byte(raw), &line))
		assert.True(t, codexKnownEnvelopeTypes[line.Type], line.Type)
		assert.NotEqual(t, codexTypeResponseItem, line.Type)
		assert.NotEqual(t, codexTypeEventMsg, line.Type)
	}
}
