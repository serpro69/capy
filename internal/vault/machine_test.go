package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMachineID_EnvVarWins(t *testing.T) {
	t.Setenv(machineIDEnv, "env-machine-id")
	assert.Equal(t, "env-machine-id", resolveMachineID())
}

func TestResolveMachineID_GenerateAndPersist(t *testing.T) {
	t.Setenv(machineIDEnv, "")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	id := resolveMachineID()
	require.NotEmpty(t, id)
	assert.False(t, strings.HasPrefix(id, "derived-"), "should generate a UUID, not the fallback")

	// The id was persisted to the machine-id file.
	data, err := os.ReadFile(filepath.Join(configHome, "capy", "machine-id"))
	require.NoError(t, err)
	assert.Equal(t, id, strings.TrimSpace(string(data)))

	// A second resolution reads the same persisted id.
	assert.Equal(t, id, resolveMachineID())
}

func TestResolveMachineID_ReadsExistingFile(t *testing.T) {
	t.Setenv(machineIDEnv, "")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	dir := filepath.Join(configHome, "capy")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "machine-id"), []byte("preexisting-id\n"), 0o644))

	assert.Equal(t, "preexisting-id", resolveMachineID())
}

func TestResolveMachineID_WriteFailureFallback(t *testing.T) {
	t.Setenv(machineIDEnv, "")

	// Point XDG_CONFIG_HOME at a regular file so MkdirAll of <home>/capy fails.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	t.Setenv("XDG_CONFIG_HOME", blocker)

	id := resolveMachineID()
	assert.True(t, strings.HasPrefix(id, "derived-"), "expected derived fallback, got %q", id)
}

func TestMachineID_Cached(t *testing.T) {
	// MachineID caches once per process; calling twice must agree.
	assert.Equal(t, MachineID(), MachineID())
}
