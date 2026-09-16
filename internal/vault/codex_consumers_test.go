package vault

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codex_consumers_test.go runs the three platform-blind consumers — the FTS
// scanner (ScanSession), the `show` renderer (RenderText / RenderMarkdown) and
// the TUI transcript (ParseTranscript) — over Codex bytes (Slice 7.6). The
// decoder is pinned separately (codex_decoder_test.go); these tests pin the
// CONSUMER policies as they apply to Codex entries: human turns index as
// role=user, tool results carry the call summary, apply_patch joins the
// Edit/Write treatment (body FTS-excluded, verbatim in show, Diff marker in the
// TUI), the assistant heading names Codex, and a resolved spawn is an openable
// child marker. Expectations are hand-derived from codex_fixtures_test.go.

func codexRaw(t *testing.T, name string) []byte {
	t.Helper()
	return codexCaseByName(t, name).raw
}

func scanCodex(t *testing.T, name string) *ScanOutput {
	t.Helper()
	out, err := ScanSession(PlatformCodex, bytes.NewReader(codexRaw(t, name)))
	require.NoError(t, err)
	return out
}

func roles(results []ScanResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Role)
	}
	return out
}

func rowsWithRole(results []ScanResult, role string) []ScanResult {
	var out []ScanResult
	for _, r := range results {
		if r.Role == role {
			out = append(out, r)
		}
	}
	return out
}

func TestCodexConsumers_ScanSession_RolesTitleAndSummaries(t *testing.T) {
	// legacy_basic: H@5, A@7 (text + exec_command call attached), R@10, A@11.
	out := scanCodex(t, "legacy_basic")
	assert.Equal(t, []string{roleUser, roleAssistant, roleTool, roleAssistant}, roles(out.Results))
	assert.Equal(t, 3, out.MessageCount, "user + 2 assistant rows; the tool row does not count")
	assert.Equal(t, "Please review the design documents under docs/ and report inconsistencies.", out.Title,
		"the title is the first human turn")
	assert.Equal(t, PlatformCodex, out.Platform)
	assert.Equal(t, codexFixtureID, out.PlatformID)
	assert.Equal(t, "/home/user/Projects/proj", out.CWD)
	assert.Equal(t, "master", out.Branch)

	user := out.Results[0]
	assert.Equal(t, 5, user.LineIndex, "anchored to the physical event line")
	assert.Equal(t, 0, user.TurnIndex)

	assistant := out.Results[1]
	assert.Equal(t, "I'll review the three documents and trace each claim against the code.\nexec_command sed -n '1,240p' README.md",
		assistant.ContentText, "assistant row = text parts then call summaries, in part order")
	assert.Equal(t, []string{"exec_command"}, assistant.ToolNames)

	tool := out.Results[2]
	assert.Equal(t, 10, tool.LineIndex)
	assert.True(t, strings.HasPrefix(tool.ContentText, "exec_command sed -n '1,240p' README.md\n"),
		"tool row is prefixed with the call summary: %q", tool.ContentText)
	assert.Contains(t, tool.ContentText, "Process exited with code 0", "the exit-code line is kept as search signal")
	assert.Contains(t, tool.ContentText, "README body")
	assert.NotContains(t, tool.ContentText, "Chunk ID", "the exec_command wrapper header is stripped")
	assert.Equal(t, 0, tool.TurnIndex, "a tool result continues the calling assistant's turn")
}

// apply_patch joins diffResultTools: its boilerplate body is FTS-excluded while
// the `apply_patch <files>` summary stays searchable on the assistant row —
// success or failure alike (the exclusion is name-keyed, like Edit/Write).
func TestCodexConsumers_ScanSession_ApplyPatchBodyExcluded(t *testing.T) {
	for _, name := range []string{"apply_patch_success", "apply_patch_failure"} {
		t.Run(name, func(t *testing.T) {
			out := scanCodex(t, name)
			assert.Empty(t, rowsWithRole(out.Results, roleTool), "no tool row for an apply_patch body")
			assistants := rowsWithRole(out.Results, roleAssistant)
			require.Len(t, assistants, 1)
			assert.Equal(t, "Adding it.\napply_patch /tmp/proj/script.py", assistants[0].ContentText)
			assert.Equal(t, 2, out.MessageCount)
		})
	}
	// The hosted exec tool is NOT excluded: its output is unique signal.
	out := scanCodex(t, "custom_exec")
	tools := rowsWithRole(out.Results, roleTool)
	require.NotEmpty(t, tools)
	assert.Equal(t, "exec const x = 1;\nProcess exited with code 0\n1", tools[0].ContentText)
}

// Both child variants archive: the ≤ 0.137 child through its spawn prompt, the
// ≥ 0.147 child (no human turn by any path) through its assistant rows, titled
// from its agent identity.
func TestCodexConsumers_ScanSession_Children(t *testing.T) {
	legacy := scanCodex(t, "child_legacy_137")
	assert.Equal(t, 2, legacy.MessageCount)
	assert.Equal(t, "Review the latest commit on the current branch…", legacy.Title)
	assert.Equal(t, codexFixtureParentID, legacy.ParentUUID)
	assert.Equal(t, "subagent", legacy.Source)
	require.NotEmpty(t, rowsWithRole(legacy.Results, roleUser), "the spawn prompt indexes as role=user")

	paginated := scanCodex(t, "child_paginated_147")
	assert.Equal(t, 1, paginated.MessageCount, "one assistant row; no human turn")
	assert.Empty(t, rowsWithRole(paginated.Results, roleUser))
	assert.Equal(t, "Chandrasekhar · code-reviewer", paginated.Title, "agent fallback title")
	assert.Equal(t, codexFixtureParentID, paginated.ParentUUID)

	shell := scanCodex(t, "aborted_shell")
	assert.Equal(t, 0, shell.MessageCount, "an aborted-at-startup shell is a zero-message session")
	assert.Empty(t, shell.Title)
}

// The title fallback is sanitized then truncated to titleMaxChars, like Claude.
func TestCodexConsumers_ScanSession_TitleBounded(t *testing.T) {
	long := strings.Repeat("x", titleMaxChars+40)
	raw := codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{payloadTS: codexPayloadTS}),
		codexUserEvent(at(1), long+" sk-ant-api03-"+strings.Repeat("A", 95)),
		codexAssistant(at(2), "ok"),
	)
	out, err := ScanSession(PlatformCodex, bytes.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, titleMaxChars+1, len([]rune(out.Title)), "≤ 120 runes plus the ellipsis")
	assert.True(t, strings.HasPrefix(out.Title, strings.Repeat("x", titleMaxChars)))
	for _, r := range out.Results {
		assert.NotContains(t, r.ContentText, "sk-ant-api03-", "secrets are stripped from Codex rows exactly as from Claude rows")
	}
}

func TestCodexConsumers_RenderText(t *testing.T) {
	text := RenderText(PlatformCodex, codexRaw(t, "apply_patch_success"))
	assert.Contains(t, text, "[You]\nadd a script\n", "the human turn renders under the user heading")
	assert.Contains(t, text, "[Codex]\nAdding it.\n→ apply_patch /tmp/proj/script.py\n",
		"the assistant heading names the platform and the call renders as an arrow line")
	assert.Contains(t, text, "[Tool result]\napply_patch /tmp/proj/script.py\nProcess exited with code 0\nSuccess. Updated the following files:\nA /tmp/proj/script.py\n",
		"show keeps the apply_patch success body verbatim — exit-code line, boilerplate and paths (only the TUI collapses it to the diff)")
	assert.NotContains(t, text, "[Claude]")

	md := RenderMarkdown(PlatformCodex, codexRaw(t, "legacy_basic"))
	assert.Contains(t, md, "## 🤖 Codex\n\nI'll review the three documents")
	assert.Contains(t, md, "## 👤 You\n\nPlease review the design documents")
	assert.NotContains(t, md, "Chunk ID", "the exec header never reaches the display path either")

	assert.Empty(t, RenderText(PlatformCodex, codexRaw(t, "aborted_shell")), "nothing to show for a shell")
}

func TestCodexConsumers_ParseTranscript_MarkersAndDiff(t *testing.T) {
	t.Run("resolved spawn is an openable child marker", func(t *testing.T) {
		msgs := ParseTranscript(PlatformCodex, codexRaw(t, "spawn_legacy"), nil)
		var markers []TranscriptMessage
		for _, m := range msgs {
			if m.Role == RoleSubagent {
				markers = append(markers, m)
			}
		}
		require.Len(t, markers, 1)
		assert.Equal(t, codexFixtureChildID, markers[0].ChildUUID)
		assert.True(t, markers[0].Openable, "a marker with a ChildUUID opens the child SESSION")
		assert.Empty(t, markers[0].AgentID, "no sidecar mapping for Codex")
		assert.Equal(t, "code-reviewer", markers[0].Body, "label falls back to agent_type (no task_name)")
		assert.Equal(t, 2, markers[0].SourceLine, "anchored to the assistant entry that spawned it")
	})

	t.Run("unresolved spawn stays visible but non-openable", func(t *testing.T) {
		msgs := ParseTranscript(PlatformCodex, codexRaw(t, "spawn_paginated"), nil)
		var resolved, unresolved int
		for _, m := range msgs {
			if m.Role != RoleSubagent {
				continue
			}
			if m.ChildUUID != "" {
				resolved++
				assert.True(t, m.Openable)
			} else {
				unresolved++
				assert.False(t, m.Openable, "no ChildUUID and no sidecar ids → not openable")
			}
		}
		assert.Equal(t, 2, resolved)
		assert.Equal(t, 1, unresolved)
	})

	t.Run("successful apply_patch is a collapsed Diff marker", func(t *testing.T) {
		msgs := ParseTranscript(PlatformCodex, codexRaw(t, "apply_patch_success"), nil)
		tools := transcriptRole(msgs, RoleTool)
		require.Len(t, tools, 1)
		m := tools[0]
		assert.True(t, m.Diff)
		assert.True(t, m.Collapsed)
		assert.Equal(t, "apply_patch /tmp/proj/script.py (+2 −0)", m.ToolSummary)
		assert.Contains(t, m.Body, "+#!/usr/bin/env python3\n+import json", "the body is the unified diff built from the call's patch")
		assert.NotContains(t, m.Body, "Success. Updated", "the boilerplate body is replaced by the diff")
		assert.Equal(t, 4, m.SourceLine, "anchored to the result line")
	})

	t.Run("failed apply_patch keeps its plain body and no Diff", func(t *testing.T) {
		msgs := ParseTranscript(PlatformCodex, codexRaw(t, "apply_patch_failure"), nil)
		tools := transcriptRole(msgs, RoleTool)
		require.Len(t, tools, 1)
		assert.False(t, tools[0].Diff, "the viewer never shows a diff that was not applied")
		assert.False(t, tools[0].Collapsed, "a short plain body renders inline")
		assert.Equal(t, "apply_patch /tmp/proj/script.py\nProcess exited with code 1\nFailed to apply patch: context mismatch in /tmp/proj/script.py\n",
			tools[0].Body, "summary prefix, exit-code line, then the verbatim (newline-terminated) body")
	})
}

func transcriptRole(msgs []TranscriptMessage, role string) []TranscriptMessage {
	var out []TranscriptMessage
	for _, m := range msgs {
		if m.Role == role {
			out = append(out, m)
		}
	}
	return out
}
