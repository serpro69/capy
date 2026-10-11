package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
)

func runIndexCLI(t *testing.T, command stdioCommand) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), stdioTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.executable, command.args...)
	cmd.Dir, cmd.Env = command.dir, command.env
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	for _, secret := range command.secrets {
		if secret != "" && strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatal("credential appeared in CLI output")
		}
	}
	if ctx.Err() != nil {
		t.Fatal("index CLI timed out")
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("start index CLI: %v", err)
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

func indexLabels(t *testing.T, project, key string) []string {
	t.Helper()
	st := store.NewContentStore(filepath.Join(project, "knowledge.db"), project, 0, 0, store.WithEncryptionKey(key, "test"))
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	sources, err := st.ListSources()
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		if source.Kind != store.KindDurable {
			t.Fatalf("non-durable CLI source: %+v", source)
		}
		labels = append(labels, source.Label)
	}
	slices.Sort(labels)
	return labels
}

func TestIndexCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	const key = "synthetic-index-cli-project-passphrase-12345"
	const otherKey = "synthetic-index-cli-wrong-passphrase-67890"
	fixture := func(t *testing.T) (string, stdioCommand) {
		t.Helper()
		project := stdioProject(t)
		return project, stdioCommand{
			executable: bin, dir: t.TempDir(), env: stdioEnv(t, key), secrets: []string{key, otherKey},
		}
	}
	t.Run("help and invalid inputs", func(t *testing.T) {
		project, command := fixture(t)
		writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = 'absent/knowledge.db'\n")
		command.env = stdioEnv(t, "") // help/validation cannot require credentials
		command.args = []string{"index", "--project-dir", project, "--help"}
		out, stderr, code := runIndexCLI(t, command)
		if code != 0 || stderr != "" {
			t.Fatalf("help: code=%d, stderr=%s", code, stderr)
		}
		for _, want := range []string{"index <path>", "selected --project-dir", "--max-depth", "--max-files", "--extensions", "--include", "--exclude", "--respect-gitignore", "--follow-symlinks", "exit 0", "exit 1"} {
			if !strings.Contains(out, want) {
				t.Errorf("help missing %q", want)
			}
		}
		for _, tc := range []struct {
			name string
			args []string
		}{
			{name: "missing"}, {name: "extra", args: []string{"docs", "extra"}},
			{name: "empty", args: []string{""}},
			{name: "depth", args: []string{"docs", "--max-depth=-1"}},
			{name: "fraction", args: []string{"docs", "--max-depth=0.5"}},
			{name: "files", args: []string{"docs", "--max-files=0"}},
			{name: "include", args: []string{"docs", "--include=../secret"}},
			{name: "exclude", args: []string{"docs", "--exclude=[ab]"}},
			{name: "extension", args: []string{"docs", "--extensions=*.md"}},
			{name: "boolean", args: []string{"docs", "--follow-symlinks=yes"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				command.args = append([]string{"index", "--project-dir", project}, tc.args...)
				_, stderr, code := runIndexCLI(t, command)
				if code != 1 || strings.Contains(stderr, "resolving knowledge credential") {
					t.Fatalf("validation order/status: %d, %s", code, stderr)
				}
				if _, err := os.Stat(filepath.Join(project, "absent")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid arguments created DB directory: %v", err)
				}
			})
		}
	})

	for _, mode := range []string{"environment", "dotenv", "key-file", "linked-owner", "external-input"} {
		t.Run(mode+" round trip", func(t *testing.T) {
			project, command := fixture(t)
			owner := project
			if mode == "linked-owner" {
				owner = t.TempDir()
				gitDir := filepath.Join(owner, ".git", "worktrees", "linked")
				writeCLIKeyFixture(t, filepath.Join(gitDir, "commondir"), "../..\n")
				writeCLIKeyFixture(t, filepath.Join(project, ".git"), "gitdir: "+gitDir+"\n")
				writeCLIKeyFixture(t, filepath.Join(project, ".env"), "CAPY_DB_KEY="+otherKey)
			}
			if mode != "environment" && mode != "external-input" {
				command.env = stdioEnv(t, otherKey)
				if mode == "key-file" {
					writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='knowledge.db'\nkey_file='db.key'\n")
					writeCLIKeyFixture(t, filepath.Join(owner, "db.key"), key)
				} else {
					writeCLIKeyFixture(t, filepath.Join(owner, ".env"), "CAPY_DB_KEY="+key)
				}
			}
			input := "note.md"
			fileRoot := project
			if mode == "external-input" {
				fileRoot = stdioProject(t)
				writeCLIKeyFixture(t, filepath.Join(fileRoot, ".env"), "CAPY_DB_KEY="+otherKey)
				input = filepath.Join(fileRoot, "note.md")
			}
			writeCLIKeyFixture(t, filepath.Join(fileRoot, "note.md"), "# Orchard\n\nThe transport orchard retains amberlynx.")
			command.args = []string{"index", input, "--project-dir", project, "--source", "cli:orchard"}
			for _, status := range []string{"Indexed", "Unchanged"} {
				out, stderr, code := runIndexCLI(t, command)
				if code != 0 || !strings.Contains(out, status+` "cli:orchard": 1 retained sections`) {
					t.Fatalf("file index: code=%d, stdout=%s, stderr=%s", code, out, stderr)
				}
				stdioCheckpointed(t, owner)
			}
			p := startStdioProcess(t, stdioCommand{
				executable: bin, args: []string{"serve", "--project-dir", project}, dir: command.dir, env: command.env, secrets: command.secrets,
			})
			p.initialize(t)
			stdioSearch(t, p, "amberlynx", "absentotter")
			p.close(t)
			stdioCheckpointed(t, owner)
			if mode == "external-input" || owner != project {
				absent := fileRoot
				if owner != project {
					absent = project
				}
				if _, err := os.Stat(filepath.Join(absent, "knowledge.db")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("wrong DB owner created a database: %v", err)
				}
			}
		})
	}

	t.Run("directory controls and labels", func(t *testing.T) {
		project, command := fixture(t)
		for _, name := range []string{"a.md", "b.go", "sub/c.txt", "sub/drop.md", ".env.md"} {
			writeCLIKeyFixture(t, filepath.Join(project, "docs", name), "orchard "+name)
		}
		if err := os.Symlink("sub", filepath.Join(project, "docs", "alias")); err != nil {
			t.Fatal(err)
		}
		command.args = []string{"index", "docs", "--project-dir", project, "--source=cli:docs", "--max-depth=0", "--max-files=1"}
		out, stderr, code := runIndexCLI(t, command)
		if code != 0 || !strings.Contains(out, "incomplete") || !strings.Contains(out, "max_files") {
			t.Fatalf("cap outcome: %d, %s, %s", code, out, stderr)
		}
		command.args = []string{"index", "docs", "--project-dir", project, "--source=cli:docs", "--extensions=MD,txt", "--include=a.md", "--include=sub/*", "--exclude=sub/drop.md", "--respect-gitignore=false", "--follow-symlinks"}
		out, stderr, code = runIndexCLI(t, command)
		if code != 0 || !strings.Contains(out, "Directory scan: complete") || !strings.Contains(out, "Indexed: 1; unchanged: 1") {
			t.Fatalf("selected outcome: %d, %s, %s", code, out, stderr)
		}
		stdioCheckpointed(t, project)
		if got := indexLabels(t, project, key); !slices.Equal(got, []string{"cli:docs:a.md", "cli:docs:sub/c.txt"}) {
			t.Fatalf("labels/filters differ from MCP: %v", got)
		}
	})

	t.Run("partial errors retain committed files", func(t *testing.T) {
		project, command := fixture(t)
		writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='knowledge.db'\nmax_source_bytes=32\n")
		writeCLIKeyFixture(t, filepath.Join(project, "docs", "a.md"), "retained first file")
		writeCLIKeyFixture(t, filepath.Join(project, "docs", "b.md"), strings.Repeat("x", 33))
		writeCLIKeyFixture(t, filepath.Join(project, "docs", "c.md"), "retained last file")
		command.args = []string{"index", "docs", "--project-dir", project, "--source=partial"}
		out, stderr, code := runIndexCLI(t, command)
		if code != 1 || !strings.Contains(out, "partial error") || !strings.Contains(out, "Indexed: 2") || !strings.Contains(out, "failed: 1") || strings.Contains(stderr, "Usage:") {
			t.Fatalf("partial exit/summary: %d, %s, %s", code, out, stderr)
		}
		stdioCheckpointed(t, project)
		if got := indexLabels(t, project, key); !slices.Equal(got, []string{"partial:a.md", "partial:c.md"}) {
			t.Fatalf("lost progress or stored partial input: %v", got)
		}
	})

	for _, flag := range []string{"follow-symlinks", "respect-gitignore"} {
		t.Run(flag+" controls traversal", func(t *testing.T) {
			project, command := fixture(t)
			root := filepath.Join(project, "docs")
			writeCLIKeyFixture(t, filepath.Join(root, "a.md"), "retained")
			if flag == "follow-symlinks" {
				if err := os.Symlink("missing", filepath.Join(root, "broken.md")); err != nil {
					t.Fatal(err)
				}
			} else {
				writeCLIKeyFixture(t, filepath.Join(root, ".git"), "gitdir: missing\n")
			}
			for _, value := range []string{"false", "true"} {
				command.args = []string{"index", "docs", "--project-dir", project, "--" + flag + "=" + value}
				out, stderr, code := runIndexCLI(t, command)
				wantCode := 0
				if value == "true" {
					wantCode = 1
				}
				if code != wantCode {
					t.Fatalf("%s=%s: code=%d, %s, %s", flag, value, code, out, stderr)
				}
				stdioCheckpointed(t, project)
			}
		})
	}

	for _, mode := range []string{"missing-key", "invalid-key-file", "malformed-policy", "denied-file", "all-denied", "empty", "all-filtered"} {
		t.Run(mode, func(t *testing.T) {
			project, command := fixture(t)
			writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='absent/knowledge.db'\n")
			writeCLIKeyFixture(t, filepath.Join(project, "docs", "a.md"), "never stored")
			input := "docs"
			args := []string{}
			wantCode := 1
			switch mode {
			case "missing-key":
				command.env = stdioEnv(t, "")
			case "invalid-key-file":
				writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='absent/knowledge.db'\nkey_file='db.key'\n")
				writeCLIKeyFixture(t, filepath.Join(project, "db.key"), key+"\n\n")
			case "malformed-policy":
				writeCLIKeyFixture(t, filepath.Join(project, ".claude", "settings.json"), "invalid")
			case "denied-file", "all-denied":
				writeCLIKeyFixture(t, filepath.Join(project, ".claude", "settings.json"), `{"permissions":{"deny":["Read(docs/a.md)"]}}`)
				if mode == "denied-file" {
					input = "docs/a.md"
				} else {
					wantCode = 0
				}
			case "empty":
				input, wantCode = "empty", 0
				if err := os.Mkdir(filepath.Join(project, input), 0o700); err != nil {
					t.Fatal(err)
				}
			case "all-filtered":
				args, wantCode = []string{"--extensions=go"}, 0
			}
			command.args = append([]string{"index", input, "--project-dir", project}, args...)
			out, stderr, code := runIndexCLI(t, command)
			if code != wantCode || (code == 0 && !strings.Contains(out, "Indexed: 0")) {
				t.Fatalf("admission outcome: %d, %s, %s", code, out, stderr)
			}
			if _, err := os.Stat(filepath.Join(project, "absent")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inadmissible input created storage: %v", err)
			}
		})
	}

	t.Run("wrong selected key preserves existing database", func(t *testing.T) {
		project, command := fixture(t)
		writeCLIKeyFixture(t, filepath.Join(project, "a.md"), "original orchard")
		command.args = []string{"index", "a.md", "--project-dir", project, "--source=original"}
		if out, stderr, code := runIndexCLI(t, command); code != 0 {
			t.Fatalf("seed: %d, %s, %s", code, out, stderr)
		}
		writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='knowledge.db'\nkey_file='db.key'\n")
		writeCLIKeyFixture(t, filepath.Join(project, "db.key"), otherKey)
		command.args = []string{"index", "a.md", "--project-dir", project, "--source=must-not-exist"}
		out, stderr, code := runIndexCLI(t, command)
		if code != 1 || !strings.Contains(stderr, "wrong passphrase or corrupted database") || strings.Contains(out+stderr, "cipher=") {
			t.Fatalf("wrong-key outcome: %d, %s, %s", code, out, stderr)
		}
		if got := indexLabels(t, project, key); !slices.Equal(got, []string{"original"}) {
			t.Fatalf("wrong key damaged prior content: %v", got)
		}
	})
}

func TestIndexCommandCanceledBeforeStorage(t *testing.T) {
	project := stdioProject(t)
	cmd := newIndexCmd()
	cmd.Flags().String("project-dir", project, "fixture project")
	cmd.SetArgs([]string{"docs"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command = %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "knowledge.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled command opened storage: %v", err)
	}
}

type indexBrokenOutput struct{}

func (indexBrokenOutput) Write([]byte) (int, error) { return 0, syscall.EPIPE }

type indexCancelingOutput struct {
	cancel context.CancelFunc
}

func (w indexCancelingOutput) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func TestIndexCommandLateCancellationRetainsCommits(t *testing.T) {
	for _, mode := range []string{"file", "directory"} {
		t.Run(mode, func(t *testing.T) {
			const key = "synthetic-index-cli-late-cancellation-key-12345"
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("CAPY_DB_KEY", key)
			project := stdioProject(t)
			writeCLIKeyFixture(t, filepath.Join(project, "docs", "note.md"), "retained after final write")
			input, label := "docs/note.md", "retained"
			if mode == "directory" {
				input, label = "docs", "retained:note.md"
			}
			cmd := newIndexCmd()
			cmd.Flags().String("project-dir", project, "fixture project")
			cmd.SetArgs([]string{input, "--source=retained"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// Cancel after the final content commit, just as the command enters
			// reporting and checkpoint cleanup. A successful writer cannot mask it.
			cmd.SetOut(indexCancelingOutput{cancel: cancel})
			cmd.SetErr(&bytes.Buffer{})
			if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("late cancellation reported success: %v", err)
			}
			stdioCheckpointed(t, project)
			if got := indexLabels(t, project, key); !slices.Equal(got, []string{label}) {
				t.Fatalf("late cancellation lost committed content: %v", got)
			}
		})
	}
}

func TestIndexCommandOutputFailureStillCloses(t *testing.T) {
	const key = "synthetic-index-cli-output-failure-key-12345"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CAPY_DB_KEY", key)
	project := stdioProject(t)
	writeCLIKeyFixture(t, filepath.Join(project, "note.md"), "retained despite output failure")
	cmd := newIndexCmd()
	cmd.Flags().String("project-dir", project, "fixture project")
	cmd.SetArgs([]string{"note.md", "--source=retained"})
	cmd.SetOut(indexBrokenOutput{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("output failure = %v", err)
	}
	stdioCheckpointed(t, project)
	if got := indexLabels(t, project, key); !slices.Equal(got, []string{"retained"}) {
		t.Fatalf("output failure lost committed content: %v", got)
	}
}

func TestIndexCLISignalRetainsProgress(t *testing.T) {
	bin := buildStdioCapy(t)
	const key = "synthetic-index-cli-cancellation-key-12345"
	project, env := stdioProject(t), stdioEnv(t, key)
	root := filepath.Join(project, "docs")
	writeCLIKeyFixture(t, filepath.Join(root, "a.md"), "retained")
	writeCLIKeyFixture(t, filepath.Join(root, "zdir", "b.md"), "not reached")
	writeCLIKeyFixture(t, filepath.Join(root, ".git", "HEAD"), "fixture discovery marker")
	gitBin := t.TempDir()
	git := filepath.Join(gitBin, "git")
	// First ignore batch admits a.md. The nested-directory batch announces
	// readiness, then blocks until CommandContext observes CLI cancellation.
	script := `#!/bin/sh
case "$1" in
rev-parse) printf '%s\n' "$PWD" ;;
check-ignore)
  cat >/dev/null
  if [ -f "$PWD/checked" ]; then
    : > "$PWD/ready"
    exec sleep 30
  fi
  : > "$PWD/checked"
  exit 1 ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(git, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	env = append(env, "PATH="+gitBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), stdioTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "index", "docs", "--project-dir", project, "--source=signal")
	cmd.Dir, cmd.Env, cmd.WaitDelay = t.TempDir(), env, time.Second
	var stdout, stderr stdioLog
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(waited)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			t.Error("index child was not reaped")
		}
	})
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			break
		}
		select {
		case <-waited:
			t.Fatalf("index exited before cancellation fixture: %v, %s", waitErr, stderr.String())
		case <-ctx.Done():
			t.Fatal("index did not reach cancellation fixture")
		case <-ticker.C:
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) || exit.ExitCode() != 1 || !strings.Contains(stdout.String(), "partial error") || !strings.Contains(stdout.String(), "Indexed: 1") {
			t.Fatalf("signal outcome: %v, %s, %s", waitErr, stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("index did not shut down after SIGTERM")
	}
	stdioCheckpointed(t, project)
	if got := indexLabels(t, project, key); !slices.Equal(got, []string{"signal:a.md"}) {
		t.Fatalf("cancellation lost committed progress: %v", got)
	}
}
