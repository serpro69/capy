package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchBudgetStdio(t *testing.T) {
	bin := buildStdioCapy(t)
	project := stdioProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[store]\npath = 'knowledge.db'\n[search]\nwindow_ms = 3600000\ntaper_after = 1\nblock_after = 2\n"), 0o600))
	const key = "synthetic-search-budget-stdio-key"
	p := startStdioProcess(t, stdioCommand{
		executable: bin, args: []string{"serve", "--project-dir", project},
		dir: t.TempDir(), env: stdioEnv(t, key), secrets: []string{key},
	})
	p.initialize(t)
	for _, args := range []map[string]any{
		{"query": "budgetmarker", "limit": 0},
		{"queries": []any{"budgetmarker", false}},
		{"query": "budgetmarker", "include_kinds": nil},
		{"query": "budgetmarker", "all_projects": 1},
	} {
		isError, out := stdioBooleanCall(t, p, "capy_search", args)
		assert.True(t, isError, out)
		assert.NotContains(t, out, "search call #")
	}
	_, err := os.Stat(filepath.Join(project, "knowledge.db"))
	require.True(t, os.IsNotExist(err), "invalid calls must not initialize storage")
	for call := 1; call <= 3; call++ {
		isError, out := stdioBooleanCall(t, p, "capy_search", map[string]any{"queries": `["budgetmarker"]`})
		assert.True(t, isError, "empty corpus or blocked result: %s", out)
		assert.Contains(t, out, fmt.Sprintf("search call #%d/2", call))
		assert.Contains(t, out, fmt.Sprintf("Results limited to %d/query", 3-call))
		assert.Contains(t, out, "window 3600000 ms; taper after 1; block after 2")
		if call < 3 {
			assert.Contains(t, out, "knowledge base is empty")
		} else {
			assert.Contains(t, out, "BLOCKED")
		}
	}
	p.close(t)
}
