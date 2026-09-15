package vault

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codex_decoder_test.go pins the Codex decoder the way claude_decoder_test.go
// pins the Claude one: one HAND-DERIVED expectation table over the fixture
// builders in codex_fixtures_test.go (describeEntries / describeMeta rows
// written from the fixture, never pasted from decoder output), a table ↔
// fixture bijection check, the payload-isolation sweep, and field-level tests
// for the load-bearing rules (event-first human turns, header stripping, Diff
// only on success, launch resolution, payload-derived StartTime, warnings).

// codexStart is the fixture session_meta payload timestamp, rendered as
// describeMeta renders it.
var codexStart = goldenTime(parseJSONLTime(codexPayloadTS))

// tsAt renders the fixture envelope time at(n) as describeMeta renders it.
func tsAt(n int) string { return goldenTime(parseJSONLTime(at(n))) }

// codexMetaLine builds the expected describeMeta string for a Codex transcript
// (ExplicitTitle is always empty: Codex records no title).
func codexMetaLine(fallback, cwd, branch, start, end string) string {
	return fmt.Sprintf("platform=codex title=%q fallback=%q cwd=%q branch=%q start=%s end=%s", "", fallback, cwd, branch, start, end)
}

func codexCaseByName(t *testing.T, name string) codexCase {
	t.Helper()
	for _, c := range codexCases(t) {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no codex case %q", name)
	return codexCase{}
}

func decodeCodex(t *testing.T, c codexCase) *Transcript {
	t.Helper()
	tr, err := codexDecoder{lineCap: c.lineCap}.Decode(bytes.NewReader(c.raw))
	require.NoError(t, err)
	require.NotNil(t, tr)
	return tr
}

const (
	codexDefaultCWD = "/home/user/Projects/proj"
	codexPrompt     = "Please review the design documents under docs/ and report inconsistencies."
	codexPatchOK    = "Process exited with code 0\nSuccess. Updated the following files:\nA /tmp/proj/script.py\n"
)

// TestCodexDecoder_Cases asserts Meta (including the Codex-only PlatformID /
// ParentUUID / Source) and the full entry list for every fixture. A case missing
// from this table fails, and so does an expectation naming no case.
func TestCodexDecoder_Cases(t *testing.T) {
	encSummary := "spawn_agent " + codexEncryptedMessage                      // message-only spawn: summary falls back to the message
	encLabel := truncateRunes(codexEncryptedMessage, codexSpawnLabelMaxChars) // …and so does an unresolved launch label, bounded
	spawnBody := func(child string) string {
		return `{"agent_id":"` + child + `","status":"pending_init"}`
	}

	type exp struct {
		meta               string
		id, parent, source string
		entries            []string
	}
	want := map[string]exp{
		"legacy_basic": {
			meta: codexMetaLine(codexPrompt, codexDefaultCWD, "master", codexStart, tsAt(6)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				fmt.Sprintf(`H@5 %q`, short(codexPrompt)), // the event, not its response_item twin at line 4
				fmt.Sprintf(`A@7 text(%q) call(exec_command "exec_command sed -n '1,240p' README.md")`, short("I'll review the three documents and trace each claim against the code.")),
				`R@10 call_1 "exec_command" "exec_command sed -n '1,240p' README.md" body="Process exited with code 0⏎# proj⏎⏎README body…"`, // header stripped, exit line kept
				`A@11 text("The README matches the design.")`,
			},
		},
		"paginated_basic": {
			meta: codexMetaLine("Let's work on issue #81.", "/tmp/proj", "", codexStart, tsAt(8)), // non-git cwd → no branch
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@5 "Let's work on issue #81."`, // the UserMessage item
				`A@9 text("I'm turning issue #81 into a reviewed design direction.") call(exec_command "exec_command sed -n '1,260p' README.md")`,
				`R@11 call_2 "exec_command" "exec_command sed -n '1,260p' README.md" body="Process exited with code 0⏎# proj…"`,
				`A@13 call(capy_search "capy_search")`,                                          // MCP tool: bare name; the result closed the previous attach window
				`R@14 call_3 "capy_search" "capy_search" body="## vault scanner conventions⏎…"`, // content array; image part skipped
				`A@16 text("Found the convention note.")`,
			},
		},
		"apply_patch_success": {
			meta: codexMetaLine("add a script", codexDefaultCWD, "master", codexStart, tsAt(4)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "add a script"`,
				`A@2 text("Adding it.") call(apply_patch "apply_patch /tmp/proj/script.py")`,
				fmt.Sprintf(`R@4 call_p "apply_patch" "apply_patch /tmp/proj/script.py" body=%q diff(+2 -0)`, short(codexPatchOK)),
			},
		},
		"apply_patch_failure": {
			meta: codexMetaLine("add a script", codexDefaultCWD, "master", codexStart, tsAt(4)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "add a script"`,
				`A@2 text("Adding it.") call(apply_patch "apply_patch /tmp/proj/script.py")`,
				fmt.Sprintf(`R@4 call_p "apply_patch" "apply_patch /tmp/proj/script.py" body=%q`, short("Process exited with code 1\nFailed to apply patch: context mismatch in /tmp/proj/script.py\n")), // no Diff
			},
		},
		"apply_patch_malformed": {
			meta: codexMetaLine("add a script", codexDefaultCWD, "master", codexStart, tsAt(4)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "add a script"`,
				`A@2 text("Adding it.") call(apply_patch "apply_patch /tmp/proj/script.py")`,                           // summary still names the file
				fmt.Sprintf(`R@4 call_p "apply_patch" "apply_patch /tmp/proj/script.py" body=%q`, short(codexPatchOK)), // success, but the patch does not convert → no Diff
			},
		},
		"apply_patch_two_hunks": {
			meta: codexMetaLine("add a script", codexDefaultCWD, "master", codexStart, tsAt(4)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "add a script"`,
				`A@2 text("Adding it.") call(apply_patch "apply_patch /p/config.toml")`,
				fmt.Sprintf(`R@4 call_p "apply_patch" "apply_patch /p/config.toml" body=%q diff(+2 -1)`, short("Process exited with code 0\nSuccess. Updated the following files:\nA /p/config.toml\n")),
			},
		},
		"apply_patch_no_exit_code": {
			meta: codexMetaLine("add a script", codexDefaultCWD, "master", codexStart, tsAt(5)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "add a script"`,
				`A@2 call(apply_patch "apply_patch /tmp/proj/script.py")`, // no assistant text → text-less assistant at the call's line
				fmt.Sprintf(`R@3 call_p "apply_patch" "apply_patch /tmp/proj/script.py" body=%q diff(+2 -0)`, short("Success. Updated the following files:\nA /tmp/proj/script.py\n")), // no exit line; Success. prefix → Diff
				`A@4 call(apply_patch "apply_patch /tmp/proj/script.py")`,
				`R@5 call_q "apply_patch" "apply_patch /tmp/proj/script.py" body="Error: patch rejected⏎"`, // neither exit code nor Success. → no Diff
			},
		},
		"custom_exec": {
			meta: codexMetaLine("compute it", codexDefaultCWD, "master", codexStart, tsAt(10)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "compute it"`,
				`A@2 text("Running a script.") call(exec "exec const x = 1;")`, // first line of the input
				`R@4 call_e "exec" "exec const x = 1;" body="Process exited with code 0⏎1⏎"`,
				`A@5 call(exec "exec")`,                                  // blank input → bare name
				`R@6 call_f "exec" "exec" body="plain text, not json {"`, // not JSON → verbatim, no exit line
				`A@7 call(exec "exec throw 1")`,
				`R@8 call_g "exec" "exec throw 1" body="{\"output\": 42, \"metadata\": {\"exit_code\": 0}}"`, // JSON but .output not a string → verbatim
				`A@9 call(exec "exec await fetch('https://a.example')")`,
				`R@10 call_h "exec" "exec await fetch('https://a.example')" body="Script completed⏎Airbnb: Vacation Rentals"`, // content-array form: parts joined, header stripped, image skipped
			},
		},
		"spawn_legacy": {
			meta: codexMetaLine("Review the latest commit", codexDefaultCWD, "master", codexStart, tsAt(5)),
			id:   codexFixtureParentID, source: "cli",
			entries: []string{
				`H@1 "Review the latest commit"`,
				`A@2 text("Spawning a reviewer.") call(spawn_agent "spawn_agent code-reviewer") launch("code-reviewer")`, // agent_type only
				fmt.Sprintf(`R@5 call_s "spawn_agent" "spawn_agent code-reviewer" body=%q`, short(spawnBody(codexFixtureChildID))),
			},
		},
		"spawn_paginated": {
			meta: codexMetaLine("Detect the profiles", codexDefaultCWD, "master", codexStart, tsAt(11)), // call_c's output carries at(11)
			id:   codexFixtureParentID, source: "cli",
			entries: []string{
				`H@1 "Detect the profiles"`,
				`A@2 text("Delegating.") call(spawn_agent "spawn_agent detect_profiles (profile-resolver)") launch("detect_profiles")`, // never the encrypted message
				fmt.Sprintf(`R@5 call_a "spawn_agent" "spawn_agent detect_profiles (profile-resolver)" body=%q`, short(spawnBody(codexFixtureChild2ID))),
				fmt.Sprintf(`A@6 call(spawn_agent %q) launch("/root/task1_review")`, short(encSummary)), // message-only: label from the parent-side agent_path
				fmt.Sprintf(`R@8 call_b "spawn_agent" %q body=%q`, short(encSummary), short(spawnBody(codexFixtureChildID))),
				fmt.Sprintf(`A@9 call(spawn_agent %q) launch(%q)`, short(encSummary), short(encLabel)), // unresolved: bounded message
				fmt.Sprintf(`R@10 call_c "spawn_agent" %q body=%q`, short(encSummary), spawnBody("")),
			},
		},
		"child_legacy_137": {
			meta: codexMetaLine("Review the latest commit on the current branch…", codexDefaultCWD, "master", codexStart, tsAt(2)),
			id:   codexFixtureChildID, parent: codexFixtureParentID, source: "subagent", // parent from source.subagent.thread_spawn
			entries: []string{
				`H@2 "Review the latest commit on the current branch…"`, // the spawn prompt is a user_message event
				`A@3 text("Reviewing.")`,
			},
		},
		"child_paginated_147": {
			meta: codexMetaLine("Chandrasekhar · code-reviewer", codexDefaultCWD, "master", codexStart, tsAt(3)), // no human turn → agent title
			id:   codexFixtureChild2ID, parent: codexFixtureParentID, source: "subagent",                         // lifted parent_thread_id
			entries: []string{
				`A@12 text("I'm loading the review instructions.")`, // <recommended_plugins> is never a Human entry
			},
		},
		"child_nickname_only": {
			meta: codexMetaLine("Raman", codexDefaultCWD, "master", codexStart, tsAt(2)),
			id:   codexFixtureChildID, parent: codexFixtureParentID, source: "subagent",
			entries: []string{`A@2 text("On it.")`},
		},
		"aborted_shell": {
			meta: codexMetaLine("", codexDefaultCWD, "master", codexStart, tsAt(2)),
			id:   codexFixtureID, source: "cli",
			entries: nil, // <turn_aborted> and <environment_context> are not human turns
		},
		"assistant_without_human": {
			meta: codexMetaLine("", codexDefaultCWD, "master", codexStart, tsAt(1)),
			id:   codexFixtureID, source: "cli",
			entries: []string{`A@2 text("Hi! What would you like to work on?")`},
		},
		"fallback_response_items": {
			meta: codexMetaLine("What about user-facing docs?", codexDefaultCWD, "master", codexStart, tsAt(3)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@4 "What about user-facing docs?"`, // trimmed; the <skill> part of the same message dropped
				`A@6 text("Documented in README.")`,
			},
		},
		"events_win_over_response_items": {
			meta: codexMetaLine("the real prompt", codexDefaultCWD, "master", codexStart, tsAt(3)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@2 "the real prompt"`, // line 1's plain response_item is not used once any event exists
				`A@3 text("ok")`,
			},
		},
		"tool_only_assistant": {
			meta: codexMetaLine("list files", codexDefaultCWD, "master", codexStart, tsAt(6)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "list files"`,
				`A@2 call(exec_command "exec_command ls")`,
				`R@3 call_1 "exec_command" "exec_command ls" body="Process exited with code 0⏎a.go⏎b.go"`,
				`A@4 call(exec_command "exec_command wc -l a.go") call(update_plan "update_plan")`, // two calls share the assistant opened at line 4
				`R@6 call_2 "exec_command" "exec_command wc -l a.go" body="Process exited with code 0⏎12 a.go"`,
				`R@7 call_3 "update_plan" "update_plan" body="Plan updated"`,  // no header → verbatim
				`R@8 call_9 "" "" body="orphan output"`,                       // unknown id: no name/summary
				`R@9 call_2 "exec_command" "exec_command wc -l a.go" body=""`, // empty body still emitted
			},
		},
		"human_closes_attach_window": {
			meta: codexMetaLine("first", codexDefaultCWD, "master", codexStart, tsAt(4)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "first"`,
				`A@2 text("Sure.")`,
				`H@3 "second"`,
				`A@4 call(exec_command "exec_command true")`, // not attached to line 2's assistant
			},
		},
		"exec_variants": {
			meta: codexMetaLine("run things", codexDefaultCWD, "master", codexStart, tsAt(11)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "run things"`,
				`A@2 text("Running.") call(exec_command "exec_command sleep 30")`,
				`R@4 c1 "exec_command" "exec_command sleep 30" body="Process running with session ID 7⏎"`, // status line kept, empty tail
				`A@5 call(exec "exec")`,                              // no cmd → bare name
				`R@6 c2 "exec" "exec" body="Script completed⏎hello"`, // content array; "Wall time" without a colon
				`A@7 call(exec_command "exec_command cat x")`,
				`R@8 c3 "exec_command" "exec_command cat x" body="Output:⏎not a header, kept whole"`,    // no header line before Output:
				`A@9 call(exec_command "exec_command")`,                                                 // malformed arguments → bare name
				`R@10 c4 "exec_command" "exec_command" body="Chunk ID: x⏎unexpected line⏎Output:⏎body"`, // unexpected header line → unchanged
				`R@11 c3 "exec_command" "exec_command cat x" body="Process exited with code 2⏎err⏎line two"`,
			},
		},
		"web_search_variants": {
			meta: codexMetaLine("search the docs", codexDefaultCWD, "master", codexStart, tsAt(4)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "search the docs"`,
				`A@2 text("Searching.") call(web_search "web_search site:developers.openai.com/codex hooks") call(web_search "web_search first query") call(web_search "web_search https://example.com/doc") call(web_search "web_search needle") call(web_search "web_search")`,
				`A@8 text("Found it.")`,
			},
		},
		"noise_and_unknown_types": {
			meta: codexMetaLine("", codexDefaultCWD, "master", codexStart, tsAt(3)),
			id:   codexFixtureID, source: "cli",
			entries: nil,
		},
		"malformed_and_oversize": {
			meta: codexMetaLine("hello", codexDefaultCWD, "master", codexStart, tsAt(6)),
			id:   codexFixtureID, source: "cli",
			entries: []string{
				`H@1 "hello"`,
				`A@8 text("after the bad lines")`, // lines 2–7 skipped but counted
				`H@9 "trailing without newline"`,
			},
		},
		"no_session_meta": {
			meta: codexMetaLine("still a prompt", "", "", tsAt(1), tsAt(3)), // StartTime falls back to the first envelope time
			entries: []string{
				`H@1 "still a prompt"`,
				`A@2 text("still an answer")`,
			},
		},
		"second_session_meta_ignored": {
			meta: codexMetaLine("hi", codexDefaultCWD, "master", codexStart, tsAt(2)),
			id:   codexFixtureID, source: "cli",
			entries: []string{`H@2 "hi"`},
		},
		"source_exec": {
			meta: codexMetaLine("non-interactive prompt", codexDefaultCWD, "master", codexStart, tsAt(2)),
			id:   codexFixtureID, source: "exec",
			entries: []string{`H@1 "non-interactive prompt"`, `A@2 text("done")`},
		},
		"source_subagent_review": {
			meta: codexMetaLine("", codexDefaultCWD, "master", codexStart, tsAt(1)), // unit-variant sub-agent: no agent identity to title from
			id:   codexFixtureID, source: "subagent",
			entries: []string{`A@1 text("Reviewing the diff.")`},
		},
		"empty": {
			meta:    codexMetaLine("", "", "", "", ""),
			entries: nil,
		},
	}

	cases := codexCases(t)
	for _, c := range cases {
		exp, ok := want[c.name]
		require.True(t, ok, "codex case %q has no decoder expectation — add it to this table", c.name)
		t.Run(c.name, func(t *testing.T) {
			tr := decodeCodex(t, c)
			assert.Equal(t, exp.meta, describeMeta(tr.Meta))
			assert.Equal(t, exp.id, tr.Meta.PlatformID, "PlatformID")
			assert.Equal(t, exp.parent, tr.Meta.ParentUUID, "ParentUUID")
			assert.Equal(t, exp.source, tr.Meta.Source, "Source")
			assert.Equal(t, strings.Join(exp.entries, "\n"), describeEntries(tr))
		})
	}
	for name := range want {
		found := false
		for _, c := range cases {
			if c.name == name {
				found = true
				break
			}
		}
		assert.True(t, found, "expectation %q has no codex case", name)
	}
}

func TestCodexDecoder_PayloadIsolation(t *testing.T) {
	for _, c := range codexCases(t) {
		assertPayloadIsolation(t, c.name, decodeCodex(t, c))
	}
}

// StartTime is the session_meta payload timestamp (creation), not line 0's
// envelope time (first turn) — the two differ by 90 s in the fixtures.
func TestCodexDecoder_StartTimeFromPayload(t *testing.T) {
	tr := decodeCodex(t, codexCaseByName(t, "legacy_basic"))
	assert.Equal(t, parseJSONLTime(codexPayloadTS), tr.Meta.StartTime)
	assert.NotEqual(t, parseJSONLTime(at(0)), tr.Meta.StartTime)
	assert.Equal(t, parseJSONLTime(at(6)), tr.Meta.EndTime, "last envelope timestamp")

	noMeta := decodeCodex(t, codexCaseByName(t, "no_session_meta"))
	assert.Equal(t, parseJSONLTime(at(1)), noMeta.Meta.StartTime, "no session_meta → first envelope timestamp")
}

// Diff text is the codex_patch conversion, and only a successful result gets it.
func TestCodexDecoder_DiffText(t *testing.T) {
	result := func(t *testing.T, name, callID string) Entry {
		t.Helper()
		for _, e := range decodeCodex(t, codexCaseByName(t, name)).Entries {
			if e.Kind == EntryToolResult && e.CallID == callID {
				return e
			}
		}
		t.Fatalf("%s: no result for %s", name, callID)
		return Entry{}
	}
	ok := result(t, "apply_patch_success", "call_p")
	require.NotNil(t, ok.Diff)
	assert.Equal(t, "*** Add File: /tmp/proj/script.py\n@@ -0,0 +1,2 @@\n+#!/usr/bin/env python3\n+import json", ok.Diff.Text)
	assert.Equal(t, 2, ok.Diff.Added)
	assert.Equal(t, 0, ok.Diff.Removed)
	assert.Equal(t, codexPatchOK, ok.Body, "the success body is kept beside the diff, verbatim")

	assert.Nil(t, result(t, "apply_patch_failure", "call_p").Diff, "exit_code 1 → no Diff")
	assert.Nil(t, result(t, "apply_patch_malformed", "call_p").Diff, "unconvertible patch → no Diff")
	assert.NotNil(t, result(t, "apply_patch_no_exit_code", "call_p").Diff, "Success. prefix stands in for a missing exit code")
	assert.Nil(t, result(t, "apply_patch_no_exit_code", "call_q").Diff)
	assert.Nil(t, result(t, "custom_exec", "call_e").Diff, "exec results never carry a Diff")
}

// Launch.ChildUUID comes from collab_agent_spawn_end (legacy) or SubAgentActivity
// (paginated), matched by call_id; the label follows task_name › agent_type ›
// agent_path › bounded message; an unresolved spawn keeps an empty ChildUUID.
func TestCodexDecoder_LaunchResolution(t *testing.T) {
	launches := func(t *testing.T, name string) map[string]*ToolCall {
		t.Helper()
		out := map[string]*ToolCall{}
		for _, e := range decodeCodex(t, codexCaseByName(t, name)).Entries {
			for _, p := range e.Parts {
				if p.Call != nil && p.Call.Launch != nil {
					out[p.Call.ID] = p.Call
				}
			}
		}
		return out
	}

	legacy := launches(t, "spawn_legacy")
	require.Len(t, legacy, 1)
	assert.Equal(t, codexFixtureChildID, legacy["call_s"].Launch.ChildUUID, "legacy: new_thread_id")
	assert.Equal(t, "code-reviewer", legacy["call_s"].Launch.Label)
	assert.JSONEq(t, `{"agent_type":"code-reviewer","fork_context":true,"message":"Review the latest commit on the current branch…"}`, string(legacy["call_s"].Input), "raw arguments kept")

	pag := launches(t, "spawn_paginated")
	require.Len(t, pag, 3)
	assert.Equal(t, codexFixtureChild2ID, pag["call_a"].Launch.ChildUUID, "paginated: agent_thread_id")
	assert.Equal(t, "detect_profiles", pag["call_a"].Launch.Label, "task_name wins")
	assert.NotContains(t, pag["call_a"].Summary, "gAAAA", "the encrypted message never labels a call that has a task_name")
	assert.NotContains(t, pag["call_a"].Launch.Label, "gAAAA")
	assert.Equal(t, codexFixtureChildID, pag["call_b"].Launch.ChildUUID)
	assert.Equal(t, "/root/task1_review", pag["call_b"].Launch.Label, "parent-side agent_path beats the message")
	assert.Empty(t, pag["call_c"].Launch.ChildUUID, "no parent-side event → not openable")
	assert.Equal(t, truncateRunes(codexEncryptedMessage, codexSpawnLabelMaxChars), pag["call_c"].Launch.Label, "last resort: the message, bounded")
	assert.Len(t, []rune(pag["call_c"].Launch.Label), codexSpawnLabelMaxChars+1)

	for _, e := range decodeCodex(t, codexCaseByName(t, "tool_only_assistant")).Entries {
		for _, p := range e.Parts {
			if p.Call != nil {
				assert.Nil(t, p.Call.Launch, "%s is not a launch", p.Call.Name)
			}
		}
	}
}

// ToolCall.Input carries the raw call input: the decoded arguments JSON for a
// function_call (nil when unparseable), the input text as a JSON string for a
// custom_tool_call, the action object for a web_search_call.
func TestCodexDecoder_InputRaw(t *testing.T) {
	calls := func(t *testing.T, name string) map[string]*ToolCall {
		t.Helper()
		out := map[string]*ToolCall{}
		for _, e := range decodeCodex(t, codexCaseByName(t, name)).Entries {
			for _, p := range e.Parts {
				if p.Call != nil {
					out[p.Call.ID+"/"+p.Call.Summary] = p.Call
				}
			}
		}
		return out
	}
	ev := calls(t, "exec_variants")
	assert.Nil(t, ev["c4/exec_command"].Input, "malformed arguments → nil Input")
	assert.JSONEq(t, `{"cmd":"cat x"}`, string(ev["c3/exec_command cat x"].Input))

	patch := calls(t, "apply_patch_success")["call_p/apply_patch /tmp/proj/script.py"]
	require.NotNil(t, patch)
	var text string
	require.NoError(t, jsonUnmarshalString(patch.Input, &text))
	assert.Equal(t, codexAddPatch, text, "custom_tool_call input is kept as a JSON string")

	ws := calls(t, "web_search_variants")["/web_search needle"]
	require.NotNil(t, ws)
	assert.JSONEq(t, `{"type":"find_in_page","pattern":"needle"}`, string(ws.Input))
	assert.Empty(t, ws.ID, "web_search_call has no call_id")
}

func jsonUnmarshalString(raw []byte, into *string) error {
	s, ok := asJSONString(raw)
	if !ok {
		return errors.New("not a JSON string")
	}
	*into = s
	return nil
}

// recordsWithMessage returns the slog records whose message equals msg.
func (h *recordingHandler) recordsWithMessage(msg string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

func recordAttrs(r slog.Record) map[string]any {
	m := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	return m
}

func captureSlog(t *testing.T) *recordingHandler {
	t.Helper()
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

const (
	codexWarnNoHuman = "vault codex decoder: no human turn found"
	codexDebugShell  = "vault codex decoder: rollout has no human or assistant entries"
	codexDebugDrift  = "vault codex decoder: unrecognized record types"
)

// The zero-human WARNING fires only for a non-subagent file with ≥ 1 Assistant
// and 0 Human entries; an aborted shell is debug; sub-agent files are exempt.
func TestCodexDecoder_ZeroHumanWarning(t *testing.T) {
	t.Run("assistant but no human warns with version attributes", func(t *testing.T) {
		h := captureSlog(t)
		decodeCodex(t, codexCaseByName(t, "assistant_without_human"))
		recs := h.recordsWithMessage(codexWarnNoHuman)
		require.Len(t, recs, 1)
		assert.Equal(t, slog.LevelWarn, recs[0].Level)
		attrs := recordAttrs(recs[0])
		assert.Equal(t, "0.152.1", attrs["cli_version"])
		assert.Equal(t, "paginated", attrs["history_mode"])
		assert.Equal(t, int64(1), attrs["assistant_entries"])
		assert.Empty(t, h.recordsWithMessage(codexDebugShell))
	})
	t.Run("aborted shell is debug, not a warning", func(t *testing.T) {
		h := captureSlog(t)
		decodeCodex(t, codexCaseByName(t, "aborted_shell"))
		assert.Empty(t, h.recordsWithMessage(codexWarnNoHuman))
		recs := h.recordsWithMessage(codexDebugShell)
		require.Len(t, recs, 1)
		assert.Equal(t, slog.LevelDebug, recs[0].Level)
		assert.Equal(t, int64(8), recordAttrs(recs[0])["lines"])
	})
	for _, name := range []string{"child_paginated_147", "child_nickname_only", "source_subagent_review"} {
		t.Run(name+" is exempt", func(t *testing.T) {
			h := captureSlog(t)
			decodeCodex(t, codexCaseByName(t, name))
			assert.Empty(t, h.recordsWithMessage(codexWarnNoHuman))
			assert.Empty(t, h.recordsWithMessage(codexDebugShell), "it has an assistant entry")
		})
	}
	for _, name := range []string{"legacy_basic", "paginated_basic", "fallback_response_items", "child_legacy_137", "custom_exec"} {
		t.Run(name+" does not warn", func(t *testing.T) {
			h := captureSlog(t)
			decodeCodex(t, codexCaseByName(t, name))
			assert.Empty(t, h.messagesWithPrefix("vault codex decoder:"), "no warnings on a well-formed file")
		})
	}
	t.Run("empty input is an empty shell at debug", func(t *testing.T) {
		h := captureSlog(t)
		decodeCodex(t, codexCaseByName(t, "empty"))
		assert.Empty(t, h.recordsWithMessage(codexWarnNoHuman))
		assert.Len(t, h.recordsWithMessage(codexDebugShell), 1)
	})
}

// Unknown record types are collected per file and logged once at debug, sorted.
func TestCodexDecoder_DriftFingerprint(t *testing.T) {
	h := captureSlog(t)
	decodeCodex(t, codexCaseByName(t, "noise_and_unknown_types"))
	recs := h.recordsWithMessage(codexDebugDrift)
	require.Len(t, recs, 1)
	assert.Equal(t, slog.LevelDebug, recs[0].Level)
	assert.Equal(t, []string{"envelope:hologram_record", "event_msg:quantum_flux", "item_completed:HoloDeck", "response_item:teleport_call"}, recordAttrs(recs[0])["types"])
	assert.Empty(t, h.messagesWithPrefix("vault codex decoder: skipping"), "known noise is not malformed")

	h2 := captureSlog(t)
	decodeCodex(t, codexCaseByName(t, "legacy_basic"))
	assert.Empty(t, h2.recordsWithMessage(codexDebugDrift), "a file of known types has no fingerprint")
}

// Malformed lines and payloads are logged and skipped, one warning each, and
// still advance LineIndex.
func TestCodexDecoder_WarnsOnMalformedContent(t *testing.T) {
	h := captureSlog(t)
	decodeCodex(t, codexCaseByName(t, "malformed_and_oversize"))
	assert.Equal(t, []string{
		"vault codex decoder: skipping malformed JSONL line", // line 2
		"vault codex decoder: skipping malformed payload",    // line 5: response_item payload is an array
		"vault codex decoder: skipping malformed payload",    // line 6: event_msg payload is a string
		"vault codex decoder: skipping malformed payload",    // line 7: message content is not an array
	}, h.messagesWithPrefix("vault codex decoder:"))
	lines := []int64{}
	for _, r := range h.recordsWithMessage("vault codex decoder: skipping malformed payload") {
		lines = append(lines, recordAttrs(r)["line"].(int64))
	}
	assert.Equal(t, []int64{5, 6, 7}, lines)
	assert.Len(t, h.messagesWithPrefix("vault scanner: skipping oversize"), 1, "line 3")
}

// A zero lineCap follows the package cap the golden harness lowers.
func TestCodexDecoder_ZeroLineCapFollowsPackageCap(t *testing.T) {
	c := codexCaseByName(t, "malformed_and_oversize")
	require.Greater(t, c.lineCap, 0)

	full, err := codexDecoder{}.Decode(bytes.NewReader(c.raw))
	require.NoError(t, err)
	assert.Len(t, full.Entries, 4, "under the production cap the 3000-byte prompt is a Human entry")

	setLineCaps(t, c.lineCap)
	capped, err := DecoderFor(PlatformCodex).Decode(bytes.NewReader(c.raw))
	require.NoError(t, err)
	assert.Equal(t, describeEntries(decodeCodex(t, c)), describeEntries(capped))
	assert.Len(t, capped.Entries, 3)
}

func TestCodexDecoder_ReadError(t *testing.T) {
	boom := errors.New("disk on fire")
	tr, err := codexDecoder{}.Decode(errReader{err: boom})
	require.Error(t, err)
	assert.Nil(t, tr)
	assert.True(t, errors.Is(err, boom))
	assert.Equal(t, "reading session: disk on fire", err.Error())
}

func TestCodexDecoder_EmptyInput(t *testing.T) {
	tr, err := codexDecoder{}.Decode(bytes.NewReader(nil))
	require.NoError(t, err)
	assert.Equal(t, Meta{Platform: PlatformCodex}, tr.Meta)
	assert.Nil(t, tr.Entries)

	tr, err = codexDecoder{}.Decode(strings.NewReader("\n\n"))
	require.NoError(t, err)
	assert.Nil(t, tr.Entries)
}

// A .jsonl.zst rollout written by writeCodexRollout decodes identically once
// decompressed through the vault codec (the import path of Slice 7).
func TestCodexDecoder_CompressedRollout(t *testing.T) {
	c := codexCaseByName(t, "legacy_basic")
	home := t.TempDir()
	rel := "sessions/2026/05/01/rollout-2026-05-01T12-00-00-" + codexFixtureID + ".jsonl.zst"
	path := writeCodexRollout(t, home, rel, c.raw, true)
	assert.True(t, strings.HasSuffix(path, ".jsonl.zst"))

	compressed, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotEqual(t, c.raw, compressed)
	plain, err := decodeBlob(encodingZstd, compressed)
	require.NoError(t, err)
	assert.Equal(t, c.raw, plain)

	tr, err := codexDecoder{}.Decode(bytes.NewReader(plain))
	require.NoError(t, err)
	assert.Equal(t, describeEntries(decodeCodex(t, c)), describeEntries(tr))

	plainPath := writeCodexRollout(t, home, strings.TrimSuffix(rel, ".zst"), c.raw, false)
	got, err := os.ReadFile(plainPath)
	require.NoError(t, err)
	assert.Equal(t, c.raw, got)
}

func TestStripExecHeader(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"full header":             {"Chunk ID: 5e6cdb\nWall time: 0.1310 seconds\nProcess exited with code 0\nOriginal token count: 5\nOutput:\n# proj\n\nbody", "Process exited with code 0\n# proj\n\nbody"},
		"running variant":         {"Chunk ID: ab\nWall time: 1.0 seconds\nProcess running with session ID 7\nOriginal token count: 0\nOutput:\n", "Process running with session ID 7\n"},
		"no chunk id":             {"Wall time: 0.2 seconds\nOutput:\n[{\"type\":\"text\"}]", "[{\"type\":\"text\"}]"},
		"script completed":        {"Script completed\nWall time 0.0 seconds\nOutput:\n", "Script completed\n"},
		"token count is hex":      {"Chunk ID: x\nWall time: 0.1 seconds\nProcess exited with code 1\nOriginal token count: 1a\nOutput:\nerr", "Process exited with code 1\nerr"},
		"no header":               {"Plan updated", "Plan updated"},
		"output first, no header": {"Output:\nkept whole", "Output:\nkept whole"},
		"unexpected header line":  {"Chunk ID: x\nunexpected\nOutput:\nbody", "Chunk ID: x\nunexpected\nOutput:\nbody"},
		"header without output":   {"Chunk ID: x\nWall time: 1 seconds\nProcess exited with code 0", "Chunk ID: x\nWall time: 1 seconds\nProcess exited with code 0"},
		"empty":                   {"", ""},
		"multiline body kept":     {"Chunk ID: x\nOutput:\na\nOutput:\nb", "a\nOutput:\nb"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, stripExecHeader(tc.in))
		})
	}
}

func TestCodexCustomOutputText(t *testing.T) {
	for name, tc := range map[string]struct {
		in                  string
		body                string
		structured, success bool
	}{
		"exit 0":             {`{"output":"Success. Updated the following files:\nA x\n","metadata":{"exit_code":0,"duration_seconds":0.1}}`, "Process exited with code 0\nSuccess. Updated the following files:\nA x\n", true, true},
		"exit 1":             {`{"output":"boom\n","metadata":{"exit_code":1}}`, "Process exited with code 1\nboom\n", true, false},
		"exit 0 empty body":  {`{"output":"","metadata":{"exit_code":0}}`, "Process exited with code 0", true, true},
		"no exit, success":   {`{"output":"Success. Updated the following files:\n"}`, "Success. Updated the following files:\n", true, true},
		"no exit, failure":   {`{"output":"Error\n"}`, "Error\n", true, false},
		"no exit, empty":     {`{"output":""}`, "", true, false},
		"leading whitespace": {"  {\"output\":\"ok\",\"metadata\":{\"exit_code\":0}}\n", "Process exited with code 0\nok", true, true},
		"not json":           {"plain text {", "plain text {", false, false},
		"json array":         {`["a"]`, `["a"]`, false, false},
		"output not string":  {`{"output":42,"metadata":{"exit_code":0}}`, `{"output":42,"metadata":{"exit_code":0}}`, false, false},
		"no output key":      {`{"metadata":{"exit_code":0}}`, `{"metadata":{"exit_code":0}}`, false, false},
		"truncated json":     {`{"output":"a`, `{"output":"a`, false, false},
		"empty":              {"", "", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			body, structured, success := codexCustomOutputText(tc.in)
			assert.Equal(t, tc.body, body)
			assert.Equal(t, tc.structured, structured, "structured")
			assert.Equal(t, tc.success, success, "success")
		})
	}
}

func TestCodexSummaries(t *testing.T) {
	assert.Equal(t, "spawn_agent detect (resolver)", codexFunctionCallSummary(codexFunctionCall{Name: "spawn_agent", Arguments: `{"task_name":"detect","agent_type":"resolver","message":"m"}`}))
	assert.Equal(t, "spawn_agent detect", codexFunctionCallSummary(codexFunctionCall{Name: "spawn_agent", Arguments: `{"task_name":"detect","message":"m"}`}))
	assert.Equal(t, "spawn_agent m", codexFunctionCallSummary(codexFunctionCall{Name: "spawn_agent", Arguments: `{"message":" m "}`}))
	long := strings.Repeat("x", 300)
	assert.Equal(t, "spawn_agent "+strings.Repeat("x", codexSpawnMessageMaxChars)+"…", codexFunctionCallSummary(codexFunctionCall{Name: "spawn_agent", Arguments: `{"message":"` + long + `"}`}))
	assert.Equal(t, "spawn_agent", codexFunctionCallSummary(codexFunctionCall{Name: "spawn_agent", Arguments: `{}`}))
	assert.Equal(t, "spawn_agent", codexFunctionCallSummary(codexFunctionCall{Name: "spawn_agent", Arguments: `not json`}))
	assert.Equal(t, "exec_command", codexFunctionCallSummary(codexFunctionCall{Name: "exec_command", Arguments: `{"cmd":""}`}))
	assert.Equal(t, "write_stdin", codexFunctionCallSummary(codexFunctionCall{Name: "write_stdin", Arguments: `{"chars":"y\n"}`}))

	assert.Equal(t, "apply_patch", codexCustomCallSummary(codexCustomToolCall{Name: "apply_patch", Input: "garbage"}))
	assert.Equal(t, "apply_patch /a /b", codexCustomCallSummary(codexCustomToolCall{Name: "apply_patch", Input: "*** Begin Patch\n*** Update File: /a\n*** Delete File: /b\n*** End Patch"}))
	assert.Equal(t, "exec ls", codexCustomCallSummary(codexCustomToolCall{Name: "exec", Input: "\n\n  ls  \nmore"}))
	assert.Equal(t, "other", codexCustomCallSummary(codexCustomToolCall{Name: "other", Input: "x"}))

	assert.Equal(t, "web_search", codexWebSearchSummary(nil))
	assert.Equal(t, "web_search", codexWebSearchSummary([]byte(`"str"`)))
	assert.Equal(t, "web_search q", codexWebSearchSummary([]byte(`{"query":" q ","queries":["a"],"url":"u"}`)))
	assert.Equal(t, "web_search a", codexWebSearchSummary([]byte(`{"queries":["","a"],"url":"u"}`)), "first non-empty query")
	assert.Equal(t, "web_search u", codexWebSearchSummary([]byte(`{"url":"u","pattern":"p"}`)))

	assert.Equal(t, "subagent", codexSpawnLabel(nil, ""))
	assert.Equal(t, "p", codexSpawnLabel(nil, "p"))
	assert.Equal(t, "p", codexSpawnLabel(&codexSpawnArgs{Message: "m"}, "p"))
	assert.Equal(t, "m", codexSpawnLabel(&codexSpawnArgs{Message: "m"}, ""))
	assert.Equal(t, "t", codexSpawnLabel(&codexSpawnArgs{TaskName: "t", AgentType: "a"}, "p"))
	assert.Equal(t, "a", codexSpawnLabel(&codexSpawnArgs{AgentType: "a"}, "p"))

	assert.Equal(t, "Nick · role", codexAgentTitle(&codexSessionMeta{AgentNickname: "Nick", AgentRole: "role", AgentPath: "/p"}))
	assert.Equal(t, "role", codexAgentTitle(&codexSessionMeta{AgentRole: "role", AgentPath: "/p"}))
	assert.Equal(t, "/p", codexAgentTitle(&codexSessionMeta{AgentPath: "/p"}))
	assert.Empty(t, codexAgentTitle(&codexSessionMeta{}))
}

func TestCodexFallbackHumanText(t *testing.T) {
	parts := []codexContentPart{
		{Type: "input_text", Text: "<environment_context>x</environment_context>"},
		{Type: "input_text", Text: "\n\n<INSTRUCTIONS>\nbody\n</INSTRUCTIONS>"}, // leading blank line: trimmed first, then dropped
		{Type: "input_text", Text: "# AGENTS.md instructions for /p\n\nbody"},
		{Type: "input_image", Text: "real looking text on an image part"},
		{Type: "input_text", Text: "  real prompt  "},
		{Type: "input_text", Text: "second line"},
	}
	assert.Equal(t, "real prompt\nsecond line", codexFallbackHumanText(parts))
	assert.Empty(t, codexFallbackHumanText(nil))
	assert.Empty(t, codexFallbackHumanText([]codexContentPart{{Type: "input_text", Text: "   "}}))
	assert.Equal(t, "#hashtag prompt", codexFallbackHumanText([]codexContentPart{{Type: "input_text", Text: "#hashtag prompt"}}), "only the AGENTS.md prefix is dropped, not every #")
}

// FuzzCodexDecoder_NeverPanics pins the Decoder contract for Codex: bad CONTENT
// is never an error, nothing panics, every entry is well-formed and in order.
func FuzzCodexDecoder_NeverPanics(f *testing.F) {
	for _, c := range codexCases(f) {
		f.Add(c.raw)
	}
	f.Add([]byte(`{"type":"session_meta","payload":{"source":{"subagent":{"thread_spawn":5}},"git":"x"}}`))
	f.Add([]byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c","output":[{"type":"input_text","text":5}]}}`))
	f.Add([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c","output":"{\"output\":\"x\",\"metadata\":{\"exit_code\":\"zero\"}}"}}`))
	f.Add([]byte(`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":"str"}}}`))
	f.Add([]byte(`{"type":"response_item","payload":{"type":"function_call","name":"spawn_agent","arguments":"{\"task_name\":1}","call_id":"c"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"collab_agent_spawn_end","call_id":"c","new_thread_id":7}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		tr, err := codexDecoder{}.Decode(bytes.NewReader(raw))
		require.NoError(t, err, "content is never an error")
		require.NotNil(t, tr)
		assert.Equal(t, PlatformCodex, tr.Meta.Platform)
		prev := -1
		for _, e := range tr.Entries {
			assert.NotEqual(t, EntryUnknown, e.Kind)
			assert.GreaterOrEqual(t, e.LineIndex, prev, "entries are in source order")
			prev = e.LineIndex
			for _, p := range e.Parts {
				assert.NotEqual(t, p.Text == "", p.Call == nil, "exactly one of Text/Call is set")
			}
		}
	})
}

// TestCodexDecoder_EdgeBranches covers the residual branches the fixture table
// does not reach: a malformed payload of every typed kind (one warning each,
// line skipped, decode continues), a spawn_agent with no call_id (label
// computed inline, no resolution), an assistant message with no text and no
// calls (no entry), an unparseable session_meta payload timestamp (StartTime
// falls back to the envelope), and agent identity carried only inside
// source.subagent.thread_spawn.
func TestCodexDecoder_EdgeBranches(t *testing.T) {
	t.Run("malformed typed payloads warn once each and are skipped", func(t *testing.T) {
		h := captureSlog(t)
		raw := strings.Join([]string{
			`{"timestamp":"` + at(0) + `","type":"response_item","payload":{"type":"function_call","name":5}}`,
			`{"timestamp":"` + at(0) + `","type":"response_item","payload":{"type":"custom_tool_call","input":[1]}}`,
			`{"timestamp":"` + at(0) + `","type":"response_item","payload":{"type":"function_call_output","call_id":7}}`,
			`{"timestamp":"` + at(0) + `","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":7}}`,
			`{"timestamp":"` + at(0) + `","type":"event_msg","payload":{"type":"user_message","message":["x"]}}`,
			`{"timestamp":"` + at(0) + `","type":"event_msg","payload":{"type":"item_completed","item":"str"}}`,
			`{"timestamp":"` + at(0) + `","type":"event_msg","payload":{"type":"collab_agent_spawn_end","call_id":[]}}`,
			`{"timestamp":"` + at(1) + `","type":"event_msg","payload":{"type":"user_message","message":"survives"}}`,
		}, "\n")
		tr, err := codexDecoder{}.Decode(strings.NewReader(raw))
		require.NoError(t, err)
		assert.Equal(t, `H@7 "survives"`, describeEntries(tr))
		var kinds []string
		for _, r := range h.recordsWithMessage("vault codex decoder: skipping malformed payload") {
			kinds = append(kinds, recordAttrs(r)["payload"].(string))
		}
		// web_search_call is absent: its only field is raw JSON, so its typed decode cannot fail.
		assert.Equal(t, []string{"function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output", "user_message", "item_completed", "collab_agent_spawn_end"}, kinds)
	})

	t.Run("spawn_agent without call_id labels inline and never resolves", func(t *testing.T) {
		tr := decodeCodexRaw(t, codexRollout(t, codexLegacy,
			codexUserEvent(at(0), "go"),
			codexFunctionCallLine(t, at(1), "", "spawn_agent", map[string]any{"agent_type": "explorer"}),
			codexSpawnEnd(at(2), "", codexFixtureChildID), // empty call_id: ignored
		))
		assert.Equal(t, `H@0 "go"`+"\n"+`A@1 call(spawn_agent "spawn_agent explorer") launch("explorer")`, describeEntries(tr))
		assert.Empty(t, tr.Entries[1].Parts[0].Call.Launch.ChildUUID)
	})

	t.Run("assistant with no text and no calls produces no entry", func(t *testing.T) {
		tr := decodeCodexRaw(t, codexRollout(t, codexLegacy,
			codexUserEvent(at(0), "go"),
			codexAssistant(at(1), "", "   "),
			codexAssistant(at(2), "real"),
		))
		assert.Equal(t, `H@0 "go"`+"\n"+`A@2 text("real")`, describeEntries(tr))
	})

	t.Run("unparseable payload timestamp falls back to the envelope time", func(t *testing.T) {
		tr := decodeCodexRaw(t, codexRollout(t, codexLegacy,
			codexSessionMetaLine(at(3), codexLegacy, codexMetaOpts{payloadTS: "yesterday-ish"}),
			codexUserEvent(at(4), "go"),
		))
		assert.Equal(t, parseJSONLTime(at(3)), tr.Meta.StartTime)
		assert.Equal(t, parseJSONLTime(at(4)), tr.Meta.EndTime)
	})

	t.Run("agent identity only inside source.subagent.thread_spawn", func(t *testing.T) {
		var m Meta
		sm := &codexSessionMeta{Source: []byte(`{"subagent":{"thread_spawn":{"parent_thread_id":"p","agent_nickname":"Nick","agent_role":"role","agent_path":"/path"}}}`)}
		assert.True(t, codexApplyMeta(&m, sm, parseJSONLTime(at(0))))
		assert.Equal(t, "p", m.ParentUUID)
		assert.Equal(t, "subagent", m.Source)
		assert.Equal(t, "Nick · role", codexAgentTitle(sm), "nickname/role lifted from thread_spawn")
		assert.Equal(t, "/path", sm.AgentPath)
		assert.Equal(t, parseJSONLTime(at(0)), m.StartTime, "no payload timestamp → envelope time")

		var top Meta
		lifted := &codexSessionMeta{ParentThreadID: "lifted", AgentNickname: "Top", Source: []byte(`{"subagent":{"thread_spawn":{"parent_thread_id":"inner","agent_nickname":"Inner"}}}`)}
		codexApplyMeta(&top, lifted, parseJSONLTime(at(0)))
		assert.Equal(t, "lifted", top.ParentUUID, "the lifted field wins over the nested one")
		assert.Equal(t, "Top", lifted.AgentNickname)
	})
}

func decodeCodexRaw(t *testing.T, raw []byte) *Transcript {
	t.Helper()
	tr, err := codexDecoder{}.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	return tr
}
