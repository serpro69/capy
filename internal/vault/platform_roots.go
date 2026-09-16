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

// resolvePlatformRoot pairs one platform with its session root (honoring its
// env override — CLAUDE_CONFIG_DIR, CODEX_HOME) and the existence probe
// discovery acts on: a Claude root exists when the projects dir is a directory;
// a Codex root exists when its home has at least one rollout root
// (HasCodexRolloutRoot), so an installed-but-unused Codex still counts as
// present. It is the single resolution site shared by DiscoverAll and
// PlatformRoots, so the doctor reports exactly what the sweep walks. The error
// covers only root resolution (an unresolvable home directory) — and only THIS
// platform's: `capy vault import --platform codex` with CODEX_HOME set must not
// fail because $HOME, which only the Claude root needs, is unresolvable.
//
// The switch is an explicit per-platform literal, not derived from
// knownPlatforms: each platform has its own root and probe, so a new constant
// needs a new arm here (and a Discoverer in DiscoverAll) — the same multi-site
// change the reader-version rule already makes it.
// TestResolvePlatformRoots_CoversKnownPlatforms fails when the two drift.
func resolvePlatformRoot(p Platform) (PlatformRoot, error) {
	switch p {
	case PlatformClaudeCode:
		root, err := config.ClaudeProjectsDir()
		if err != nil {
			return PlatformRoot{}, fmt.Errorf("resolving claude projects dir: %w", err)
		}
		return PlatformRoot{Platform: p, Root: root, RootExists: isDir(root)}, nil
	case PlatformCodex:
		home, err := config.CodexHome()
		if err != nil {
			return PlatformRoot{}, fmt.Errorf("resolving codex home: %w", err)
		}
		return PlatformRoot{Platform: p, Root: home, RootExists: HasCodexRolloutRoot(home)}, nil
	default:
		return PlatformRoot{}, fmt.Errorf("%w: %q has no session root", ErrUnknownPlatform, string(p))
	}
}

// resolvePlatformRoots resolves every known platform's root, in knownPlatforms
// order (see resolvePlatformRoot). Any platform failing to resolve fails the
// whole call: the doctor (PlatformRoots) reports that as its Warn row, and
// DiscoverAll — which must isolate one platform's failure from the other —
// resolves per platform itself.
func resolvePlatformRoots() ([]PlatformRoot, error) {
	roots := make([]PlatformRoot, 0, len(knownPlatforms))
	for _, p := range knownPlatforms {
		r, err := resolvePlatformRoot(p)
		if err != nil {
			return nil, err
		}
		roots = append(roots, r)
	}
	return roots, nil
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
