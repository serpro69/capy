package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const stdioTimeout = 15 * time.Second

// buildStdioCapy builds once for a fixture; its subtests and reopened sessions
// all use the same candidate binary. Keep these tests in the normal FTS5 suite.
func buildStdioCapy(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "capy")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-tags", "fts5", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	cmd.WaitDelay = 5 * time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stdio fixture: %v\n%s", err, output)
	}
	return bin
}

// stdioEnv deliberately inherits only PATH. Each child's config, data, home,
// session discovery roots, and vault target live under this fixture, including
// when a later credential test opts in to the vault. No parent project selector,
// credential, or Git override reaches the child.
func stdioEnv(t *testing.T, key string) []string {
	t.Helper()
	root := t.TempDir()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"CLAUDE_CONFIG_DIR=" + filepath.Join(root, "claude"),
		"CODEX_HOME=" + filepath.Join(root, "codex"),
		"CAPY_VAULT_PATH=" + filepath.Join(root, "vault.db"),
		"CAPY_VAULT_KEY=",
		"CAPY_DB_KEY=" + key,
		"GIT_CONFIG_NOSYSTEM=1",
	}
}

func stdioProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte("[store]\npath = 'knowledge.db'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stdioProcess is intentionally sequential: only one request is outstanding.
// It accepts a command as well as cwd/env so wrapper tests can reuse the same
// transport. Credentials are supplied solely for leak checks and redaction.
type stdioProcess struct {
	cmd     *exec.Cmd
	stdin   *os.File
	stdout  *os.File
	scanner *bufio.Scanner
	stderr  stdioLog
	secrets []string
	nextID  int
	timeout time.Duration
	waited  chan struct{}
	waitErr error // read only after waited closes
}

type stdioCommand struct {
	executable string
	args       []string
	dir        string
	env        []string
	secrets    []string
}

func startStdioProcess(t *testing.T, command stdioCommand) *stdioProcess {
	t.Helper()
	p := &stdioProcess{
		cmd:     exec.Command(command.executable, command.args...),
		secrets: command.secrets,
		timeout: stdioTimeout,
	}
	p.cmd.Dir, p.cmd.Env = command.dir, command.env
	p.cmd.Stderr = &p.stderr
	p.cmd.WaitDelay = 2 * time.Second
	// Own the pipe ends explicitly so requests can deadline both writes and reads.
	childIn, parentIn, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer childIn.Close()
	parentOut, childOut, err := os.Pipe()
	if err != nil {
		parentIn.Close()
		t.Fatal(err)
	}
	defer childOut.Close()
	p.stdin, p.stdout = parentIn, parentOut
	p.cmd.Stdin, p.cmd.Stdout = childIn, childOut
	p.scanner = bufio.NewScanner(parentOut)
	p.scanner.Buffer(make([]byte, 4096), 1024*1024)
	if err := p.cmd.Start(); err != nil {
		parentIn.Close()
		parentOut.Close()
		t.Fatalf("start stdio child: %v", err)
	}
	p.waited = make(chan struct{})
	go func() {
		p.waitErr = p.cmd.Wait()
		close(p.waited)
	}()
	t.Cleanup(func() {
		if err := p.abort(); err != nil {
			t.Error(err)
		}
	})
	return p
}

// stdioLog drains stderr independently of stdout, bounding memory even if a
// failing child logs continuously. Reads are safe while exec copies stderr.
type stdioLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *stdioLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := min(len(b), 64*1024-l.buf.Len())
	l.buf.Write(b[:n])
	return len(b), nil
}

func (l *stdioLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (p *stdioProcess) containsSecret(s string) bool {
	for _, secret := range p.secrets {
		if secret != "" && strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

func (p *stdioProcess) diagnostics() string {
	s := p.stderr.String()
	for _, secret := range p.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	return s
}

// abort closes both ends, kills when needed, and always waits to reap the child.
// It is also the t.Cleanup path when a caller fails before graceful shutdown.
func (p *stdioProcess) abort() error {
	p.stdin.Close()
	p.stdout.Close()
	select {
	case <-p.waited:
		return nil
	default:
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill stdio child: %w", err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-p.waited:
		return nil // a forced exit is expected on this cleanup path
	case <-timer.C:
		return errors.New("stdio child was not reaped after kill")
	}
}

// shutdown uses MCP's stdio EOF contract, not a nonexistent shutdown RPC.
// A forced exit is an error here, even though abort still reaps the child.
func (p *stdioProcess) shutdown(timeout time.Duration) (err error) {
	defer func() { err = errors.Join(err, p.abort()) }()
	if err := p.stdin.Close(); err != nil {
		return fmt.Errorf("close stdio input: %w", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.waited:
		if p.waitErr != nil {
			return fmt.Errorf("stdio child exit: %w", p.waitErr)
		}
		if p.containsSecret(p.stderr.String()) {
			return errors.New("credential appeared on stderr")
		}
		return nil
	case <-timer.C:
		return errors.New("stdio shutdown timed out")
	}
}

func (p *stdioProcess) write(message any, deadline time.Time) error {
	if err := p.stdin.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return json.NewEncoder(p.stdin).Encode(message)
}

func (p *stdioProcess) request(method string, params, result any) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, p.abort())
		}
	}()
	p.nextID++
	deadline := time.Now().Add(p.timeout)
	message := map[string]any{"jsonrpc": "2.0", "id": p.nextID, "method": method, "params": params}
	if err := p.write(message, deadline); err != nil {
		return fmt.Errorf("write %s: %w", method, err)
	}
	if err := p.stdout.SetReadDeadline(deadline); err != nil {
		return err
	}
	for p.scanner.Scan() {
		if p.containsSecret(p.scanner.Text()) {
			return errors.New("credential appeared on MCP stdout")
		}
		var response struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(p.scanner.Bytes(), &response); err != nil {
			return fmt.Errorf("decode %s envelope: %w", method, err)
		}
		if response.JSONRPC != "2.0" {
			return errors.New("unexpected JSON-RPC version")
		}
		if response.ID == nil && response.Method != "" {
			continue // notifications can precede a response
		}
		if response.ID == nil || *response.ID != p.nextID {
			return fmt.Errorf("response ID does not match %s request %d", method, p.nextID)
		}
		if response.Error != nil {
			return fmt.Errorf("%s: JSON-RPC error %d", method, response.Error.Code)
		}
		if len(response.Result) == 0 || string(response.Result) == "null" {
			return fmt.Errorf("%s response has no result", method)
		}
		return json.Unmarshal(response.Result, result)
	}
	if err := p.scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", method, err)
	}
	return fmt.Errorf("read %s: %w", method, io.EOF)
}

func (p *stdioProcess) initialize(t *testing.T) {
	t.Helper()
	var result struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	err := p.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "capy-stdio-test", "version": "1"},
	}, &result)
	if err != nil {
		t.Fatalf("initialize: %v\nstderr: %s", err, p.diagnostics())
	}
	if result.ProtocolVersion != "2025-06-18" || result.ServerInfo.Name != "capy" {
		t.Fatal("initialize did not negotiate the expected capy tool server")
	}
	if result.Capabilities["tools"] == nil {
		t.Fatal("initialize did not advertise tools")
	}
	message := map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
	if err := p.write(message, time.Now().Add(p.timeout)); err != nil {
		t.Fatalf("initialized notification: %v", err)
	}
}

func (p *stdioProcess) tool(t *testing.T, name string, arguments map[string]any) string {
	t.Helper()
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := p.request("tools/call", map[string]any{"name": name, "arguments": arguments}, &result); err != nil {
		t.Fatalf("%s: %v\nstderr: %s", name, err, p.diagnostics())
	}
	var text strings.Builder
	for _, content := range result.Content {
		if content.Type == "text" {
			text.WriteString(content.Text)
		}
	}
	if p.containsSecret(text.String()) {
		t.Fatal("credential appeared in decoded tool content")
	}
	if result.IsError || text.Len() == 0 {
		t.Fatalf("%s failed or returned no text: %s", name, text.String())
	}
	return text.String()
}

func (p *stdioProcess) close(t *testing.T) {
	t.Helper()
	if err := p.shutdown(stdioTimeout); err != nil {
		t.Fatalf("shutdown: %v\nstderr: %s", err, p.diagnostics())
	}
}
