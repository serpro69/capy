package security

import (
	"path/filepath"
	"strings"
)

// CommandDecision represents the result of evaluating a command against policies.
type CommandDecision struct {
	Decision       string // "allow", "deny", or "ask"
	MatchedPattern string
}

// EvaluateCommandDenyOnly evaluates a command against policies, enforcing only
// deny patterns. The MCP server has no UI for "ask" prompts, so allow/ask
// patterns are irrelevant. Returns "deny" if any deny pattern matches,
// otherwise "allow".
//
// Splits chained commands to prevent bypass via prepending innocent commands.
func EvaluateCommandDenyOnly(command string, policies []SecurityPolicy) CommandDecision {
	segments := SplitChainedCommands(command)
	for _, segment := range segments {
		for _, policy := range policies {
			if match := matchesAnyBashPattern(segment, policy.Deny, false); match != "" {
				return CommandDecision{Decision: "deny", MatchedPattern: match}
			}
		}
	}
	return CommandDecision{Decision: "allow"}
}

// EvaluateCommand evaluates a command against policies with full deny > ask > allow logic.
//
// Splits chained commands and checks each segment against deny patterns.
// Then checks the full command against ask/allow patterns.
// Within each policy: deny > ask > allow (most restrictive wins).
// First definitive match across policies wins.
// Default (no match in any policy): "ask".
func EvaluateCommand(command string, policies []SecurityPolicy) CommandDecision {
	// Check each segment of chained commands against deny patterns
	segments := SplitChainedCommands(command)
	for _, segment := range segments {
		for _, policy := range policies {
			if match := matchesAnyBashPattern(segment, policy.Deny, false); match != "" {
				return CommandDecision{Decision: "deny", MatchedPattern: match}
			}
		}
	}

	// Check ask/allow against the full command
	for _, policy := range policies {
		if match := matchesAnyBashPattern(command, policy.Ask, false); match != "" {
			return CommandDecision{Decision: "ask", MatchedPattern: match}
		}
		if match := matchesAnyBashPattern(command, policy.Allow, false); match != "" {
			return CommandDecision{Decision: "allow", MatchedPattern: match}
		}
	}

	return CommandDecision{Decision: "ask"}
}

// EvaluateFilePath checks if a file path should be denied based on deny globs.
//
// Normalizes backslashes to forward slashes before matching so that
// Windows paths work with Unix-style glob patterns.
//
// When projectRoot is non-empty, the path is matched against up to three
// candidates to defeat traversal bypasses: (1) the raw input, (2) the lexical
// absolute path (relative inputs joined to projectRoot, then Clean), and (3)
// the canonical realpath with symlinks resolved via filepath.EvalSymlinks. A
// match against any candidate triggers denial. This closes the bypass where a
// relative path like "../../.ssh/id_rsa" or a symlink to a denied target
// sidesteps an absolute-path deny glob.
//
// When projectRoot is empty the behavior is identical to matching the raw input
// only — backward compatible with callers that have no project context.
func EvaluateFilePath(filePath string, denyGlobs [][]string, projectRoot string) (denied bool, matchedPattern string) {
	candidates := []string{filePath}

	if projectRoot != "" {
		// Lexical absolute resolution catches relative ".." traversal even when
		// the target does not exist on disk. filepath.Join treats an absolute
		// filePath as a path component and would corrupt it, so absolute inputs
		// are only Cleaned; relative inputs are anchored to projectRoot.
		//
		// physicalInput is the same anchored path but deliberately left
		// UNcleaned. filepath.Clean collapses ".." lexically, which would
		// discard a symlinked directory before EvalSymlinks can resolve it —
		// e.g. "dir-link/../secrets" cleans to "secrets", hiding the physical
		// escape through dir-link's target. The OS resolves symlinks first and
		// only then applies "..", so EvalSymlinks must see the uncleaned path.
		var lexicalAbs, physicalInput string
		if filepath.IsAbs(filePath) {
			lexicalAbs = filepath.Clean(filePath)
			physicalInput = filePath
		} else {
			lexicalAbs = filepath.Clean(filepath.Join(projectRoot, filePath))
			// DO NOT replace the manual join below with filepath.Join: Join calls
			// Clean, which collapses ".." lexically and defeats the symlink-then-".."
			// physical-traversal detection this candidate exists to catch.
			physicalInput = strings.TrimRight(projectRoot, `/\`) + string(filepath.Separator) + filePath
		}
		// Skip the lexical candidate when it is identical to the raw input
		// (already-clean absolute paths) to avoid evaluating it twice.
		if lexicalAbs != filePath {
			candidates = append(candidates, lexicalAbs)
		}

		// Canonical realpath catches symlink escapes — both a direct symlink
		// (safe-link -> /etc/passwd) and a symlinked directory followed by ".."
		// (dir-link/../secrets). EvalSymlinks fails for non-existent paths; skip
		// the candidate then — the raw and lexical-absolute candidates still apply.
		if realPath, err := filepath.EvalSymlinks(physicalInput); err == nil {
			candidates = append(candidates, realPath)
		}
	}

	for _, candidate := range candidates {
		normalized := strings.ReplaceAll(candidate, "\\", "/")
		for _, globs := range denyGlobs {
			for _, glob := range globs {
				if fileGlobToRegex(glob, false).MatchString(normalized) {
					return true, glob
				}
			}
		}
	}

	return false, ""
}
