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

func TestResolveCwd(t *testing.T) {
	project, external := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, "with spaces"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(external, "child"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(external, "child"), filepath.Join(project, "alias")))
	require.NoError(t, os.Symlink("missing", filepath.Join(project, "dangling")))
	writeCwdFixture(t, project, "file", "not a directory")
	for _, tc := range []struct {
		name, cwd, want string
		invalid         bool
	}{
		{name: "omitted", want: project},
		{name: "dot", cwd: ".", want: project},
		{name: "relative spaces", cwd: "with spaces", want: filepath.Join(project, "with spaces")},
		{name: "absolute external", cwd: external, want: external},
		{name: "symlink parent", cwd: "alias/..", want: external},
		{name: "missing", cwd: "missing", invalid: true},
		{name: "file", cwd: "file", invalid: true},
		{name: "file parent", cwd: "file/..", invalid: true},
		{name: "dangling", cwd: "dangling", invalid: true},
		{name: "nul", cwd: "bad\x00dir", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCwd(project, tc.cwd)
			if tc.invalid {
				require.ErrorContains(t, err, "cwd")
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			want, err := filepath.EvalSymlinks(tc.want)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
	processDir, err := os.Getwd()
	require.NoError(t, err)
	relativeProject, err := filepath.Rel(processDir, project)
	require.NoError(t, err)
	got, err := ResolveCwd(relativeProject, "with spaces")
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(filepath.Join(project, "with spaces"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestExecuteCwdInvalidBeforeSideEffects(t *testing.T) {
	e := newTestExecutor(t)
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	for _, lang := range []Language{Shell, Rust} {
		_, err := e.Execute(t.Context(), ExecRequest{Language: lang, Cwd: "missing", Code: "must not run"})
		require.ErrorContains(t, err, "cwd")
	}
	entries, err := os.ReadDir(tmpRoot)
	require.NoError(t, err)
	assert.Empty(t, entries, "invalid cwd must fail before temporary files or compilation")
}

func TestExecuteCwdConcurrent(t *testing.T) {
	e := newTestExecutor(t)
	processDir, err := os.Getwd()
	require.NoError(t, err)
	t.Run("concurrent calls", func(t *testing.T) {
		for _, marker := range []string{"first", "second"} {
			t.Run(marker, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				writeCwdFixture(t, dir, "marker", marker)
				result, err := e.Execute(t.Context(), ExecRequest{Language: Shell, Cwd: dir, Code: "cat marker; pwd"})
				require.NoError(t, err)
				require.Zero(t, result.ExitCode, result.Stderr)
				assert.True(t, strings.HasPrefix(result.Stdout, marker), result.Stdout)
				wantDir, err := filepath.EvalSymlinks(dir)
				require.NoError(t, err)
				assert.Contains(t, result.Stdout, wantDir)
			})
		}
	})
	result, err := e.Execute(t.Context(), ExecRequest{Language: Shell, Code: "pwd"})
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(e.projectDir)
	require.NoError(t, err)
	assert.Equal(t, want, strings.TrimSpace(result.Stdout), "overrides must not persist")
	after, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, processDir, after)
}

func TestExecuteCwdElixirWrapper(t *testing.T) {
	// Capture generated source to exercise wrapper selection even on hosts with
	// no Elixir runtime. Real runtime execution remains in the shared cwd matrix.
	for _, mixAt := range []string{"project", "override"} {
		t.Run(mixAt, func(t *testing.T) {
			e := newTestExecutor(t)
			override := t.TempDir()
			root := e.projectDir
			if mixAt == "override" {
				root = override
			}
			writeCwdFixture(t, root, "mix.exs", "# fixture")
			shim := filepath.Join(t.TempDir(), "elixir")
			require.NoError(t, os.WriteFile(shim, []byte("#!/bin/sh\ncat \"$1\"\n"), 0o700))
			e.Runtimes()[Elixir] = shim
			result, err := e.Execute(context.Background(), ExecRequest{Language: Elixir, Cwd: override, Code: `IO.puts("marker")`})
			require.NoError(t, err)
			require.Zero(t, result.ExitCode, result.Stderr)
			assert.Equal(t, mixAt == "override", strings.Contains(result.Stdout, "Code.prepend_path"))
		})
	}
}

func TestExecuteCwdGoModuleSelection(t *testing.T) {
	isolateGoExecution(t)
	e := newTestExecutor(t)
	if e.Runtimes()[Go] == "" {
		t.Skip("no Go runtime")
	}
	// The selected project intentionally cannot run with the local toolchain.
	// A cwd override must discover its own module while project identity stays put.
	writeCwdFixture(t, e.projectDir, "go.mod", "module example.test/selected\n\ngo 1.99.0\n")
	override := t.TempDir()
	writeCwdFixture(t, override, "go.mod", "module example.test/override\n\ngo 1.21.0\n")
	writeCwdFixture(t, override, "local/value.go", "package local\nconst Value = \"override-module\"\n")
	result, err := e.Execute(t.Context(), ExecRequest{
		Language: Go, Cwd: override, TimeoutSec: 60,
		Code: "package main\nimport (\"fmt\"; \"example.test/override/local\")\nfunc main() { fmt.Print(local.Value) }",
	})
	require.NoError(t, err)
	require.Zero(t, result.ExitCode, result.Stderr)
	assert.Equal(t, "override-module", result.Stdout)
}
