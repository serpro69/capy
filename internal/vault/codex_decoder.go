package vault

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// codex_decoder.go is the Codex CLI Decoder: it turns a rollout
// ($CODEX_HOME/{sessions,archived_sessions}/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl,
// decompressed) into a Transcript. Wire types live in codex_types.go; the
// apply_patch → unified-diff conversion in codex_patch.go. Design:
// docs/feat/wip/codex-vault-sessions/design.md § The Codex Decoder.
//
// Like the Claude decoder it is pure and pre-policy: no secret stripping, no
// truncation, no exclusion. Every physical line advances LineIndex so anchors
// stay physical (the envelope `ordinal` is ignored). Unknown record types are
// skipped, never fatal (ADR-021), and the ones outside the known-type sets are
// logged once per file at debug level as a format-drift fingerprint.
//
// The load-bearing rule is WHERE HUMAN TURNS COME FROM. `response_item` user
// messages are dominated by context Codex injects under the user role
// (<environment_context>, AGENTS.md bodies, <skill>, <turn_aborted>,
// <recommended_plugins>, …), so the human prompt is read from the EVENT stream:
// legacy (< 0.147) `event_msg`/`user_message`, paginated (≥ 0.147)
// `event_msg`/`item_completed` with item.type == "UserMessage". Both paths are
// always attempted. Only when a whole file yields zero human entries from events
// does the decoder fall back to noise-filtered `response_item` user messages.

// codexDecoder decodes Codex rollouts. The zero value is ready to use and is
// what DecoderFor(PlatformCodex) returns.
type codexDecoder struct {
	// lineCap bounds one physical JSONL line; zero selects the package cap
	// scanLineCap (see claudeDecoder.lineCap).
	lineCap int
}

var _ Decoder = codexDecoder{}

const (
	// codexSpawnMessageMaxChars bounds a spawn_agent `message` used as the call
	// SUMMARY when neither task_name nor agent_type is present (the message is
	// encrypted from CLI 0.147, so this is a last resort). Reuses the Claude
	// Agent prompt cap.
	codexSpawnMessageMaxChars = agentPromptMaxChars
	// codexSpawnLabelMaxChars bounds the same message used as a Launch LABEL,
	// matching the Claude launch-label cap (subagentLabelMaxChars).
	codexSpawnLabelMaxChars = subagentLabelMaxChars
	// codexExitCodeLinePrefix is the exec_command wrapper line kept as the
	// first body line of a shell result — an exit code is search signal.
	codexExitCodeLinePrefix = "Process exited with code "
)

// codexNoiseAgentsMDPrefix is the one non-tag prefix the response_item fallback
// filter drops: the AGENTS.md body Codex injects as a user message. Everything
// else it drops starts with `<` (a tag) — see codexFallbackHumanText.
const codexNoiseAgentsMDPrefix = "# AGENTS.md instructions"

// codexSlot is one in-order record collected during pass 1, before call↔result
// correlation is possible.
type codexSlot struct {
	kind      EntryKind
	lineIndex int
	timestamp time.Time

	// EntryHuman: the prompt text. fallback marks a noise-filtered
	// response_item user message, used only if no event yields a human turn.
	text     string
	fallback bool

	// EntryAssistant: ordered parts (text first when present, then the calls
	// that attach to this message).
	parts []Part

	// EntryToolResult: correlation id and verbatim body. structured / success
	// describe a custom_tool_call_output whose inner JSON decoded (structured)
	// and reported success (exit_code == 0, else a `Success.` body).
	callID     string
	body       string
	structured bool
	success    bool
}

// codexCallInfo is the pass-2 correlation record for one call id.
type codexCallInfo struct {
	name    string
	summary string
	patch   string // apply_patch input text, for Diff on success
	call    *ToolCall
	spawn   *codexSpawnArgs // spawn_agent arguments, for the Launch label
}

// Decode reads a Codex rollout from r. It never fails on content — malformed
// lines are logged and skipped, oversize lines are skipped, unknown types are
// ignored — and returns only a genuine read error, wrapped as the Claude
// decoder wraps it ("reading session: …").
func (d codexDecoder) Decode(r io.Reader) (*Transcript, error) {
	lineCap := d.lineCap
	if lineCap <= 0 {
		lineCap = scanLineCap
	}
	t := &Transcript{Meta: Meta{Platform: PlatformCodex}}
	var (
		slots        []codexSlot
		meta         *codexSessionMeta
		spawnEnds    = map[string]codexCollabSpawnEnd{} // call_id → legacy spawn event
		spawnItems   = map[string]codexTurnItem{}       // call_id → paginated SubAgentActivity
		unknown      = map[string]bool{}                // drift fingerprint
		firstTS      time.Time
		openAsst     = -1 // index of the assistant slot calls attach to; -1 when none
		eventHumans  = 0
		lineIndex    = -1
		isSubagent   = false
		spawnArgsFor = map[string]*codexSpawnArgs{}
	)

	// newAssistant appends a text-less assistant slot at the current line and
	// makes it the attach target.
	newAssistant := func(ts time.Time) int {
		slots = append(slots, codexSlot{kind: EntryAssistant, lineIndex: lineIndex, timestamp: ts})
		openAsst = len(slots) - 1
		return openAsst
	}
	// attachCall adds a ToolCall part to the most recent assistant entry not yet
	// followed by a result or human entry, else opens a new one at this line.
	attachCall := func(ts time.Time, call *ToolCall) {
		idx := openAsst
		if idx < 0 {
			idx = newAssistant(ts)
		}
		slots[idx].parts = append(slots[idx].parts, Part{Call: call})
	}
	addHuman := func(ts time.Time, text string, fallback bool) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		slots = append(slots, codexSlot{kind: EntryHuman, lineIndex: lineIndex, timestamp: ts, text: text, fallback: fallback})
		if !fallback {
			eventHumans++
			openAsst = -1
		}
	}

	err := scanLines(r, lineCap, func(data []byte, oversize bool) {
		lineIndex++
		if oversize || len(data) == 0 {
			return
		}
		var line codexLine
		if err := json.Unmarshal(data, &line); err != nil {
			slog.Warn("vault codex decoder: skipping malformed JSONL line", "line", lineIndex, "error", err)
			return
		}
		ts := parseJSONLTime(line.Timestamp)
		if !ts.IsZero() {
			if firstTS.IsZero() {
				firstTS = ts
			}
			t.Meta.EndTime = ts
		}

		switch line.Type {
		case codexSessionMetaType:
			if meta != nil {
				return // first session_meta wins
			}
			var m codexSessionMeta
			if !codexUnmarshalPayload(line.Payload, &m, lineIndex, codexSessionMetaType) {
				return
			}
			meta = &m
			isSubagent = codexApplyMeta(&t.Meta, &m, ts)

		case codexTypeResponseItem:
			var probe codexPayloadType
			if !codexUnmarshalPayload(line.Payload, &probe, lineIndex, codexTypeResponseItem) {
				return
			}
			switch probe.Type {
			case "message":
				var msg codexMessage
				if !codexUnmarshalPayload(line.Payload, &msg, lineIndex, "message") {
					return
				}
				switch msg.Role {
				case "assistant":
					idx := newAssistant(ts)
					if text := codexTextParts(msg.Content); text != "" {
						slots[idx].parts = append(slots[idx].parts, Part{Text: text})
					}
				case "user":
					addHuman(ts, codexFallbackHumanText(msg.Content), true)
					// developer-role messages are system-prompt fragments: skipped.
				}

			case "function_call":
				var fc codexFunctionCall
				if !codexUnmarshalPayload(line.Payload, &fc, lineIndex, "function_call") {
					return
				}
				call := &ToolCall{ID: fc.CallID, Name: fc.Name, Summary: codexFunctionCallSummary(fc)}
				if json.Valid([]byte(fc.Arguments)) {
					call.Input = json.RawMessage(fc.Arguments)
				}
				if fc.Name == "spawn_agent" {
					args := &codexSpawnArgs{}
					_ = json.Unmarshal([]byte(fc.Arguments), args) // malformed → empty args, label falls through
					call.Launch = &Launch{}
					if fc.CallID != "" {
						spawnArgsFor[fc.CallID] = args
					} else {
						call.Launch.Label = codexSpawnLabel(args, "")
					}
				}
				attachCall(ts, call)

			case "custom_tool_call":
				var cc codexCustomToolCall
				if !codexUnmarshalPayload(line.Payload, &cc, lineIndex, "custom_tool_call") {
					return
				}
				input, _ := json.Marshal(cc.Input)
				attachCall(ts, &ToolCall{ID: cc.CallID, Name: cc.Name, Summary: codexCustomCallSummary(cc), Input: input})

			case "web_search_call":
				var ws codexWebSearchCall
				if !codexUnmarshalPayload(line.Payload, &ws, lineIndex, "web_search_call") {
					return
				}
				attachCall(ts, &ToolCall{Name: "web_search", Summary: codexWebSearchSummary(ws.Action), Input: ws.Action})

			case "function_call_output":
				var out codexFunctionCallOutput
				if !codexUnmarshalPayload(line.Payload, &out, lineIndex, "function_call_output") {
					return
				}
				slots = append(slots, codexSlot{
					kind: EntryToolResult, lineIndex: lineIndex, timestamp: ts,
					callID: out.CallID, body: stripExecHeader(codexOutputText(out.Output)),
				})
				openAsst = -1

			case "custom_tool_call_output":
				var out codexCustomToolCallOutput
				if !codexUnmarshalPayload(line.Payload, &out, lineIndex, "custom_tool_call_output") {
					return
				}
				s := codexSlot{kind: EntryToolResult, lineIndex: lineIndex, timestamp: ts, callID: out.CallID}
				if str, ok := asJSONString(out.Output); ok {
					s.body, s.structured, s.success = codexCustomOutputText(str)
				} else {
					// Content-array form (hosted exec): plain parts, no inner JSON,
					// never a Diff.
					s.body = stripExecHeader(codexOutputText(out.Output))
				}
				slots = append(slots, s)
				openAsst = -1

			default:
				if !codexKnownResponseItemTypes[probe.Type] {
					unknown["response_item:"+probe.Type] = true
				}
			}

		case codexTypeEventMsg:
			var probe codexPayloadType
			if !codexUnmarshalPayload(line.Payload, &probe, lineIndex, codexTypeEventMsg) {
				return
			}
			switch probe.Type {
			case "user_message":
				var ev codexUserMessageEvent
				if !codexUnmarshalPayload(line.Payload, &ev, lineIndex, "user_message") {
					return
				}
				addHuman(ts, ev.Message, false)

			case "item_completed":
				var ev codexItemCompleted
				if !codexUnmarshalPayload(line.Payload, &ev, lineIndex, "item_completed") {
					return
				}
				switch ev.Item.Type {
				case "UserMessage":
					addHuman(ts, codexTextParts(ev.Item.Content), false)
				case "SubAgentActivity":
					if ev.Item.ID != "" {
						if prev, ok := spawnItems[ev.Item.ID]; !ok || (prev.AgentThreadID == "" && ev.Item.AgentThreadID != "") {
							spawnItems[ev.Item.ID] = ev.Item
						}
					}
				default:
					if !codexKnownTurnItemTypes[ev.Item.Type] {
						unknown["item_completed:"+ev.Item.Type] = true
					}
				}

			case "collab_agent_spawn_end":
				var ev codexCollabSpawnEnd
				if !codexUnmarshalPayload(line.Payload, &ev, lineIndex, "collab_agent_spawn_end") {
					return
				}
				if ev.CallID != "" {
					if _, ok := spawnEnds[ev.CallID]; !ok {
						spawnEnds[ev.CallID] = ev
					}
				}

			default:
				if !codexKnownEventTypes[probe.Type] && !strings.HasPrefix(probe.Type, "collab_") {
					unknown["event_msg:"+probe.Type] = true
				}
			}

		default:
			if !codexKnownEnvelopeTypes[line.Type] {
				unknown["envelope:"+line.Type] = true
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("reading session: %w", err)
	}

	if t.Meta.StartTime.IsZero() {
		t.Meta.StartTime = firstTS
	}

	// Pass 2a: the whole-transcript call map, and Launch resolution for spawns.
	calls := map[string]*codexCallInfo{}
	for i := range slots {
		s := &slots[i]
		if s.kind != EntryAssistant {
			continue
		}
		for _, p := range s.parts {
			if p.Call == nil {
				continue
			}
			if p.Call.Launch != nil && p.Call.ID != "" {
				codexResolveLaunch(p.Call, spawnArgsFor[p.Call.ID], spawnEnds, spawnItems)
			}
			if p.Call.ID == "" {
				continue
			}
			info := &codexCallInfo{name: p.Call.Name, summary: p.Call.Summary, call: p.Call}
			if p.Call.Name == "apply_patch" {
				var patch string
				_ = json.Unmarshal(p.Call.Input, &patch)
				info.patch = patch
			}
			calls[p.Call.ID] = info
		}
	}

	// Pass 2b: slots → entries. Fallback humans are used only when the event
	// stream produced none (design § Human turns come from events).
	useFallback := eventHumans == 0
	humans, assistants := 0, 0
	for _, s := range slots {
		switch s.kind {
		case EntryHuman:
			if s.fallback && !useFallback {
				continue
			}
			humans++
			t.Entries = append(t.Entries, Entry{Kind: EntryHuman, LineIndex: s.lineIndex, Timestamp: s.timestamp, Text: s.text})
			if t.Meta.TitleFallback == "" {
				t.Meta.TitleFallback = s.text
			}
		case EntryAssistant:
			if len(s.parts) == 0 {
				continue
			}
			assistants++
			t.Entries = append(t.Entries, Entry{Kind: EntryAssistant, LineIndex: s.lineIndex, Timestamp: s.timestamp, Parts: s.parts})
		case EntryToolResult:
			e := Entry{Kind: EntryToolResult, LineIndex: s.lineIndex, Timestamp: s.timestamp, CallID: s.callID, Body: s.body}
			if info := calls[s.callID]; info != nil {
				e.CallName, e.CallSummary = info.name, info.summary
				// Diff only for a structured, successful apply_patch result whose
				// patch converts — the viewer must never show a diff that was not
				// applied (design § Tool results).
				if info.name == "apply_patch" && s.structured && s.success {
					if text, added, removed, ok := codexPatchToDiff(info.patch); ok {
						e.Diff = &Diff{Text: text, Added: added, Removed: removed}
					}
				}
			}
			t.Entries = append(t.Entries, e)
		}
	}

	// A sub-agent rollout with no human turn (CLI ≥ 0.147 children record no
	// spawn prompt) titles itself from its agent identity.
	if t.Meta.TitleFallback == "" && isSubagent && meta != nil {
		t.Meta.TitleFallback = codexAgentTitle(meta)
	}

	// Observability (design § Observability). The warning is the Codex analogue
	// of ADR-021's zero-turns signal and fires only when the model answered but
	// no prompt was found; an aborted-at-startup shell (nothing at all) is an
	// ordinary empty session and stays at debug; sub-agent files are exempt.
	cliVersion, historyMode := "", ""
	if meta != nil {
		cliVersion, historyMode = meta.CLIVersion, meta.HistoryMode
	}
	switch {
	case humans == 0 && assistants > 0 && !isSubagent:
		slog.Warn("vault codex decoder: no human turn found", "cli_version", cliVersion, "history_mode", historyMode, "assistant_entries", assistants)
	case humans == 0 && assistants == 0:
		slog.Debug("vault codex decoder: rollout has no human or assistant entries", "cli_version", cliVersion, "history_mode", historyMode, "lines", lineIndex+1)
	}
	if len(unknown) > 0 {
		types := make([]string, 0, len(unknown))
		for k := range unknown {
			types = append(types, k)
		}
		sort.Strings(types)
		slog.Debug("vault codex decoder: unrecognized record types", "cli_version", cliVersion, "types", types)
	}
	return t, nil
}

// codexUnmarshalPayload decodes a payload into v, logging one warning and
// reporting false when it does not fit its declared shape — the line is then
// skipped, never fatal (ADR-021), and never silently: a shape the wire types no
// longer match is exactly what the warning is for.
func codexUnmarshalPayload(data json.RawMessage, v any, lineIndex int, what string) bool {
	if err := json.Unmarshal(data, v); err != nil {
		slog.Warn("vault codex decoder: skipping malformed payload", "line", lineIndex, "payload", what, "error", err)
		return false
	}
	return true
}

// codexApplyMeta fills Meta from the session_meta payload (envelopeTS is line
// 0's envelope timestamp, the StartTime fallback) and reports whether the
// rollout is sub-agent sourced.
func codexApplyMeta(m *Meta, sm *codexSessionMeta, envelopeTS time.Time) (isSubagent bool) {
	m.PlatformID = sm.ID
	m.CWD = sm.CWD
	if sm.Git != nil {
		m.Branch = sm.Git.Branch
	}
	// payload.timestamp is the creation time Codex derives the filename from;
	// the envelope timestamp of line 0 is written when the first turn begins
	// and trails it by up to minutes.
	if st := parseJSONLTime(sm.Timestamp); !st.IsZero() {
		m.StartTime = st
	} else {
		m.StartTime = envelopeTS
	}
	m.Source = codexSourceLabel(sm.Source)
	m.ParentUUID = sm.ParentThreadID
	if spawn := codexThreadSpawnOf(sm.Source); spawn != nil {
		if m.ParentUUID == "" {
			m.ParentUUID = spawn.ThreadSpawn.ParentThreadID
		}
		if sm.AgentNickname == "" {
			sm.AgentNickname = spawn.ThreadSpawn.AgentNickname
		}
		if sm.AgentRole == "" {
			sm.AgentRole = spawn.ThreadSpawn.AgentRole
		}
		if sm.AgentPath == "" {
			sm.AgentPath = spawn.ThreadSpawn.AgentPath
		}
	}
	return m.Source == "subagent"
}

// codexAgentTitle is the TitleFallback for a sub-agent rollout without a human
// turn: `agent_nickname · agent_role` (whichever parts are present), else
// agent_path.
func codexAgentTitle(sm *codexSessionMeta) string {
	var parts []string
	if sm.AgentNickname != "" {
		parts = append(parts, sm.AgentNickname)
	}
	if sm.AgentRole != "" {
		parts = append(parts, sm.AgentRole)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " · ")
	}
	return sm.AgentPath
}

// codexFallbackHumanText applies the response_item noise filter: developer
// role is handled by the caller; here each input_text part whose trimmed text
// starts with `<` (a tag — <environment_context>, <skill>, <turn_aborted>,
// <subagent_notification>, <recommended_plugins>, <INSTRUCTIONS>, …) or with the
// AGENTS.md prefix is dropped; the survivors are joined by "\n". The prefix
// test on `<` is the filter; the tag list is documentation of what has been
// observed — a whitelist of tags would have let <recommended_plugins> through
// as a bogus human turn and title.
func codexFallbackHumanText(parts []codexContentPart) string {
	var kept []string
	for _, p := range parts {
		if p.Type != "input_text" {
			continue
		}
		text := strings.TrimSpace(p.Text)
		if text == "" || strings.HasPrefix(text, "<") || strings.HasPrefix(text, codexNoiseAgentsMDPrefix) {
			continue
		}
		kept = append(kept, text)
	}
	return strings.Join(kept, "\n")
}

// codexFunctionCallSummary builds the Summary for a function_call:
// `exec_command <cmd>`, `spawn_agent <task_name> (<agent_type>)`, and the bare
// name for everything else (MCP tools appear as bare names; generic input
// rendering stays deferred per ADR-025 — design § Assistant entries).
func codexFunctionCallSummary(fc codexFunctionCall) string {
	args := json.RawMessage(fc.Arguments)
	switch fc.Name {
	case "exec_command":
		if cmd := jsonStringField(args, "cmd"); cmd != "" {
			return fc.Name + " " + cmd
		}
	case "spawn_agent":
		var sa codexSpawnArgs
		_ = json.Unmarshal(args, &sa)
		switch {
		case sa.TaskName != "" && sa.AgentType != "":
			return fmt.Sprintf("%s %s (%s)", fc.Name, sa.TaskName, sa.AgentType)
		case sa.TaskName != "":
			return fc.Name + " " + sa.TaskName
		case sa.AgentType != "":
			return fc.Name + " " + sa.AgentType
		case strings.TrimSpace(sa.Message) != "":
			return fc.Name + " " + truncateRunes(strings.TrimSpace(sa.Message), codexSpawnMessageMaxChars)
		}
	}
	return fc.Name
}

// codexCustomCallSummary builds the Summary for a custom_tool_call:
// `apply_patch <files>` (paths from the patch headers, space-separated) and
// `exec <first line of input>`; the bare name when nothing summarises.
func codexCustomCallSummary(cc codexCustomToolCall) string {
	switch cc.Name {
	case "apply_patch":
		if files := codexPatchFiles(cc.Input); len(files) > 0 {
			return cc.Name + " " + strings.Join(files, " ")
		}
	case "exec":
		first, _, _ := strings.Cut(strings.TrimSpace(cc.Input), "\n")
		if first = strings.TrimSpace(first); first != "" {
			return cc.Name + " " + first
		}
	}
	return cc.Name
}

// codexWebSearchSummary builds `web_search <query>`, preferring action.query,
// then queries[0], then url, then pattern; the bare name otherwise.
func codexWebSearchSummary(action json.RawMessage) string {
	var a codexWebSearchAction
	if len(action) > 0 {
		_ = json.Unmarshal(action, &a)
	}
	for _, cand := range append([]string{a.Query}, append(a.Queries, a.URL, a.Pattern)...) {
		if c := strings.TrimSpace(cand); c != "" {
			return "web_search " + c
		}
	}
	return "web_search"
}

// codexSpawnLabel is the Launch.Label chain: task_name › agent_type › the
// parent-side SubAgentActivity agent_path › the (possibly encrypted) message
// bounded to codexSpawnLabelMaxChars › "subagent" (a marker needs a label).
func codexSpawnLabel(args *codexSpawnArgs, agentPath string) string {
	if args != nil {
		if args.TaskName != "" {
			return args.TaskName
		}
		if args.AgentType != "" {
			return args.AgentType
		}
	}
	if agentPath != "" {
		return agentPath
	}
	if args != nil {
		if msg := strings.TrimSpace(args.Message); msg != "" {
			return truncateRunes(msg, codexSpawnLabelMaxChars)
		}
	}
	return "subagent"
}

// codexResolveLaunch fills a spawn_agent call's Launch from the parent-side
// events: ChildUUID from a legacy collab_agent_spawn_end (new_thread_id) or a
// paginated SubAgentActivity item (agent_thread_id), matched by call_id; the
// label per codexSpawnLabel. An unresolved spawn keeps an empty ChildUUID and
// stays visible but non-openable.
func codexResolveLaunch(call *ToolCall, args *codexSpawnArgs, ends map[string]codexCollabSpawnEnd, items map[string]codexTurnItem) {
	agentPath := ""
	if item, ok := items[call.ID]; ok {
		call.Launch.ChildUUID = item.AgentThreadID
		agentPath = item.AgentPath
	}
	if call.Launch.ChildUUID == "" {
		if end, ok := ends[call.ID]; ok {
			call.Launch.ChildUUID = end.NewThreadID
		}
	}
	call.Launch.Label = codexSpawnLabel(args, agentPath)
}

// codexOutputText normalises function_call_output.output: a JSON string is
// taken as-is; a content array yields its input_text parts joined by "\n"
// (image/audio parts skipped); anything else yields "".
func codexOutputText(raw json.RawMessage) string {
	if s, ok := asJSONString(raw); ok {
		return s
	}
	var parts []codexContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	return codexTextParts(parts)
}

// codexCustomOutputText decodes the JSON string inside custom_tool_call_output
// .output ({output, metadata{exit_code}}). On success it returns the inner
// output as the body — with `Process exited with code N` prepended when an exit
// code is present, for parity with exec_command — structured=true, and whether
// the tool reported success (exit_code == 0, or a `Success.`-prefixed body when
// no exit code is present). When the string is not a JSON object with a string
// `.output`, the raw string is the body verbatim with no exit-code line,
// structured=false and success=false: the content is preserved, only the
// structure is not trusted (and no Diff is ever built from it).
func codexCustomOutputText(raw string) (body string, structured, success bool) {
	trimmed := strings.TrimSpace(raw)
	if !isJSONObject(json.RawMessage(trimmed)) {
		return raw, false, false
	}
	var probe struct {
		Output   json.RawMessage `json:"output"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return raw, false, false
	}
	inner, ok := asJSONString(probe.Output)
	if !ok {
		return raw, false, false
	}
	if code := probe.Metadata.ExitCode; code != nil {
		success = *code == 0
		line := fmt.Sprintf("%s%d", codexExitCodeLinePrefix, *code)
		if inner == "" {
			return line, true, success
		}
		return line + "\n" + inner, true, success
	}
	return inner, true, strings.HasPrefix(inner, "Success.")
}

// stripExecHeader removes the exec_command wrapper header from a shell result:
//
//	Chunk ID: 5e6cdb
//	Wall time: 0.1310 seconds
//	Process exited with code 0
//	Original token count: 5
//	Output:
//	<body>
//
// The `Chunk ID:`, `Wall time…` and `Original token count:` lines are dropped;
// a `Process …` / `Script …` status line (`Process exited with code N`,
// `Process running with session ID N`, `Script completed`) is KEPT as the first
// body line because it is search signal; the text after `Output:` follows. A
// body whose leading lines do not follow this grammar — no `Output:` line, an
// unexpected line before it, or no header line at all — is returned unchanged.
//
// The header is a handful of lines but the body can be megabytes (a shell dump
// up to the line cap), so the scan walks lines with strings.Cut and returns the
// tail as a slice of the input rather than splitting and re-joining the body.
func stripExecHeader(body string) string {
	var kept []string
	dropped := 0
	rest := body
	for {
		line, after, found := strings.Cut(rest, "\n")
		switch {
		case line == "Output:":
			if dropped == 0 {
				return body
			}
			if !found {
				return strings.Join(kept, "\n")
			}
			if len(kept) == 0 {
				return after
			}
			return strings.Join(kept, "\n") + "\n" + after
		case strings.HasPrefix(line, "Chunk ID:"), strings.HasPrefix(line, "Wall time"), strings.HasPrefix(line, "Original token count:"):
			dropped++
		case strings.HasPrefix(line, "Process "), strings.HasPrefix(line, "Script "):
			kept = append(kept, line)
		default:
			return body
		}
		if !found {
			return body // header lines but no Output: marker
		}
		rest = after
	}
}
