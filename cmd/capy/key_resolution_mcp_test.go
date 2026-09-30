package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPProjectCredentials(t *testing.T) {
	bin := buildStdioCapy(t)
	const keyA = "synthetic-mcp-project-a-passphrase-12345"
	const keyB = "synthetic-mcp-project-b-passphrase-67890"
	secrets := []string{keyA, keyB}

	for _, source := range []string{"key file", "dotenv"} {
		t.Run(source+" crossed inheritance", func(t *testing.T) {
			projects := []struct {
				key, inherited, marker, other string
				dir                           string
				env                           []string
				process                       *stdioProcess
			}{
				{key: keyA, inherited: keyB, marker: "ambercapybara", other: "violetheron"},
				{key: keyB, inherited: keyA, marker: "violetheron", other: "ambercapybara"},
			}
			for i := range projects {
				p := &projects[i]
				p.dir, p.env = stdioProject(t), stdioEnv(t, p.inherited)
				writeMCPProjectCredential(t, p.dir, source, p.key)
			}
			for _, reopen := range []bool{false, true} {
				for i := range projects {
					p := &projects[i]
					p.process = startStdioProcess(t, stdioCommand{
						executable: bin, args: []string{"serve"}, dir: p.dir, env: p.env, secrets: secrets,
					})
					p.process.initialize(t)
				}
				// Both processes stay alive throughout the actual database calls.
				for _, p := range projects {
					if !reopen {
						stdioIndex(t, p.process, p.marker)
					}
					stdioSearch(t, p.process, p.marker, p.other)
					stdioDoctor(t, p.process, p.dir)
				}
				for _, p := range projects {
					p.process.close(t)
					stdioCheckpointed(t, p.dir)
				}
			}
		})
	}

	for _, mode := range []string{"bare", "subdirectory cwd", "bare override", "leading flag", "trailing flag"} {
		t.Run(mode, func(t *testing.T) {
			project := stdioProject(t)
			writeMCPProjectCredential(t, project, "key file", keyB)
			dir := project
			var args []string
			switch mode {
			case "subdirectory cwd":
				dir = filepath.Join(project, "subdir")
				require.NoError(t, os.Mkdir(dir, 0o700))
			case "bare override", "leading flag", "trailing flag":
				dir = stdioProject(t)
				writeMCPProjectCredential(t, dir, "dotenv", keyA)
				args = []string{"--project-dir", project}
				if mode == "leading flag" {
					args = append(args, "serve")
				} else if mode == "trailing flag" {
					args = append([]string{"serve"}, args...)
				}
			}
			p := startStdioProcess(t, stdioCommand{
				executable: bin, args: args, dir: dir, env: stdioEnv(t, ""), secrets: secrets,
			})
			p.initialize(t)
			stdioIndex(t, p, "copperbadger")
			stdioSearch(t, p, "copperbadger", "absentotter")
			stdioDoctor(t, p, project)
			p.close(t)
			stdioCheckpointed(t, project)
			if dir != project {
				assert.NoFileExists(t, filepath.Join(dir, "knowledge.db"))
			}
		})
	}

	t.Run("startup failures precede MCP and database creation", func(t *testing.T) {
		for _, tc := range []struct {
			name, config, credential, inherited, want string
		}{
			{name: "missing file", config: "key_file = 'db.key'", inherited: keyA, want: "inspecting credential file"},
			{name: "empty file", config: "key_file = 'db.key'", inherited: keyA, want: "passphrase is empty"},
			{name: "invalid file", config: "key_file = 'db.key'", credential: keyB + "\n\n", inherited: keyA, want: "remaining line ending"},
			{name: "invalid dotenv", credential: "CAPY_DB_KEY='" + keyB + "'\necho unsupported\n", inherited: keyA, want: "store.key_file"},
			{name: "missing environment", want: "CAPY_DB_KEY environment variable is required"},
			{name: "invalid config", config: "key_file = [", inherited: keyA, want: "loading configuration"},
			{name: "invalid config type", config: "key_file = 42", inherited: keyA, want: "loading configuration"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				project := stdioProject(t)
				writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = 'absent/knowledge.db'\n"+tc.config+"\n")
				if tc.name == "invalid dotenv" {
					writeCLIKeyFixture(t, filepath.Join(project, ".env"), tc.credential)
				} else if tc.name != "missing file" && strings.HasPrefix(tc.config, "key_file = '") {
					writeCLIKeyFixture(t, filepath.Join(project, "db.key"), tc.credential)
				}
				env := stdioEnv(t, tc.inherited)
				p := startStdioProcess(t, stdioCommand{
					executable: bin, args: []string{"serve", "--project-dir", project},
					dir: project, env: env, secrets: secrets,
				})
				assertMCPStartupFailure(t, p, tc.want)
				assert.NoDirExists(t, filepath.Join(project, "absent"))
				assert.NoFileExists(t, filepath.Join(project, ".project"))
				for _, value := range env {
					if data, ok := strings.CutPrefix(value, "XDG_DATA_HOME="); ok {
						assert.NoDirExists(t, filepath.Join(data, "capy"), "must not create a fallback database or marker")
					}
				}
			})
		}
	})

	for _, source := range []string{"key file", "dotenv"} {
		t.Run(source+" wrong selected key fails lazily and preserves data", func(t *testing.T) {
			project := stdioProject(t)
			writeMCPProjectCredential(t, project, source, keyB)
			launch := func() *stdioProcess {
				p := startStdioProcess(t, stdioCommand{
					executable: bin, args: []string{"serve"}, dir: project,
					env: stdioEnv(t, keyB), secrets: secrets,
				})
				p.initialize(t)
				return p
			}
			p := launch()
			stdioIndex(t, p, "copperbadger")
			p.close(t)
			dbPath := filepath.Join(project, "knowledge.db")
			before, err := os.ReadFile(dbPath)
			require.NoError(t, err)

			// The inherited key is correct, but the selected project key is not.
			writeMCPProjectCredential(t, project, source, keyA)
			p = launch() // preflight does not authenticate; initialize must succeed
			text := p.tool(t, "capy_doctor", map[string]any{})
			assert.Contains(t, text, "[x] FTS5: available")
			assert.Contains(t, text, "[-] Knowledge base: error reading stats (wrong passphrase")
			wantSource := "store.key_file"
			if source == "dotenv" {
				wantSource = "CAPY_DB_KEY in dotenv"
			}
			assert.Contains(t, text, wantSource)
			for _, tool := range []struct {
				name string
				args map[string]any
			}{
				{name: "capy_index", args: map[string]any{"source": "failed-write", "content": "Unwanted absentotter content."}},
				{name: "capy_search", args: map[string]any{"queries": []string{"transport orchard"}}},
			} {
				var result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				require.NoError(t, p.request("tools/call", map[string]any{"name": tool.name, "arguments": tool.args}, &result))
				// Search reports per-query errors in its text, allowing other
				// queries/corpora to succeed. Index uses the tool error flag.
				assert.Equal(t, tool.name == "capy_index", result.IsError)
				require.NotEmpty(t, result.Content)
				text := result.Content[0].Text
				require.False(t, p.containsSecret(text), "credential appeared in decoded tool error")
				assert.Contains(t, text, "wrong passphrase")
				assert.Contains(t, strings.ToLower(text), "error:")
				assert.NotContains(t, text, "copperbadger")
				assert.Contains(t, text, wantSource)
				assert.NotContains(t, text, "_key=")
			}
			p.close(t)
			after, err := os.ReadFile(dbPath)
			require.NoError(t, err)
			assert.Equal(t, before, after, "wrong key must not rewrite the encrypted database")
			backups, err := filepath.Glob(dbPath + ".corrupt*")
			require.NoError(t, err)
			assert.Empty(t, backups)

			writeMCPProjectCredential(t, project, source, keyB)
			p = launch()
			stdioSearch(t, p, "copperbadger", "absentotter")
			stdioDoctor(t, p, project)
			p.close(t)
			stdioCheckpointed(t, project)
		})
	}

	t.Run("selected file key rejects plaintext before MCP", func(t *testing.T) {
		project := stdioProject(t)
		writeMCPProjectCredential(t, project, "key file", keyB)
		dbPath := filepath.Join(project, "knowledge.db")
		db, err := openUnencrypted(dbPath)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, db.Close()) })
		_, err = db.Exec("CREATE TABLE fixture (id INTEGER)")
		require.NoError(t, err)
		require.NoError(t, db.Close())
		before, err := os.ReadFile(dbPath)
		require.NoError(t, err)
		p := startStdioProcess(t, stdioCommand{
			executable: bin, args: []string{"serve"}, dir: project, env: stdioEnv(t, ""), secrets: secrets,
		})
		assertMCPStartupFailure(t, p, "not encrypted")
		assert.Contains(t, p.diagnostics(), "capy encrypt")
		after, err := os.ReadFile(dbPath)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assert.NoFileExists(t, filepath.Join(project, ".project"))
	})
}

func writeMCPProjectCredential(t *testing.T, project, source, key string) {
	t.Helper()
	if source == "key file" {
		writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = 'knowledge.db'\nkey_file = 'db.key'\n")
		writeCLIKeyFixture(t, filepath.Join(project, "db.key"), key+"\n")
	} else {
		writeCLIKeyFixture(t, filepath.Join(project, ".env"), "CAPY_DB_KEY='"+key+"'\n")
	}
}

func assertMCPStartupFailure(t *testing.T, p *stdioProcess, want string) {
	t.Helper()
	// Keep stdin open and send nothing: preflight must exit on its own. Writing
	// initialize would race the expected early exit and could yield EPIPE.
	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	select {
	case <-p.waited:
	case <-timer.C:
		t.Fatal("startup did not reject invalid configuration/credentials")
	}
	require.NotZero(t, p.cmd.ProcessState.ExitCode())
	require.NoError(t, p.stdout.SetReadDeadline(time.Now().Add(p.timeout)))
	require.False(t, p.scanner.Scan(), "startup failure wrote unexpected stdout")
	require.NoError(t, p.scanner.Err())
	require.False(t, p.containsSecret(p.stderr.String()), "credential appeared on stderr")
	assert.Contains(t, p.diagnostics(), "capy serve:")
	assert.Contains(t, p.diagnostics(), want)
	assert.NotContains(t, p.diagnostics(), "_key=")
	assert.NotContains(t, p.diagnostics(), "cipher=")
}
