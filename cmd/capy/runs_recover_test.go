package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRunsRecoverPreviewCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	key := "synthetic-plan-key"
	env := stdioEnv(t, key)
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, key, true)
	require.NoError(t, err)
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	for range 3 {
		r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, runstore.Program{})
		require.NoError(t, err)
		require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, r.Cleanup.Scope))
	}
	out, diag, status := capturedCLI(t, bin, project, env, "runs", "recover", "--json", "--project-dir", project)
	require.NotZero(t, status)
	require.Empty(t, diag)
	require.Contains(t, string(out), "storage_busy")
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	var token, cursor string
	seen := map[string]bool{}
	for range 5 {
		args := []string{"runs", "recover", "--json", "--project-dir", project, "--limit", "1", "--timeout", "15m"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		out, diag, status = capturedCLI(t, bin, project, env, args...)
		require.Zero(t, status, "%s", out)
		require.Empty(t, diag)
		var v runstore.RecoveryPlan
		require.NoError(t, json.Unmarshal(out, &v))
		require.True(t, v.Complete)
		require.Equal(t, "ready", v.Status)
		require.NotEmpty(t, v.Token)
		if token != "" {
			require.Equal(t, token, v.Token)
		}
		token = v.Token
		for _, s := range v.Subjects {
			require.False(t, seen[s.Key])
			seen[s.Key] = true
		}
		cursor = v.NextCursor
		if cursor == "" {
			break
		}
		require.Equal(t, "15m0s", v.NextTimeout)
		if v.NextCommand != "" {
			require.Contains(t, v.NextCommand, "--cursor "+cursor)
			require.Contains(t, v.NextCommand, "--timeout 15m0s")
		} else {
			require.Contains(t, v.NextStep, "--project-dir")
		}
	}
	require.Len(t, seen, 3)
	out, diag, status = capturedCLI(t, bin, project, env, "runs", "recover", "--project-dir", project, "--limit", "1")
	require.Zero(t, status, "%s", out)
	require.Empty(t, diag)
	require.Contains(t, string(out), "Preview only")
	require.Contains(t, string(out), "Plan: "+token)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoFileExists(t, path+"-wal")
	// Receipt inspection does not reconstruct an unapplied plan.
	out, _, status = capturedCLI(t, bin, project, env, "runs", "inspect", "--json", "--project-dir", project, "--plan", token)
	require.NotZero(t, status)
	require.Contains(t, string(out), "receipt_unavailable")
}

func TestRunsRecoverLongOwnerBudget(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, strings.Repeat("q", max(1, 210-len(base)))+"''''''''''''")
	require.NoError(t, os.Mkdir(project, 0700))
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, "synthetic-plan-key", true)
	require.NoError(t, err)
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	for range 2 {
		r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, runstore.Program{})
		require.NoError(t, err)
		require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, r.Cleanup.Scope))
	}
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	view, err := runstore.PlanRecovery(t.Context(), path, "synthetic-plan-key", runstore.RecoveryPlanOptions{Limit: 64, Timeout: runstore.DefaultRecoveryTimeout})
	require.NoError(t, err)
	for _, encode := range []func(any) ([]byte, error){encodeRunJSON, encodeRunText} {
		bounded, err := execution.Budget(2048, func(n int) (runstore.RecoveryPlan, error) { return runstore.BoundRecoveryPlan(view, n/1024), nil }, encode)
		require.NoError(t, err)
		data, err := encode(bounded)
		require.NoError(t, err)
		require.LessOrEqual(t, len(data), 2048)
		require.NotEmpty(t, bounded.Token)
		require.Len(t, bounded.Subjects, 1)
		require.NotEmpty(t, bounded.NextCursor)
		if bounded.NextCommand != "" {
			require.Contains(t, bounded.NextCommand, bounded.NextCursor)
		} else {
			require.NotEmpty(t, bounded.NextTimeout)
			require.Contains(t, bounded.NextStep, "--project-dir")
		}
	}
}

func TestRunsRecoverQuoteHeavyOwnerBudget(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	project := ""
	for quotes := 1; quotes < 100; quotes++ {
		candidate := filepath.Join(base, strings.Repeat("'", quotes))
		command := "capy --project-dir '" + strings.ReplaceAll(candidate, "'", "'\"'\"'") + "' runs inspect"
		if len(command) > 400 {
			break
		}
		project = candidate
	}
	require.NotEmpty(t, project)
	require.NoError(t, os.Mkdir(project, 0700))
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, "synthetic-plan-key", true)
	require.NoError(t, err)
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	for range 2 {
		r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, runstore.Program{})
		require.NoError(t, err)
		require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, r.Cleanup.Scope))
	}
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	view, err := runstore.PlanRecovery(t.Context(), path, "synthetic-plan-key", runstore.RecoveryPlanOptions{Limit: 64, Timeout: 15 * time.Minute})
	require.NoError(t, err)
	// Large counts are a response-boundary fixture, not a claim about this DB.
	view.Counts.Subjects = 1_000_000_000_000
	view.Counts.Dispositions = view.Counts.Subjects
	fullCommand := runstore.BoundRecoveryPlan(view, 1).NextCommand
	require.Greater(t, len(fullCommand), 390, "fixture must reach the printable owner command cap before compact rendering")
	small := runstore.BoundRecoveryPlan(view, 0)
	encoded, err := encodeRunJSON(small)
	require.NoError(t, err)
	t.Logf("compact JSON bytes=%d, original command bytes=%d", len(encoded), len(fullCommand))
	bounded, err := execution.Budget(2048, func(n int) (runstore.RecoveryPlan, error) { return runstore.BoundRecoveryPlan(view, n/1024), nil }, encodeRunJSON)
	require.NoError(t, err)
	require.NotEmpty(t, bounded.Token)
	require.NotEmpty(t, bounded.NextCursor)
	require.Len(t, bounded.Subjects, 1)
	require.Equal(t, "15m0s", bounded.NextTimeout)
	require.Contains(t, bounded.NextStep, "--project-dir")
	text, err := encodeRunText(bounded)
	require.NoError(t, err)
	require.Contains(t, string(text), "--timeout 15m0s")
	require.Contains(t, string(text), "--cursor "+bounded.NextCursor)
}

func TestRunsRecoverFlagsAndTimeout(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	env := stdioEnv(t, "synthetic-plan-key")
	for _, flags := range [][]string{{"--timeout", "0"}, {"--timeout", "-1s"}, {"--timeout", "forever"}, {"--timeout", "99999999999999999h"}, {"--apply"}, {"--confirm-stopped"}, {"--plan", "unused"}, {"--limit", "0"}} {
		args := append([]string{"runs", "recover", "--json", "--project-dir", project}, flags...)
		out, diag, status := capturedCLI(t, bin, project, env, args...)
		require.Equal(t, 2, status, "%s", out)
		require.Empty(t, diag)
		require.Contains(t, string(out), "invalid_argument")
	}
	require.NoDirExists(t, filepath.Join(project, ".capy", "runs"))
	cmd := exec.Command(bin, "runs", "recover", "--help")
	cmd.Dir, cmd.Env = project, env
	help, err := cmd.Output()
	require.NoError(t, err)
	require.NotContains(t, string(help), "--apply")
	require.Contains(t, string(help), "5m0s")
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, "synthetic-plan-key", true)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	out, _, status := capturedCLI(t, bin, project, env, "runs", "recover", "--json", "--project-dir", project, "--timeout", "1ns")
	require.NotZero(t, status)
	var v runstore.RecoveryPlan
	require.NoError(t, json.Unmarshal(out, &v))
	require.Equal(t, "recovery_inventory_incomplete", v.Status)
	require.Empty(t, v.Token)
	require.Contains(t, v.NextCommand, "--timeout 15m0s")
	// Execute the exact same-owner retry emitted by the command (no mutation).
	for i, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			env[i] = "PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
		}
	}
	cmd = exec.Command("sh", "-c", v.NextCommand)
	cmd.Dir, cmd.Env = project, env
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "no_recovery_needed")
}
