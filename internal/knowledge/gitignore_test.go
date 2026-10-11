package knowledge

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git not installed")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestDirectoryGitIgnoresUseInputWorktree(t *testing.T) {
	i, st, project := testIndexer(t, 0, "")
	fixtureGit(t, project, "init", "-q")
	writeFile(t, filepath.Join(project, ".gitignore"), "*.md\n")
	external := t.TempDir()
	fixtureGit(t, external, "init", "-q")
	directoryFiles(t, external, map[string]string{
		".gitignore":   "docs/*.md\n!docs/keep.md\n!docs/new*\n!docs/-*\n!docs/tab*\n",
		"docs/keep.md": "keep", "docs/tracked.md": "tracked but ignored", "docs/new\nline.md": "newline",
		"docs/-option.md": "dash", "docs/tab\tname.md": "tab", "docs/sub/.gitignore": "*.md\n!allowed.md\n",
		"docs/sub/allowed.md": "allowed", "docs/sub/hidden.md": "hidden",
		"docs/nested/.git": "gitdir: missing\n", "docs/nested/private.md": "nested",
	})
	fixtureGit(t, external, "add", "-f", "docs/tracked.md")
	t.Setenv("GIT_DIR", filepath.Join(project, ".git"))
	t.Setenv("GIT_WORK_TREE", project)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.excludesFile")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(project, ".gitignore"))
	out, err := i.IndexDirectory(t.Context(), filepath.Join(external, "docs"), "external", DirectoryOptions{})
	want := []string{"external:-option.md", "external:keep.md", "external:new\nline.md", "external:sub/allowed.md", "external:tab\tname.md"}
	if err != nil || !out.Complete || out.Indexed != len(want) || !strings.Contains(out.GitIgnoreStatus, external) {
		t.Fatalf("external Git scan = %+v, %v", out, err)
	}
	if got := sourceLabels(t, st); !slices.Equal(got, want) {
		t.Fatalf("Git ignores failed: got %q want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(external, "knowledge.db")); !os.IsNotExist(err) {
		t.Fatalf("external input changed DB ownership: %v", err)
	}
}

func TestDirectoryNestedGitBoundaries(t *testing.T) {
	for _, respect := range []bool{true, false} {
		t.Run(map[bool]string{true: "ignores", false: "disabled"}[respect], func(t *testing.T) {
			i, st, project := testIndexer(t, 0, "")
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{"plain.md": "plain", "repo/child.md": "repo", "worktree/child.md": "worktree", "submodule/child.md": "submodule"})
			fixtureGit(t, root, "init", "-q")
			fixtureGit(t, filepath.Join(root, "repo"), "init", "-q")
			fixtureGit(t, filepath.Join(root, "repo"), "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "--allow-empty", "-m", "fixture", "-q")
			if err := os.Remove(filepath.Join(root, "worktree", "child.md")); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, filepath.Join(root, "repo"), "worktree", "add", "--detach", filepath.Join(root, "worktree"), "HEAD")
			writeFile(t, filepath.Join(root, "worktree", "child.md"), "worktree")
			// A real submodule-layout gitfile; no network clone required.
			if err := os.MkdirAll(filepath.Join(root, ".git", "modules"), 0o700); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, filepath.Join(root, "submodule"), "init", "-q", "--separate-git-dir", filepath.Join(root, ".git", "modules", "child"))
			out, err := i.IndexDirectory(t.Context(), root, "outer", DirectoryOptions{RespectGitignore: &respect})
			if err != nil || out.Indexed != 1 || out.Skipped != 4 {
				t.Fatalf("nested boundary = %+v, %v", out, err)
			}
			if got := sourceLabels(t, st); !slices.Equal(got, []string{"outer:plain.md"}) {
				t.Fatalf("crossed Git boundary: %v", got)
			}
			for _, name := range []string{"repo", "worktree", "submodule"} {
				out, err := i.IndexDirectory(t.Context(), filepath.Join(root, name), "explicit-"+name, DirectoryOptions{})
				if err != nil || out.Indexed != 1 {
					t.Fatalf("explicit %s = %+v, %v", name, out, err)
				}
			}
		})
	}
}

func TestDirectoryGitFailuresAndNonGit(t *testing.T) {
	for _, name := range []string{"non-git-no-binary", "git-no-binary", "broken-marker", "check-ignore-failure"} {
		t.Run(name, func(t *testing.T) {
			i, _, project := testIndexer(t, 0, "")
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{"a.md": "a"})
			if name != "non-git-no-binary" {
				fixtureGit(t, root, "init", "-q")
			}
			switch name {
			case "non-git-no-binary", "git-no-binary":
				t.Setenv("PATH", t.TempDir())
			case "broken-marker":
				if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(root, ".git"), "gitdir: missing\n")
			case "check-ignore-failure":
				bin := t.TempDir()
				fake := filepath.Join(bin, "git")
				if err := os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = rev-parse ]; then pwd; else echo fixture-error >&2; exit 128; fi\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin)
			}
			out, err := i.IndexDirectory(t.Context(), root, "", DirectoryOptions{})
			if name == "non-git-no-binary" {
				if err != nil || !out.Complete || out.Indexed != 1 || !strings.Contains(out.GitIgnoreStatus, "outside") {
					t.Fatalf("non-Git = %+v, %v", out, err)
				}
				return
			}
			if err == nil || out.Complete || out.Indexed != 0 || out.Failed != 1 {
				t.Fatalf("Git failure hidden: %+v, %v", out, err)
			}
		})
	}
}

func TestDirectoryGitIgnoreBatches(t *testing.T) {
	i, _, project := testIndexer(t, 0, "")
	root := filepath.Join(project, "docs")
	directoryFiles(t, root, map[string]string{".gitignore": "*.md\n"})
	fixtureGit(t, root, "init", "-q")
	for n := 0; n < 3*gitBatchSize; n++ {
		writeFile(t, filepath.Join(root, strings.Repeat("x", n/200+1)+string(rune(0x100+n))+".md"), "ignored")
	}
	out, err := i.IndexDirectory(t.Context(), root, "", DirectoryOptions{})
	if err != nil || !out.Complete || out.Attempts != 0 || out.Skipped != 3*gitBatchSize+2 {
		t.Fatalf("batched ignores = %+v, %v", out, err)
	}
}
