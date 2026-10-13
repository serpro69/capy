package hook

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/require"
)

func mainHTTP(t *testing.T, project, session, agent, command string) map[string]any {
	t.Helper()
	return codexObservationHook(t, project, "pretooluse", "Bash", map[string]any{"command": command}, map[string]any{"session_id": session, "agent_id": agent})
}

func mainPost(t *testing.T, project, session, agent, tool string, success bool) {
	t.Helper()
	require.Nil(t, codexObservationHook(t, project, "posttooluse", tool, nil, map[string]any{
		"session_id": session, "agent_id": agent, "tool_response": map[string]any{"isError": !success},
	}))
}

func requireMainAdvice(t *testing.T, response map[string]any) {
	t.Helper()
	require.NotContains(t, response, "permissionDecision")
	require.NotContains(t, response, "updatedInput")
	require.Equal(t, codexMainToolGuidance, response["additionalContext"])
}

func requireExecuteRedirect(t *testing.T, response map[string]any) {
	t.Helper()
	require.Equal(t, "deny", response["permissionDecision"])
	require.Contains(t, response["permissionDecisionReason"], "Use capy_execute(language, code)")
	require.Contains(t, response["permissionDecisionReason"], "retry the native tool")
	require.NotContains(t, response, "updatedInput")
}

func TestCodexMainRoutingEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	for _, command := range []string{"curl https://example.invalid", "wget https://example.invalid", `node -e "fetch('https://example.invalid')"`} {
		requireMainAdvice(t, mainHTTP(t, project, "main", "", command))
	}
	require.NoDirExists(t, filepath.Join(project, ".capy"))
	for _, tool := range []string{observedFetch, "capy_batch_execute", "capy_stats"} {
		mainPost(t, project, "main", "", tool, true)
		requireMainAdvice(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
	}
	require.NoDirExists(t, filepath.Join(project, ".capy"))
	mainPost(t, project, "main", "", observedExecute, true)
	s := newCodexMainObservationStore(project)
	before, err := os.ReadFile(observationFile(s))
	require.NoError(t, err)
	for _, command := range []string{"true", "curl -s -o file https://example.invalid", "wget -q -O file https://example.invalid"} {
		response := mainHTTP(t, project, "main", "", command)
		require.NotContains(t, response, "permissionDecision")
		after, err := os.ReadFile(observationFile(s))
		require.NoError(t, err)
		require.Equal(t, before, after, "ordinary/safe-download commands cannot consume evidence")
	}
	require.Nil(t, codexObservationHook(t, project, "pretooluse", "capy_stats", map[string]any{}, map[string]any{"session_id": "main"}))
	requireExecuteRedirect(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
	requireMainAdvice(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
	mainPost(t, project, "main", "", observedExecute, false)
	requireMainAdvice(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
	mainPost(t, project, "main", "", observedExecute, true)
	mainPost(t, project, "main", "", observedExecute, false)
	requireExecuteRedirect(t, mainHTTP(t, project, "main", "", `node -e "fetch('https://example.invalid')"`))
	requireMainAdvice(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
	s.now = func() time.Time { return time.Now().Add(-observationTTL) }
	require.NoError(t, s.record("codex:main", "", observedExecute))
	requireMainAdvice(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
	mainPost(t, project, "main", "", observedExecute, true)
	requireMainAdvice(t, mainHTTP(t, project, "", "", "curl https://example.invalid"))
	requireExecuteRedirect(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
}

func TestCodexMainRoutingSecurityPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, evidence := range []string{"absent", "fresh", "expired", "corrupt", "locked"} {
		t.Run(evidence, func(t *testing.T) {
			project := t.TempDir()
			s := newCodexMainObservationStore(project)
			if evidence != "absent" {
				if evidence == "expired" {
					s.now = func() time.Time { return time.Now().Add(-observationTTL) }
				}
				require.NoError(t, s.record("codex:main", "", observedExecute))
			}
			if evidence == "corrupt" {
				require.NoError(t, os.WriteFile(observationFile(s), []byte(`{broken`), 0o600))
			}
			if evidence == "locked" {
				lock, err := lockObservations(filepath.Join(s.dir, "codex-main-observations.lock"))
				require.NoError(t, err)
				defer lock.Close()
			}
			var before []byte
			if evidence != "absent" {
				var err error
				before, err = os.ReadFile(observationFile(s))
				require.NoError(t, err)
			}
			require.NoError(t, os.Mkdir(filepath.Join(project, ".claude"), 0o755))
			source := filepath.Join(project, ".claude", "settings.json")
			require.NoError(t, os.WriteFile(source, []byte(`{"permissions":{"deny":["Bash(blocked:*)"],"ask":["Bash(confirm:*)"]}}`), 0o600))
			for _, tc := range []struct{ name, command, pattern string }{
				{"deny", "blocked private; curl https://example.invalid", "Bash(blocked:*)"},
				{"ask", "confirm private; curl https://example.invalid", "Bash(confirm:*)"},
				{"later deny", "confirm private; curl https://example.invalid; blocked private", "Bash(blocked:*)"},
				{"scanner", "curl https://example.invalid; " + strings.Repeat(":;", 4097), "limit exceeded"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					response := mainHTTP(t, project, "main", "", tc.command)
					require.Equal(t, "deny", response["permissionDecision"])
					require.Contains(t, response["permissionDecisionReason"], tc.pattern)
					if tc.name != "scanner" {
						require.Contains(t, response["permissionDecisionReason"], source)
					}
					require.NotContains(t, response["permissionDecisionReason"], "private")
					if evidence == "absent" {
						require.NoDirExists(t, s.dir)
					} else {
						after, err := os.ReadFile(observationFile(s))
						require.NoError(t, err)
						require.Equal(t, before, after, "policy checks cannot consume, expire or repair routing state")
					}
				})
			}
		})
	}
}

func TestCodexMainRoutingStateFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, failure := range []string{"locked", "write failure", "corrupt"} {
		t.Run(failure, func(t *testing.T) {
			project := t.TempDir()
			s := newCodexMainObservationStore(project)
			require.NoError(t, s.record("codex:main", "", observedExecute))
			before, err := os.ReadFile(observationFile(s))
			require.NoError(t, err)
			switch failure {
			case "locked":
				lock, err := lockObservations(filepath.Join(s.dir, "codex-main-observations.lock"))
				require.NoError(t, err)
				defer lock.Close()
			case "write failure":
				// A nonempty staging directory deterministically prevents the
				// consumption commit, even when tests run as root.
				require.NoError(t, os.Mkdir(observationFile(s)+".tmp", 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(observationFile(s)+".tmp", "sentinel"), nil, 0o600))
			case "corrupt":
				before = []byte(`{broken`)
				require.NoError(t, os.WriteFile(observationFile(s), before, 0o600))
			}
			requireMainAdvice(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
			after, err := os.ReadFile(observationFile(s))
			require.NoError(t, err)
			require.Equal(t, before, after, "an uncommitted consumption cannot authorize a denial")
		})
	}
}

func TestCodexMainConcurrentRedirect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	mainPost(t, project, "main", "", observedExecute, true)
	input := hookInput(t, "Bash", map[string]any{"command": "curl https://example.invalid"}, map[string]any{"session_id": "main"})
	const contenders = 16
	start := make(chan struct{})
	type result struct {
		output []byte
		err    error
	}
	results := make(chan result, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			output, err := handleEvent("pretooluse", input, &adapter.CodexAdapter{}, &project)
			results <- result{output, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	redirects := 0
	for result := range results {
		require.NoError(t, result.err)
		response := ccParse(t, result.output)
		if response["permissionDecision"] == "deny" {
			requireExecuteRedirect(t, response)
			redirects++
		} else {
			requireMainAdvice(t, response)
		}
	}
	require.Equal(t, 1, redirects)
}

func TestCodexMainChildIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	command := "curl https://example.invalid"
	mainPost(t, project, "main", "", observedExecute, true)
	require.NotContains(t, mainHTTP(t, project, "main", "child", command), "permissionDecision")
	requireMainAdvice(t, mainHTTP(t, project, "sibling", "", command))
	requireMainAdvice(t, mainHTTP(t, t.TempDir(), "main", "", command))
	// Even a Claude raw ID equal to the internal Codex ID cannot select main scope.
	response := observationHook(t, project, "pretooluse", "Bash", map[string]any{"command": command}, map[string]any{"session_id": "codex:main"})
	require.Equal(t, "deny", response["permissionDecision"], "Claude main routing remains enforced")
	requireExecuteRedirect(t, mainHTTP(t, project, "main", "", command))
	mainPost(t, project, "main", "child", observedExecute, true)
	requireMainAdvice(t, mainHTTP(t, project, "main", "", command))
	require.NotContains(t, mainHTTP(t, project, "main", "sibling-child", command), "permissionDecision")
	requireExecuteRedirect(t, mainHTTP(t, project, "main", "child", command))
	require.NotContains(t, mainHTTP(t, project, "main", "child", command), "permissionDecision")
}

func TestCodexMainSessionEnd(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "")
	t.Setenv("CAPY_VAULT_KEY", "")
	for _, failure := range []string{"none", "main", "child"} {
		t.Run(failure, func(t *testing.T) {
			project := t.TempDir()
			main, child := newCodexMainObservationStore(project), newObservationStore(project)
			for _, session := range []string{"ending", "sibling"} {
				require.NoError(t, main.record("codex:"+session, "", observedExecute))
				require.NoError(t, child.record("codex:"+session, "child", observedFetch))
			}
			beforeMain, err := os.Stat(filepath.Join(main.dir, "codex-main-observations.lock"))
			require.NoError(t, err)
			beforeChild, err := os.Stat(filepath.Join(main.dir, "tool-observations.lock"))
			require.NoError(t, err)
			for _, id := range []struct{ session, agent string }{{"", ""}, {"ending", "child"}} {
				require.Nil(t, codexObservationHook(t, project, "sessionend", "", nil, map[string]any{"session_id": id.session, "agent_id": id.agent}))
				for _, s := range []observationStore{main, child} {
					state, err := readObservations(observationFile(s), time.Now().UnixMilli(), s.scope)
					require.NoError(t, err)
					require.Len(t, state.Entries, 2)
				}
			}
			if failure == "main" {
				require.NoError(t, os.WriteFile(observationFile(main), []byte(`{broken`), 0o600))
			}
			if failure == "child" {
				require.NoError(t, os.WriteFile(observationFile(child), []byte(`{broken`), 0o600))
			}
			require.Nil(t, codexObservationHook(t, project, "sessionend", "", nil, map[string]any{"session_id": "ending"}))
			for i, s := range []observationStore{main, child} {
				if failure == "main" && i == 0 || failure == "child" && i == 1 {
					continue
				}
				state, err := readObservations(observationFile(s), time.Now().UnixMilli(), s.scope)
				require.NoError(t, err)
				require.Len(t, state.Entries, 1)
				require.Equal(t, sessionIDComponent("codex:sibling"), state.Entries[0].Session)
			}
			afterMain, err := os.Stat(filepath.Join(main.dir, "codex-main-observations.lock"))
			require.NoError(t, err)
			afterChild, err := os.Stat(filepath.Join(main.dir, "tool-observations.lock"))
			require.NoError(t, err)
			require.True(t, os.SameFile(beforeMain, afterMain))
			require.True(t, os.SameFile(beforeChild, afterChild))
			files, err := os.ReadDir(main.dir)
			require.NoError(t, err)
			require.Len(t, files, 4, "cleanup must not create database or guidance files")
		})
	}
	t.Run("absent state", func(t *testing.T) {
		project := t.TempDir()
		require.Nil(t, codexObservationHook(t, project, "sessionend", "", nil, map[string]any{"session_id": "ending"}))
		entries, err := os.ReadDir(project)
		require.NoError(t, err)
		require.Empty(t, entries)
	})
	t.Run("Claude cleanup cannot touch main", func(t *testing.T) {
		project := t.TempDir()
		main := newCodexMainObservationStore(project)
		require.NoError(t, main.record("codex:ending", "", observedExecute))
		before, err := os.ReadFile(observationFile(main))
		require.NoError(t, err)
		require.Nil(t, observationHook(t, project, "sessionend", "", nil, map[string]any{"session_id": "codex:ending"}))
		after, err := os.ReadFile(observationFile(main))
		require.NoError(t, err)
		require.Equal(t, before, after)
	})
}
