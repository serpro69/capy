package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func observationRun(state, capture string, exit any, persisted bool) map[string]any {
	return map[string]any{
		"version": 1, "kind": "run", "run_id": "11111111-2222-4333-8444-555555555555",
		"state": state, "exit_code": exit, "signal": nil, "error": nil,
		"started_at": "2000-01-01T00:00:00Z",
		"capture":    map[string]any{"state": capture, "terminal_persisted": persisted},
	}
}

func TestCapturedV2ObservationScopedEligibility(t *testing.T) {
	isolateRoutineHook(t)
	for _, agent := range []string{"", "child"} {
		for _, scope := range []executor.CleanupScope{executor.CleanupLinuxDescendants, executor.CleanupDarwinObservation} {
			completed, wrong := "retired", "observed"
			if scope == executor.CleanupDarwinObservation {
				completed, wrong = wrong, completed
			}
			for _, tc := range []struct {
				name     string
				mutate   func(map[string]any, map[string]any)
				eligible bool
			}{
				{"terminal", func(map[string]any, map[string]any) {}, true},
				{"running", func(r, c map[string]any) {
					r["state"], r["exit_code"] = "running", nil
					r["capture"] = map[string]any{"state": "recording", "terminal_persisted": false}
					c["state"] = "pending"
				}, true},
				{"cross scope", func(r, c map[string]any) { c["state"] = wrong }, false},
				{"pending terminal", func(r, c map[string]any) { c["state"] = "pending" }, false},
				{"not spawned terminal", func(r, c map[string]any) { c["state"] = "not_spawned" }, false},
				{"completed running", func(r, c map[string]any) {
					r["state"] = "running"
					r["exit_code"] = nil
					r["capture"] = map[string]any{"state": "recording"}
				}, false},
				{"unknown scope", func(r, c map[string]any) { c["scope"] = "future" }, false},
				{"old Darwin scope", func(r, c map[string]any) { c["scope"] = "darwin_process_group_v1" }, false},
				{"unknown state", func(r, c map[string]any) { c["state"] = "future" }, false},
				{"null proof", func(r, c map[string]any) { r["cleanup"] = nil }, false},
				{"missing proof", func(r, c map[string]any) { delete(r, "cleanup") }, false},
				{"string proof", func(r, c map[string]any) { r["cleanup"] = "retired" }, false},
				{"missing scope", func(r, c map[string]any) { delete(c, "scope") }, false},
				{"numeric state", func(r, c map[string]any) { c["state"] = 1 }, false},
				{"extra proof field", func(r, c map[string]any) { c["retired"] = true }, false},
				{"unknown version", func(r, c map[string]any) { r["version"] = 3 }, false},
				{"string version", func(r, c map[string]any) { r["version"] = "2" }, false},
				{"missing exit", func(r, c map[string]any) { delete(r, "exit_code") }, false},
				{"signal", func(r, c map[string]any) { r["signal"] = 9 }, false},
				{"nonzero", func(r, c map[string]any) { r["exit_code"] = 7 }, false},
				{"error", func(r, c map[string]any) { r["error"] = map[string]any{"code": "failed"} }, false},
				{"incomplete", func(r, c map[string]any) {
					r["capture"] = map[string]any{"state": "incomplete", "terminal_persisted": true}
				}, false},
				{"unpersisted", func(r, c map[string]any) {
					r["capture"] = map[string]any{"state": "complete", "terminal_persisted": false}
				}, false},
			} {
				t.Run(agent+"/"+string(scope)+"/"+tc.name, func(t *testing.T) {
					project := t.TempDir()
					r := observationRun("exited", "complete", 0, true)
					proof := map[string]any{"scope": string(scope), "state": completed}
					r["version"], r["cleanup"] = 2, proof
					tc.mutate(r, proof)
					// Printed success JSON cannot rescue a rejected structured envelope.
					printed := `{"version":2,"kind":"run","state":"exited","exit_code":0,"cleanup":{"scope":"linux_descendants_v1","state":"retired"}}`
					response := map[string]any{"structuredContent": r, "content": []any{map[string]any{"type": "text", "text": printed}}}
					require.Nil(t, codexObservationHook(t, project, "posttooluse", observedExecute, nil, map[string]any{
						"session_id": "main", "agent_id": agent, "tool_response": response,
					}))
					store := newCodexMainObservationStore(project)
					if agent != "" {
						store = newObservationStore(project)
					}
					if tc.eligible {
						requireExecuteRedirect(t, mainHTTP(t, project, "main", agent, "curl https://example.invalid"))
						require.NotContains(t, mainHTTP(t, project, "main", agent, "curl https://example.invalid"), "permissionDecision")
					} else {
						require.NoDirExists(t, store.dir)
					}
				})
			}
		}
	}
}

func TestCapturedRunObservationStates(t *testing.T) {
	isolateRoutineHook(t)
	for _, agent := range []string{"", "child"} {
		for _, tc := range []struct {
			name, state, capture string
			exit                 any
			persisted, eligible  bool
		}{
			{"healthy running", "running", "recording", nil, false, true},
			{"terminal success", "exited", "complete", 0, true, true},
			{"not started", "not_started", "not_started", nil, false, false},
			{"admitted", "admitted", "not_started", nil, false, false},
			{"queued", "queued", "not_started", nil, false, false},
			{"startup failed", "start_failed", "not_started", nil, true, false},
			{"timed out", "timed_out", "complete", 0, true, false},
			{"cancelled", "cancelled", "complete", 0, true, false},
			{"interrupted", "interrupted", "complete", nil, true, false},
			{"nonzero", "exited", "complete", 1, true, false},
			{"running capture failed", "running", "incomplete", nil, false, false},
			{"terminal capture failed", "exited", "incomplete", 0, true, false},
			{"terminal not persisted", "exited", "complete", 0, false, false},
			{"missing exit code", "exited", "complete", nil, true, false},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				project := t.TempDir()
				store := newCodexMainObservationStore(project)
				if agent != "" {
					store = newObservationStore(project)
				}
				response := map[string]any{"isError": false, "structuredContent": observationRun(tc.state, tc.capture, tc.exit, tc.persisted)}
				invoke := func() {
					require.Nil(t, codexObservationHook(t, project, "posttooluse", "mcp__alias__capy_execute", nil, map[string]any{
						"session_id": "main", "agent_id": agent, "hook_event_name": "PostToolUse", "tool_response": response,
					}))
				}
				beforeTime := time.Now().UnixMilli()
				invoke()
				if tc.eligible {
					state, err := readObservations(observationFile(store), time.Now().UnixMilli(), store.scope)
					require.NoError(t, err)
					require.Len(t, state.Entries, 1)
					require.GreaterOrEqual(t, state.Entries[0].Tools[observedExecute], beforeTime, "timestamp the observed response, not execution start/completion")
					requireExecuteRedirect(t, mainHTTP(t, project, "main", agent, "curl https://example.invalid"))
					require.NotContains(t, mainHTTP(t, project, "main", agent, "curl https://example.invalid"), "permissionDecision")
					return
				}
				require.NoDirExists(t, store.dir)
				store.now = func() time.Time { return time.Now().Add(-30 * time.Second) }
				require.NoError(t, store.record("codex:main", agent, observedExecute))
				before, err := os.ReadFile(observationFile(store))
				require.NoError(t, err)
				invoke()
				after, err := os.ReadFile(observationFile(store))
				require.NoError(t, err)
				require.Equal(t, before, after, "late failure must neither refresh nor erase past evidence")
			})
		}
	}
}

func TestCapturedObservationEnvelopeValidation(t *testing.T) {
	isolateRoutineHook(t)
	for _, tc := range []struct {
		name     string
		mutate   func(map[string]any)
		eligible bool
	}{
		{"valid", func(map[string]any) {}, true},
		{"null id", func(m map[string]any) { m["run_id"] = nil }, false},
		{"blank id", func(m map[string]any) { m["run_id"] = " " }, false},
		{"wrong version", func(m map[string]any) { m["version"] = 2 }, false},
		{"string version", func(m map[string]any) { m["version"] = "1" }, false},
		{"query", func(m map[string]any) { m["kind"] = "query" }, false},
		{"missing capture", func(m map[string]any) { delete(m, "capture") }, false},
		{"signal", func(m map[string]any) { m["signal"] = 9 }, false},
		{"error", func(m map[string]any) { m["error"] = map[string]any{"code": "failed"} }, false},
		{"running exit", func(m map[string]any) { m["exit_code"] = 1 }, false},
	} {
		for _, shape := range []string{"direct", "structured", "snake case", "serialized", "array"} {
			t.Run(tc.name+"/"+shape, func(t *testing.T) {
				project := t.TempDir()
				run := observationRun("running", "recording", nil, false)
				tc.mutate(run)
				var response any = run
				if shape != "direct" {
					response = map[string]any{"isError": false, "structuredContent": run, "content": []any{map[string]any{"type": "text", "text": "arbitrary error discussion"}}}
				}
				if shape == "snake case" {
					response = map[string]any{"isError": false, "structured_content": run}
				}
				if shape == "serialized" {
					encoded, err := json.Marshal(response)
					require.NoError(t, err)
					response = string(encoded)
				} else if shape == "array" {
					response = []any{response}
				}
				require.Nil(t, codexObservationHook(t, project, "posttooluse", "other/capy_execute", nil, map[string]any{"tool_response": response}))
				store := newCodexMainObservationStore(project)
				if tc.eligible {
					require.FileExists(t, observationFile(store))
				} else {
					require.NoDirExists(t, store.dir)
				}
			})
		}
	}
	for _, content := range []any{nil, "", "{}", []any{}, map[string]any{}} {
		project := t.TempDir()
		require.Nil(t, codexObservationHook(t, project, "posttooluse", observedExecute, nil, map[string]any{"tool_response": map[string]any{"isError": false, "structuredContent": content}}))
		require.NoDirExists(t, newCodexMainObservationStore(project).dir, "malformed modern envelopes must not use legacy fallback")
	}
}

func TestRunQueriesNeverRefreshObservations(t *testing.T) {
	isolateRoutineHook(t)
	for _, agent := range []string{"", "child"} {
		project := t.TempDir()
		store := newCodexMainObservationStore(project)
		if agent != "" {
			store = newObservationStore(project)
		}
		store.now = func() time.Time { return time.Now().Add(-30 * time.Second) }
		require.NoError(t, store.record("codex:main", agent, observedExecute))
		before, err := os.ReadFile(observationFile(store))
		require.NoError(t, err)
		for _, tool := range []string{"capy_runs", "mcp__alias__capy_runs", "capy_execute_file", "capy_batch_execute", "Bash"} {
			for _, action := range []string{"status", "read", "inspect", "search", "cancel"} {
				require.Nil(t, codexObservationHook(t, project, "posttooluse", tool, map[string]any{"action": action}, map[string]any{
					"session_id": "main", "agent_id": agent,
					"tool_response": map[string]any{"isError": false, "structuredContent": observationRun("running", "recording", nil, false)},
				}))
			}
		}
		after, err := os.ReadFile(observationFile(store))
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
}

func TestRecoveryEnvelopesNeverCreateObservations(t *testing.T) {
	isolateRoutineHook(t)
	for _, agent := range []string{"", "child"} {
		for _, version := range []int{1, 2} {
			for _, scope := range []executor.CleanupScope{executor.CleanupLinuxDescendants, executor.CleanupDarwinObservation} {
				for _, state := range []string{"running", "exited"} {
					for _, value := range []struct {
						name  string
						value any
					}{
						{"object", map[string]any{"state": "operator_resolved"}}, {"null", nil}, {"string", "resolved"}, {"array", []any{}}, {"number", 1}, {"boolean", false},
					} {
						t.Run(fmt.Sprintf("%s/v%d/%s/%s/%s", agent, version, scope, state, value.name), func(t *testing.T) {
							project := t.TempDir()
							r := observationRun("running", "recording", nil, false)
							cleanup := "pending"
							if state == "exited" {
								r = observationRun("exited", "complete", 0, true)
								cleanup = "retired"
								if scope == executor.CleanupDarwinObservation {
									cleanup = "observed"
								}
							}
							r["version"], r["cleanup"], r["recovery"] = version, map[string]any{"scope": scope, "state": cleanup}, value.value
							for _, response := range []any{r, map[string]any{"structuredContent": r}, map[string]any{"recovery": value.value}} {
								require.Nil(t, codexObservationHook(t, project, "posttooluse", observedExecute, nil, map[string]any{"session_id": "main", "agent_id": agent, "tool_response": response}))
							}
							store := newCodexMainObservationStore(project)
							if agent != "" {
								store = newObservationStore(project)
							}
							require.NoDirExists(t, store.dir)
							delete(r, "recovery")
							require.Nil(t, codexObservationHook(t, project, "posttooluse", observedExecute, nil, map[string]any{"session_id": "main", "agent_id": agent, "tool_response": map[string]any{"structuredContent": r}}))
							require.FileExists(t, observationFile(store), "healthy control still records evidence")
						})
					}
				}
			}
		}
	}
}

func TestRecoveryPrintedContentAndFetchRemainObservations(t *testing.T) {
	isolateRoutineHook(t)
	for _, tool := range []string{observedExecute, observedFetch} {
		t.Run(tool, func(t *testing.T) {
			project := t.TempDir()
			response := map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"recovery":null}`}}}
			if tool == observedFetch {
				response["recovery"] = nil
			}
			require.Nil(t, codexObservationHook(t, project, "posttooluse", tool, nil, map[string]any{"session_id": "main", "agent_id": "child", "tool_response": response}))
			require.FileExists(t, observationFile(newObservationStore(project)))
		})
	}
}
