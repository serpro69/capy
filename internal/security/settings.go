package security

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SecurityPolicy holds Bash permission patterns from a single settings file.
type SecurityPolicy struct {
	Allow []string
	Deny  []string
	Ask   []string
}

// settingsFile represents the structure of a Claude settings JSON file.
type settingsFile struct {
	Permissions *settingsPermissions `json:"permissions"`
}

type settingsPermissions struct {
	Allow []any `json:"allow"`
	Deny  []any `json:"deny"`
	Ask   []any `json:"ask"`
}

// readSingleSettings reads one settings file and returns a SecurityPolicy
// with only Bash patterns. Returns nil if the file is missing or invalid.
func readSingleSettings(path string) *SecurityPolicy {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var sf settingsFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil
	}

	if sf.Permissions == nil {
		return nil
	}

	return &SecurityPolicy{
		Allow: filterBashPatterns(sf.Permissions.Allow),
		Deny:  filterBashPatterns(sf.Permissions.Deny),
		Ask:   filterBashPatterns(sf.Permissions.Ask),
	}
}

// filterBashPatterns filters an array to only Bash(...) patterns.
func filterBashPatterns(arr []any) []string {
	var result []string
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if parseBashPattern(s) != "" {
			result = append(result, s)
		}
	}
	return result
}

// ReadBashPolicies reads Bash permission policies from up to 3 settings files.
//
// Returns policies in precedence order (most local first):
//  1. .claude/settings.local.json (project-local)
//  2. .claude/settings.json (project-shared)
//  3. ~/.claude/settings.json (global)
//
// Missing or invalid files are silently skipped.
// globalSettingsPath can be empty to use the default (~/.claude/settings.json).
func ReadBashPolicies(projectDir, globalSettingsPath string) []SecurityPolicy {
	var policies []SecurityPolicy

	if projectDir != "" {
		if p := readSingleSettings(filepath.Join(projectDir, ".claude", "settings.local.json")); p != nil {
			policies = append(policies, *p)
		}
		if p := readSingleSettings(filepath.Join(projectDir, ".claude", "settings.json")); p != nil {
			policies = append(policies, *p)
		}
	}

	if globalSettingsPath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			globalSettingsPath = filepath.Join(home, ".claude", "settings.json")
		}
	}
	if globalSettingsPath != "" {
		if p := readSingleSettings(globalSettingsPath); p != nil {
			policies = append(policies, *p)
		}
	}

	return policies
}

// ReadToolDenyPatterns reads deny patterns for a specific tool from settings files.
//
// Returns an array of arrays (one per settings file, in precedence order).
// Each inner array contains the extracted glob strings.
// globalSettingsPath can be empty to use the default.
func ReadToolDenyPatterns(toolName, projectDir, globalSettingsPath string) [][]string {
	var result [][]string

	extractGlobs := func(path string) []string {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var sf settingsFile
		if err := json.Unmarshal(data, &sf); err != nil {
			return nil
		}

		if sf.Permissions == nil {
			return []string{}
		}

		var globs []string
		for _, v := range sf.Permissions.Deny {
			s, ok := v.(string)
			if !ok {
				continue
			}
			tool, glob := parseToolPattern(s)
			if tool == toolName {
				globs = append(globs, glob)
			}
		}
		return globs
	}

	if projectDir != "" {
		if g := extractGlobs(filepath.Join(projectDir, ".claude", "settings.local.json")); g != nil {
			result = append(result, g)
		}
		if g := extractGlobs(filepath.Join(projectDir, ".claude", "settings.json")); g != nil {
			result = append(result, g)
		}
	}

	if globalSettingsPath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			globalSettingsPath = filepath.Join(home, ".claude", "settings.json")
		}
	}
	if globalSettingsPath != "" {
		if g := extractGlobs(globalSettingsPath); g != nil {
			result = append(result, g)
		}
	}

	return result
}
