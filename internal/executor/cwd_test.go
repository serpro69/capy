package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Keep Go fixtures independent of the developer's workspace and network access.
// GOFLAGS is deliberately hostile: BuildSafeEnv must continue stripping it.
func isolateGoExecution(t *testing.T) {
	t.Helper()
	t.Setenv("GOENV", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GO111MODULE", "on")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOFLAGS", "-capy-must-strip-this-flag")
}

func writeCwdFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestExecuteProjectCwd(t *testing.T) {
	isolateGoExecution(t)
	// The test process stays in its original cwd. Every runtime must find the
	// project fixture and leave its relative output there, including ExecuteFile.
	cases := []struct {
		lang     Language
		code     string
		fileCode string
	}{
		{Shell, `cat marker.txt > output.txt`, `printf '%s' "$FILE_CONTENT" > output.txt`},
		{JavaScript, `require("fs").writeFileSync("output.txt", require("fs").readFileSync("marker.txt"))`, `require("fs").writeFileSync("output.txt", FILE_CONTENT)`},
		{TypeScript, `require("fs").writeFileSync("output.txt", require("fs").readFileSync("marker.txt"))`, `require("fs").writeFileSync("output.txt", FILE_CONTENT)`},
		{Python, `from pathlib import Path; Path("output.txt").write_text(Path("marker.txt").read_text())`, `open("output.txt", "w").write(FILE_CONTENT)`},
		{Ruby, `File.write("output.txt", File.read("marker.txt"))`, `File.write("output.txt", FILE_CONTENT)`},
		{Go, `package main
import "os"
func main() { b, err := os.ReadFile("marker.txt"); if err != nil { panic(err) }; if err := os.WriteFile("output.txt", b, 0600); err != nil { panic(err) } }`, `if err := os.WriteFile("output.txt", []byte(FILE_CONTENT), 0600); err != nil { panic(err) }`},
		{Rust, `fn main() { std::fs::write("output.txt", std::fs::read("marker.txt").unwrap()).unwrap(); }`, `fs::write("output.txt", file_content).unwrap();`},
		{PHP, `file_put_contents("output.txt", file_get_contents("marker.txt"));`, `file_put_contents("output.txt", $FILE_CONTENT);`},
		{Perl, `open(my $in, '<', 'marker.txt') or die $!; open(my $out, '>', 'output.txt') or die $!; print $out do { local $/; <$in> };`, `open(my $out, '>', 'output.txt') or die $!; print $out $FILE_CONTENT;`},
		{R, `cat(readLines("marker.txt", warn=FALSE), file="output.txt")`, `cat(FILE_CONTENT, file="output.txt")`},
		{Elixir, `File.write!("output.txt", File.read!("marker.txt"))`, `File.write!("output.txt", file_content)`},
	}
	for _, tc := range cases {
		t.Run(string(tc.lang), func(t *testing.T) {
			e := newTestExecutor(t)
			if e.Runtimes()[tc.lang] == "" {
				t.Skipf("no %s runtime", tc.lang)
			}
			processDir, err := os.Getwd()
			require.NoError(t, err)
			require.NotEqual(t, processDir, e.projectDir)
			writeCwdFixture(t, e.projectDir, "marker.txt", "project-relative-marker")
			external := t.TempDir()
			writeCwdFixture(t, external, "marker.txt", "execution-directory-marker")
			relative, err := filepath.Rel(e.projectDir, external)
			require.NoError(t, err)
			for _, location := range []struct{ name, cwd, dir, content string }{
				{name: "default", cwd: "", dir: e.projectDir, content: "project-relative-marker"},
				{name: "relative", cwd: relative, dir: external, content: "execution-directory-marker"},
				{name: "absolute", cwd: external, dir: external, content: "execution-directory-marker"},
			} {
				t.Run(location.name, func(t *testing.T) {
					for _, mode := range []string{"execute", "file-relative", "file-absolute"} {
						t.Run(mode, func(t *testing.T) {
							req := ExecRequest{Language: tc.lang, Code: tc.code, Cwd: location.cwd, TimeoutSec: 60}
							want := location.content
							var result *ExecResult
							var err error
							if mode == "execute" {
								result, err = e.Execute(context.Background(), req)
							} else {
								want = "project-relative-marker"
								req.Code, req.FilePath = tc.fileCode, "marker.txt"
								if mode == "file-absolute" {
									req.FilePath = filepath.Join(e.projectDir, req.FilePath)
								}
								result, err = e.ExecuteFile(context.Background(), req)
							}
							require.NoError(t, err)
							require.Equal(t, 0, result.ExitCode, "%+v", result)
							output := filepath.Join(location.dir, "output.txt")
							content, err := os.ReadFile(output)
							require.NoError(t, err)
							assert.Equal(t, want, string(content))
							require.NoError(t, os.Remove(output))
						})
					}
				})
			}
			entries, err := os.ReadDir(e.projectDir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "scripts and compiler artifacts must stay temporary")
			assert.Equal(t, "marker.txt", entries[0].Name())
		})
	}
}

func TestExecuteRustCompileDirectory(t *testing.T) {
	e := newTestExecutor(t)
	rustc := e.Runtimes()[Rust]
	if rustc == "" {
		t.Skip("no Rust runtime")
	}
	// Observe the real compiler's cwd, then compile and run normally.
	record := filepath.Join(t.TempDir(), "compile-cwd")
	shim := filepath.Join(t.TempDir(), "rustc")
	script := fmt.Sprintf("#!/bin/sh\npwd > %s\nexec %s \"$@\"\n", quotePosixSingle(record), quotePosixSingle(rustc))
	require.NoError(t, os.WriteFile(shim, []byte(script), 0o700))
	e.Runtimes()[Rust] = shim
	result, err := e.Execute(context.Background(), ExecRequest{
		Language: Rust,
		Code: `fn main() {
println!("{}", std::env::current_dir().unwrap().display());
println!("{}", std::env::current_exe().unwrap().display());
println!("{}", std::env::var("TMPDIR").unwrap());
}`,
	})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode, "%+v", result)
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	require.Len(t, lines, 3)
	project, err := filepath.EvalSymlinks(e.projectDir)
	require.NoError(t, err)
	assert.Equal(t, project, lines[0])
	compileDir, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(compileDir)), filepath.Dir(lines[1]))
	assert.NotEqual(t, project, filepath.Dir(lines[1]))
	assert.Equal(t, filepath.Base(lines[2]), filepath.Base(filepath.Dir(lines[1])))
	assert.NoDirExists(t, lines[2], "foreground temporary workspace is cleaned")
}

func TestExecuteRelativeTempDirectory(t *testing.T) {
	for _, lang := range []Language{Shell, Python, Rust} {
		t.Run(string(lang), func(t *testing.T) {
			e := newTestExecutor(t)
			if e.Runtimes()[lang] == "" {
				t.Skipf("no %s runtime", lang)
			}
			tempRoot := t.TempDir()
			processDir, err := os.Getwd()
			require.NoError(t, err)
			relativeTemp, err := filepath.Rel(processDir, tempRoot)
			require.NoError(t, err)
			// A deeper project prevents leading .. components from coincidentally
			// reaching the filesystem root from both directories.
			for range strings.Count(processDir, string(os.PathSeparator)) + 1 {
				e.projectDir = filepath.Join(e.projectDir, "nested")
			}
			require.NoError(t, os.MkdirAll(e.projectDir, 0o755))
			t.Setenv("TMPDIR", relativeTemp)
			code := map[Language]string{
				Shell:  `printf '%s' "$TMPDIR"`,
				Python: `import os; print(os.environ["TMPDIR"])`,
				Rust:   `fn main() { println!("{}", std::env::var("TMPDIR").unwrap()); }`,
			}[lang]
			result, err := e.Execute(context.Background(), ExecRequest{Language: lang, Code: code})
			require.NoError(t, err)
			require.Equal(t, 0, result.ExitCode, "%+v", result)
			tmpDir := strings.TrimSpace(result.Stdout)
			require.True(t, filepath.IsAbs(tmpDir), "child TMPDIR must survive a cwd change")
			assert.Equal(t, tempRoot, filepath.Dir(tmpDir))
			assert.NoDirExists(t, tmpDir)
		})
	}
}

func TestExecuteGoProjectModules(t *testing.T) {
	isolateGoExecution(t)
	cases := []struct {
		name      string
		files     map[string]string
		code      string
		want      string
		wantError string
		workspace bool
		toolchain string
	}{
		{
			name: "local import and compatible toolchain",
			files: map[string]string{
				"go.mod":         "module example.test/project\n\ngo 1.21.0\ntoolchain go1.21.1\n",
				"local/value.go": "package local\nconst Value = \"local-module\"\n",
			},
			code: "package main\nimport (\"fmt\"; \"example.test/project/local\")\nfunc main() { fmt.Print(local.Value) }",
			want: "local-module",
		},
		{
			name: "relative replace",
			files: map[string]string{
				"go.mod":              "module example.test/project\n\ngo 1.21.0\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => ./dependency\n",
				"dependency/go.mod":   "module example.test/dependency\n\ngo 1.21.0\n",
				"dependency/value.go": "package dependency\nconst Value = \"replacement\"\n",
			},
			code: "package main\nimport (\"fmt\"; \"example.test/dependency\")\nfunc main() { fmt.Print(dependency.Value) }",
			want: "replacement",
		},
		{
			name: "automatic vendor",
			files: map[string]string{
				"go.mod":             "module example.test/project\n\ngo 1.21.0\nrequire example.test/dependency v0.0.0\n",
				"vendor/modules.txt": "# example.test/dependency v0.0.0\n## explicit; go 1.21.0\nexample.test/dependency\n",
				"vendor/example.test/dependency/value.go": "package dependency\nconst Value = \"vendored\"\n",
			},
			code: "package main\nimport (\"fmt\"; \"example.test/dependency\")\nfunc main() { fmt.Print(dependency.Value) }",
			want: "vendored",
		},
		{
			name: "workspace discovery",
			files: map[string]string{
				"go.mod":              "module example.test/project\n\ngo 1.21.0\n",
				"go.work":             "go 1.21.0\nuse (\n.\n./dependency\n)\n",
				"dependency/go.mod":   "module example.test/dependency\n\ngo 1.21.0\n",
				"dependency/value.go": "package dependency\nconst Value = \"workspace\"\n",
			},
			code:      "package main\nimport (\"fmt\"; \"example.test/dependency\")\nfunc main() { fmt.Print(dependency.Value) }",
			want:      "workspace",
			workspace: true,
		},
		{
			name:      "module requires newer Go",
			files:     map[string]string{"go.mod": "module example.test/project\n\ngo 1.99.0\n"},
			code:      `fmt.Print("must not run")`,
			wantError: "requires go >= 1.99.0",
		},
		{
			name:      "project toolchain selection",
			files:     map[string]string{"go.mod": "module example.test/project\n\ngo 1.21.0\ntoolchain go1.99.0\n"},
			code:      `fmt.Print("must not run")`,
			toolchain: "auto",
			wantError: "go1.99.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestExecutor(t)
			if e.Runtimes()[Go] == "" {
				t.Skip("no Go runtime")
			}
			if tc.workspace {
				t.Setenv("GOWORK", "")
			}
			if tc.toolchain != "" {
				t.Setenv("GOTOOLCHAIN", tc.toolchain)
			}
			for name, content := range tc.files {
				writeCwdFixture(t, e.projectDir, name, content)
			}
			result, err := e.Execute(context.Background(), ExecRequest{Language: Go, Code: tc.code, TimeoutSec: 60})
			require.NoError(t, err)
			if tc.wantError != "" {
				require.NotEqual(t, 0, result.ExitCode, "%+v", result)
				assert.Contains(t, result.Stderr, tc.wantError)
				assert.Empty(t, result.Stdout)
				return
			}
			require.Equal(t, 0, result.ExitCode, "%+v", result)
			assert.Equal(t, tc.want, result.Stdout)
		})
	}
}

func TestAutoWrapElixirProjectDetection(t *testing.T) {
	project := t.TempDir()
	code := `IO.puts("marker")`
	assert.Equal(t, code, autoWrap(Elixir, code, project))
	writeCwdFixture(t, project, "mix.exs", "# project marker")
	assert.Contains(t, autoWrap(Elixir, code, project), `Path.wildcard("_build/dev/lib/*/ebin")`)
	assert.Equal(t, code, autoWrap(Elixir, code, t.TempDir()), "another project must not inherit Mix detection")
}

func TestExecuteElixirProjectPaths(t *testing.T) {
	e := newTestExecutor(t)
	if e.Runtimes()[Elixir] == "" {
		t.Skip("no Elixir runtime")
	}
	writeCwdFixture(t, e.projectDir, "mix.exs", "# project marker")
	// autoWrap prepends these project-relative directories without invoking Mix.
	ebin := filepath.Join(e.projectDir, "_build", "dev", "lib", "fixture", "ebin")
	require.NoError(t, os.MkdirAll(ebin, 0o755))
	result, err := e.Execute(context.Background(), ExecRequest{
		Language: Elixir,
		Code: `expected = Path.expand("_build/dev/lib/fixture/ebin")
IO.puts(Enum.any?(:code.get_path(), fn path -> Path.expand(to_string(path)) == expected end))`,
	})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode, "%+v", result)
	assert.Equal(t, "true", strings.TrimSpace(result.Stdout))
}
