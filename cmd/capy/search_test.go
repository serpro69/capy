package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/store"
)

func TestSearchCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	const key = "synthetic-search-cli-project-passphrase-12345"
	const otherKey = "synthetic-search-cli-wrong-passphrase-67890"
	fixture := func(t *testing.T) (string, stdioCommand) {
		t.Helper()
		project := stdioProject(t)
		return project, stdioCommand{executable: bin, dir: t.TempDir(), env: stdioEnv(t, key), secrets: []string{key, otherKey}}
	}
	launch := func(t *testing.T, project string, command stdioCommand) *stdioProcess {
		t.Helper()
		command.args = []string{"serve", "--project-dir", project}
		p := startStdioProcess(t, command)
		p.initialize(t)
		return p
	}
	search := func(t *testing.T, project string, command stdioCommand, args ...string) string {
		t.Helper()
		command.args = append([]string{"search", "--project-dir", project}, args...)
		out, stderr, code := runKnowledgeCLI(t, command)
		if code != 0 {
			t.Fatalf("search exit %d: %s", code, stderr)
		}
		return out
	}

	t.Run("help and validation before storage", func(t *testing.T) {
		project, command := fixture(t)
		writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='absent/knowledge.db'\n")
		command.env = stdioEnv(t, "")
		out := search(t, project, command, "--help")
		for _, want := range []string{"search <query...>", "--source", "--limit", "--type", "--include-kind", "knowledge only", "capy vault search", "exit 0", "exit 1"} {
			if !strings.Contains(out, want) {
				t.Errorf("help missing %q", want)
			}
		}
		for _, tc := range []struct {
			name string
			args []string
		}{
			{name: "missing"}, {name: "empty", args: []string{""}}, {name: "blank", args: []string{" ", "\t"}},
			{name: "zero limit", args: []string{"query", "--limit=0"}},
			{name: "negative limit", args: []string{"query", "--limit=-1"}},
			{name: "excess limit", args: []string{"query", "--limit=101"}},
			{name: "fractional limit", args: []string{"query", "--limit=1.5"}},
			{name: "overflow limit", args: []string{"query", "--limit=99999999999999999999"}},
			{name: "invalid type", args: []string{"query", "--type=markdown"}},
			{name: "empty type", args: []string{"query", "--type="}},
			{name: "empty kind", args: []string{"query", "--include-kind="}},
			{name: "session kind", args: []string{"query", "--include-kind=session"}},
			{name: "comma kinds", args: []string{"query", "--include-kind=durable,ephemeral"}},
			{name: "invalid kind with source", args: []string{"query", "--source=chosen", "--include-kind=invalid"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				command.args = append([]string{"search", "--project-dir", project}, tc.args...)
				_, stderr, code := runKnowledgeCLI(t, command)
				if code != 1 || strings.Contains(stderr, "resolving knowledge credential") {
					t.Fatalf("validation order/status: %d, %s", code, stderr)
				}
				if _, err := os.Stat(filepath.Join(project, "absent")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid arguments created DB directory: %v", err)
				}
			})
		}
	})

	for _, mode := range []string{"environment", "dotenv", "key-file", "linked-owner"} {
		t.Run(mode+" MCP to CLI round trip", func(t *testing.T) {
			project, command := fixture(t)
			owner := project
			if mode == "linked-owner" {
				owner = t.TempDir()
				gitDir := filepath.Join(owner, ".git", "worktrees", "linked")
				writeCLIKeyFixture(t, filepath.Join(gitDir, "commondir"), "../..\n")
				writeCLIKeyFixture(t, filepath.Join(project, ".git"), "gitdir: "+gitDir+"\n")
				writeCLIKeyFixture(t, filepath.Join(project, ".env"), "CAPY_DB_KEY="+otherKey)
			}
			if mode != "environment" {
				command.env = stdioEnv(t, otherKey)
				if mode == "key-file" {
					writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='knowledge.db'\nkey_file='db.key'\n")
					writeCLIKeyFixture(t, filepath.Join(owner, "db.key"), key)
				} else {
					writeCLIKeyFixture(t, filepath.Join(owner, ".env"), "CAPY_DB_KEY="+key)
				}
			}
			p := launch(t, project, command)
			stdioIndex(t, p, "amberlynx")
			p.close(t)
			out := search(t, project, command, "transport", "orchard")
			if !strings.Contains(out, "amberlynx") || !strings.Contains(out, "Source:") || !strings.Contains(out, "Title:") {
				t.Fatalf("missing MCP result: %s", out)
			}
			stdioCheckpointed(t, owner)
			if owner != project {
				if _, err := os.Stat(filepath.Join(project, "knowledge.db")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("created DB under wrong owner: %v", err)
				}
			}
		})
	}

	t.Run("kinds literal source type limit and MCP schema", func(t *testing.T) {
		project, command := fixture(t)
		p := launch(t, project, command)
		p.tool(t, "capy_index", map[string]any{"source": "durable-guide", "content": "# Guide\n\norchidlookup prosemarker\n\n## Example\n\n```go\nfunc orchidlookup() { /* codemarker */ }\n```"})
		var descriptors struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		}
		if err := p.request("tools/list", map[string]any{}, &descriptors); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range descriptors.Tools {
			if tool.Name == "capy_search" {
				found = true
				for _, name := range []string{"type", "contentType", "content_type"} {
					if _, ok := tool.InputSchema.Properties[name]; ok {
						t.Errorf("CLI type leaked into MCP schema: %s", name)
					}
				}
			}
		}
		if !found {
			t.Fatal("capy_search descriptor missing")
		}
		p.close(t)
		// capy_index always creates durable knowledge. Seed transient sources
		// through the store API so kind-filter assertions exercise actual rows.
		st := store.NewContentStore(filepath.Join(project, "knowledge.db"), project, 0, 0, store.WithEncryptionKey(key, "fixture"))
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Error(err)
			}
		})
		for _, label := range []string{`literal%_\'label`, `literalXXa\'label`} {
			if _, err := st.Index("orchidlookup transientmarker", label, "", store.KindEphemeral); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name         string
			flags        []string
			want, absent string
		}{
			{name: "default durable", want: "durable-guide", absent: "transientmarker"},
			{name: "ephemeral replaces default", flags: []string{"--include-kind=ephemeral"}, want: "transientmarker", absent: "durable-guide"},
			{name: "both kinds", flags: []string{"--include-kind=durable", "--include-kind=ephemeral", "--limit=100"}, want: "durable-guide"},
			{name: "literal source bypass", flags: []string{`--source=literal%_\'label`}, want: "transientmarker", absent: "literalXXa"},
			{name: "code", flags: []string{"--type=code"}, want: "codemarker", absent: "prosemarker"},
			{name: "prose", flags: []string{"--type=prose"}, want: "prosemarker", absent: "codemarker"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				out := search(t, project, command, append([]string{"orchidlookup"}, tc.flags...)...)
				if !strings.Contains(out, tc.want) || (tc.absent != "" && strings.Contains(out, tc.absent)) {
					t.Fatalf("filter results: %s", out)
				}
				if tc.name == "both kinds" && !strings.Contains(out, "transientmarker") {
					t.Fatalf("repeatable kind lost ephemeral: %s", out)
				}
			})
		}
		out := search(t, project, command, "orchidlookup", "--limit=1")
		if strings.Count(out, "Source:") != 1 {
			t.Fatalf("limit ignored: %s", out)
		}
		out = search(t, project, command, "nonexistentplatypus")
		if out != "No knowledge results found.\n" {
			t.Fatalf("empty result: %s", out)
		}
	})

	for _, anchor := range []string{"relative", "settings", "absolute"} {
		t.Run(anchor+" Read denies retain cached content", func(t *testing.T) {
			project, command := fixture(t)
			path := filepath.Join(project, "private.md")
			writeCLIKeyFixture(t, path, "cachedotter prior content")
			p := launch(t, project, command)
			p.tool(t, "capy_index", map[string]any{"path": path})
			p.close(t)
			writeCLIKeyFixture(t, path, "forbiddenpanther changed backing content")
			pattern := "private.md"
			if anchor == "settings" {
				pattern = "/private.md"
			}
			if anchor == "absolute" {
				pattern = "/" + filepath.ToSlash(path)
			}
			settings, err := json.Marshal(map[string]any{"permissions": map[string]any{"deny": []string{"Read(" + pattern + ")"}}})
			if err != nil {
				t.Fatal(err)
			}
			writeCLIKeyFixture(t, filepath.Join(project, ".claude", "settings.json"), string(settings))
			if out := search(t, project, command, "cachedotter"); !strings.Contains(out, "cachedotter") {
				t.Fatalf("deny lost cached content: %s", out)
			}
			if out := search(t, project, command, "forbiddenpanther"); out != "No knowledge results found.\n" {
				t.Fatalf("read denied backing file: %s", out)
			}
			// A later process observes changed settings and may refresh again.
			writeCLIKeyFixture(t, filepath.Join(project, ".claude", "settings.json"), "{}")
			if out := search(t, project, command, "forbiddenpanther"); !strings.Contains(out, "forbiddenpanther") {
				t.Fatalf("new process did not refresh: %s", out)
			}
			stdioCheckpointed(t, project)
		})
	}

	t.Run("fresh processes advance bounded stale checks", func(t *testing.T) {
		project, command := fixture(t)
		st := store.NewContentStore(filepath.Join(project, "knowledge.db"), project, 0, 0, store.WithEncryptionKey(key, "fixture"))
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Error(err)
			}
		})
		const total = 65
		for i := range total {
			path := filepath.Join(project, fmt.Sprintf("note-%03d.md", i))
			writeCLIKeyFixture(t, path, "refreshedorchard current contents")
			if _, err := st.IndexWithFilePath("old contents", path, "", store.KindDurable, path); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		previous := 0
		for attempt := range total {
			out := search(t, project, command, "refreshedorchard", "--limit=100")
			count := strings.Count(out, "Source:")
			if count <= previous || count-previous > 32 {
				t.Fatalf("pass %d did not advance within 32 files: %d -> %d", attempt, previous, count)
			}
			if count == total {
				break
			}
			previous = count
			if attempt == total-1 {
				t.Fatal("high-ID files starved")
			}
		}
		stdioCheckpointed(t, project)
	})

	t.Run("vault-only content stays separate", func(t *testing.T) {
		project, command := fixture(t)
		vaultPath := filepath.Join(t.TempDir(), "vault.db")
		buildCodexVault(t, vaultPath)
		command.env = replaceWrapperEnv(command.env, "CAPY_VAULT_PATH", vaultPath)
		command.env = replaceWrapperEnv(command.env, "CAPY_VAULT_KEY", os.Getenv("CAPY_VAULT_KEY"))
		command.args = []string{"vault", "search", "brontosaurus"}
		out, stderr, code := runKnowledgeCLI(t, command)
		if code != 0 || !strings.Contains(out, "brontosaurus") {
			t.Fatalf("vault fixture: %d, %s, %s", code, out, stderr)
		}
		if out := search(t, project, command, "brontosaurus"); out != "No knowledge results found.\n" {
			t.Fatalf("knowledge search federated vault: %s", out)
		}
	})

	t.Run("retrieval row failures exit nonzero", func(t *testing.T) {
		project, command := fixture(t)
		p := launch(t, project, command)
		stdioIndex(t, p, "amberlynx")
		p.close(t)
		// FTS5 columns permit NULL, but the result mapper requires a string
		// classification. This preserves a readable encrypted DB and a matching
		// posting while deterministically failing only when results are scanned.
		db, err := sql.Open("sqlite3", store.EncryptedDSN(filepath.Join(project, "knowledge.db"), key))
		if err != nil {
			t.Fatal(err)
		}
		_, updateErr := db.ExecContext(t.Context(), "UPDATE chunks SET content_type = NULL; UPDATE chunks_trigram SET content_type = NULL")
		closeErr := db.Close()
		if updateErr != nil || closeErr != nil {
			t.Fatalf("fault fixture: %v, %v", updateErr, closeErr)
		}
		command.args = []string{"search", "amberlynx", "--project-dir", project}
		out, stderr, code := runKnowledgeCLI(t, command)
		if code != 1 || out != "" || !strings.Contains(stderr, "search row scan") {
			t.Fatalf("retrieval failure reported success: %d, %s, %s", code, out, stderr)
		}
	})

	for _, mode := range []string{"missing-key", "empty-key-file", "invalid-dotenv", "wrong-key", "db-path", "invalid-policy", "invalid-config"} {
		t.Run(mode+" fails", func(t *testing.T) {
			project, command := fixture(t)
			switch mode {
			case "missing-key":
				command.env = stdioEnv(t, "")
			case "empty-key-file":
				writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='knowledge.db'\nkey_file='db.key'\n")
				writeCLIKeyFixture(t, filepath.Join(project, "db.key"), "")
			case "invalid-dotenv":
				writeCLIKeyFixture(t, filepath.Join(project, ".env"), "CAPY_DB_KEY=\n")
			case "wrong-key":
				p := launch(t, project, command)
				stdioIndex(t, p, "retainedlynx")
				p.close(t)
				command.env = stdioEnv(t, otherKey)
			case "db-path":
				writeCLIKeyFixture(t, filepath.Join(project, "blocked"), "ordinary file")
				writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath='blocked/knowledge.db'\n")
			case "invalid-policy":
				writeCLIKeyFixture(t, filepath.Join(project, ".claude", "settings.json"), `{"permissions":{"deny":["Read([ab])"]}}`)
			case "invalid-config":
				writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "not valid TOML")
			}
			command.args = []string{"search", "retainedlynx", "--project-dir", project}
			out, stderr, code := runKnowledgeCLI(t, command)
			if code != 1 || stderr == "" || strings.Contains(out, "No knowledge results") {
				t.Fatalf("failure outcome: %d, %s, %s", code, out, stderr)
			}
			if mode == "wrong-key" {
				command.env = stdioEnv(t, key)
				if out := search(t, project, command, "retainedlynx"); !strings.Contains(out, "retainedlynx") {
					t.Fatal("wrong key damaged data")
				}
			} else if _, err := os.Stat(filepath.Join(project, "knowledge.db")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid configuration created storage: %v", err)
			}
		})
	}
}

func TestKnowledgeSearchOutput(t *testing.T) {
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	var out bytes.Buffer
	err := writeKnowledgeSearch(&out, []store.SearchResult{{Label: secret + strings.Repeat("界", 400), Title: secret + strings.Repeat("é", 400), Content: secret + "\x1b[31m" + strings.Repeat("界", 3000) + "TAIL"}})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !utf8.ValidString(text) || strings.Contains(text, secret) || strings.Contains(text, "\x1b") || strings.Contains(text, "TAIL") || len(text) > 2400 {
		t.Fatalf("output is unsafe, invalid UTF-8 or unbounded: %d bytes", len(text))
	}
	if !strings.Contains(text, "[REDACTED_SECRET]") || !strings.Contains(text, "…") {
		t.Fatal("missing redaction/truncation indication")
	}
	for _, tc := range []struct{ name, input string }{
		{name: "boundary", input: strings.Repeat("a", searchSnippetBytes)},
		{name: "multibyte", input: strings.Repeat("界", searchSnippetBytes)},
		{name: "secret across cutoff", input: strings.Repeat("x", searchSnippetBytes-10) + secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := knowledgeSearchText(tc.input, searchSnippetBytes)
			if len(text) > searchSnippetBytes || !utf8.ValidString(text) || strings.Contains(text, "ghp_") {
				t.Fatal("invalid snippet boundary")
			}
		})
	}
}

func TestSearchCommandCancellationAndOutputFailure(t *testing.T) {
	for _, mode := range []string{"before storage", "late cancellation", "output failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			project, dbPath := newCLIProject(t)
			cmd := newSearchCmd()
			cmd.Flags().String("project-dir", project, "fixture project")
			cmd.SetArgs([]string{"orchard"})
			cmd.SetErr(&bytes.Buffer{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := error(context.Canceled)
			if mode == "before storage" {
				cancel()
				cmd.SetOut(&bytes.Buffer{})
			} else {
				seedKeyResolutionDB(t, dbPath, project)
				t.Setenv("CAPY_DB_KEY", projectFileTestKey)
				if mode == "late cancellation" {
					cmd.SetOut(indexCancelingOutput{cancel: cancel})
				} else {
					cmd.SetOut(indexBrokenOutput{})
					want = syscall.EPIPE
				}
			}
			if err := cmd.ExecuteContext(ctx); !errors.Is(err, want) {
				t.Fatalf("command error: %v, want %v", err, want)
			}
			if mode == "before storage" {
				if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("canceled search opened store: %v", err)
				}
			} else {
				if info, err := os.Stat(dbPath + "-wal"); err == nil && info.Size() > 0 {
					t.Fatal("search did not checkpoint")
				}
				st := store.NewContentStore(dbPath, project, 0, 0, store.WithEncryptionKey(projectFileTestKey, "fixture"))
				results, err := st.SearchWithFallback("orchard", 3, store.SearchOptions{})
				closeErr := st.Close()
				if err != nil || closeErr != nil || len(results) == 0 {
					t.Fatalf("reopen: %v, %v, results=%d", err, closeErr, len(results))
				}
			}
		})
	}
}
