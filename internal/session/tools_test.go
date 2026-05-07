package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPALExtractor_ValidInput(t *testing.T) {
	ext, ok := DefaultRegistry.Lookup("mcp__pal__chat")
	if !ok {
		t.Fatal("mcp__pal__chat not in registry")
	}
	if ext.Action != ActionPromote {
		t.Errorf("Action = %d, want ActionPromote", ext.Action)
	}

	input := mustJSON(t, map[string]any{"prompt": "Review this design for session indexing"})
	got := ext.Extract(input)

	if !strings.HasPrefix(got, "--- PAL: chat ---") {
		t.Errorf("missing opening delimiter: %q", got)
	}
	if !strings.HasSuffix(got, "--- End PAL ---") {
		t.Errorf("missing closing delimiter: %q", got)
	}
	if !strings.Contains(got, "Review this design") {
		t.Errorf("prompt content missing: %q", got)
	}
	if strings.Contains(got, "truncated") {
		t.Error("short prompt should not be truncated")
	}
}

func TestPALExtractor_AllRegisteredTools(t *testing.T) {
	for _, name := range palToolNames {
		ext, ok := DefaultRegistry.Lookup(name)
		if !ok {
			t.Errorf("%s not in registry", name)
			continue
		}
		if ext.Action != ActionPromote {
			t.Errorf("%s: Action = %d, want ActionPromote", name, ext.Action)
		}
	}
}

func TestPALExtractor_Truncation(t *testing.T) {
	ext, ok := DefaultRegistry.Lookup("mcp__pal__thinkdeep")
	if !ok {
		t.Fatal("mcp__pal__thinkdeep not in registry")
	}

	t.Run("at boundary no truncation", func(t *testing.T) {
		prompt := strings.Repeat("x", palPromptMaxLen)
		input := mustJSON(t, map[string]any{"prompt": prompt})
		got := ext.Extract(input)
		if strings.Contains(got, "truncated") {
			t.Error("prompt at exactly 768 chars should not be truncated")
		}
	})

	t.Run("over boundary truncated", func(t *testing.T) {
		prompt := strings.Repeat("x", palPromptMaxLen+100)
		input := mustJSON(t, map[string]any{"prompt": prompt})
		got := ext.Extract(input)
		want := fmt.Sprintf("(truncated, %d chars total)", palPromptMaxLen+100)
		if !strings.Contains(got, want) {
			t.Errorf("missing truncation suffix, got: %q", got)
		}
		if !strings.HasPrefix(got, "--- PAL: thinkdeep ---") {
			t.Errorf("wrong subtool in delimiter: %q", got)
		}
	})

	t.Run("multibyte runes truncated at rune boundary", func(t *testing.T) {
		// Each emoji is 4 bytes; 768 runes = 3072 bytes. Byte-level slicing would cut mid-rune.
		prompt := strings.Repeat("\U0001F600", palPromptMaxLen+10)
		input := mustJSON(t, map[string]any{"prompt": prompt})
		got := ext.Extract(input)
		want := fmt.Sprintf("(truncated, %d chars total)", palPromptMaxLen+10)
		if !strings.Contains(got, want) {
			t.Errorf("missing truncation suffix for multibyte input, got: %q", got[:100])
		}
		// The truncated content must be valid UTF-8 with exactly palPromptMaxLen runes before the suffix.
		lines := strings.SplitN(got, "\n", 3)
		// lines[0] = "--- PAL: thinkdeep ---", lines[1] = truncated content + suffix
		contentLine := lines[1]
		runeCount := 0
		for _, r := range contentLine {
			if r == '\U0001F600' {
				runeCount++
			}
		}
		if runeCount != palPromptMaxLen {
			t.Errorf("expected %d emoji runes in truncated output, got %d", palPromptMaxLen, runeCount)
		}
	})
}

func TestPALExtractor_MalformedInput(t *testing.T) {
	ext, ok := DefaultRegistry.Lookup("mcp__pal__chat")
	if !ok {
		t.Fatal("mcp__pal__chat not in registry")
	}

	tests := []struct {
		name  string
		input json.RawMessage
	}{
		{"invalid json", json.RawMessage(`{not json}`)},
		{"empty prompt", mustJSON(t, map[string]any{"prompt": ""})},
		{"missing prompt field", mustJSON(t, map[string]any{"model": "gpt-4"})},
		{"null input", json.RawMessage(`null`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ext.Extract(tt.input)
			if got != "" {
				t.Errorf("expected empty string, got %q", got)
			}
		})
	}
}

func TestReadExtractor(t *testing.T) {
	ext, ok := DefaultRegistry.Lookup("Read")
	if !ok {
		t.Fatal("Read not in registry")
	}
	if ext.Action != ActionEnrich {
		t.Errorf("Action = %d, want ActionEnrich", ext.Action)
	}

	input := mustJSON(t, map[string]any{"file_path": "/internal/session/parse.go"})
	got := ext.Extract(input)
	if got != "[Read: /internal/session/parse.go]" {
		t.Errorf("got %q", got)
	}
}

func TestWriteExtractor(t *testing.T) {
	ext, ok := DefaultRegistry.Lookup("Write")
	if !ok {
		t.Fatal("Write not in registry")
	}
	if ext.Action != ActionEnrich {
		t.Errorf("Action = %d, want ActionEnrich", ext.Action)
	}

	input := mustJSON(t, map[string]any{"file_path": "/config.toml"})
	got := ext.Extract(input)
	if got != "[Write: /config.toml]" {
		t.Errorf("got %q", got)
	}
}

func TestEditExtractor(t *testing.T) {
	ext, ok := DefaultRegistry.Lookup("Edit")
	if !ok {
		t.Fatal("Edit not in registry")
	}

	input := mustJSON(t, map[string]any{"file_path": "/types.go"})
	got := ext.Extract(input)
	if got != "[Edit: /types.go]" {
		t.Errorf("got %q", got)
	}
}

func TestGrepExtractor(t *testing.T) {
	ext, ok := DefaultRegistry.Lookup("Grep")
	if !ok {
		t.Fatal("Grep not in registry")
	}
	if ext.Action != ActionEnrich {
		t.Errorf("Action = %d, want ActionEnrich", ext.Action)
	}

	input := mustJSON(t, map[string]any{"pattern": "func.*Extract"})
	got := ext.Extract(input)
	if got != "[Grep: func.*Extract]" {
		t.Errorf("got %q", got)
	}
}

func TestAgentExtractor(t *testing.T) {
	ext, ok := DefaultRegistry.Lookup("Agent")
	if !ok {
		t.Fatal("Agent not in registry")
	}
	if ext.Action != ActionEnrich {
		t.Errorf("Action = %d, want ActionEnrich", ext.Action)
	}

	t.Run("full input", func(t *testing.T) {
		input := mustJSON(t, map[string]any{
			"description":   "Search API endpoints",
			"subagent_type": "Explore",
		})
		got := ext.Extract(input)
		if got != `[Agent: Explore — "Search API endpoints"]` {
			t.Errorf("got %q", got)
		}
	})

	t.Run("missing subagent_type", func(t *testing.T) {
		input := mustJSON(t, map[string]any{"description": "do stuff"})
		got := ext.Extract(input)
		if got != `[Agent: Agent — "do stuff"]` {
			t.Errorf("got %q", got)
		}
	})

	t.Run("missing description", func(t *testing.T) {
		input := mustJSON(t, map[string]any{"subagent_type": "Plan"})
		got := ext.Extract(input)
		if got != `[Agent: Plan — "subagent task"]` {
			t.Errorf("got %q", got)
		}
	})
}

func TestEnrichExtractors_MalformedInput(t *testing.T) {
	tools := []string{"Read", "Write", "Edit", "Grep", "Agent"}
	for _, name := range tools {
		ext, ok := DefaultRegistry.Lookup(name)
		if !ok {
			t.Fatalf("%s not in registry", name)
		}

		t.Run(name+"/invalid json", func(t *testing.T) {
			got := ext.Extract(json.RawMessage(`{bad}`))
			if got != "" {
				t.Errorf("expected empty, got %q", got)
			}
		})

		t.Run(name+"/empty fields", func(t *testing.T) {
			got := ext.Extract(mustJSON(t, map[string]any{}))
			if name == "Agent" {
				// Agent has defaults for both fields, so empty fields still produce output.
				if got != `[Agent: Agent — "subagent task"]` {
					t.Errorf("expected default agent output, got %q", got)
				}
			} else {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
			}
		})
	}
}

func TestRegistryLookup_ExactMatch(t *testing.T) {
	_, ok := DefaultRegistry.Lookup("Read")
	if !ok {
		t.Error("Read should be found")
	}
}

func TestRegistryLookup_UnknownTool(t *testing.T) {
	unknowns := []string{
		"mcp__capy__capy_search",
		"mcp__capy__capy_execute",
		"Bash",
		"mcp__serena__find_symbol",
		"mcp__claude_ai_Slack__authenticate",
		"mcp__pal__listmodels",
		"mcp__pal__version",
		"UnknownTool",
	}
	for _, name := range unknowns {
		_, ok := DefaultRegistry.Lookup(name)
		if ok {
			t.Errorf("%s should NOT be in registry", name)
		}
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
