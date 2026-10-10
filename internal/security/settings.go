package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// LoadReadPolicy prepares a snapshot of local, shared and user Read rules.
// Unlike the legacy Bash loader, invalid selected settings fail file admission.
// Callers must retain and enforce the error, never substitute an empty policy.
func LoadReadPolicy(ctx FilePolicyContext) (*FilePolicy, error) {
	ctx, err := normalizeFilePolicyContext(ctx)
	if err != nil {
		return nil, err
	}
	policy := &FilePolicy{context: ctx}
	sources := []struct{ path, anchor string }{
		{filepath.Join(ctx.ProjectDir, ".claude", "settings.local.json"), ctx.ProjectDir},
		{filepath.Join(ctx.ProjectDir, ".claude", "settings.json"), ctx.ProjectDir},
		{ctx.GlobalSettingsPath, filepath.Dir(ctx.GlobalSettingsPath)},
	}
	for _, source := range sources {
		permissions, err := readFilePermissions(source.path)
		if err != nil {
			return nil, err
		}
		for _, action := range []string{"deny", "allow", "ask"} {
			for _, pattern := range permissions[action] {
				if pattern != "Read" && !strings.HasPrefix(pattern, "Read(") {
					continue
				}
				rule, warning, err := prepareReadRule(pattern, action, source.path, source.anchor, ctx)
				if err != nil {
					return nil, fmt.Errorf("invalid Read policy %q in %q: %w", pattern, source.path, err)
				}
				policy.rules = append(policy.rules, rule)
				if warning != "" && !slices.Contains(policy.warnings, warning) {
					policy.warnings = append(policy.warnings, warning)
				}
			}
		}
	}
	return policy, nil
}

func readFilePermissions(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// A dangling selected settings symlink is invalid, not absent policy.
		if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
			return nil, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read policy settings %q: %w", path, err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		// Do not echo parser input: settings may contain credentials.
		return nil, fmt.Errorf("invalid policy settings %q: expected JSON object", path)
	}
	raw, exists := document["permissions"]
	if !exists {
		return nil, nil
	}
	var permissions map[string]json.RawMessage
	if err := json.Unmarshal(raw, &permissions); err != nil || permissions == nil {
		return nil, fmt.Errorf("invalid policy settings %q: permissions must be an object", path)
	}
	result := make(map[string][]string)
	for _, action := range []string{"deny", "allow", "ask"} {
		raw, exists := permissions[action]
		if !exists {
			continue
		}
		var values []any
		if err := json.Unmarshal(raw, &values); err != nil || values == nil {
			return nil, fmt.Errorf("invalid policy settings %q: permissions.%s must be an array of strings", path, action)
		}
		for _, value := range values {
			pattern, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("invalid policy settings %q: permissions.%s must contain strings", path, action)
			}
			result[action] = append(result[action], pattern)
		}
	}
	return result, nil
}
