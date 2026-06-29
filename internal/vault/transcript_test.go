package vault

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findMessage returns the first message with the given role, or fails.
func findMessage(t *testing.T, msgs []TranscriptMessage, role string) TranscriptMessage {
	t.Helper()
	for _, m := range msgs {
		if m.Role == role {
			return m
		}
	}
	t.Fatalf("no %q message in transcript", role)
	return TranscriptMessage{}
}

func TestParseTranscript_RolesAndAnchors(t *testing.T) {
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "Fix the timeout bug"), // line 0
		assistantLine("a1", "m1", []map[string]any{ // line 1
			{"type": "text", "text": "Reading the config."},
			{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"file_path": "/p/config.toml"}},
		}),
		userToolResultLine("u2", "build log: error at line 5"), // line 2
		aiTitleLine("Timeout fix"),                              // line 3 (no message)
	)

	msgs := ParseTranscript(raw, nil)
	require.Len(t, msgs, 3)

	assert.Equal(t, RoleUser, msgs[0].Role)
	assert.Equal(t, 0, msgs[0].SourceLine)
	assert.Contains(t, msgs[0].Body, "Fix the timeout bug")

	assert.Equal(t, RoleAssistant, msgs[1].Role)
	assert.Equal(t, 1, msgs[1].SourceLine)
	assert.Contains(t, msgs[1].Body, "Reading the config.")
	assert.Contains(t, msgs[1].Body, "→ Read /p/config.toml", "non-subagent tool_use renders inline, not as a marker")

	assert.Equal(t, RoleTool, msgs[2].Role)
	assert.Equal(t, 2, msgs[2].SourceLine)
	// The tool_result is a Read result: an excluded tool, so the viewer marks it
	// Collapsed (A1) — rendered as an openable marker labeled by the call summary,
	// with the full body carried on Body for the expand-on-demand target.
	assert.True(t, msgs[2].Collapsed, "an excluded-tool (Read) result collapses regardless of size")
	assert.Equal(t, "Read /p/config.toml", msgs[2].ToolSummary, "collapsed marker is labeled by the call summary")
	assert.Equal(t, "build log: error at line 5", msgs[2].Body, "the full body is carried for the open target")
}

// bashResultSession builds a transcript whose single tool_result is a Bash result
// (a NON-excluded tool) with the given body, so the collapse decision turns purely
// on the size/line threshold.
func bashResultSession(t *testing.T, body string) []TranscriptMessage {
	t.Helper()
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "run it"),
		assistantLine("a1", "m1", []map[string]any{
			{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
		}),
		map[string]any{
			"type": "user", "uuid": "u2", "timestamp": "2026-05-01T10:00:10Z",
			"cwd": "/p", "gitBranch": "main",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "t1", "content": body},
			}},
		},
	)
	return ParseTranscript(raw, nil)
}

// editDiffSession builds a transcript whose Edit tool_result (id t1) carries a
// structuredPatch in its top-level toolUseResult. patch is the structuredPatch
// value (nil/empty exercises the no-patch fallback).
func editDiffSession(t *testing.T, filePath, successBody string, patch []map[string]any) []TranscriptMessage {
	t.Helper()
	toolResult := map[string]any{"filePath": filePath}
	if patch != nil {
		toolResult["structuredPatch"] = patch
	}
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "set the status to done"),
		assistantLine("a1", "m1", []map[string]any{
			{"type": "tool_use", "id": "t1", "name": "Edit", "input": map[string]any{"file_path": filePath}},
		}),
		map[string]any{
			"type": "user", "uuid": "u2", "timestamp": "2026-05-01T10:00:10Z",
			"cwd": "/p", "gitBranch": "main",
			"message": map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "t1", "content": successBody},
			}},
			"toolUseResult": toolResult,
		},
	)
	return ParseTranscript(raw, nil)
}

func TestParseTranscript_EditDiffCollapsesToDiffMarker(t *testing.T) {
	tool := findMessage(t, editDiffSession(t, "/p/tasks.md",
		"The file /p/tasks.md has been updated successfully.",
		[]map[string]any{
			{"oldStart": 244, "oldLines": 1, "newStart": 244, "newLines": 1, "lines": []string{"-pending", "+done"}},
		}), RoleTool)

	assert.True(t, tool.Collapsed, "an Edit result collapses to a marker")
	assert.True(t, tool.Diff, "the Edit body is reconstructed diff text, not the raw success string")
	assert.Equal(t, "Edit /p/tasks.md (+1 −1)", tool.ToolSummary, "marker stat is baked into the summary")
	assert.Contains(t, tool.Body, "@@ -244,1 +244,1 @@", "body is unified-diff text")
	assert.Contains(t, tool.Body, "+done")
	assert.NotContains(t, tool.Body, "updated successfully", "the diff replaces the success boilerplate")
}

func TestParseTranscript_EditWithoutPatchFallsBackToSuccessBody(t *testing.T) {
	// An Edit with no structuredPatch (rare) falls back to the plain success body —
	// short, so it stays inline (not collapsed), not a diff.
	tool := findMessage(t, editDiffSession(t, "/p/tasks.md",
		"The file /p/tasks.md has been updated successfully.", nil), RoleTool)

	assert.False(t, tool.Diff, "no patch → not a diff message")
	assert.False(t, tool.Collapsed, "the short success body stays inline")
	assert.Contains(t, tool.Body, "updated successfully")
}

func TestParseTranscript_LargeToolResultCollapses(t *testing.T) {
	// A many-line Bash result (over collapseToolResultLines) collapses even though
	// Bash is NOT in excludedResultTools — large results collapse for read-through.
	var b strings.Builder
	for i := 0; i < collapseToolResultLines+5; i++ {
		fmt.Fprintf(&b, "log line %d\n", i)
	}
	body := strings.TrimRight(b.String(), "\n")

	tool := findMessage(t, bashResultSession(t, body), RoleTool)
	assert.True(t, tool.Collapsed, "a result over the line threshold collapses")
	assert.Equal(t, "Bash go test ./...", tool.ToolSummary)
	assert.Equal(t, body, tool.Body, "the full body is carried for the open target")
}

func TestParseTranscript_ByteHeavyToolResultCollapses(t *testing.T) {
	// A single-line body under the line threshold but over the byte threshold
	// collapses too (overCollapseThreshold ORs lines and bytes).
	body := strings.Repeat("x", collapseToolResultBytes+1)
	require.NotContains(t, body, "\n", "one line — only the byte threshold can trigger collapse")

	tool := findMessage(t, bashResultSession(t, body), RoleTool)
	assert.True(t, tool.Collapsed, "a single huge line collapses on the byte threshold")
	assert.Equal(t, body, tool.Body)
}

func TestParseTranscript_SmallToolResultStaysInline(t *testing.T) {
	// A short Bash result stays inline (not a marker); Body is the summary-prefixed
	// text, matching the pre-A1 rendering.
	tool := findMessage(t, bashResultSession(t, "one short line of output"), RoleTool)
	assert.False(t, tool.Collapsed, "a small non-excluded result renders inline")
	assert.Equal(t, "Bash go test ./...\none short line of output", tool.Body,
		"inline tool results keep the summary-prefixed body")
}

func TestParseTranscript_DeduplicatesProgressiveSnapshots(t *testing.T) {
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "hi"), // line 0
		assistantLine("a1", "m1", []map[string]any{ // line 1 — first snapshot
			{"type": "text", "text": "Part one"},
		}),
		assistantLine("a2", "m1", []map[string]any{ // line 2 — fuller snapshot, same id
			{"type": "text", "text": "Part one"},
			{"type": "text", "text": "Part two"},
		}),
	)

	msgs := ParseTranscript(raw, nil)
	require.Len(t, msgs, 2, "the two snapshots of m1 merge into one assistant message")
	a := findMessage(t, msgs, RoleAssistant)
	assert.Equal(t, 1, a.SourceLine, "anchor is the first/canonical snapshot line")
	assert.Contains(t, a.Body, "Part one")
	assert.Contains(t, a.Body, "Part two")
}

func TestParseTranscript_SubagentMarkerMappingAligned(t *testing.T) {
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "do work"),
		assistantLine("a1", "m1", []map[string]any{
			{"type": "text", "text": "Delegating."},
			{"type": "tool_use", "id": "t1", "name": "Task", "input": map[string]any{
				"description": "explore the code", "subagent_type": "Explore", "prompt": "look around",
			}},
		}),
	)

	// One marker, one archived subagent → mapped + openable.
	msgs := ParseTranscript(raw, []string{"abc"})
	marker := findMessage(t, msgs, RoleSubagent)
	assert.Equal(t, "explore the code", marker.Body, "marker label prefers the description field")
	assert.True(t, marker.Openable)
	assert.Equal(t, "abc", marker.AgentID)
}

func TestParseTranscript_SubagentMarkerMappingMismatchNotOpenable(t *testing.T) {
	raw := jsonlBytes(t,
		assistantLine("a1", "m1", []map[string]any{
			{"type": "tool_use", "id": "t1", "name": "Agent", "input": map[string]any{"prompt": "go"}},
		}),
	)

	// One marker, two archived subagents → ambiguous → visible but not openable.
	msgs := ParseTranscript(raw, []string{"a", "b"})
	marker := findMessage(t, msgs, RoleSubagent)
	assert.False(t, marker.Openable)
	assert.Empty(t, marker.AgentID)
	assert.Equal(t, "go", marker.Body, "label falls back to the prompt when no description")
}

func TestParseTranscript_QueuedCommandAttachment(t *testing.T) {
	// A queued_command attachment (A2) parses to a RoleUser message flagged Queued,
	// anchored to its source line so a search hit still scrolls to it.
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "kick off"), // line 0
		assistantLine("a1", "m1", []map[string]any{{"type": "text", "text": "working"}}), // line 1
		map[string]any{ // line 2
			"type": "attachment", "uuid": "at1", "timestamp": "2026-05-01T10:00:09Z",
			"attachment": map[string]any{"type": "queued_command", "prompt": "wait, also do X", "commandMode": "prompt"},
		},
	)

	msgs := ParseTranscript(raw, nil)
	require.Len(t, msgs, 3)

	q := msgs[2]
	assert.Equal(t, RoleUser, q.Role)
	assert.True(t, q.Queued, "the queued_command is flagged for the · queued annotation")
	assert.Equal(t, "wait, also do X", q.Body)
	assert.Equal(t, 2, q.SourceLine, "anchored to the attachment line")

	assert.False(t, msgs[0].Queued, "a normal user message is not flagged queued")
}

func TestParseTranscript_Empty(t *testing.T) {
	assert.Nil(t, ParseTranscript(nil, nil))
	assert.Nil(t, ParseTranscript([]byte{}, nil))
}

func TestSubagentPathRoundTrip(t *testing.T) {
	rel := SubagentRelPath("abc123")
	assert.Equal(t, "subagents/agent-abc123.jsonl", rel)
	id, ok := SubagentIDFromPath(rel)
	assert.True(t, ok)
	assert.Equal(t, "abc123", id)

	_, ok = SubagentIDFromPath("tool-results/toolu_1.json")
	assert.False(t, ok)
}
