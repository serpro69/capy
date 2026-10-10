package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func filePolicyFixture(t *testing.T) FilePolicyContext {
	t.Helper()
	root := t.TempDir()
	ctx := FilePolicyContext{
		ProjectDir: filepath.Join(root, "project"),
		WorkingDir: filepath.Join(root, "project", "cwd"),
		HomeDir:    filepath.Join(root, "home"),
	}
	for _, path := range []string{ctx.ProjectDir, ctx.WorkingDir, ctx.HomeDir} {
		require.NoError(t, os.MkdirAll(path, 0o755))
	}
	return ctx
}

func writeReadRules(t *testing.T, path, action string, patterns ...string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"permissions": map[string]any{action: patterns}})
	require.NoError(t, err)
	writeSettingsFile(t, path, string(data))
}

func TestReadPolicyAnchors(t *testing.T) {
	ctx := filePolicyFixture(t)
	local := filepath.Join(ctx.ProjectDir, ".claude", "settings.local.json")
	shared := filepath.Join(ctx.ProjectDir, ".claude", "settings.json")
	user := filepath.Join(ctx.HomeDir, ".claude", "settings.json")
	tests := []struct {
		name, settings, pattern, denied, allowed string
	}{
		{"root", local, "Read(/" + ctx.HomeDir + "/**)", ctx.HomeDir + "/secret", ctx.ProjectDir + "/secret"},
		{"home", local, "Read(~/secret)", ctx.HomeDir + "/secret", ctx.WorkingDir + "/secret"},
		{"local source", local, "Read(/secret)", ctx.ProjectDir + "/secret", ctx.WorkingDir + "/secret"},
		{"shared source", shared, "Read(/secret)", ctx.ProjectDir + "/secret", ctx.ProjectDir + "/.claude/secret"},
		{"user source", user, "Read(/secret)", ctx.HomeDir + "/.claude/secret", ctx.ProjectDir + "/secret"},
		{"cwd", user, "Read(docs/**)", ctx.WorkingDir + "/docs/a/b", ctx.ProjectDir + "/docs/a/b"},
		{"dot cwd", local, "Read(./secret)", ctx.WorkingDir + "/secret", ctx.WorkingDir + "/nested/secret"},
		{"basename nested", local, "Read(*.env)", ctx.WorkingDir + "/a/b/test.env", ctx.ProjectDir + "/test.env"},
		{"literal basename", local, "Read(.env)", ctx.WorkingDir + "/a/.env", ctx.WorkingDir + "/a/.env.txt"},
		{"anchored basename", local, "Read(./*.env)", ctx.WorkingDir + "/test.env", ctx.WorkingDir + "/a/test.env"},
		{"zero globstar dirs", local, "Read(docs/**/*.txt)", ctx.WorkingDir + "/docs/a.txt", ctx.WorkingDir + "/else/a.txt"},
		{"many globstar dirs", local, "Read(docs/**/*.txt)", ctx.WorkingDir + "/docs/a/b/c.txt", ctx.WorkingDir + "/docs/a/b/c.md"},
		{"question", local, "Read(./?.txt)", ctx.WorkingDir + "/é.txt", ctx.WorkingDir + "/ab.txt"},
		{"escaped star", local, `Read(./literal\*.txt)`, ctx.WorkingDir + "/literal*.txt", ctx.WorkingDir + "/literalZZ.txt"},
		{"escaped brackets", local, `Read(./\[a\].txt)`, ctx.WorkingDir + "/[a].txt", ctx.WorkingDir + "/a.txt"},
		{"escaped question basename", local, `Read(literal\?)`, ctx.WorkingDir + "/nested/literal?", ctx.WorkingDir + "/literalX"},
		{"newline wildcard", local, "Read(docs/**)", ctx.WorkingDir + "/docs/a\nb", ctx.WorkingDir + "/docs-sibling/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeReadRules(t, tt.settings, "deny", tt.pattern)
			t.Cleanup(func() { require.NoError(t, os.Remove(tt.settings)) })
			p, err := LoadReadPolicy(ctx)
			require.NoError(t, err)
			for _, path := range []string{tt.denied, relativePolicyPath(t, ctx.WorkingDir, tt.denied)} {
				err := p.Check(path)
				require.ErrorContains(t, err, "Read deny pattern")
				assert.Contains(t, err.Error(), tt.settings)
			}
			assert.NoError(t, p.Check(tt.allowed))
		})
	}
}

func relativePolicyPath(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	require.NoError(t, err)
	return rel
}

func TestReadPolicyBareAndPrecedence(t *testing.T) {
	ctx := filePolicyFixture(t)
	writeReadRules(t, filepath.Join(ctx.ProjectDir, ".claude", "settings.local.json"), "allow", "Read")
	writeReadRules(t, filepath.Join(ctx.HomeDir, ".claude", "settings.json"), "deny", "Read")
	p, err := LoadReadPolicy(ctx)
	require.NoError(t, err)
	require.ErrorContains(t, p.Check("anywhere"), `"Read"`)
	allowed, err := p.Allows("anywhere")
	require.Error(t, err)
	assert.False(t, allowed)
}

func TestReadPolicyInvalidSettings(t *testing.T) {
	for _, data := range []string{
		`{broken`, `null`, `[]`, `{"permissions":null}`, `{"permissions":[]}`,
		`{"permissions":{"deny":null}}`, `{"permissions":{"allow":"Read"}}`,
		`{"permissions":{"deny":[3]}}`, `{"permissions":{"ask":[null]}}`,
	} {
		t.Run(data, func(t *testing.T) {
			ctx := filePolicyFixture(t)
			path := filepath.Join(ctx.ProjectDir, ".claude", "settings.json")
			writeSettingsFile(t, path, data)
			p, err := LoadReadPolicy(ctx)
			require.ErrorContains(t, err, "invalid policy settings")
			assert.Contains(t, err.Error(), path)
			assert.Nil(t, p)
		})
	}
	for _, pattern := range []string{
		"Read()", "Read(foo", "Read([abc])", "Read(!secret)", "Read({a,b})",
		`Read(foo\)`, "Read(~someone/x)", "Read(@(a|b))", "Read(a\x00b)", "Read(*/../secret)",
	} {
		t.Run(pattern, func(t *testing.T) {
			ctx := filePolicyFixture(t)
			writeReadRules(t, filepath.Join(ctx.ProjectDir, ".claude", "settings.json"), "allow", pattern)
			p, err := LoadReadPolicy(ctx)
			require.ErrorContains(t, err, "invalid Read policy")
			assert.Nil(t, p)
		})
	}
}

func TestReadPolicySettingsFailures(t *testing.T) {
	ctx := filePolicyFixture(t)
	p, err := LoadReadPolicy(ctx)
	require.NoError(t, err) // Missing settings are normal.
	assert.NoError(t, p.Check("missing.txt"))
	settings := filepath.Join(ctx.ProjectDir, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o755))
	require.NoError(t, os.Symlink(settings+"-missing", settings))
	_, err = LoadReadPolicy(ctx)
	require.ErrorContains(t, err, "read policy settings")
	require.NoError(t, os.Remove(settings))
	require.NoError(t, os.Mkdir(settings, 0o755))
	_, err = LoadReadPolicy(ctx)
	require.ErrorContains(t, err, "read policy settings")
	ctx.ProjectDir += "-missing"
	_, err = LoadReadPolicy(ctx)
	require.ErrorContains(t, err, "resolve policy directory")
}

func TestReadPolicyLegacyAndSymlinks(t *testing.T) {
	ctx := filePolicyFixture(t)
	ctx.WorkingDir = ctx.ProjectDir
	external := filepath.Join(ctx.HomeDir, "external")
	require.NoError(t, os.MkdirAll(external+"/child", 0o755))
	target := external + "/secret.txt"
	require.NoError(t, os.WriteFile(target, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(external+"/child", ctx.ProjectDir+"/link"))
	require.NoError(t, os.Symlink(external, ctx.ProjectDir+"/alias"))
	settings := filepath.Join(ctx.ProjectDir, ".claude", "settings.json")
	for _, pattern := range []string{"Read(" + external + "/**)", "Read(/" + external + "/**)", "Read(alias/**)"} {
		writeReadRules(t, settings, "deny", pattern)
		p, err := LoadReadPolicy(ctx)
		require.NoError(t, err)
		for _, path := range []string{target, "alias/secret.txt", "link/../secret.txt", ctx.ProjectDir + "/link/../secret.txt"} {
			require.ErrorContains(t, p.Check(path), "Read deny pattern", "%s: %s", pattern, path)
		}
		if strings.HasPrefix(pattern, "Read(//") || pattern == "Read(alias/**)" {
			assert.Empty(t, p.Warnings())
		} else {
			require.Len(t, p.Warnings(), 1)
			assert.Contains(t, p.Warnings()[0], "legacy absolute")
			// The host-anchored interpretation remains denied too.
			require.Error(t, p.Check(ctx.ProjectDir+target))
		}
	}
	for _, pattern := range []string{"Read(alias/**)", "Read(" + external + "/**)", "Read(/" + external + "/**)"} {
		writeReadRules(t, settings, "allow", pattern)
		p, err := LoadReadPolicy(ctx)
		require.NoError(t, err)
		assert.Empty(t, p.Warnings())
		allowed, err := p.Allows("alias/secret.txt")
		require.NoError(t, err)
		assert.False(t, allowed, "an alias-only grant cannot cover the physical target")
		allowed, err = p.Allows(target)
		require.NoError(t, err)
		assert.Equal(t, strings.HasPrefix(pattern, "Read(//"), allowed, "legacy interpretation must never broaden an allow")
	}
	writeReadRules(t, settings, "allow", "Read(~/external/**)")
	p, err := LoadReadPolicy(ctx)
	require.NoError(t, err)
	allowed, err := p.Allows(target)
	require.NoError(t, err)
	assert.True(t, allowed)
	allowed, err = p.Allows(external + "/missing")
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestReadPolicySymlinkedProject(t *testing.T) {
	ctx := filePolicyFixture(t)
	realProject := ctx.ProjectDir
	alias := realProject + "-alias"
	require.NoError(t, os.Symlink(realProject, alias))
	require.NoError(t, os.WriteFile(realProject+"/doc.txt", []byte("doc"), 0o600))
	ctx.ProjectDir, ctx.WorkingDir = alias, alias
	writeReadRules(t, realProject+"/.claude/settings.json", "allow", "Read(./doc.txt)")
	p, err := LoadReadPolicy(ctx)
	require.NoError(t, err)
	allowed, err := p.Allows("doc.txt")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestReadPolicySnapshotAndDiagnostics(t *testing.T) {
	ctx := filePolicyFixture(t)
	settings := ctx.ProjectDir + "/.claude/settings.json"
	writeReadRules(t, settings, "deny", "Read(/private/**)", "Read(/private/**)")
	p, err := LoadReadPolicy(ctx)
	require.NoError(t, err)
	require.Len(t, p.Warnings(), 1)
	warnings := p.Warnings()
	warnings[0] = "modified by caller"
	assert.NotEqual(t, warnings, p.Warnings(), "snapshot diagnostics must not be mutable")
	// Preserve the legacy raw-spelling deny even when cleaning escapes it.
	require.Error(t, p.Check("/private/../public.txt"))
	writeSettingsFile(t, settings, `{}`)
	require.Error(t, p.Check(ctx.ProjectDir+"/private/doc"), "a captured policy remains enforced until reload")
	reloaded, err := LoadReadPolicy(ctx)
	require.NoError(t, err)
	assert.NoError(t, reloaded.Check(ctx.ProjectDir+"/private/doc"))
}

func TestReadPolicyGrantAndResolutionErrors(t *testing.T) {
	ctx := filePolicyFixture(t)
	target := ctx.WorkingDir + "/doc"
	require.NoError(t, os.WriteFile(target, []byte("doc"), 0o600))
	settings := ctx.ProjectDir + "/.claude/settings.json"
	writeReadRules(t, settings, "ask", "Read")
	p, err := LoadReadPolicy(ctx)
	require.NoError(t, err)
	allowed, err := p.Allows(target)
	require.NoError(t, err)
	assert.False(t, allowed, "ask is not an external grant")
	writeReadRules(t, settings, "allow", "Read")
	p, err = LoadReadPolicy(ctx)
	require.NoError(t, err)
	allowed, err = p.Allows(target)
	require.NoError(t, err)
	assert.True(t, allowed)
	require.NoError(t, os.Symlink("loop", ctx.WorkingDir+"/loop"))
	require.ErrorContains(t, p.Check("loop"), "resolve file path")
	require.Error(t, p.Check("bad\x00path"))
	var absent *FilePolicy
	require.ErrorContains(t, absent.Check(target), "unavailable")
}

func TestReadPolicyLegacyMatcherCompatibility(t *testing.T) {
	for _, tt := range []struct{ name, pattern, path string }{
		{"embedded globstar", "/private**/secret", "/privatesecret"},
		{"backslash spelling", "/private/**", `\private\secret`},
		{"raw traversal", "/private/**", "/private/../public"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := filePolicyFixture(t)
			writeReadRules(t, ctx.ProjectDir+"/.claude/settings.json", "deny", "Read("+tt.pattern+")")
			denied, _ := EvaluateFilePath(tt.path, [][]string{{tt.pattern}}, ctx.ProjectDir)
			require.True(t, denied, "fixture must establish the legacy denial")
			p, err := LoadReadPolicy(ctx)
			require.NoError(t, err)
			require.ErrorContains(t, p.Check(tt.path), "Read deny pattern")
		})
	}
}
