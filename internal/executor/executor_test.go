package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestExecutor(t *testing.T) *PolyglotExecutor {
	t.Helper()
	return NewExecutor(t.TempDir(), MaxOutputBytes)
}

// --- Runtime detection ---

func TestDetectRuntimes(t *testing.T) {
	runtimes := detectRuntimes()
	// At minimum, bash/sh should be available on any Unix system.
	assert.NotEmpty(t, runtimes[Shell], "shell runtime should be detected")
}

func TestRuntimesLazyDetection(t *testing.T) {
	e := newTestExecutor(t)
	// First call triggers detection.
	r1 := e.Runtimes()
	require.NotNil(t, r1)
	// Second call returns cached.
	r2 := e.Runtimes()
	assert.Equal(t, r1, r2)
}

// --- Smart truncation ---

func TestSmartTruncateUnderThreshold(t *testing.T) {
	output := "short output"
	assert.Equal(t, output, SmartTruncate(output, 100))
}

func TestSmartTruncateOverThreshold(t *testing.T) {
	var lines []string
	for i := range 1000 {
		lines = append(lines, strings.Repeat("x", 100))
		_ = i
	}
	output := strings.Join(lines, "\n")
	truncated := SmartTruncate(output, 1000)

	assert.Less(t, len(truncated), len(output))
	assert.Contains(t, truncated, "lines omitted")
}

func TestSmartTruncate60_40Split(t *testing.T) {
	var lines []string
	for i := range 100 {
		lines = append(lines, "line-"+strings.Repeat("a", 50)+"-"+string(rune('0'+i%10)))
	}
	output := strings.Join(lines, "\n")
	truncated := SmartTruncate(output, 2000)

	parts := strings.SplitN(truncated, "---", 2)
	require.Len(t, parts, 2, "should have separator")

	head := parts[0]
	// Head should be roughly 60% of budget.
	assert.Greater(t, len(head), 500, "head should be substantial")
}

func TestSmartTruncateUTF8Safety(t *testing.T) {
	// Lines with multi-byte characters.
	var lines []string
	for range 100 {
		lines = append(lines, "日本語テスト行 — emoji: 🎉")
	}
	output := strings.Join(lines, "\n")
	truncated := SmartTruncate(output, 500)

	// Should not corrupt UTF-8 (line-boundary snapping).
	assert.True(t, isValidUTF8(truncated), "truncated output should be valid UTF-8")
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' { // replacement character indicates corruption
			return false
		}
	}
	return true
}

// --- Safe environment ---

func TestBuildSafeEnvStrippedVars(t *testing.T) {
	// Set a dangerous var, verify it's stripped.
	t.Setenv("BASH_ENV", "/evil/script")
	t.Setenv("LD_PRELOAD", "/evil/lib.so")
	t.Setenv("NODE_OPTIONS", "--evil")

	env := BuildSafeEnv(t.TempDir())
	envMap := envToMap(env)

	assert.Empty(t, envMap["BASH_ENV"], "BASH_ENV should be stripped")
	assert.Empty(t, envMap["LD_PRELOAD"], "LD_PRELOAD should be stripped")
	assert.Empty(t, envMap["NODE_OPTIONS"], "NODE_OPTIONS should be stripped")
}

func TestBuildSafeEnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	env := BuildSafeEnv(tmpDir)
	envMap := envToMap(env)

	assert.Equal(t, tmpDir, envMap["TMPDIR"])
	assert.Equal(t, "1", envMap["NO_COLOR"])
	assert.Equal(t, "1", envMap["PYTHONUNBUFFERED"])
	assert.Equal(t, "en_US.UTF-8", envMap["LANG"])
}

func TestBuildSafeEnvBashFuncStripped(t *testing.T) {
	t.Setenv("BASH_FUNC_evil%%", "() { evil; }")
	env := BuildSafeEnv(t.TempDir())
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "BASH_FUNC_"), "BASH_FUNC_ vars should be stripped")
	}
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string)
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

// --- Exit classification ---

func TestClassifyShellSoftFail(t *testing.T) {
	c := ClassifyNonZeroExit(Shell, 1, "some output", "")
	assert.False(t, c.IsError, "shell exit 1 with stdout should be soft fail")
	assert.Equal(t, "some output", c.Output)
}

func TestClassifyShellHardFail(t *testing.T) {
	c := ClassifyNonZeroExit(Shell, 2, "", "error")
	assert.True(t, c.IsError)
}

func TestClassifyNonShellFail(t *testing.T) {
	c := ClassifyNonZeroExit(Python, 1, "output", "error")
	assert.True(t, c.IsError, "non-shell exit 1 should be hard fail")
}

func TestClassifyShellExit1NoStdout(t *testing.T) {
	c := ClassifyNonZeroExit(Shell, 1, "", "error")
	assert.True(t, c.IsError, "shell exit 1 without stdout should be hard fail")
}

// --- Execution ---

func TestExecuteBash(t *testing.T) {
	e := newTestExecutor(t)
	if e.Runtimes()[Shell] == "" {
		t.Skip("no shell runtime")
	}

	result, err := e.Execute(context.Background(), ExecRequest{
		Language: Shell,
		Code:     `echo "hello from bash"`,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Contains(t, result.Stdout, "hello from bash")
}

func TestExecutePython(t *testing.T) {
	e := newTestExecutor(t)
	if e.Runtimes()[Python] == "" {
		t.Skip("no python runtime")
	}

	result, err := e.Execute(context.Background(), ExecRequest{
		Language: Python,
		Code:     `print("hello from python")`,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Contains(t, result.Stdout, "hello from python")
}

func TestExecuteTimeout(t *testing.T) {
	e := newTestExecutor(t)
	if e.Runtimes()[Shell] == "" {
		t.Skip("no shell runtime")
	}

	result, err := e.Execute(context.Background(), ExecRequest{
		Language:   Shell,
		Code:       "sleep 60",
		TimeoutSec: 1,
	})
	require.NoError(t, err)
	assert.True(t, result.TimedOut)
}

func TestExecuteNonZeroExit(t *testing.T) {
	e := newTestExecutor(t)
	if e.Runtimes()[Shell] == "" {
		t.Skip("no shell runtime")
	}

	result, err := e.Execute(context.Background(), ExecRequest{
		Language: Shell,
		Code:     "exit 42",
	})
	require.NoError(t, err)
	assert.Equal(t, 42, result.ExitCode)
}

// --- File content injection ---

func TestInjectFileContentPython(t *testing.T) {
	code := injectFileContent(Python, "print(FILE_CONTENT)", "/tmp/test.txt")
	assert.Contains(t, code, "FILE_CONTENT_PATH")
	assert.Contains(t, code, "/tmp/test.txt")
	assert.Contains(t, code, "print(FILE_CONTENT)")
}

func TestInjectFileContentShell(t *testing.T) {
	code := injectFileContent(Shell, "echo $FILE_CONTENT", "/tmp/test.txt")
	assert.Contains(t, code, "FILE_CONTENT_PATH=")
	assert.Contains(t, code, "echo $FILE_CONTENT")
}

func TestInjectFileContentJS(t *testing.T) {
	code := injectFileContent(JavaScript, "console.log(FILE_CONTENT)", "/tmp/test.txt")
	assert.Contains(t, code, "readFileSync")
	assert.Contains(t, code, "console.log(FILE_CONTENT)")
}

func TestInjectFileContentRuby(t *testing.T) {
	code := injectFileContent(Ruby, "puts FILE_CONTENT", "/tmp/test.txt")
	assert.Contains(t, code, "File.read")
}

// --- Auto-wrapping ---

func TestAutoWrapGo(t *testing.T) {
	code := autoWrap(Go, `fmt.Println("hello")`, "")
	assert.Contains(t, code, "package main")
	assert.Contains(t, code, "func main()")
}

func TestAutoWrapGoAlreadyWrapped(t *testing.T) {
	code := "package main\n\nfunc main() {}"
	assert.Equal(t, code, autoWrap(Go, code, ""))
}

func TestAutoWrapPHP(t *testing.T) {
	code := autoWrap(PHP, `echo "hello";`, "")
	assert.True(t, strings.HasPrefix(code, "<?php"))
}

// --- ExecuteFile integration ---

func TestExecuteFile(t *testing.T) {
	e := newTestExecutor(t)
	if e.Runtimes()[Shell] == "" {
		t.Skip("no shell runtime")
	}

	// Create a test file.
	tmpFile := filepath.Join(t.TempDir(), "test.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("file content here"), 0o644))

	result, err := e.ExecuteFile(context.Background(), ExecRequest{
		Language: Shell,
		Code:     `echo "$FILE_CONTENT"`,
		FilePath: tmpFile,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Contains(t, result.Stdout, "file content here")
}

// --- Background cleanup ---

func TestCleanupBackgrounded(t *testing.T) {
	e := newTestExecutor(t)
	// Track some fake PIDs and verify cleanup doesn't panic.
	e.bgMu.Lock()
	e.backgroundPids[99999] = struct{}{} // non-existent PID
	e.bgMu.Unlock()

	// Should not panic.
	e.CleanupBackgrounded()

	e.bgMu.Lock()
	assert.Empty(t, e.backgroundPids)
	e.bgMu.Unlock()
}
