package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Action determines how a tool's content enters the transcript.
type Action int

const (
	ActionSkip    Action = iota // zero value: unregistered extractors default to skip
	ActionEnrich                // content becomes a metadata line on text-bearing turns
	ActionPromote               // content becomes part of AssistantText
)

// ToolExtractor defines how to extract searchable content from a tool_use input.
type ToolExtractor struct {
	Extract func(input json.RawMessage) string
	Action  Action
}

// ExtractorRegistry maps tool names to their extraction behavior.
type ExtractorRegistry struct {
	exact map[string]ToolExtractor
}

// Lookup returns the extractor for a tool name.
// Returns false if the tool is not registered (caller should skip).
func (r *ExtractorRegistry) Lookup(name string) (ToolExtractor, bool) {
	e, ok := r.exact[name]
	return e, ok
}

const palPromptMaxLen = 768

func makePALExtractor(toolName string) ToolExtractor {
	subtool := strings.TrimPrefix(toolName, "mcp__pal__")
	return ToolExtractor{
		Action: ActionPromote,
		Extract: func(input json.RawMessage) string {
			var v struct {
				Prompt string `json:"prompt"`
			}
			if err := json.Unmarshal(input, &v); err != nil || v.Prompt == "" {
				return ""
			}
			runes := []rune(v.Prompt)
			totalLen := len(runes)
			prompt := v.Prompt
			if totalLen > palPromptMaxLen {
				prompt = string(runes[:palPromptMaxLen]) + fmt.Sprintf("\n... (truncated, %d chars total)", totalLen)
			}
			return fmt.Sprintf("--- PAL: %s ---\n%s\n--- End PAL ---", subtool, prompt)
		},
	}
}

func filePathExtractor(toolLabel string) ToolExtractor {
	return ToolExtractor{
		Action: ActionEnrich,
		Extract: func(input json.RawMessage) string {
			var v struct {
				FilePath string `json:"file_path"`
			}
			if err := json.Unmarshal(input, &v); err != nil || v.FilePath == "" {
				return ""
			}
			return fmt.Sprintf("[%s: %s]", toolLabel, v.FilePath)
		},
	}
}

var grepExtractor = ToolExtractor{
	Action: ActionEnrich,
	Extract: func(input json.RawMessage) string {
		var v struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(input, &v); err != nil || v.Pattern == "" {
			return ""
		}
		return fmt.Sprintf("[Grep: %s]", v.Pattern)
	},
}

var agentExtractor = ToolExtractor{
	Action: ActionEnrich,
	Extract: func(input json.RawMessage) string {
		var v struct {
			Description  string `json:"description"`
			SubagentType string `json:"subagent_type"`
		}
		if err := json.Unmarshal(input, &v); err != nil {
			return ""
		}
		typ := v.SubagentType
		if typ == "" {
			typ = "Agent"
		}
		desc := v.Description
		if desc == "" {
			desc = "subagent task"
		}
		return fmt.Sprintf("[Agent: %s — %q]", typ, desc)
	},
}

var palToolNames = []string{
	"mcp__pal__chat",
	"mcp__pal__thinkdeep",
	"mcp__pal__codereview",
	"mcp__pal__consensus",
	"mcp__pal__analyze",
	"mcp__pal__debug",
	"mcp__pal__planner",
	"mcp__pal__challenge",
	"mcp__pal__secaudit",
	"mcp__pal__refactor",
}

// NewDefaultRegistry builds the registry with all known tool extractors.
func NewDefaultRegistry() *ExtractorRegistry {
	r := &ExtractorRegistry{
		exact: make(map[string]ToolExtractor, len(palToolNames)+5),
	}
	for _, name := range palToolNames {
		r.exact[name] = makePALExtractor(name)
	}
	r.exact["Read"] = filePathExtractor("Read")
	r.exact["Write"] = filePathExtractor("Write")
	r.exact["Edit"] = filePathExtractor("Edit")
	r.exact["Grep"] = grepExtractor
	r.exact["Agent"] = agentExtractor
	return r
}

// DefaultRegistry is the package-level registry used by the parser.
var DefaultRegistry = NewDefaultRegistry()
