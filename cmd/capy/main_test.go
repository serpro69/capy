package main

import (
	"bytes"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capy(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "-tags", "fts5", "."}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

func TestVersionFlag(t *testing.T) {
	stdout, _, code := capy(t, "--version")
	assert.Equal(t, 0, code)
	assert.NotEmpty(t, stdout)
}

func TestServeSubcommand(t *testing.T) {
	stdout, _, code := capy(t, "serve")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "MCP server")
}

func TestHookSubcommand(t *testing.T) {
	stdout, _, code := capy(t, "hook", "pretooluse")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "hook pretooluse")
}

func TestHookRequiresEventArg(t *testing.T) {
	_, _, code := capy(t, "hook")
	assert.NotEqual(t, 0, code)
}

func TestSetupSubcommand(t *testing.T) {
	stdout, _, code := capy(t, "setup")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "setup")
}

func TestDoctorSubcommand(t *testing.T) {
	stdout, _, code := capy(t, "doctor")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "doctor")
}

func TestCleanupSubcommand(t *testing.T) {
	stdout, _, code := capy(t, "cleanup")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "cleanup")
}

func TestDefaultCommandIsServe(t *testing.T) {
	stdout, _, code := capy(t)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "MCP server")
}

func TestUnknownSubcommand(t *testing.T) {
	_, _, code := capy(t, "nonexistent")
	require.NotEqual(t, 0, code)
}
