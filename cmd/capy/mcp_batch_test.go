package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Legacy parallel contract remains until Task 13. Serial transport has separate captured fixtures.
func TestMCPStdioLegacyParallelBatchControls(t *testing.T) {
	bin := buildStdioCapy(t)
	project := stdioProject(t)
	const key = "synthetic-stdio-batch-key-123456789"
	command := stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project},
		dir: t.TempDir(), env: stdioEnv(t, key), secrets: []string{key}}
	p := startStdioProcess(t, command)
	p.initialize(t)
	var descriptors struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Required   []string                  `json:"required"`
				Properties map[string]map[string]any `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, p.request("tools/list", map[string]any{}, &descriptors))
	found := false
	for _, tool := range descriptors.Tools {
		if tool.Name == "capy_batch_execute" {
			found = true
			assert.NotContains(t, tool.InputSchema.Required, "queries")
			assert.NotContains(t, tool.InputSchema.Required, "query_scope")
			assert.Equal(t, []any{"batch", "global"}, tool.InputSchema.Properties["query_scope"]["enum"])
		}
	}
	require.True(t, found)
	for _, args := range []map[string]any{
		{"queries": nil}, {"queries": `["valid",null]`}, {"query_scope": "vault"},
	} {
		args["commands"] = []any{"printf bad > invalid-spawn"}
		isError, output := stdioBooleanCall(t, p, "capy_batch_execute", args)
		require.True(t, isError, output)
	}
	assert.NoFileExists(t, filepath.Join(project, "invalid-spawn"))
	assert.NoFileExists(t, filepath.Join(project, "knowledge.db"))
	batch := []any{map[string]any{"label": "stdio provenance", "command": "printf batchmarker # producingcommand"}}
	output := p.tool(t, "capy_batch_execute", map[string]any{"commands": batch, "concurrency": 2})
	assert.Contains(t, output, "Searched 0 queries")
	assert.Contains(t, output, `source: "batch:stdio provenance"`)
	p.tool(t, "capy_index", map[string]any{"source": "earlier durable", "content": "# shared\n\nbatchmarker prior evidence"})
	output = p.tool(t, "capy_batch_execute", map[string]any{"commands": batch, "concurrency": 2, "queries": `["batchmarker"]`, "query_scope": "global"})
	assert.Contains(t, output, "Unchanged; retained 1 sections")
	assert.Contains(t, output, "Source: earlier durable")
	assert.Contains(t, output, "Source: batch:stdio provenance")
	// Measure the actual result JSON read from stdio, excluding the RPC envelope.
	require.NoError(t, os.WriteFile(filepath.Join(project, "escaped.md"), []byte("# escaped\n\nneedle "+strings.Repeat("<&\\\"界", 10000)), 0o600))
	var raw json.RawMessage
	queries := make([]string, 100)
	for i := range queries {
		queries[i] = "needle"
	}
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_batch_execute", "arguments": map[string]any{
		"commands": []any{"cat escaped.md"}, "concurrency": 2, "queries": queries,
	}}, &raw))
	assert.LessOrEqual(t, len(raw), 81920)
	assert.Contains(t, string(raw), "Query outputs omitted:")
	p.close(t)
	stdioCheckpointed(t, project)
	p = startStdioProcess(t, command)
	p.initialize(t)
	output = p.tool(t, "capy_search", map[string]any{"queries": []any{"producingcommand"}, "source": "batch:stdio provenance", "include_kinds": []any{"ephemeral"}})
	assert.Contains(t, output, "Command:")
	assert.Contains(t, output, "producingcommand")
	p.close(t)
}
