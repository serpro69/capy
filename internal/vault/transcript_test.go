package vault

import (
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
	assert.Contains(t, msgs[2].Body, "build log")
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
