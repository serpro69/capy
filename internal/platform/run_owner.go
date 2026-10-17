package platform

import (
	"os"
	"path/filepath"

	"github.com/serpro69/capy/internal/config"
)

// ResolveRunOwner is shared by direct serve and the future capture CLI. Explicit
// selection keeps the CLI contract; implicit selection validates every physical
// Codex ancestor before choosing the nearest owner, with no malformed fallback.
func ResolveRunOwner(cwd, explicit string) (string, error) {
	selected := explicit
	if selected == "" {
		owners, err := InspectCodexOwners(cwd)
		if err != nil {
			return "", err
		}
		if len(owners) > 0 {
			return ResolveCodexOwner(cwd, owners[0].ID, "serve", "")
		}
		selected = os.Getenv("CLAUDE_PROJECT_DIR")
		if selected == "" {
			selected = config.DetectProjectRootFrom(cwd)
		}
	}
	if !filepath.IsAbs(selected) {
		selected = filepath.Join(cwd, selected)
	}
	return filepath.EvalSymlinks(selected)
}
