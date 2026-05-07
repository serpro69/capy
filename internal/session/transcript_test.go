package session

import (
	"strings"
	"testing"
	"time"
)

func TestBuildTranscript_ToolMetaRendered(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{
				HumanText:    "Check parse.go",
				AssistantText: "Here are the relevant files.",
				ToolMeta:     []string{"[Read: internal/session/parse.go]", "[Edit: internal/session/types.go]"},
				ToolNames:    []string{"Read", "Edit"},
			},
		},
	}

	tr := BuildTranscript(s)

	if !strings.Contains(tr.Text, "[Read: internal/session/parse.go]\n") {
		t.Errorf("missing Read metadata line, got:\n%s", tr.Text)
	}
	if !strings.Contains(tr.Text, "[Edit: internal/session/types.go]\n") {
		t.Errorf("missing Edit metadata line, got:\n%s", tr.Text)
	}
	if strings.Contains(tr.Text, "[Tools:") {
		t.Errorf("old [Tools: ...] format should not appear, got:\n%s", tr.Text)
	}
}

func TestBuildTranscript_PALPassthrough(t *testing.T) {
	palBlock := "--- PAL: chat ---\nReview this design for session indexing.\n--- End PAL ---"
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{
				HumanText:    "Check the design",
				AssistantText: "Let me review.\n" + palBlock,
				ToolMeta:     []string{"[Read: internal/session/parse.go]"},
				ToolNames:    []string{"mcp__pal__chat", "Read"},
			},
		},
	}

	tr := BuildTranscript(s)

	if !strings.Contains(tr.Text, "--- PAL: chat ---") {
		t.Errorf("PAL delimiter block missing from assistant section, got:\n%s", tr.Text)
	}
	if !strings.Contains(tr.Text, "[Read: internal/session/parse.go]") {
		t.Errorf("Read metadata line missing after PAL block, got:\n%s", tr.Text)
	}
}

func TestBuildTranscript_EmptyToolMeta(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Hello", AssistantText: "Hi there"},
		},
	}

	tr := BuildTranscript(s)

	lines := strings.Split(tr.Text, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && !strings.HasPrefix(line, "[Session") {
			t.Errorf("no metadata lines expected for turn without ToolMeta, got line: %s", line)
		}
	}
}

func TestBuildTranscript_SubagentWithToolMeta(t *testing.T) {
	s := &ParsedSession{
		SessionID: "abc",
		StartTime: time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		TurnPairs: []TurnPair{
			{HumanText: "Main question", AssistantText: "Delegating."},
			{
				HumanText:    "Find the config",
				AssistantText: "Found it in config.go.",
				ToolMeta:     []string{"[Read: config.go]", "[Grep: configPath]"},
				ToolNames:    []string{"Read", "Grep"},
				IsSubagent:   true,
				SubagentType: "Explore",
				SubagentDesc: "Find config",
			},
			{HumanText: "Follow up", AssistantText: "Here is the summary."},
		},
	}

	tr := BuildTranscript(s)

	if !strings.Contains(tr.Text, "--- Subagent: Explore") {
		t.Errorf("subagent delimiter missing, got:\n%s", tr.Text)
	}
	if !strings.Contains(tr.Text, "[Read: config.go]") {
		t.Errorf("Read metadata missing inside subagent block, got:\n%s", tr.Text)
	}
	if !strings.Contains(tr.Text, "[Grep: configPath]") {
		t.Errorf("Grep metadata missing inside subagent block, got:\n%s", tr.Text)
	}
	if strings.Contains(tr.Text, "[Tools:") {
		t.Errorf("old [Tools: ...] format should not appear, got:\n%s", tr.Text)
	}
}
