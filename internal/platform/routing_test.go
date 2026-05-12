package platform

import (
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/hook"
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
	assert.Contains(t, instructions, "Decision principle")
	assert.Contains(t, instructions, "When to use direct tools")
	assert.Contains(t, instructions, "When to use capy tools")
	assert.Contains(t, instructions, "Blocked commands")
	assert.Contains(t, instructions, "Source kinds")
	assert.Contains(t, instructions, "Subagent routing")
	assert.Contains(t, instructions, "Output constraints")
	assert.Contains(t, instructions, "capy commands")

	// Must mention curl/wget blocking
	assert.Contains(t, instructions, "curl")
	assert.Contains(t, instructions, "wget")
	assert.Contains(t, instructions, "WebFetch")

	// Must reference sandbox execution
	assert.Contains(t, instructions, "sandbox")

	// Must distinguish web comprehension from web extraction (issue #47)
	assert.Contains(t, instructions, "authoritative web pages",
		"routing instructions should guide web comprehension to direct tools")
	assert.Contains(t, instructions, "gh issue view",
		"routing instructions should mention gh CLI as a runtime web tool")
	assert.Contains(t, instructions, "WebSearch",
		"routing instructions should mention WebSearch as a runtime web tool")
}

func TestRoutingBlockWebComprehension(t *testing.T) {
	block := hook.RoutingBlock()

	// Must distinguish web comprehension from extraction (issue #47)
	assert.Contains(t, block, "authoritative web pages",
		"routing block should mention authoritative web pages for comprehension")
	assert.Contains(t, block, "runtime web tools",
		"routing block should reference runtime web tools as alternative")
	assert.Contains(t, block, "web comprehension",
		"routing block blocked section should mention web comprehension alternative")
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
