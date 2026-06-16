package executor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// dotNetProfilerVars enumerates the .NET/C# profiler-attach and diagnostic
// hijack vectors that must be stripped from the sandbox environment. This is a
// one-way removal guard: any new .NET entry added to deniedEnvVars in env.go
// must also be added here to gain stripping coverage in the tests below.
var dotNetProfilerVars = []string{
	"CORECLR_PROFILER", "CORECLR_PROFILER_PATH",
	"CORECLR_PROFILER_PATH_32", "CORECLR_PROFILER_PATH_64",
	"CORECLR_PROFILER_PATH_ARM32", "CORECLR_PROFILER_PATH_ARM64",
	"CORECLR_ENABLE_PROFILING",
	"DOTNET_PROFILER_PATH", "DOTNET_PROFILER_PATH_32",
	"DOTNET_PROFILER_PATH_64", "DOTNET_PROFILER_PATH_ARM32",
	"DOTNET_PROFILER_PATH_ARM64",
	"DOTNET_DiagnosticPorts", "DOTNET_BUNDLE_EXTRACT_BASE_DIR",
}

// TestDeniedEnvVarsCoversDotNet asserts every .NET/C# profiler vector is in the
// deny set, guarding against accidental removal during future edits.
func TestDeniedEnvVarsCoversDotNet(t *testing.T) {
	for _, key := range dotNetProfilerVars {
		assert.Truef(t, deniedEnvVars[key], "%s must be in deniedEnvVars", key)
	}
}

// TestBuildSafeEnvStripsDotNetProfilerVars verifies the .NET/C# profiler vars
// are removed from the sandbox environment while a benign var survives.
func TestBuildSafeEnvStripsDotNetProfilerVars(t *testing.T) {
	for _, key := range dotNetProfilerVars {
		t.Setenv(key, "/tmp/evil.so")
	}
	t.Setenv("CAPY_TEST_BENIGN", "keep-me")

	env := envToMap(BuildSafeEnv(t.TempDir()))

	for _, key := range dotNetProfilerVars {
		_, present := env[key]
		assert.Falsef(t, present, "%s should be stripped from sandbox env", key)
	}
	assert.Equal(t, "keep-me", env["CAPY_TEST_BENIGN"], "benign var should survive")
}

// TestBuildSafeEnvStripsCOMPlusPrefix verifies the legacy COMPlus_ back-compat
// prefix is stripped, mirroring the BASH_FUNC_ prefix handling.
func TestBuildSafeEnvStripsCOMPlusPrefix(t *testing.T) {
	t.Setenv("COMPlus_EnableDiagnostics", "1")
	t.Setenv("COMPlus_ZapDisable", "1")

	env := BuildSafeEnv(t.TempDir())
	for _, entry := range env {
		assert.Falsef(t, strings.HasPrefix(entry, "COMPlus_"),
			"COMPlus_ prefixed var should be stripped: %s", entry)
	}
}
