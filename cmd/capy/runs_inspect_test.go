package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
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
