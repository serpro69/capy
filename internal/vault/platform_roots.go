package vault

import (
	"fmt"

	"github.com/serpro69/capy/internal/config"
)

// PlatformRoot is one agent CLI's on-disk session root as the vault sees it:
// where it is, whether it exists (by the same probe DiscoverAll and the server
// sweep use to decide whether to walk it), and how many of its sessions this
// vault has archived. It is the input of the `capy doctor` / capy_doctor
// "Vault platforms" check, which lives in internal/platform and therefore
// receives a strings-only copy — internal/platform must not import this
// package.
type PlatformRoot struct {
	Platform   Platform
	Root       string
	RootExists bool
	// Archived is the vault's session count for this platform (VaultStats
	// .ByPlatform); 0 when the vault holds none or does not exist yet.
	Archived int
}

// resolvePlatformRoots pairs every known platform with its session root (each
// honoring its env override — CLAUDE_CONFIG_DIR, CODEX_HOME) and the existence
// probe discovery acts on: a Claude root exists when the projects dir is a
// directory; a Codex root exists when its home has at least one rollout root
// (HasCodexRolloutRoot), so an installed-but-unused Codex still counts as
// present. It is the single resolution site shared by DiscoverAll and
// PlatformRoots, so the doctor reports exactly what the sweep walks. The error
// covers only root resolution (an unresolvable home directory).
//
// The list is an explicit literal, not derived from knownPlatforms: each
// platform has its own root and probe, so a new constant needs a new entry
// here (and a Discoverer in DiscoverAll) — the same multi-site change the
// reader-version rule already makes it. TestResolvePlatformRoots_CoversKnownPlatforms
// fails when the two lists drift, in knownPlatforms order.
func resolvePlatformRoots() ([]PlatformRoot, error) {
	claudeRoot, err := config.ClaudeProjectsDir()
	if err != nil {
		return nil, fmt.Errorf("resolving claude projects dir: %w", err)
	}
	codexRoot, err := config.CodexHome()
	if err != nil {
		return nil, fmt.Errorf("resolving codex home: %w", err)
	}
	return []PlatformRoot{
		{Platform: PlatformClaudeCode, Root: claudeRoot, RootExists: isDir(claudeRoot)},
		{Platform: PlatformCodex, Root: codexRoot, RootExists: HasCodexRolloutRoot(codexRoot)},
	}, nil
}

// PlatformRoots returns every known platform's root (see resolvePlatformRoots)
// with Archived filled from stats.ByPlatform. stats may be nil — the vault DB
// does not exist yet — in which case every count is 0. A ByPlatform row whose
// platform is not a known constant is ignored: the reader-version rule makes
// such a row corruption, and the doctor reports roots, not row repair.
func PlatformRoots(stats *VaultStats) ([]PlatformRoot, error) {
	roots, err := resolvePlatformRoots()
	if err != nil {
		return nil, err
	}
	if stats == nil {
		return roots, nil
	}
	for i := range roots {
		for _, ps := range stats.ByPlatform {
			if ps.Platform == roots[i].Platform {
				roots[i].Archived = ps.Sessions
				break
			}
		}
	}
	return roots, nil
}
