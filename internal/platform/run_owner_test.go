package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveRunOwner(t *testing.T) {
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	cwd := filepath.Join(nested, "sub")
	require.NoError(t, os.MkdirAll(cwd, 0o700))
	for dir, id := range map[string]string{root: testCodexOwnerID, nested: strings.Repeat("b", 32)} {
		writeCodexFixture(t, dir, codexOwnerRelPath, codexOwnerContent(id))
		writeCodexFixture(t, dir, ".codex/config.toml", codexMCPBlock(id))
	}
	selected, err := ResolveRunOwner(cwd, "")
	require.NoError(t, err)
	require.Equal(t, nested, selected)
	selected, err = ResolveRunOwner(cwd, root)
	require.NoError(t, err)
	require.Equal(t, root, selected)
	// A bad ancestor must not silently fall back to the nearer valid owner/Git.
	writeCodexFixture(t, root, codexOwnerRelPath, "broken")
	_, err = ResolveRunOwner(cwd, "")
	require.Error(t, err)
	require.NoDirExists(t, filepath.Join(nested, ".capy"))
}
