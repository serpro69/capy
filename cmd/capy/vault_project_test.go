package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVaultProject_Command(t *testing.T) {
	root, uuid := setupVaultEnv(t)
	const uuid2 = "abcd1234-bbbb-cccc-dddd-1234567890ab"
	raw, err := os.ReadFile(filepath.Join(root, uuid+".jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, uuid2+".jsonl"), raw, 0o644))
	imported, stderr, code := capy(t, "vault", "import", "--source", root)
	require.Equal(t, 0, code, stderr)
	require.Contains(t, imported, "imported 2")
	target := uuid[:10]
	before, stderr, code := capy(t, "vault", "show", target, "--format", "json")
	require.Equal(t, 0, code, stderr)
	_, stderr, code = capy(t, "vault", "rename", target, "Independent title")
	require.Equal(t, 0, code, stderr)

	readSession := func(id string) *vault.Session {
		t.Helper()
		st := vault.NewVaultStore(os.Getenv("CAPY_VAULT_PATH"))
		defer st.Close()
		got, err := st.GetSession(t.Context(), id)
		require.NoError(t, err)
		return got
	}
	original := readSession(uuid)
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing id", want: "accepts between 1 and 2"},
		{name: "missing name", args: []string{target}, want: "provide a project name"},
		{name: "both", args: []string{target, "label", "--clear"}, want: "mutually exclusive"},
		{name: "empty with clear", args: []string{target, "", "--clear"}, want: "mutually exclusive"},
		{name: "too many", args: []string{target, "one", "two"}, want: "accepts between 1 and 2"},
		{name: "empty", args: []string{target, " \t "}, want: "must not be empty"},
		{name: "control", args: []string{target, "a\nb"}, want: "control characters"},
		{name: "too long", args: []string{target, strings.Repeat("界", 121)}, want: "120 characters"},
		{name: "ambiguous", args: []string{uuid[:8], "label"}, want: "ambiguous"},
		{name: "not found", args: []string{"eeeeeeee", "label"}, want: "no session matches"},
		{name: "short prefix", args: []string{"short", "label"}, want: "at least 8"},
		{name: "wildcard", args: []string{"%%%%%%%%", "label"}, want: "no session matches"},
		{name: "tui", args: []string{target, "label", "--tui"}, want: "not supported"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, code := capy(t, append([]string{"vault", "project"}, tt.args...)...)
			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr, tt.want)
		})
	}
	assert.Nil(t, readSession(uuid).ProjectOverride)
	for _, label := range []string{"Café project", "~/literal/../replacement"} {
		stdout, stderr, code := capy(t, "vault", "project", target, "  "+label+"  ")
		require.Equal(t, 0, code, stderr)
		assert.Contains(t, stdout, `set project for abcd1234 to "`+label+`"`)
		got := readSession(uuid)
		assert.Equal(t, label, got.EffectiveProject())
		assert.Equal(t, original.ProjectPath, got.ProjectPath)
		assert.Equal(t, original.Name, got.Name)
		assert.Nil(t, readSession(uuid2).ProjectOverride, "edits target only one UUID")
		after, stderr, code := capy(t, "vault", "show", target, "--format", "json")
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, before, after, "raw show remains byte-identical during the override")
	}
	stdout, stderr, code := capy(t, "vault", "project", target, "--clear")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `cleared custom project for abcd1234 — project is now "/home/user/proj"`)
	got := readSession(uuid)
	require.NotNil(t, got.ProjectOverride)
	assert.Nil(t, got.ProjectOverride.CustomProject)
	got.ProjectOverride = nil
	assert.Equal(t, original, got)
	after, stderr, code := capy(t, "vault", "show", target, "--format", "json")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, before, after)
}

func TestVaultProject_ValidationDoesNotCreateVault(t *testing.T) {
	setupVaultEnv(t)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "missing name", args: []string{"aaaaaaaa"}},
		{name: "both", args: []string{"aaaaaaaa", "label", "--clear"}},
		{name: "invalid label", args: []string{"aaaaaaaa", "bad\nlabel"}},
		{name: "invalid prefix", args: []string{"short", "label"}},
		{name: "tui", args: []string{"aaaaaaaa", "label", "--tui"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "must-not-exist.db")
			args := append([]string{"vault", "project", "--path", path}, tt.args...)
			_, stderr, code := capy(t, args...)
			require.NotEqual(t, 0, code, stderr)
			_, err := os.Stat(path)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestVaultProject_Browse(t *testing.T) {
	root, uuid := setupVaultEnv(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	path := filepath.Join(home, "vp")
	label := filepath.Join(home, "label")
	fixture := filepath.Join(root, uuid+".jsonl")
	raw, err := os.ReadFile(fixture)
	require.NoError(t, err)
	raw = []byte(strings.ReplaceAll(string(raw), "/home/user/proj", path))
	require.NoError(t, os.WriteFile(fixture, raw, 0o644))
	run := func(args ...string) string {
		t.Helper()
		stdout, stderr, code := capy(t, append([]string{"vault"}, args...)...)
		require.Equal(t, 0, code, stderr)
		return stdout
	}
	run("import", "--source", root)
	listJSON := func() map[string]any {
		t.Helper()
		var rows []map[string]any
		require.NoError(t, json.Unmarshal([]byte(run("list", "--json")), &rows))
		require.Len(t, rows, 1)
		return rows[0]
	}
	baseline := listJSON()
	assert.Equal(t, map[string]any{
		"uuid": uuid, "title": "Fix the brontosaurus timeout", "project_path": path, "project": path,
		"git_branch": "main", "start_time": "2026-05-01T10:00:00Z", "end_time": "2026-05-01T10:00:05Z",
		"message_count": float64(2), "size_bytes": float64(len(raw)), "platform": "claude-code",
	}, baseline)
	for _, tt := range []struct {
		name    string
		args    []string
		project string
		display string
	}{
		{name: "unset", project: path, display: "~/vp"},
		{name: "override", args: []string{label}, project: label, display: label},
		{name: "equal to raw path", args: []string{path}, project: path, display: path},
		{name: "clear", args: []string{"--clear"}, project: path, display: "~/vp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.args != nil {
				run(append([]string{"project", uuid}, tt.args...)...)
			}
			row := listJSON()
			assert.Equal(t, tt.project, row["project"])
			row["project"] = path
			assert.Equal(t, baseline, row, "every pre-existing field remains unchanged")
			assert.Equal(t, string(raw), run("show", uuid, "--format", "json"))
			assert.Contains(t, run("list"), truncate(tt.display, 28))
			assert.Contains(t, run("list", "--project", tt.project, "--limit", "1"), uuid[:8])
			text := run("show", uuid)
			markdown := run("show", uuid, "--format", "markdown")
			assert.Contains(t, text, "project: "+tt.display+"  branch:")
			assert.Contains(t, markdown, "- **Project:** "+tt.display+"\n")
			_, preview, code := capy(t, "vault", "delete", uuid)
			require.Equal(t, 0, code, preview)
			assert.Contains(t, preview, "Project:  "+tt.display+"\n")
			if tt.project != path {
				assert.Contains(t, text, "original path: ~/vp\n")
				assert.Contains(t, markdown, "- **Original path:** ~/vp\n")
				assert.Contains(t, preview, "Original path: ~/vp\n")
				assert.Contains(t, run("list", "--project", path), "no sessions archived")
			} else {
				assert.NotContains(t, text, "original path:")
				assert.NotContains(t, markdown, "Original path:")
				assert.NotContains(t, preview, "Original path:")
			}
		})
	}
}

func TestVaultProject_ChildAndAmbiguityDisplays(t *testing.T) {
	home := setupCodexVaultEnv(t)
	writeCodexChild(t, home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, stderr, code := capy(t, "vault", "import")
	require.Equal(t, 0, code, stderr)
	_, stderr, code = capy(t, "vault", "project", codexChildUUID, "child assignment")
	require.Equal(t, 0, code, stderr)
	stdout, stderr, code := capy(t, "vault", "list", "--project", "child assignment", "--include-children")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "child assignment")
	assert.Contains(t, stdout, "↳ "+shortUUID(codexParentUUID, vault.PlatformCodex))
	assert.Contains(t, stdout, shortUUID(codexChildUUID, vault.PlatformCodex))
	stdout, stderr, code = capy(t, "vault", "list", "--project", "child assignment")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "no sessions archived")
	_, stderr, code = capy(t, "vault", "show", codexChildUUID[:8])
	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "child assignment")
	assert.Contains(t, stderr, "ambiguous session id")
}
