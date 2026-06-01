package vault

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderText_RolesToolsAndResults(t *testing.T) {
	raw := jsonlBytes(t,
		userLine("u1", "/home/user/proj", "main", "Please fix the timeout"),
		assistantLine("a1", "m1", []map[string]any{
			{"type": "thinking", "thinking": "internal reasoning that must not render", "signature": "sig"},
			{"type": "text", "text": "Reading the config first."},
			{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"file_path": "/proj/config.toml"}},
		}),
		userToolResultLine("u2", "build log: timeout at line 5"),
	)

	out := RenderText(raw)

	assert.Contains(t, out, "[You]")
	assert.Contains(t, out, "Please fix the timeout")
	assert.Contains(t, out, "[Claude]")
	assert.Contains(t, out, "Reading the config first.")
	assert.Contains(t, out, "→ Read /proj/config.toml", "tool_use renders as an indicator line")
	assert.Contains(t, out, "[Tool result]")
	assert.Contains(t, out, "build log: timeout at line 5")
	assert.NotContains(t, out, "internal reasoning", "thinking blocks are not rendered")
}

func TestRenderMarkdown_HeadingsAndFences(t *testing.T) {
	raw := jsonlBytes(t,
		userLine("u1", "/home/user/proj", "main", "hello"),
		assistantLine("a1", "m1", []map[string]any{{"type": "text", "text": "hi there"}}),
		userToolResultLine("u2", "some tool output"),
	)

	out := RenderMarkdown(raw)

	assert.Contains(t, out, "## 👤 You")
	assert.Contains(t, out, "## 🤖 Claude")
	assert.Contains(t, out, "## ⎿ Tool result")
	assert.Contains(t, out, "```\nsome tool output\n```", "tool results are fenced in markdown")
}

func TestRenderMarkdown_WidensFenceForBacktickContent(t *testing.T) {
	// A tool result that itself contains a ``` run must be wrapped in a longer
	// fence so it doesn't prematurely close the markdown code block.
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "show me"),
		userToolResultLine("u2", "before\n```\ncode\n```\nafter"),
	)

	out := RenderMarkdown(raw)

	assert.Contains(t, out, "````", "fence widened to 4 backticks")
	assert.Contains(t, out, "```\ncode\n```", "inner triple-backtick content preserved")
}

func TestMdFence(t *testing.T) {
	assert.Equal(t, "```", mdFence("no backticks"))
	assert.Equal(t, "```", mdFence("a single ` tick"))
	assert.Equal(t, "````", mdFence("triple ``` inside"))
	assert.Equal(t, "`````", mdFence("```` four backticks"))
}

func TestRenderText_DeduplicatesProgressiveSnapshots(t *testing.T) {
	// Two assistant lines share message id m1; the second is a superset snapshot.
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "go"),
		assistantLine("a1", "m1", []map[string]any{
			{"type": "text", "text": "partial answer"},
		}),
		assistantLine("a2", "m1", []map[string]any{
			{"type": "text", "text": "partial answer"},
			{"type": "text", "text": "final answer"},
		}),
	)

	out := RenderText(raw)

	assert.Equal(t, 1, strings.Count(out, "partial answer"), "shared snapshot block rendered once")
	assert.Contains(t, out, "final answer")
}

func TestRenderText_ToolResultNotTruncated(t *testing.T) {
	// A tool_result larger than the scanner's 16KB FTS cap must render in full —
	// the renderer is for faithful display, not bounded search extraction.
	big := strings.Repeat("x", maxToolResultChars+5000)
	raw := jsonlBytes(t,
		userLine("u1", "/p", "main", "run it"),
		userToolResultLine("u2", big),
	)

	out := RenderText(raw)

	assert.Contains(t, out, big, "full tool result is rendered, not head/tail truncated")
	assert.NotContains(t, out, "…")
}

func TestRenderText_EmptyBlob(t *testing.T) {
	assert.Equal(t, "", RenderText(nil))
	assert.Equal(t, "", RenderText([]byte("")))
}

func TestRenderText_SystemAndPRLink(t *testing.T) {
	raw := jsonlBytes(t,
		map[string]any{"type": "system", "subtype": "away_summary", "content": "You were away; here is a summary.", "timestamp": "2026-05-01T10:00:00Z"},
		map[string]any{"type": "pr-link", "prUrl": "https://github.com/o/r/pull/7", "prRepository": "o/r", "prNumber": 7, "timestamp": "2026-05-01T10:01:00Z"},
	)

	out := RenderText(raw)

	assert.Contains(t, out, "[System]")
	assert.Contains(t, out, "You were away")
	assert.Contains(t, out, "PR #7")
	assert.Contains(t, out, "o/r")
}

func TestRender_SubagentBlobRendersStandalone(t *testing.T) {
	// A subagent JSONL is just another session JSONL — the same renderer applies.
	sub := jsonlBytes(t,
		userLine("su1", "/p", "main", "subagent task prompt"),
		assistantLine("sa1", "sm1", []map[string]any{{"type": "text", "text": "subagent reply"}}),
	)
	out := RenderText(sub)
	require.NotEmpty(t, out)
	assert.Contains(t, out, "subagent task prompt")
	assert.Contains(t, out, "subagent reply")
}
