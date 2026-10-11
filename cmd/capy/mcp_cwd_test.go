package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPStdioExecutionCwd(t *testing.T) {
	bin := buildStdioCapy(t)
	project := stdioProject(t)
	workDir := filepath.Join(project, "work dir")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, "input.txt"), []byte("selected-input"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "input.txt"), []byte("wrong-cwd-input"), 0o600))
	const key = "synthetic-stdio-cwd-passphrase-12345"
	p := startStdioProcess(t, stdioCommand{
		executable: bin, args: []string{"serve", "--project-dir", project},
		dir: t.TempDir(), env: stdioEnv(t, key), secrets: []string{key},
	})
	p.initialize(t)
	var descriptors struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]map[string]any `json:"properties"`
				Required   []string                  `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, p.request("tools/list", map[string]any{}, &descriptors))
	var advertised int
	for _, tool := range descriptors.Tools {
		switch tool.Name {
		case "capy_execute", "capy_execute_file", "capy_batch_execute":
			advertised++
			require.Equal(t, "string", tool.InputSchema.Properties["cwd"]["type"])
			assert.NotContains(t, tool.InputSchema.Required, "cwd")
		}
	}
	require.Equal(t, 3, advertised)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{tool: "capy_execute", args: map[string]any{"language": "shell", "code": "printf cwdmarker; pwd"}},
		{tool: "capy_execute_file", args: map[string]any{"language": "shell", "path": "input.txt", "code": `printf '%s\n' "$FILE_CONTENT"; printf cwdmarker; pwd`}},
		{tool: "capy_batch_execute", args: map[string]any{
			"commands": []any{map[string]any{"label": "cwd fixture", "command": "printf cwdmarker; pwd"}},
			"queries":  []any{"cwdmarker"},
		}},
	}
	t.Run("invalid before side effects", func(t *testing.T) {
		for _, tc := range cases {
			for _, value := range []any{nil, "", 1, true, []any{"."}, "missing", "input.txt"} {
				args := map[string]any{}
				for k, v := range tc.args {
					args[k] = v
				}
				args["cwd"] = value
				args["code"] = "printf spawned > invalid-cwd-spawn"
				if tc.tool == "capy_batch_execute" {
					args["commands"] = []any{map[string]any{"label": "must not run", "command": "printf spawned > invalid-cwd-spawn"}}
				}
				isError, output := stdioBooleanCall(t, p, tc.tool, args)
				require.True(t, isError, output)
				assert.Contains(t, output, "cwd")
			}
		}
		assert.NoFileExists(t, filepath.Join(project, "invalid-cwd-spawn"))
		assert.NoFileExists(t, filepath.Join(project, "knowledge.db"))
	})
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			tc.args["cwd"] = "work dir"
			output := p.tool(t, tc.tool, tc.args)
			canonical, err := filepath.EvalSymlinks(workDir)
			require.NoError(t, err)
			assert.Contains(t, output, canonical)
			if tc.tool == "capy_execute_file" {
				assert.Contains(t, output, "selected-input")
				assert.NotContains(t, output, "wrong-cwd-input")
			}
		})
	}
	// Explicit absolute external cwd remains allowed and does not persist.
	external := t.TempDir()
	output := p.tool(t, "capy_execute", map[string]any{"language": "shell", "code": "pwd", "cwd": external})
	canonical, err := filepath.EvalSymlinks(external)
	require.NoError(t, err)
	assert.Contains(t, output, canonical)
	output = p.tool(t, "capy_execute", map[string]any{"language": "shell", "code": "pwd"})
	canonical, err = filepath.EvalSymlinks(project)
	require.NoError(t, err)
	assert.Contains(t, output, canonical)
	assert.NoFileExists(t, filepath.Join(workDir, "knowledge.db"))
	assert.NoFileExists(t, filepath.Join(external, "knowledge.db"))
	p.close(t)
	stdioCheckpointed(t, project)
}
