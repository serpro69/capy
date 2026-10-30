package main

import (
	"bytes"
	"encoding/json"
	"io"
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

func recoveryCLIPending(t *testing.T, project, key string) (string, string) {
	t.Helper()
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	s, err := runstore.Open(t.Context(), path, key, true)
	require.NoError(t, err)
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, runstore.Program{Request: executor.ExecRequest{Language: "shell", Code: "printf replayed >> must-not-replay"}})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, r.Cleanup.Scope))
	require.NoError(t, s.Started(t.Context(), r.ID, "run"))
	require.NoError(t, s.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte("retained uncertain output\n")}))
	require.NoError(t, s.QuarantineRun(t.Context(), runstore.RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: runstore.IncidentSupervisorLost, Stage: runstore.StageHelperWait}))
	require.NoError(t, o.Lease.Close())
	require.NoError(t, s.Close())
	return path, r.ID
}

func recoveryCLIPlan(t *testing.T, bin, project string, env []string) runstore.RecoveryPlan {
	t.Helper()
	out, diag, status := capturedCLI(t, bin, project, env, "runs", "recover", "--json", "--project-dir", project)
	require.Zero(t, status, "%s", out)
	require.Empty(t, diag)
	var plan runstore.RecoveryPlan
	require.NoError(t, json.Unmarshal(out, &plan))
	require.NotEmpty(t, plan.Token)
	return plan
}

func recoveryCLIApplyArgs(project, token string) []string {
	return []string{"runs", "recover", "--json", "--project-dir", project, "--apply", "--plan", token, "--confirm-stopped", "--cleanup-basis", "groups_stopped", "--reason", "Stopped originating groups"}
}

func TestRunsRecoverApplyOfflineCLI(t *testing.T) {
	capturedCleanup(t)
	bin := buildStdioCapy(t)
	project, key := cliProject(t, ""), "synthetic-apply-cli-key"
	env := stdioEnv(t, key)
	sibling := cliRun(t, bin, project, env, "printf 'sibling retained bytes\n'; printf x >> executed-once")
	require.NotNil(t, sibling.RunID)
	out, _, status := capturedCLI(t, bin, project, env, "runs", "read", "--json", "--project-dir", project, "--run-id", *sibling.RunID, "--raw", "--max-bytes", "7")
	require.Zero(t, status, "%s", out)
	var prefix runstore.ReadPage
	require.NoError(t, json.Unmarshal(out, &prefix))
	require.NotNil(t, prefix.NextCursor)
	path, uncertain := recoveryCLIPending(t, project, key)
	db := upgradeFixtureDB(t, path, key)
	var original, siblingOriginal []byte
	require.NoError(t, db.QueryRow("SELECT result FROM runs WHERE id=?", uncertain).Scan(&original))
	require.NoError(t, db.QueryRow("SELECT result FROM runs WHERE id=?", *sibling.RunID).Scan(&siblingOriginal))
	require.NoError(t, db.Close())
	// An attached MCP refuses work and prevents offline planning. Shut it down.
	p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
	p.initialize(t)
	refusal := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf forbidden > refused-work"})
	require.Nil(t, refusal.Structured.RunID)
	require.Equal(t, "cleanup_unverified", refusal.Structured.Error.Code)
	out, _, status = capturedCLI(t, bin, project, env, "runs", "recover", "--json", "--project-dir", project)
	require.NotZero(t, status)
	require.Contains(t, string(out), "storage_busy")
	p.close(t)
	plan := recoveryCLIPlan(t, bin, project, env)
	args := recoveryCLIApplyArgs(project, plan.Token)
	wrong, _, wrongStatus := capturedCLI(t, bin, project, stdioEnv(t, "wrong-synthetic-key"), args...)
	require.NotZero(t, wrongStatus)
	require.Contains(t, string(wrong), "wrong_key")
	// A reader attaching after preview excludes apply too.
	holder, err := runstore.Open(t.Context(), path, key, false)
	require.NoError(t, err)
	busy, _, busyStatus := capturedCLI(t, bin, project, env, args...)
	require.NotZero(t, busyStatus)
	require.Contains(t, string(busy), "storage_busy")
	require.NoError(t, holder.Close())
	out, diag, status := capturedCLI(t, bin, project, env, args...)
	require.Zero(t, status, "%s", out)
	require.Empty(t, diag)
	var applied runstore.RecoveryApplication
	require.NoError(t, json.Unmarshal(out, &applied))
	require.Equal(t, "applied", applied.Status)
	require.NotNil(t, applied.Recovery)
	require.NotNil(t, applied.Admission)
	require.False(t, applied.Admission.Blocked)
	require.Contains(t, applied.NextStep, "uncertainty is unchanged")
	for _, selector := range [][]string{{"--plan", plan.Token}, {"--receipt-id", applied.Recovery.ID}} {
		out, _, status = capturedCLI(t, bin, project, env, append([]string{"runs", "inspect", "--json", "--project-dir", project}, selector...)...)
		require.Zero(t, status, "%s", out)
		var inspected runstore.Inspection
		require.NoError(t, json.Unmarshal(out, &inspected))
		require.Equal(t, applied.Recovery.ID, inspected.Receipt.ID)
		require.Len(t, inspected.Subjects, 1)
		require.NotNil(t, inspected.Subjects[0].RetentionExpiresAt)
		require.Equal(t, time.Unix(0, applied.Recovery.AppliedAt).Add(24*time.Hour).UnixMilli()+1, inspected.Subjects[0].RetentionExpiresAt.UnixMilli())
		bad, _, badStatus := capturedCLI(t, bin, project, env, "runs", "inspect", "--json", "--project-dir", project, "--receipt-id", inspected.Subjects[0].IncidentID)
		require.NotZero(t, badStatus)
		require.Contains(t, string(bad), "receipt_unavailable")
	}
	// Changed display/timeout is an exact retry; changed attestation is a conflict.
	out, _, status = capturedCLI(t, bin, project, env, append(args, "--timeout", "15m", "--json=false")...)
	require.Zero(t, status, "%s", out)
	require.Contains(t, string(out), "already_applied")
	require.Contains(t, string(out), applied.Recovery.ID)
	out, _, status = capturedCLI(t, bin, project, env, append(args, "--reason", "A different attestation")...)
	require.NotZero(t, status)
	require.Contains(t, string(out), "recovery_receipt_conflict")
	require.Contains(t, string(out), applied.Recovery.ID)
	// Restart a fresh MCP and deliberately verify new capture, never replay history.
	p = startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
	p.initialize(t)
	fresh := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf 'verification\n'"})
	require.NotNil(t, fresh.Structured.RunID)
	require.Equal(t, 0, *fresh.Structured.ExitCode)
	require.Equal(t, []byte("sibling retained bytes\n"), p.rawRun(t, *sibling.RunID, "stdout"))
	var retained struct {
		IsError    bool              `json:"isError"`
		Structured runstore.ReadPage `json:"structuredContent"`
	}
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "read", "run_id": uncertain, "raw": true, "stream": "stdout"}}, &retained))
	require.False(t, retained.IsError)
	require.False(t, retained.Structured.EOF, "operator closure must not fabricate original completion")
	require.True(t, retained.Structured.EndOfAvailable)
	require.True(t, retained.Structured.Run.Recovery.CaptureClosed)
	require.Len(t, retained.Structured.Fragments, 1)
	require.Equal(t, []byte("retained uncertain output\n"), retained.Structured.Fragments[0].Data)
	lookup := stdioInspection(t, p, map[string]any{"action": "inspect", "receipt_id": applied.Recovery.ID})
	require.Equal(t, applied.Recovery.ID, lookup.Receipt.ID)
	p.close(t)
	out, _, status = capturedCLI(t, bin, project, env, "runs", "read", "--json", "--project-dir", project, "--run-id", *sibling.RunID, "--raw", "--cursor", *prefix.NextCursor)
	require.Zero(t, status, "%s", out)
	var suffix runstore.ReadPage
	require.NoError(t, json.Unmarshal(out, &suffix))
	var all []byte
	for _, page := range []runstore.ReadPage{prefix, suffix} {
		for _, fragment := range page.Fragments {
			all = append(all, fragment.Data...)
		}
	}
	require.Equal(t, []byte("sibling retained bytes\n"), all)
	db = upgradeFixtureDB(t, path, key)
	var after, siblingAfter []byte
	require.NoError(t, db.QueryRow("SELECT result FROM runs WHERE id=?", uncertain).Scan(&after))
	require.NoError(t, db.QueryRow("SELECT result FROM runs WHERE id=?", *sibling.RunID).Scan(&siblingAfter))
	require.Equal(t, original, after)
	require.Equal(t, siblingOriginal, siblingAfter)
	require.NoError(t, db.Close())
	marker, err := os.ReadFile(filepath.Join(project, "executed-once"))
	require.NoError(t, err)
	require.Equal(t, "x", string(marker))
	require.NoFileExists(t, filepath.Join(project, "must-not-replay"))
	require.NoFileExists(t, filepath.Join(project, "refused-work"))
	// Replaying an old receipt must report a later independent blocker.
	recoveryCLIPending(t, project, key)
	out, _, status = capturedCLI(t, bin, project, env, args...)
	require.Zero(t, status, "%s", out)
	var again runstore.RecoveryApplication
	require.NoError(t, json.Unmarshal(out, &again))
	require.Equal(t, "already_applied", again.Status)
	require.Equal(t, applied.Recovery, again.Recovery)
	require.True(t, again.Admission.Blocked)
	require.Positive(t, again.Admission.UnresolvedIncidents)
}

func TestRunsRecoverApplyInvalidBeforeWrites(t *testing.T) {
	bin := buildStdioCapy(t)
	project, key := cliProject(t, ""), "synthetic-apply-validation-key"
	env := stdioEnv(t, key)
	path, _ := recoveryCLIPending(t, project, key)
	plan := recoveryCLIPlan(t, bin, project, env)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"missing plan", []string{"--apply", "--confirm-stopped", "--cleanup-basis", "groups_stopped", "--reason", "stopped"}},
		{"missing confirmation", []string{"--apply", "--plan", plan.Token, "--cleanup-basis", "groups_stopped", "--reason", "stopped"}},
		{"missing basis", []string{"--apply", "--plan", plan.Token, "--confirm-stopped", "--reason", "stopped"}},
		{"missing reason", []string{"--apply", "--plan", plan.Token, "--confirm-stopped", "--cleanup-basis", "groups_stopped"}},
		{"false confirmation", []string{"--confirm-stopped=false"}},
		{"invalid basis", []string{"--cleanup-basis", "guessed"}},
		{"empty basis", []string{"--cleanup-basis="}},
		{"empty reason", []string{"--reason=  "}},
		{"large reason", []string{"--reason", strings.Repeat("x", 1025)}},
		{"invalid UTF8", []string{"--reason", "\xff"}},
		{"malformed token", []string{"--plan", "invalid"}},
		{"empty token", []string{"--plan="}},
		{"preview cursor", []string{"--cursor="}},
		{"preview limit", []string{"--limit", "16"}},
		{"zero timeout", []string{"--timeout", "0"}},
		{"negative timeout", []string{"--timeout", "-1s"}},
		{"malformed timeout", []string{"--timeout", "forever"}},
		{"overflow timeout", []string{"--timeout", "99999999999999999h"}},
		{"old selector", []string{"--recovery-id", plan.Token}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := recoveryCLIApplyArgs(project, plan.Token)
			if strings.HasPrefix(tc.name, "missing ") {
				args = args[:5]
			}
			out, diag, status := capturedCLI(t, bin, project, env, append(args, tc.flags...)...)
			require.Equal(t, 2, status, "%s", out)
			require.Empty(t, diag)
			require.Contains(t, string(out), "invalid_argument")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.NoFileExists(t, path+"-wal")
		})
	}
	// A short valid duration has a usable retry of the same token/attestation.
	out, _, status := capturedCLI(t, bin, project, env, append(recoveryCLIApplyArgs(project, plan.Token), "--timeout", "1ns")...)
	require.Equal(t, 1, status)
	var timed runstore.RecoveryApplication
	require.NoError(t, json.Unmarshal(out, &timed))
	require.Equal(t, "recovery_timeout", timed.Status)
	require.Equal(t, plan.Token, timed.Plan)
	require.Equal(t, "15m0s", timed.NextTimeout)
	require.Nil(t, timed.Admission)
	// Obtain the complete retry instruction without a transport budget, then run
	// it verbatim through the shell with adversarial reason quoting.
	reason := "Stopped 'groups'; $(touch must-not-inject) `touch must-not-inject`"
	options := runstore.RecoveryApplyOptions{Plan: plan.Token, ConfirmStopped: true, Basis: "groups_stopped", Explanation: reason, Timeout: time.Nanosecond}
	timed, err = runstore.ApplyRecovery(t.Context(), path, key, options)
	require.Error(t, err)
	require.NotEmpty(t, timed.NextCommand)
	for i, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			env[i] = "PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + strings.TrimPrefix(value, "PATH=")
		}
	}
	retry := exec.Command("sh", "-c", timed.NextCommand)
	retry.Dir, retry.Env = project, env
	out, err = retry.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "Recovery: applied")
	require.NoFileExists(t, filepath.Join(project, "must-not-inject"))
}

type recoveryShortWriter struct {
	calls int
	data  []byte
}

func (w *recoveryShortWriter) Write(p []byte) (int, error) {
	w.calls++
	w.data = append([]byte(nil), p...)
	return len(p) / 2, io.ErrShortWrite
}

func TestRunsRecoverApplyDeliveryFailure(t *testing.T) {
	project, key := cliProject(t, ""), "synthetic-delivery-key"
	path, _ := recoveryCLIPending(t, project, key)
	t.Setenv("CAPY_DB_KEY", key)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	plan, err := runstore.PlanRecovery(t.Context(), path, key, runstore.RecoveryPlanOptions{Limit: 16, Timeout: runstore.DefaultRecoveryTimeout})
	require.NoError(t, err)
	cmd := newRunsRecoverCmd()
	cmd.Flags().String("project-dir", "", "fixture owner")
	reason := strings.Repeat("<&'", 300)
	cmd.SetArgs(append(recoveryCLIApplyArgs(project, plan.Token)[2:], "--reason", reason, "--cleanup-basis", "host_restarted"))
	w := &recoveryShortWriter{}
	cmd.SetOut(w)
	cmd.SetErr(io.Discard)
	err = cmd.ExecuteContext(t.Context())
	var exit *exitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 74, exit.code)
	require.Equal(t, 1, w.calls)
	var delivered runstore.RecoveryApplication
	require.NoError(t, json.Unmarshal(w.data, &delivered))
	require.Equal(t, "applied", delivered.Status, "budget retries must not reapply and turn the result into already_applied")
	require.Equal(t, "host_restarted", delivered.Recovery.Basis)
	require.True(t, delivered.MetadataOmitted)
	lookup, err := runstore.InspectRuns(t.Context(), path, key, runstore.InspectionOptions{Plan: plan.Token, Limit: 16})
	require.NoError(t, err)
	require.NotNil(t, lookup.Receipt)
	options := runstore.RecoveryApplyOptions{Plan: plan.Token, ConfirmStopped: true, Basis: "host_restarted", Explanation: reason, Timeout: runstore.DefaultRecoveryTimeout}
	again, err := runstore.ApplyRecovery(t.Context(), path, key, options)
	require.NoError(t, err)
	require.Equal(t, "already_applied", again.Status)
	require.Equal(t, lookup.Receipt, again.Recovery)
	// A deadline after finding an old receipt must retain both lookup identities
	// without presenting tentative current blockers as healthy.
	options.Timeout = time.Nanosecond
	timed, err := runstore.ApplyRecovery(t.Context(), path, key, options)
	require.Error(t, err)
	timed.Recovery = again.Recovery // response-shape boundary: receipt read before deadline
	for _, state := range []string{"recovery_timeout", "recovery_outcome_unknown"} {
		timed.Status = state
		for _, encode := range []func(any) ([]byte, error){encodeRunJSON, encodeRunText} {
			bounded, err := execution.Budget(2048, func(n int) (runstore.RecoveryApplication, error) {
				return runstore.BoundRecoveryApplication(timed, n), nil
			}, encode)
			require.NoError(t, err)
			require.Nil(t, bounded.Admission)
			require.Equal(t, plan.Token, bounded.Plan)
			require.Equal(t, again.Recovery.ID, bounded.Recovery.ID)
		}
	}
	// Force multiple render passes; builders consume only the captured result.
	again.Recovery.Explanation = strings.Repeat("<&'", 300)
	for _, encode := range []func(any) ([]byte, error){encodeRunJSON, encodeRunText} {
		calls := 0
		bounded, err := execution.Budget(2048, func(n int) (runstore.RecoveryApplication, error) {
			calls++
			return runstore.BoundRecoveryApplication(again, n), nil
		}, encode)
		require.NoError(t, err)
		data, err := encode(bounded)
		require.NoError(t, err)
		require.LessOrEqual(t, len(data), 2048)
		if bytes.Contains(data, []byte(`"version"`)) {
			require.Greater(t, calls, 1)
		}
		require.Equal(t, lookup.Receipt.ID, bounded.Recovery.ID)
	}
	still, err := runstore.InspectRuns(t.Context(), path, key, runstore.InspectionOptions{Plan: plan.Token, Limit: 16})
	require.NoError(t, err)
	require.Equal(t, lookup.Receipt, still.Receipt)
}
