package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/fileio"
	"github.com/serpro69/capy/internal/store"
)

func directoryLink(t *testing.T, root, alias, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(root, alias)); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryFollowSymlinksIdentity(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(fmt.Sprintf("follow-%t", follow), func(t *testing.T) {
			i, st, project := testIndexer(t, 0, "")
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{"real/a.md": "alpha", "real/b.txt": "bravo"})
			directoryLink(t, root, "aaa", "real")
			directoryLink(t, root, "bbb", "real")
			directoryLink(t, root, "aaa.md", "real/a.md")
			directoryLink(t, root, "real/cycle", "..")
			directoryLink(t, root, "real/self", ".")
			directoryLink(t, project, "root-alias", "docs")
			options := DirectoryOptions{FollowSymlinks: follow}
			for n := range 2 {
				out, err := i.IndexDirectory(t.Context(), "root-alias", "prefix:v1", options)
				if err != nil || !out.Complete || out.Attempts != 2 || out.BytesRead != 10 || out.Chunks != 2 || out.Visited != 8 {
					t.Fatalf("scan = %+v, %v", out, err)
				}
				if out.Indexed != 2*(1-n) || out.Unchanged != 2*n {
					t.Fatalf("incorrect dedup: %+v", out)
				}
			}
			if got := sourceLabels(t, st); !slices.Equal(got, []string{"prefix:v1:real/a.md", "prefix:v1:real/b.txt"}) {
				t.Fatalf("alias labels: %v", got)
			}
			checks := 0
			st.SetDenyChecker(func(path string) bool {
				checks++
				if !strings.HasPrefix(path, filepath.Join(root, "real")+"/") {
					t.Errorf("noncanonical refresh path: %q", path)
				}
				return false
			})
			if _, err := st.SearchWithFallbackContext(t.Context(), "alpha", 1, store.SearchOptions{}); err != nil || checks != 2 {
				t.Fatalf("file-backed refresh: checks=%d, err=%v", checks, err)
			}
		})
	}
}

func TestDirectoryFollowSymlinksFilters(t *testing.T) {
	for _, tc := range []struct {
		name, target, alias, settings string
		options                       DirectoryOptions
	}{
		{name: "credential-file", target: ".env.md", alias: "innocent.md"},
		{name: "credential-directory", target: ".capy/private.md", alias: "innocent"},
		{name: "credential-ancestor", target: ".capy/sub/private.md", alias: "innocent.md"},
		{name: "denied-file", target: "real/private.md", alias: "innocent.md", settings: `{"permissions":{"deny":["Read(docs/real/private.md)"]}}`},
		{name: "denied-ancestor", target: "real/private.md", alias: "innocent.md", settings: `{"permissions":{"deny":["Read(docs/real)"]}}`},
		{name: "excluded-file", target: "real/private.md", alias: "innocent.md", options: DirectoryOptions{Exclude: []string{"real/private.md"}}},
		{name: "excluded-ancestor", target: "real/private.md", alias: "innocent.md", options: DirectoryOptions{Exclude: []string{"real"}}},
		{name: "extension", target: "real/private.bin", alias: "innocent.md"},
		{name: "include", target: "real/private.md", alias: "innocent.md", options: DirectoryOptions{Include: []string{"innocent.md"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, st, project := testIndexer(t, 0, tc.settings)
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{tc.target: "must never read"})
			target := tc.target
			if tc.name == "credential-directory" {
				target = ".capy"
			}
			directoryLink(t, root, tc.alias, target)
			i.readContent = func(io.Reader, int) ([]byte, error) {
				t.Fatal("excluded/denied target opened for content")
				return nil, nil
			}
			tc.options.FollowSymlinks = true
			out, err := i.IndexDirectory(t.Context(), root, "filters", tc.options)
			if err != nil || !out.Complete || out.Attempts != 0 || out.BytesRead != 0 || len(sourceLabels(t, st)) != 0 {
				t.Fatalf("filter bypassed: %+v, %v", out, err)
			}
		})
	}
}

func TestDirectoryFollowSymlinksAliasPolicy(t *testing.T) {
	for _, deny := range []string{"docs/aaa", "docs/aaa/private.md", "root-alias/aaa/private.md"} {
		t.Run(deny, func(t *testing.T) {
			i, st, project := testIndexer(t, 0, fmt.Sprintf(`{"permissions":{"deny":["Read(%s)"]}}`, deny))
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{"real/private.md": "permitted through real path"})
			directoryLink(t, root, "aaa", "real")
			directoryLink(t, project, "root-alias", "docs")
			out, err := i.IndexDirectory(t.Context(), "root-alias", "policy", DirectoryOptions{FollowSymlinks: true})
			if err != nil || !out.Complete || out.Denied != 1 || out.Attempts != 1 || out.Indexed != 1 {
				t.Fatalf("alias denied or real path lost: %+v, %v", out, err)
			}
			if got := sourceLabels(t, st); !slices.Equal(got, []string{"policy:real/private.md"}) {
				t.Fatalf("unexpected labels: %v", got)
			}
		})
	}
}

func TestDirectoryFollowSymlinksCanonicalSelection(t *testing.T) {
	i, st, project := testIndexer(t, 0, "")
	root := filepath.Join(project, "docs")
	directoryFiles(t, root, map[string]string{"real/keep.md": "keep", "real/drop.md": "drop"})
	directoryLink(t, root, "aaa.bin", "real/keep.md")
	directoryLink(t, root, "bbb", "real")
	directoryLink(t, root, ".env.alias", "real/keep.md")
	files := 1
	out, err := i.IndexDirectory(t.Context(), root, "selection", DirectoryOptions{
		FollowSymlinks: true, MaxFiles: &files, Include: []string{"real/*.md"}, Exclude: []string{"real/drop.md", "aaa.bin", "bbb"},
	})
	if err != nil || !out.Complete || out.Attempts != 1 || out.Indexed != 1 {
		t.Fatalf("canonical selection/dedup = %+v, %v", out, err)
	}
	if got := sourceLabels(t, st); !slices.Equal(got, []string{"selection:real/keep.md"}) {
		t.Fatalf("selection used alias: %v", got)
	}
}

func TestDirectoryFollowSymlinksErrorsAndContainment(t *testing.T) {
	i, st, project := testIndexer(t, 0, "")
	root := filepath.Join(project, "docs")
	directoryFiles(t, root, map[string]string{"a.md": "retained"})
	outside := t.TempDir()
	directoryFiles(t, outside, map[string]string{"outside.md": "outside"})
	directoryLink(t, root, "external", outside)
	directoryLink(t, root, "external.md", filepath.Join(outside, "outside.md"))
	directoryLink(t, root, "loop", "loop")
	for n := range 12 {
		directoryLink(t, root, fmt.Sprintf("broken%d.md", n), "missing")
	}
	out, err := i.IndexDirectory(t.Context(), root, "partial", DirectoryOptions{FollowSymlinks: true})
	if err == nil || out.Complete || out.Failed != 13 || len(out.Failures) != 10 || out.OmittedFailures != 3 || out.Indexed != 1 || out.Attempts != 1 || out.Skipped != 2 {
		t.Fatalf("bad link outcomes = %+v, %v", out, err)
	}
	if got := sourceLabels(t, st); !slices.Equal(got, []string{"partial:a.md"}) {
		t.Fatalf("escaped root or lost partial progress: %v", got)
	}
}

func TestDirectoryFollowSymlinksLimits(t *testing.T) {
	for _, name := range []string{"depth", "files", "bytes", "cancellation"} {
		t.Run(name, func(t *testing.T) {
			i, st, project := testIndexer(t, maxDirectoryBytes+100, "")
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{"real/deep/a.md": "alpha", "real/deep/b.md": "bravo"})
			directoryLink(t, root, "aaa.md", "real/deep/a.md")
			directoryLink(t, root, "bbb", "real/deep")
			options := DirectoryOptions{FollowSymlinks: true}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := "max_" + name
			switch name {
			case "depth":
				depth := 0
				options.MaxDepth = &depth
			case "files":
				files := 1
				options.MaxFiles = &files
			case "bytes":
				i.readContent = func(r io.Reader, limit int) ([]byte, error) {
					if err := os.Truncate(filepath.Join(root, "real/deep/a.md"), maxDirectoryBytes+1); err != nil {
						t.Fatal(err)
					}
					return fileio.ReadAll(r, limit)
				}
			case "cancellation":
				i.readContent = func(r io.Reader, limit int) ([]byte, error) {
					cancel()
					return fileio.ReadAll(r, limit)
				}
			}
			out, err := i.IndexDirectory(ctx, root, "bounds", options)
			if out.Complete {
				t.Fatalf("claimed complete: %+v", out)
			}
			if name == "cancellation" {
				if !errors.Is(err, context.Canceled) || out.Failed != 1 || out.Attempts != 1 {
					t.Fatalf("cancellation = %+v, %v", out, err)
				}
			} else if err != nil || !slices.Equal(out.Limits, []string{want}) {
				t.Fatalf("limit = %+v, %v", out, err)
			}
			if name == "files" {
				if out.Attempts != 1 || out.Indexed != 1 {
					t.Fatalf("file cap = %+v", out)
				}
			} else if len(sourceLabels(t, st)) != 0 {
				t.Fatal("partial or too-deep content indexed")
			}
			if name == "bytes" && out.BytesRead != maxDirectoryBytes {
				t.Fatalf("raw bytes = %d", out.BytesRead)
			}
		})
	}
}

func TestDirectoryFollowSymlinksGitCanonicalPaths(t *testing.T) {
	i, st, project := testIndexer(t, 0, "")
	root := filepath.Join(project, "docs")
	directoryFiles(t, root, map[string]string{
		".gitignore":   "real/*.md\n!real/keep.md\nignored/\nblocked-alias\n",
		"real/keep.md": "keep", "real/hidden.md": "hidden", "ignored/sub/private.md": "private",
	})
	fixtureGit(t, root, "init", "-q")
	fixtureGit(t, root, "add", "-f", "real/hidden.md")
	directoryLink(t, root, "aaa", "real")
	directoryLink(t, root, "bbb.md", "ignored/sub/private.md")
	directoryLink(t, root, "ccc", "ignored/sub")
	directoryLink(t, root, "blocked-alias", "real")
	// This is the actual Git failure the canonical-path integration must avoid.
	_, code, err := runGit(t.Context(), root, []byte(filepath.Join(root, "aaa/keep.md")+"\x00"), 4096, "check-ignore", "--no-index", "--stdin", "-z")
	if code != 128 || err == nil || !strings.Contains(err.Error(), "beyond a symbolic link") {
		t.Fatalf("Git failure fixture: code=%d, err=%v", code, err)
	}
	out, err := i.IndexDirectory(t.Context(), root, "git:prefix", DirectoryOptions{FollowSymlinks: true})
	if err != nil || !out.Complete || out.Indexed != 1 || out.Attempts != 1 {
		t.Fatalf("canonical ignores = %+v, %v", out, err)
	}
	if got := sourceLabels(t, st); !slices.Equal(got, []string{"git:prefix:real/keep.md"}) {
		t.Fatalf("Git filter bypass: %v", got)
	}
}

func TestDirectoryFollowSymlinksEntryLimit(t *testing.T) {
	i, _, project := testIndexer(t, 0, "")
	root := filepath.Join(project, "docs")
	large := filepath.Join(root, "real")
	if err := os.MkdirAll(large, 0o700); err != nil {
		t.Fatal(err)
	}
	for n := range maxDirectoryEntries {
		writeFile(t, filepath.Join(large, fmt.Sprintf("%05d.md", n)), "")
	}
	directoryLink(t, root, "aaa", "real")
	out, err := i.IndexDirectory(t.Context(), root, "entries", DirectoryOptions{FollowSymlinks: true})
	if err != nil || out.Complete || out.Visited != maxDirectoryEntries || out.Attempts != 0 || !slices.Equal(out.Limits, []string{"max_entries"}) {
		t.Fatalf("followed directory exceeded entry allowance: %+v, %v", out, err)
	}
}

func TestDirectoryFollowSymlinksShallowerRevisit(t *testing.T) {
	i, st, project := testIndexer(t, 0, "")
	root := filepath.Join(project, "docs")
	directoryFiles(t, root, map[string]string{"z/sub/keep.md": "keep", "a/skip.bin": "skip"})
	directoryLink(t, filepath.Join(root, "a"), "link", "../z")
	depth := 2
	out, err := i.IndexDirectory(t.Context(), root, "revisit", DirectoryOptions{FollowSymlinks: true, MaxDepth: &depth})
	if err != nil || out.Indexed != 1 || out.Attempts != 1 {
		t.Fatalf("earlier deep alias hid ordinary target: %+v, %v", out, err)
	}
	if got := sourceLabels(t, st); !slices.Equal(got, []string{"revisit:z/sub/keep.md"}) {
		t.Fatalf("missing deeper file: %v", got)
	}
}

func TestDirectoryFollowSymlinksGitBoundaries(t *testing.T) {
	for _, respect := range []bool{true, false} {
		t.Run(fmt.Sprintf("ignores-%t", respect), func(t *testing.T) {
			i, st, project := testIndexer(t, 0, "")
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{"nested/deep/private.md": "private", "plain.md": "plain"})
			fixtureGit(t, root, "init", "-q")
			fixtureGit(t, filepath.Join(root, "nested"), "init", "-q")
			directoryLink(t, root, "aaa", "nested/deep")
			directoryLink(t, root, "bbb.md", "nested/deep/private.md")
			out, err := i.IndexDirectory(t.Context(), root, "boundary", DirectoryOptions{FollowSymlinks: true, RespectGitignore: &respect})
			if err != nil || !out.Complete || out.Indexed != 1 || out.Attempts != 1 {
				t.Fatalf("nested target = %+v, %v", out, err)
			}
			if got := sourceLabels(t, st); !slices.Equal(got, []string{"boundary:plain.md"}) {
				t.Fatalf("crossed Git boundary: %v", got)
			}
			out, err = i.IndexDirectory(t.Context(), filepath.Join(root, "aaa"), "explicit", DirectoryOptions{FollowSymlinks: true})
			if err != nil || out.Indexed != 1 || !strings.Contains(out.GitIgnoreStatus, filepath.Join(root, "nested")) {
				t.Fatalf("explicit root should select nested worktree: %+v, %v", out, err)
			}
		})
	}
}
