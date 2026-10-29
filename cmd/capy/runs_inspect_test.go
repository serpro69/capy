package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRunsInspectCLIAndDoctorStats(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	key := "synthetic-inspection-key"
	env := stdioEnv(t, key)
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	// The missing-store command must not initialize anything.
	out, diag, status := capturedCLI(t, bin, project, env, "runs", "inspect", "--json", "--project-dir", project)
	require.Zero(t, status, "%s", out)
	require.Empty(t, diag)
	require.Contains(t, string(out), `"admission":"not_initialized"`)
	require.NoDirExists(t, filepath.Dir(path))
	s, err := runstore.Open(t.Context(), path, key, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	owner, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	var old string
	for i := range 11 {
		run, err := s.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{Request: executor.ExecRequest{Code: "PRIVATE_PROGRAM"}})
		require.NoError(t, err)
		if i == 0 {
			old = run.ID
			require.NoError(t, s.Pending(t.Context(), old, owner.ID, executor.CleanupLinuxDescendants))
		} else {
			require.NoError(t, s.Finish(t.Context(), run.ID, runstore.Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupNotSpawned}}))
		}
	}
	require.NoError(t, owner.Lease.Close())
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	var cursor string
	var found bool
	for range 6 {
		args := []string{"runs", "inspect", "--project-dir", project, "--json", "--limit", "1"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		out, diag, status = capturedCLI(t, bin, project, env, args...)
		require.Zero(t, status, "%s", out)
		require.Empty(t, diag)
		var page runstore.Inspection
		require.NoError(t, json.Unmarshal(out, &page))
		require.Equal(t, "recorded_blocked", page.Admission)
		require.EqualValues(t, 2, page.Counts.QuarantineMarkers)
		require.NotContains(t, string(out), "PRIVATE_PROGRAM")
		for _, subject := range page.Subjects {
			found = found || subject.RunID == old
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	require.True(t, found, "minimum-budget pagination must eventually reveal the old blocker")
	out, diag, status = capturedCLI(t, bin, project, env, "runs", "inspect", "--run-id", old, "--project-dir", project)
	require.Zero(t, status, "%s", out)
	require.Empty(t, diag)
	require.Contains(t, string(out), old)
	require.Contains(t, string(out), "slot=0 generation="+owner.ID)
	require.Contains(t, string(out), "missing=accepted_cleanup,terminal_outcome")
	require.Contains(t, string(out), "output_available=true")
	cli := exec.Command(bin, "doctor", "--project-dir", project)
	cli.Dir, cli.Env = project, env
	doctor, err := cli.CombinedOutput()
	require.NoError(t, err, "%s", doctor)
	p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
	p.initialize(t)
	for _, name := range []string{"capy_doctor", "capy_stats"} {
		result := p.tool(t, name, map[string]any{})
		for _, line := range strings.Split(string(doctor), "\n") {
			if strings.Contains(line, "] Run blockers:") || strings.Contains(line, "] Run observation ") {
				require.Contains(t, result, line)
			}
		}
		require.Contains(t, result, old)
	}
	p.close(t)
	for _, extra := range [][]string{{"--recovery-id", old}, {"--receipt-id", "invalid"}, {"--apply"}, {"--limit", "65"}} {
		args := append([]string{"runs", "inspect", "--json", "--project-dir", project}, extra...)
		out, _, status := capturedCLI(t, bin, project, env, args...)
		require.Equal(t, 2, status)
		require.Contains(t, string(out), "invalid_argument")
	}
}

func TestRunsInspectTextDistinguishesOwnerMarkers(t *testing.T) {
	one, two := 1, 2
	view := runstore.Inspection{Subjects: []runstore.InspectionSubject{
		{Kind: "owner", Slot: &one, Generation: "10000000-0000-0000-0000-000000000001", Attribution: "unattributed", Reason: "legacy_unattributed", MissingEvidence: []string{"causal_run", "detection_time"}},
		{Kind: "owner", Slot: &two, Generation: "20000000-0000-0000-0000-000000000002", Attribution: "unattributed", Reason: "legacy_unattributed", MissingEvidence: []string{"causal_run", "detection_time"}},
	}}
	text, err := encodeRunText(view)
	require.NoError(t, err)
	require.Contains(t, string(text), "slot=1 generation=10000000-0000-0000-0000-000000000001")
	require.Contains(t, string(text), "slot=2 generation=20000000-0000-0000-0000-000000000002")
	require.Contains(t, string(text), "missing=causal_run,detection_time")
}

func TestRunsIncidentFailurePreservesReadsAndBlocksOtherProcess(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	key := "synthetic-incident-cli-key"
	env := stdioEnv(t, key)
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	cfg := runstore.PolicyConfig{TTLHours: 24, MaxRunBytes: 64 << 20, MaxRetainedBytes: 512 << 20}
	s, err := runstore.OpenWithPolicy(t.Context(), path, key, true, cfg)
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if !closed {
			require.NoError(t, s.Close())
		}
	})
	owner, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	pending, err := s.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), pending.ID, owner.ID, executor.CleanupLinuxDescendants))
	kept, err := s.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), kept.ID, owner.ID, executor.CleanupLinuxDescendants))
	require.NoError(t, s.Started(t.Context(), kept.ID))
	data := []byte("successful sibling retained")
	require.NoError(t, s.Append(t.Context(), kept.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: data}))
	require.NoError(t, s.Finish(t.Context(), kept.ID, runstore.Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true}))
	db := upgradeFixtureDB(t, path, key)
	var used int64
	require.NoError(t, db.QueryRow("SELECT accounted_bytes FROM run_meta").Scan(&used))
	_, err = db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=?", used)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	configText := "[store]\npath='knowledge.db'\n[runs]\nmax_response_bytes=2048\nmax_run_bytes=1\nmax_retained_bytes=" + strconv.FormatInt(used, 10) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte(configText), 0600))
	require.NoError(t, s.Quarantine(t.Context()), "live safety marker cannot depend on audit capacity")
	rejected := cliRun(t, bin, project, env, "touch must-not-start")
	require.Nil(t, rejected.RunID)
	require.NotNil(t, rejected.Error)
	require.Equal(t, "cleanup_unverified", rejected.Error.Code)
	require.NoFileExists(t, filepath.Join(project, "must-not-start"))
	require.NoError(t, owner.Lease.Close())
	err = s.Close()
	closed = true
	require.Error(t, err, "offline incident publication has no headroom")
	out, diag, status := capturedCLI(t, bin, project, env, "runs", "read", "--json", "--raw", "--run-id", kept.ID, "--project-dir", project)
	require.Zero(t, status, "%s", out)
	require.Empty(t, diag)
	var page runstore.ReadPage
	require.NoError(t, json.Unmarshal(out, &page))
	require.Equal(t, data, page.Fragments[0].Data)
	for _, action := range []string{"status", "list"} {
		args := []string{"runs", action, "--json", "--project-dir", project}
		if action == "status" {
			args = append(args, "--run-id", kept.ID)
		}
		out, diag, status = capturedCLI(t, bin, project, env, args...)
		require.Zero(t, status, "%s", out)
		require.Empty(t, diag)
		require.Contains(t, string(out), kept.ID)
	}
}
