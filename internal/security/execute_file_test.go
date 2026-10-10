package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveExecuteFileAdmission(t *testing.T) {
	ctx := filePolicyFixture(t)
	project, home := ctx.ProjectDir, ctx.HomeDir
	sibling := project + "-sibling"
	for _, dir := range []string{sibling, home + "/child"} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
	for _, dir := range []string{project, home, sibling, ctx.WorkingDir} {
		require.NoError(t, os.WriteFile(dir+"/doc.txt", []byte(dir), 0o600))
	}
	require.NoError(t, os.Symlink(home, project+"/alias"))
	require.NoError(t, os.Symlink(home+"/child", project+"/link"))
	require.NoError(t, os.Symlink(project+"/doc.txt", project+"/local.txt"))
	require.NoError(t, os.Symlink(project, home+"/inward"))
	rootGrant := "Read(/" + filepath.Dir(project) + "/**)"
	for _, tc := range []struct {
		name, path, want, deny, reason string
		allows                         []string
	}{
		{name: "relative uses selected project", path: "doc.txt", want: project + "/doc.txt"},
		{name: "absolute local", path: project + "/doc.txt", want: project + "/doc.txt"},
		{name: "internal symlink", path: "local.txt", want: project + "/doc.txt"},
		{name: "absolute external", path: home + "/doc.txt", reason: "outside the selected project"},
		{name: "traversal", path: "../home/doc.txt", reason: "outside the selected project"},
		{name: "sibling prefix", path: sibling + "/doc.txt", reason: "outside the selected project"},
		{name: "symlink escape", path: "alias/doc.txt", reason: "outside the selected project"},
		{name: "symlink before parent", path: "link/../doc.txt", reason: "outside the selected project"},
		{name: "external spelling to internal target", path: home + "/inward/doc.txt", reason: "outside the selected project"},
		{name: "root grant", path: home + "/doc.txt", allows: []string{"Read(/" + home + "/**)"}, want: home + "/doc.txt"},
		{name: "home grant", path: "../home/doc.txt", allows: []string{"Read(~/doc.txt)"}, want: home + "/doc.txt"},
		{name: "bare grant", path: home + "/doc.txt", allows: []string{"Read"}, want: home + "/doc.txt"},
		{name: "legacy allow is not a grant", path: home + "/doc.txt", allows: []string{"Read(" + home + "/**)"}, reason: "outside the selected project"},
		{name: "alias alone cannot grant", path: "alias/doc.txt", allows: []string{"Read(/alias/**)"}, reason: "outside the selected project"},
		{name: "target alone cannot grant alias", path: "alias/doc.txt", allows: []string{"Read(~/doc.txt)"}, reason: "outside the selected project"},
		{name: "separate partial grants cannot combine", path: "alias/doc.txt", allows: []string{"Read(/alias/**)", "Read(~/doc.txt)"}, reason: "outside the selected project"},
		{name: "grant covers alias and target", path: "alias/doc.txt", allows: []string{rootGrant}, want: home + "/doc.txt"},
		{name: "grant preserves symlink before parent", path: "link/../doc.txt", allows: []string{rootGrant}, want: home + "/doc.txt"},
		{name: "deny wins external", path: home + "/doc.txt", allows: []string{"Read"}, deny: "Read(~/doc.txt)", reason: "Read deny pattern"},
		{name: "deny wins local", path: "doc.txt", allows: []string{"Read"}, deny: "Read(/doc.txt)", reason: "Read deny pattern"},
		{name: "legacy deny wins", path: home + "/doc.txt", allows: []string{"Read"}, deny: "Read(" + home + "/**)", reason: "Read deny pattern"},
		{name: "physical deny wins", path: "alias/doc.txt", allows: []string{"Read"}, deny: "Read(~/doc.txt)", reason: "Read deny pattern"},
		{name: "lexical deny wins", path: "link/../doc.txt", allows: []string{"Read"}, deny: "Read(/doc.txt)", reason: "Read deny pattern"},
		{name: "raw deny survives cleaning", path: project + "/cwd/../doc.txt", deny: "Read(/cwd/**)", reason: "Read deny pattern"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permissions := map[string][]string{}
			if tc.allows != nil {
				permissions["allow"] = tc.allows
			}
			if tc.deny != "" {
				permissions["deny"] = []string{tc.deny}
			}
			data, err := json.Marshal(map[string]any{"permissions": permissions})
			require.NoError(t, err)
			writeSettingsFile(t, project+"/.claude/settings.json", string(data))
			policy, err := LoadReadPolicy(ctx)
			require.NoError(t, err)
			got, err := policy.ResolveExecuteFile(tc.path)
			if tc.reason != "" {
				require.ErrorContains(t, err, tc.reason)
				assert.Empty(t, got)
				if tc.reason == "outside the selected project" {
					assert.Contains(t, err.Error(), "permissions.allow")
					assert.Contains(t, err.Error(), "Read(//absolute/path/**)")
				}
				return
			}
			require.NoError(t, err)
			want, err := filepath.EvalSymlinks(tc.want)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestResolveExecuteFilePolicyOrigins(t *testing.T) {
	for _, origin := range []string{"local", "shared", "user"} {
		t.Run(origin, func(t *testing.T) {
			ctx := filePolicyFixture(t)
			path := ctx.HomeDir + "/doc.txt"
			require.NoError(t, os.WriteFile(path, []byte("external"), 0o600))
			settings := ctx.ProjectDir + "/.claude/settings.json"
			if origin == "local" {
				settings = ctx.ProjectDir + "/.claude/settings.local.json"
			} else if origin == "user" {
				settings = ctx.HomeDir + "/.claude/settings.json"
			}
			writeReadRules(t, settings, "allow", "Read(~/doc.txt)")
			policy, err := LoadReadPolicy(ctx)
			require.NoError(t, err)
			got, err := policy.ResolveExecuteFile(path)
			require.NoError(t, err)
			assert.Equal(t, path, got)
		})
	}
}

func TestResolveExecuteFileSymlinkedProject(t *testing.T) {
	ctx := filePolicyFixture(t)
	realProject := ctx.ProjectDir
	require.NoError(t, os.WriteFile(realProject+"/doc.txt", []byte("doc"), 0o600))
	ctx.ProjectDir += "-alias"
	require.NoError(t, os.Symlink(realProject, ctx.ProjectDir))
	policy, err := LoadReadPolicy(ctx)
	require.NoError(t, err)
	for _, path := range []string{"doc.txt", ctx.ProjectDir + "/doc.txt", realProject + "/doc.txt"} {
		got, err := policy.ResolveExecuteFile(path)
		require.NoError(t, err)
		assert.Equal(t, realProject+"/doc.txt", got)
	}
}

func TestResolveExecuteFileFailures(t *testing.T) {
	ctx := filePolicyFixture(t)
	require.NoError(t, os.Symlink("missing", ctx.ProjectDir+"/dangling"))
	require.NoError(t, os.Symlink("loop", ctx.ProjectDir+"/loop"))
	writeReadRules(t, ctx.ProjectDir+"/.claude/settings.json", "allow", "Read")
	policy, err := LoadReadPolicy(ctx)
	require.NoError(t, err)
	for _, path := range []string{"", "bad\x00path", "missing", "dangling", "loop", "../home/missing"} {
		t.Run(path, func(t *testing.T) {
			got, err := policy.ResolveExecuteFile(path)
			require.Error(t, err)
			assert.Empty(t, got)
			if path == "missing" || path == "dangling" || path == "../home/missing" {
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
	var absent *FilePolicy
	_, err = absent.ResolveExecuteFile("doc.txt")
	require.ErrorContains(t, err, "unavailable")
	// A valid snapshot must not disable containment if its project disappears.
	require.NoError(t, os.Rename(ctx.ProjectDir, ctx.ProjectDir+"-moved"))
	_, err = policy.ResolveExecuteFile(ctx.HomeDir)
	require.ErrorContains(t, err, "resolve execute-file project")
	require.NoError(t, os.WriteFile(ctx.ProjectDir, []byte("not a directory"), 0o600))
	_, err = policy.ResolveExecuteFile(ctx.HomeDir)
	require.ErrorContains(t, err, "not a directory")
}
