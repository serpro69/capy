//go:build integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/execution"
)

func TestExecuteSupervisorPrivateEntry(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, dir)
	}
	t.Setenv("CAPY_DB_KEY", "synthetic-supervisor-test")
	t.Setenv("CAPY_VAULT_KEY", "synthetic-vault-test")
	bin := buildStdioCapy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, execution.SupervisorCommand)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err == nil || len(output) > 1024 {
		t.Fatalf("private entry accepted missing descriptors: %v %q", err, output)
	}
	if _, err := os.Stat(filepath.Join(dir, ".capy")); !os.IsNotExist(err) {
		t.Fatal("private entry created storage")
	}
	cmd = exec.CommandContext(ctx, bin, "--help")
	output, err = cmd.CombinedOutput()
	if err != nil || strings.Contains(string(output), execution.SupervisorCommand) {
		t.Fatalf("hidden helper is public: %v %q", err, output)
	}
}
