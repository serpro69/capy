package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPStdio(t *testing.T) {
	bin := buildStdioCapy(t)
	t.Run("environment round trip and reopen", func(t *testing.T) {
		const key = "synthetic-stdio-baseline-passphrase-12345"
		project, env := stdioProject(t), stdioEnv(t, key)
		launch := func() *stdioProcess {
			p := startStdioProcess(t, stdioCommand{
				executable: bin, args: []string{"serve", "--project-dir", project},
				dir: project, env: env, secrets: []string{key},
			})
			p.initialize(t)
			return p
		}
		p := launch()
		stdioIndex(t, p, "copperbadger")
		stdioSearch(t, p, "copperbadger", "absentotter")
		stdioDoctor(t, p, project)
		p.close(t)
		stdioCheckpointed(t, project)

		p = launch()
		// No re-index: the second process must read the first process's content.
		stdioSearch(t, p, "copperbadger", "absentotter")
		stdioDoctor(t, p, project)
		p.close(t)
		stdioCheckpointed(t, project)
	})

	t.Run("simultaneous projects retain distinct content", func(t *testing.T) {
		projects := []struct {
			key, marker, other string
			dir                string
			env                []string
			process            *stdioProcess
		}{
			{key: "synthetic-stdio-project-a-passphrase-12345", marker: "ambercapybara", other: "violetheron"},
			{key: "synthetic-stdio-project-b-passphrase-67890", marker: "violetheron", other: "ambercapybara"},
		}
		for i := range projects {
			project := &projects[i]
			project.dir, project.env = stdioProject(t), stdioEnv(t, project.key)
			project.process = startStdioProcess(t, stdioCommand{
				executable: bin, args: []string{"serve"}, dir: project.dir, env: project.env,
				secrets: []string{projects[0].key, projects[1].key},
			})
			project.process.initialize(t)
		}
		// Keep both processes alive while writing and reading both databases.
		for _, project := range projects {
			stdioIndex(t, project.process, project.marker)
		}
		for _, project := range projects {
			stdioSearch(t, project.process, project.marker, project.other)
			stdioDoctor(t, project.process, project.dir)
		}
		for _, project := range projects {
			project.process.close(t)
			stdioCheckpointed(t, project.dir)
		}
		for i := range projects {
			project := &projects[i]
			project.process = startStdioProcess(t, stdioCommand{
				executable: bin, args: []string{"serve"}, dir: project.dir, env: project.env,
				secrets: []string{projects[0].key, projects[1].key},
			})
			project.process.initialize(t)
		}
		for _, project := range projects {
			stdioSearch(t, project.process, project.marker, project.other)
			stdioDoctor(t, project.process, project.dir)
			project.process.close(t)
			stdioCheckpointed(t, project.dir)
		}
	})
}

func stdioIndex(t *testing.T, p *stdioProcess, marker string) {
	t.Helper()
	text := p.tool(t, "capy_index", map[string]any{
		"source":  "stdio-fixture",
		"content": "# Transport orchard\n\nThe transport orchard holds the retained marker " + marker + ".",
	})
	if !strings.Contains(text, "Indexed 1 sections") {
		t.Fatalf("index did not confirm the fixture: %s", text)
	}
}

func stdioSearch(t *testing.T, p *stdioProcess, want, absent string) {
	t.Helper()
	// Neither marker occurs in the request: query echo cannot satisfy the oracle.
	// Do not filter by source/project, which could conceal cross-project leakage.
	text := p.tool(t, "capy_search", map[string]any{"queries": []string{"transport orchard"}, "limit": 2})
	if !strings.Contains(text, want) || strings.Contains(text, absent) {
		t.Fatalf("search did not return only the project's retained marker: %s", text)
	}
}

func stdioDoctor(t *testing.T, p *stdioProcess, project string) {
	t.Helper()
	text := p.tool(t, "capy_doctor", map[string]any{})
	for _, want := range []string{
		"[x] FTS5: available", "[x] Knowledge base: 1 sources, 1 chunks",
		"[x] Project: " + project, "[-] Vault: disabled",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor missing %q: %s", want, text)
		}
	}
}

func stdioCheckpointed(t *testing.T, project string) {
	t.Helper()
	db := filepath.Join(project, "knowledge.db")
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || strings.HasPrefix(string(data), "SQLite format 3") {
		t.Fatal("round trip did not leave an encrypted database")
	}
	if info, err := os.Stat(db + "-wal"); err == nil {
		if info.Size() != 0 {
			t.Fatal("clean shutdown left an uncheckpointed WAL")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestMCPStdioProcessFailures(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const key = "synthetic-stdio-failure-passphrase-12345"
	for _, mode := range []string{
		"exit", "read-timeout", "write-timeout", "wrong-id", "rpc-error", "shutdown-timeout", "cleanup",
	} {
		t.Run(mode, func(t *testing.T) {
			var p *stdioProcess
			// Assert after the nested test's registered cleanup, including when
			// the caller leaves without performing a graceful shutdown.
			t.Run("child", func(t *testing.T) {
				env := append(stdioEnv(t, key), "CAPY_STDIO_HELPER_MODE="+mode)
				p = startStdioProcess(t, stdioCommand{
					executable: executable, args: []string{"-test.run=^TestMCPStdioHelperProcess$"},
					dir: t.TempDir(), env: env, secrets: []string{key},
				})
				var result map[string]any
				if mode == "shutdown-timeout" || mode == "cleanup" {
					if err := p.request("ready", nil, &result); err != nil {
						t.Fatal(err)
					}
					if mode == "cleanup" {
						return
					}
					err := p.shutdown(100 * time.Millisecond)
					if err == nil || !strings.Contains(err.Error(), "shutdown timed out") {
						t.Fatalf("expected bounded shutdown failure, got %v", err)
					}
					return
				}
				params := "fixture"
				if mode == "write-timeout" {
					params = strings.Repeat("x", 1024*1024)
				}
				p.timeout = time.Second
				err := p.request("fixture", params, &result)
				if err == nil {
					t.Fatal("expected request failure")
				}
				switch mode {
				case "exit":
					if !errors.Is(err, io.EOF) || p.cmd.ProcessState.ExitCode() != 7 {
						t.Fatalf("expected EOF and exit 7, got %v", err)
					}
					diagnostic := p.diagnostics()
					if strings.Contains(diagnostic, key) || !strings.Contains(diagnostic, "[REDACTED]") {
						t.Fatal("stderr credential was not redacted")
					}
				case "read-timeout", "write-timeout":
					if !errors.Is(err, os.ErrDeadlineExceeded) {
						t.Fatalf("expected I/O deadline, got %v", err)
					}
				case "wrong-id":
					if !strings.Contains(err.Error(), "response ID does not match") {
						t.Fatalf("expected ID mismatch, got %v", err)
					}
				case "rpc-error":
					if !strings.Contains(err.Error(), "JSON-RPC error -32601") {
						t.Fatalf("expected protocol error, got %v", err)
					}
				}
			})
			if p == nil {
				t.Fatal("child fixture was not started")
			}
			select {
			case <-p.waited:
				if p.cmd.ProcessState == nil {
					t.Fatal("child was not reaped")
				}
			default:
				t.Fatal("cleanup left a running child")
			}
		})
	}
}

// Re-execute only this test to exercise transport failures without sleeps,
// shell dependencies, or intentionally failing the parent test suite.
func TestMCPStdioHelperProcess(t *testing.T) {
	mode := os.Getenv("CAPY_STDIO_HELPER_MODE")
	if mode == "" {
		return
	}
	block := func() {
		// A live pipe blocks in the OS poller even with no Go test timeout.
		// Keeping its writer open avoids EOF without a sleep or busy loop.
		r, w, err := os.Pipe()
		if err != nil {
			os.Exit(8)
		}
		defer r.Close()
		defer w.Close()
		if _, err := io.Copy(io.Discard, r); err != nil {
			os.Exit(8)
		}
		os.Exit(9)
	}
	if mode == "write-timeout" {
		block() // never read stdin; the parent's write deadline must terminate us
	}
	var request struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(8)
	}
	switch mode {
	case "exit":
		fmt.Fprintln(os.Stderr, "synthetic failure:", os.Getenv("CAPY_DB_KEY"))
		os.Exit(7)
	case "read-timeout":
		fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","method":"notifications/ready"}`)
	case "wrong-id":
		fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n", request.ID+1)
	case "rpc-error":
		fmt.Fprintf(os.Stdout,
			"{\"jsonrpc\":\"2.0\",\"id\":%d,\"error\":{\"code\":-32601,\"message\":\"fixture\"}}\n", request.ID)
	default:
		fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n", request.ID)
	}
	block() // ignore EOF as well; only forced cleanup should end this child
}
