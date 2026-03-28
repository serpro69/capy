package security

import "strings"

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
// NOTE: fileGlobToRegex is recompiled per glob on each call. Acceptable for
// current usage (handful of globs, called once per tool invocation) but worth
// caching if this ever lands on a hot path.
func EvaluateFilePath(filePath string, denyGlobs [][]string) (denied bool, matchedPattern string) {
	normalized := strings.ReplaceAll(filePath, "\\", "/")

	for _, globs := range denyGlobs {
		for _, glob := range globs {
			if fileGlobToRegex(glob, false).MatchString(normalized) {
				return true, glob
			}
		}
	}

	return false, ""
}
