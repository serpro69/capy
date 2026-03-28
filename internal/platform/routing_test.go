package platform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateRoutingInstructions(t *testing.T) {
	instructions := GenerateRoutingInstructions()

	// Must contain the primary tools used in routing
	routingTools := []string{
		"capy_execute",
		"capy_execute_file",
		"capy_index",
		"capy_search",
		"capy_fetch_and_index",
		"capy_batch_execute",
		"capy_stats",
		"capy_doctor",
	}
	for _, tool := range routingTools {
		assert.Contains(t, instructions, tool, "routing instructions should reference %s", tool)
	}

	// Must contain key sections
	assert.Contains(t, instructions, "BLOCKED commands")
	assert.Contains(t, instructions, "REDIRECTED tools")
	assert.Contains(t, instructions, "Tool selection hierarchy")
	assert.Contains(t, instructions, "Subagent routing")
	assert.Contains(t, instructions, "Output constraints")
	assert.Contains(t, instructions, "capy commands")

	// Must mention curl/wget blocking
	assert.Contains(t, instructions, "curl")
	assert.Contains(t, instructions, "wget")
	assert.Contains(t, instructions, "WebFetch")

	// Must reference sandbox execution
	assert.Contains(t, instructions, "sandbox")
}

func TestCapyToolNamesComplete(t *testing.T) {
	expected := []string{
		"capy_execute",
		"capy_execute_file",
		"capy_index",
		"capy_search",
		"capy_fetch_and_index",
		"capy_batch_execute",
		"capy_stats",
		"capy_doctor",
		"capy_cleanup",
	}
	assert.Equal(t, expected, CapyToolNames)
}

func TestPreToolUseMatcherPattern(t *testing.T) {
	// The matcher pattern should include all key tool names
	parts := strings.Split(PreToolUseMatcherPattern, "|")
	assert.Contains(t, parts, "Bash")
	assert.Contains(t, parts, "WebFetch")
	assert.Contains(t, parts, "Read")
	assert.Contains(t, parts, "Grep")
	assert.Contains(t, parts, "Agent")
	assert.Contains(t, parts, "Task")

	// Should have a wildcard for capy MCP tools
	hasCapyWildcard := false
	for _, p := range parts {
		if strings.Contains(p, "capy") {
			hasCapyWildcard = true
			break
		}
	}
	assert.True(t, hasCapyWildcard, "matcher should include capy MCP tool wildcard")
}
