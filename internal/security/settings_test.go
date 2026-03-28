package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSettingsFile(t *testing.T, dir string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
	require.NoError(t, os.WriteFile(dir, []byte(content), 0o644))
}

func TestReadBashPolicies_ThreeTierPrecedence(t *testing.T) {
	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "project")
	globalDir := filepath.Join(tmp, "global")

	// Project local
	writeSettingsFile(t, filepath.Join(projectDir, ".claude", "settings.local.json"), `{
		"permissions": {
			"deny": ["Bash(sudo *)"],
			"allow": ["Bash(echo *)"],
			"ask": []
		}
	}`)

	// Project shared
	writeSettingsFile(t, filepath.Join(projectDir, ".claude", "settings.json"), `{
		"permissions": {
			"deny": ["Bash(rm -rf *)"],
			"allow": [],
			"ask": ["Bash(git push *)"]
		}
	}`)

	// Global
	globalPath := filepath.Join(globalDir, "settings.json")
	writeSettingsFile(t, globalPath, `{
		"permissions": {
			"deny": ["Bash(shutdown *)"],
			"allow": ["Bash(ls *)"],
			"ask": []
		}
	}`)

	policies := ReadBashPolicies(projectDir, globalPath)
	require.Len(t, policies, 3)

	// Order: local → shared → global
	assert.Equal(t, []string{"Bash(sudo *)"}, policies[0].Deny)
	assert.Equal(t, []string{"Bash(echo *)"}, policies[0].Allow)
	assert.Equal(t, []string{"Bash(rm -rf *)"}, policies[1].Deny)
	assert.Equal(t, []string{"Bash(git push *)"}, policies[1].Ask)
	assert.Equal(t, []string{"Bash(shutdown *)"}, policies[2].Deny)
}

func TestReadBashPolicies_MissingFiles(t *testing.T) {
	policies := ReadBashPolicies("/nonexistent/path", "/nonexistent/global.json")
	assert.Empty(t, policies)
}

func TestReadBashPolicies_MalformedJSON(t *testing.T) {
	tmp := t.TempDir()
	writeSettingsFile(t, filepath.Join(tmp, ".claude", "settings.json"), `{not valid json}`)
	policies := ReadBashPolicies(tmp, "/nonexistent/global.json")
	assert.Empty(t, policies)
}

func TestReadBashPolicies_FiltersBashOnly(t *testing.T) {
	tmp := t.TempDir()
	writeSettingsFile(t, filepath.Join(tmp, ".claude", "settings.json"), `{
		"permissions": {
			"deny": ["Bash(sudo *)", "Read(.env)", "Grep(**/*.key)"],
			"allow": ["Bash(echo *)"],
			"ask": []
		}
	}`)

	policies := ReadBashPolicies(tmp, "/nonexistent/global.json")
	require.Len(t, policies, 1)
	// Only Bash patterns survive
	assert.Equal(t, []string{"Bash(sudo *)"}, policies[0].Deny)
	assert.Equal(t, []string{"Bash(echo *)"}, policies[0].Allow)
}

func TestReadToolDenyPatterns(t *testing.T) {
	tmp := t.TempDir()
	writeSettingsFile(t, filepath.Join(tmp, ".claude", "settings.json"), `{
		"permissions": {
			"deny": ["Read(.env)", "Read(**/.env*)", "Bash(sudo *)", "Grep(**/*.key)"]
		}
	}`)

	result := ReadToolDenyPatterns("Read", tmp, "/nonexistent/global.json")
	require.Len(t, result, 1)
	assert.Equal(t, []string{".env", "**/.env*"}, result[0])
}

func TestReadToolDenyPatterns_NoPermissions(t *testing.T) {
	tmp := t.TempDir()
	writeSettingsFile(t, filepath.Join(tmp, ".claude", "settings.json"), `{}`)

	result := ReadToolDenyPatterns("Read", tmp, "/nonexistent/global.json")
	require.Len(t, result, 1)
	assert.Empty(t, result[0])
}
