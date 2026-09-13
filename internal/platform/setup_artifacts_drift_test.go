package platform

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests guard against a recurring class of bug: a fix applied to a file
// that `capy setup` generates — the wrapper scripts, routing instructions, MCP
// config, hooks, .gitignore, etc. — but NOT to the generator in internal/platform
// (or vice versa). When they drift, THIS repo works, but every consumer that
// re-runs `capy setup` gets the stale, unfixed artifact. This has bitten us more
// than once (PR #93 wrapper fix; commit 8d5f2a2 routing wording).
//
// The contract: every file `capy setup` writes and this repo commits MUST be
// reproducible from its generator. Three tests enforce it together:
//
//   - TestGeneratedWholeFileArtifacts     — files written verbatim must match byte-for-byte.
//   - TestMergedArtifactsAreIdempotent    — files merged into must not change when setup re-runs.
//   - TestDriftGuardCoversEverySetupArtifact — every file setup writes is registered above,
//                                              so a NEW artifact can't be added without a guard.
//
// If one fails, apply the SAME change to BOTH the generator and the committed
// file. Never "fix" a failure by editing just one side.
//
// Adding a new file to `capy setup`? Register it in one of the three lists below
// (wholeFileArtifacts / jsonMergeArtifacts / textMergeArtifacts) or, if it has no
// committed counterpart to guard, in exemptSetupPaths with a reason — otherwise
// TestDriftGuardCoversEverySetupArtifact fails.

// repoRootFromPkg is the path from internal/platform back to the repo root, where
// this repo's own committed copies of every capy-setup-generated artifact live.
const repoRootFromPkg = "../.."

// wholeFileArtifacts are files `capy setup` writes verbatim from a generator; the
// committed copy must be byte-identical to what the generator produces.
var wholeFileArtifacts = []struct {
	path    string // repo-relative path to the committed artifact
	want    string // generator output
	genName string // generator symbol, named in the failure message
}{
	{capyWrapperRelPath, capyWrapperScript, "capyWrapperScript"},
	{codexWrapperRelPath, capyWrapperScript, "capyWrapperScript"},
	{agentsRelPath, GenerateRoutingInstructions(), "GenerateRoutingInstructions()"},
}

// mergeArtifact is a file `capy setup` merges its entry into rather than
// overwriting. Re-running merge against the committed file must be a no-op.
type mergeArtifact struct {
	path    string           // repo-relative path to the committed artifact
	merge   func(string) error // the setup function that upserts capy's entry
	genName string           // merge-function symbol, named in the failure message
}

// jsonMergeArtifacts are compared as parsed maps, so key ordering / whitespace
// never causes a false positive — only a semantic change in the capy entry fails.
var jsonMergeArtifacts = []mergeArtifact{
	{".mcp.json", mergeMCPServer, "mergeMCPServer"},
	{".claude/settings.local.json", mergeHooks, "mergeHooks"},
}

// textMergeArtifacts preserve surrounding formatting and are a no-op when the
// committed content already matches the generator, so they are compared byte-wise.
var textMergeArtifacts = []mergeArtifact{
	{".codex/config.toml", mergeCodexMCPServer, "mergeCodexMCPServer"},
	{"CLAUDE.md", ensureClaudeMDImport, "ensureClaudeMDImport"},
}

// exemptSetupPaths are files `capy setup` writes that intentionally have no
// committed counterpart to guard. Each needs a documented reason.
var exemptSetupPaths = map[string]string{
	".git/hooks/pre-commit": "per-machine git hook under .git/, not committed to the repo",
	// The completeness test runs setup with the default SettingsProject target, so
	// it writes settings.json; this repo happens to commit its capy hooks in
	// settings.local.json (guarded by jsonMergeArtifacts). Both hold the same
	// generated hooks; only one is present in any given repo.
	".claude/settings.json": "hooks target varies per repo; committed copy here is settings.local.json (guarded above)",
}

// TestGeneratedWholeFileArtifacts: committed file must equal its generator output.
func TestGeneratedWholeFileArtifacts(t *testing.T) {
	for _, a := range wholeFileArtifacts {
		got, err := os.ReadFile(filepath.Join(repoRootFromPkg, a.path))
		require.NoError(t, err, "reading committed %s", a.path)
		assert.Equal(t, a.want, string(got),
			"%s has drifted from its generator %s (internal/platform) — `capy setup` "+
				"would write different content than this repo commits. Apply the change to "+
				"BOTH %s and the committed file.", a.path, a.genName, a.genName)
	}
}

// TestMergedArtifactsAreIdempotent: re-running setup's merge against the committed
// file must not change it. A change means the committed capy entry drifted from the
// generator.
func TestMergedArtifactsAreIdempotent(t *testing.T) {
	for _, a := range jsonMergeArtifacts {
		t.Run(a.path, func(t *testing.T) {
			src := filepath.Join(repoRootFromPkg, a.path)
			before := mustReadJSONMap(t, src)
			tmp := copyToTemp(t, src)
			require.NoError(t, a.merge(tmp))
			after := mustReadJSONMap(t, tmp)
			assert.True(t, reflect.DeepEqual(before, after),
				"%s changed when %s re-ran — the committed capy entry drifted from the "+
					"generator. Apply the change to BOTH %s and the committed file.\n"+
					"before: %v\nafter:  %v", a.path, a.genName, a.genName, before, after)
		})
	}

	for _, a := range textMergeArtifacts {
		t.Run(a.path, func(t *testing.T) {
			src := filepath.Join(repoRootFromPkg, a.path)
			before, err := os.ReadFile(src)
			require.NoError(t, err)
			tmp := copyToTemp(t, src)
			require.NoError(t, a.merge(tmp))
			after, err := os.ReadFile(tmp)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after),
				"%s changed when %s re-ran — committed content drifted from the generator. "+
					"Apply the change to BOTH %s and the committed file.", a.path, a.genName, a.genName)
		})
	}

	// .gitignore: setup appends two entries; both must already be present (a no-op).
	t.Run(".gitignore", func(t *testing.T) {
		src := filepath.Join(repoRootFromPkg, ".gitignore")
		before, err := os.ReadFile(src)
		require.NoError(t, err)
		tmp := copyToTemp(t, src)
		require.NoError(t, ensureGitignoreEntry(tmp, ".capy/**"))
		require.NoError(t, ensureGitignoreEntry(tmp, "!.capy/AGENTS.md"))
		after, err := os.ReadFile(tmp)
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after),
			".gitignore is missing a capy entry that `capy setup` adds. Update the committed .gitignore.")
	})
}

// TestDriftGuardCoversEverySetupArtifact runs `capy setup` in a throwaway repo and
// asserts that every file it writes is registered in one of the drift-guard lists
// above (or explicitly exempt). This closes the completeness gap: without it, a
// contributor could add a new generated file and the drift guard would silently
// fail to cover it — the exact regression these tests exist to prevent.
func TestDriftGuardCoversEverySetupArtifact(t *testing.T) {
	covered := map[string]bool{".gitignore": true}
	for _, a := range wholeFileArtifacts {
		covered[a.path] = true
	}
	for _, a := range jsonMergeArtifacts {
		covered[a.path] = true
	}
	for _, a := range textMergeArtifacts {
		covered[a.path] = true
	}

	dir := t.TempDir()
	// installPreCommitHook only runs when .git/hooks exists; create it (without a
	// real git repo) so setup exercises — and this test observes — that path too.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))

	require.NoError(t, SetupClaudeCode("/usr/local/bin/capy", dir, SettingsProject))
	require.NoError(t, SetupCodex("/usr/local/bin/capy", dir))

	var uncovered []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		// Ignore git internals except the pre-commit hook setup itself writes.
		if strings.HasPrefix(rel, ".git/") && rel != ".git/hooks/pre-commit" {
			return nil
		}
		if covered[rel] {
			return nil
		}
		if _, ok := exemptSetupPaths[rel]; ok {
			return nil
		}
		uncovered = append(uncovered, rel)
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, uncovered,
		"`capy setup` writes these files but no drift-guard test covers them: %v.\n"+
			"Register each in wholeFileArtifacts / jsonMergeArtifacts / textMergeArtifacts "+
			"(so its content is guarded), or add it to exemptSetupPaths with a reason. "+
			"Otherwise a future edit to it can ship stale to consumers unnoticed.", uncovered)
}

func copyToTemp(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	require.NoError(t, os.WriteFile(dst, data, 0o644))
	return dst
}

func mustReadJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}
