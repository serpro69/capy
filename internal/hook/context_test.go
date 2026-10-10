package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveHookContext(t *testing.T) {
	project := t.TempDir()
	envProject := t.TempDir()
	payloadProject := t.TempDir()
	cwd := filepath.Join(payloadProject, "nested")
	require.NoError(t, os.Mkdir(cwd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(payloadProject, ".capy.toml"), nil, 0o600))
	processDir, err := os.Getwd()
	require.NoError(t, err)
	relativeProject, err := filepath.Rel(processDir, project)
	require.NoError(t, err)
	file := filepath.Join(payloadProject, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	tests := []struct {
		name, explicit, env, cwd, wantProject, wantCwd string
	}{
		{name: "explicit wins", explicit: project, env: envProject, cwd: cwd, wantProject: project, wantCwd: cwd},
		{name: "relative explicit", explicit: relativeProject, env: envProject, cwd: cwd, wantProject: project, wantCwd: cwd},
		{name: "environment wins", env: envProject, cwd: cwd, wantProject: envProject, wantCwd: cwd},
		{name: "relative environment", env: relativeProject, cwd: cwd, wantProject: project, wantCwd: cwd},
		{name: "payload detection", cwd: cwd, wantProject: payloadProject, wantCwd: cwd},
		{name: "missing payload", env: envProject, wantProject: envProject, wantCwd: envProject},
		{name: "relative payload", env: envProject, cwd: "relative", wantProject: envProject, wantCwd: envProject},
		{name: "nonexistent payload", env: envProject, cwd: filepath.Join(project, "absent"), wantProject: envProject, wantCwd: envProject},
		{name: "file payload", env: envProject, cwd: file, wantProject: envProject, wantCwd: envProject},
		{name: "NUL payload", env: envProject, cwd: "/bad\x00cwd", wantProject: envProject, wantCwd: envProject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLAUDE_PROJECT_DIR", tt.env)
			var explicit *string
			if tt.explicit != "" {
				explicit = &tt.explicit
			}
			beforeEnv := os.Environ()
			ctx, err := resolveHookContext(explicit, &adapter.PreToolUseEvent{Cwd: tt.cwd})
			require.NoError(t, err)
			wantProject, err := filepath.EvalSymlinks(tt.wantProject)
			require.NoError(t, err)
			wantCwd, err := filepath.EvalSymlinks(tt.wantCwd)
			require.NoError(t, err)
			assert.Equal(t, wantProject, ctx.projectDir)
			assert.Equal(t, wantCwd, ctx.workingDir)
			afterCwd, err := os.Getwd()
			require.NoError(t, err)
			assert.Equal(t, processDir, afterCwd)
			assert.Equal(t, beforeEnv, os.Environ())
		})
	}
}

func TestHookInvalidSelection(t *testing.T) {
	project := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", project)
	file := filepath.Join(project, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	for _, path := range []string{"", file, filepath.Join(project, "missing"), "/bad\x00dir"} {
		t.Run("path="+path, func(t *testing.T) {
			input := hookInput(t, "Read", nil, map[string]any{"cwd": project})
			for _, event := range []string{"pretooluse", "sessionstart", "sessionend", "posttooluse", "precompact", "userpromptsubmit"} {
				output, err := handleEvent(event, input, ccAdapter(), &path)
				if event == "pretooluse" {
					require.NoError(t, err)
					assert.Equal(t, "deny", ccParse(t, output)["permissionDecision"])
				} else {
					assert.Error(t, err)
					assert.Empty(t, output)
				}
			}
			assert.NoDirExists(t, filepath.Join(project, ".capy"))
		})
	}
	t.Setenv("CLAUDE_PROJECT_DIR", filepath.Join(project, "missing-env"))
	output, err := handleEvent("pretooluse", hookInput(t, "Read", nil, map[string]any{"cwd": project}), ccAdapter(), nil)
	require.NoError(t, err)
	assert.Equal(t, "deny", ccParse(t, output)["permissionDecision"])
}

func TestHookContextSymlinkBeforeParent(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(target, "nested"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(target, "nested"), filepath.Join(root, "alias")))
	path := root + "/alias/.." // filepath.Join would erase the alias before resolution
	ctx, err := resolveHookContext(&path, &adapter.PreToolUseEvent{Cwd: path})
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)
	assert.Equal(t, want, ctx.projectDir)
	assert.Equal(t, want, ctx.workingDir)
}

func TestHookReadPolicyWorkingDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", cwd) // explicit selection must win for settings/state
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0o755))
	settings := filepath.Join(project, ".claude", "settings.json")
	require.NoError(t, os.WriteFile(settings, []byte(`{"permissions":{"deny":["Read(./secret.txt)","Read(/project-secret.txt)"]}}`), 0o600))
	for _, tool := range []string{"capy_index", "capy_execute_file"} {
		for _, tt := range []struct {
			name, path string
			deny       bool
		}{
			{name: "cwd anchored deny", path: filepath.Join(cwd, "secret.txt"), deny: true},
			{name: "settings anchored deny", path: "project-secret.txt", deny: true},
			{name: "project-relative input", path: "secret.txt"},
			{name: "absolute project input", path: filepath.Join(project, "secret.txt")},
		} {
			t.Run(tool+"/"+tt.name, func(t *testing.T) {
				input := hookInput(t, tool, map[string]any{"path": tt.path}, map[string]any{"cwd": cwd, "agent_id": "child"})
				output, err := handleEvent("pretooluse", input, ccAdapter(), &project)
				require.NoError(t, err)
				if tt.deny {
					assert.Equal(t, "deny", ccParse(t, output)["permissionDecision"])
				} else {
					assert.Empty(t, output)
				}
			})
		}
	}
	// Read policy preparation errors must be structured blocks, even for children.
	require.NoError(t, os.WriteFile(settings, []byte(`{broken`), 0o600))
	output, err := handleEvent("pretooluse", hookInput(t, "capy_index", map[string]any{"path": "public.txt"}, map[string]any{"cwd": cwd}), ccAdapter(), &project)
	require.NoError(t, err)
	assert.Equal(t, "deny", ccParse(t, output)["permissionDecision"])

	// Guidance belongs to the selected project, not the payload or environment.
	output, err = handleEvent("pretooluse", hookInput(t, "Read", nil, map[string]any{"cwd": cwd}), ccAdapter(), &project)
	require.NoError(t, err)
	require.NotEmpty(t, output)
	assert.FileExists(t, filepath.Join(project, ".capy", "guidance-context-session.json"))
	assert.NoDirExists(t, filepath.Join(cwd, ".capy"))
}

func hookInput(t *testing.T, tool string, input, metadata map[string]any) []byte {
	t.Helper()
	value := map[string]any{"tool_name": tool, "tool_input": input, "session_id": "context-session"}
	for key, field := range metadata {
		value[key] = field
	}
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
