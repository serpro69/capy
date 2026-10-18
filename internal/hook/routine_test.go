package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/require"
)

func isolateRoutineHook(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("CAPY_DB_KEY", "")
	t.Setenv("CAPY_VAULT_KEY", "")
}

func TestRoutineGatePlatformScope(t *testing.T) {
	isolateRoutineHook(t)
	for _, a := range []adapter.HookAdapter{&adapter.CodexAdapter{}, &adapter.ClaudeCodeAdapter{}} {
		t.Run(string(a.Platform()), func(t *testing.T) {
			project := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[routing.routine]\nenabled=true\n"), 0600))
			out, err := handleEvent("pretooluse", hookInput(t, "Bash", map[string]any{"command": "make test"}, nil), a, &project)
			require.NoError(t, err)
			if a.Platform() == adapter.Codex {
				require.Equal(t, routineCaptureReason, ccParse(t, out)["permissionDecisionReason"])
			} else if out != nil {
				require.NotEqual(t, "deny", ccParse(t, out)["permissionDecision"])
			}
		})
	}
}

func TestRoutineGateBeforeHTTPObservations(t *testing.T) {
	isolateRoutineHook(t)
	for _, agent := range []string{"", "child"} {
		for _, evidence := range []string{"absent", "fresh", "expired", "consumed", "corrupt", "locked", "write failure"} {
			t.Run(agent+"/"+evidence, func(t *testing.T) {
				project := t.TempDir()
				store := newCodexMainObservationStore(project)
				if agent != "" {
					store = newObservationStore(project)
				}
				if evidence != "absent" {
					if evidence == "expired" {
						store.now = func() time.Time { return time.Now().Add(-observationTTL) }
					}
					require.NoError(t, store.record("codex:main", agent, observedExecute))
					if agent != "" {
						require.NoError(t, store.record("codex:main", agent, observedFetch))
					}
				}
				switch evidence {
				case "consumed":
					_, err := store.consume("codex:main", agent, false)
					require.NoError(t, err)
				case "corrupt":
					require.NoError(t, os.WriteFile(observationFile(store), []byte("{broken"), 0600))
				case "locked":
					lock, err := lockObservations(filepath.Join(store.dir, store.scope.stem()+".lock"))
					require.NoError(t, err)
					defer lock.Close()
				case "write failure":
					require.NoError(t, os.Mkdir(observationFile(store)+".tmp", 0700))
					require.NoError(t, os.WriteFile(filepath.Join(observationFile(store)+".tmp", "sentinel"), nil, 0600))
				}
				var before []byte
				if evidence != "absent" {
					var err error
					before, err = os.ReadFile(observationFile(store))
					require.NoError(t, err)
				}
				for _, command := range []string{
					"make test", "curl https://example.invalid; make test",
					"make test; curl -s -o file https://example.invalid",
					`node -e "fetch('https://example.invalid')"; go test ./...`,
					`capy execute --code 'make test'; cargo test`,
				} {
					response := mainHTTP(t, project, "main", agent, command)
					require.Equal(t, "deny", response["permissionDecision"])
					require.Equal(t, routineCaptureReason, response["permissionDecisionReason"])
					require.NotContains(t, response, "updatedInput")
					require.NotContains(t, response["permissionDecisionReason"], "retry the native tool")
					if evidence == "absent" {
						require.NoDirExists(t, store.dir)
					} else {
						after, err := os.ReadFile(observationFile(store))
						require.NoError(t, err)
						require.Equal(t, before, after, "routine denial must not consume, expire or repair observations")
					}
				}
			})
		}
	}
}

func TestRoutineExceptionsNeverBypassSecurity(t *testing.T) {
	isolateRoutineHook(t)
	for _, tc := range []struct {
		name, command, routing, pattern string
		routine                         bool
	}{
		{name: "disabled", command: "make test", routing: "enabled=false"},
		{name: "exception", command: "env command make test", routing: `native_allow=['Bash(make:*)']`},
		{name: "exception cannot cover sibling", command: "make test; cargo test", routing: `native_allow=['Bash(make:*)']`, routine: true},
		{name: "all excepted", command: "make test; cargo test", routing: `native_allow=['Bash(make:*)','Bash(cargo:*)']`},
		{name: "deny with disabled gate", command: "blocked PRIVATE; make test", routing: "enabled=false", pattern: "Bash(blocked:*)"},
		{name: "ask with blanket exception", command: "confirm PRIVATE; make test", routing: `native_allow=['Bash(*)']`, pattern: "Bash(confirm:*)"},
		{name: "capture deny after ask", command: `confirm PRIVATE; capy execute --code 'blocked PRIVATE'; make test`, pattern: "Bash(blocked:*)"},
		{name: "captured ask", command: `capy execute --code 'confirm PRIVATE'`, pattern: "Bash(confirm:*)"},
		{name: "captured python deny", command: `capy execute --language python --code 'import os; os.system("blocked PRIVATE")'`, pattern: "Bash(blocked:*)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[routing.routine]\n"+tc.routing), 0600))
			require.NoError(t, os.Mkdir(filepath.Join(project, ".claude"), 0700))
			source := filepath.Join(project, ".claude", "settings.json")
			require.NoError(t, os.WriteFile(source, []byte(`{"permissions":{"deny":["Bash(blocked:*)"],"ask":["Bash(confirm:*)"]}}`), 0600))
			response := mainHTTP(t, project, "main", "", tc.command)
			if tc.pattern != "" {
				require.Equal(t, "deny", response["permissionDecision"])
				require.Contains(t, response["permissionDecisionReason"], tc.pattern)
				require.Contains(t, response["permissionDecisionReason"], source)
				require.NotContains(t, response["permissionDecisionReason"], "PRIVATE")
			} else if tc.routine {
				require.Equal(t, routineCaptureReason, response["permissionDecisionReason"])
			} else {
				require.NotContains(t, response, "permissionDecision")
			}
		})
	}
}

func TestCapturedCLIExcludesOnlyItsOwnHTTP(t *testing.T) {
	isolateRoutineHook(t)
	project := t.TempDir()
	mainPost(t, project, "main", "", observedExecute, true)
	store := newCodexMainObservationStore(project)
	before, err := os.ReadFile(observationFile(store))
	require.NoError(t, err)
	command := `capy execute --language javascript --code "fetch('https://example.invalid')"`
	require.NotContains(t, mainHTTP(t, project, "main", "", command), "permissionDecision")
	after, err := os.ReadFile(observationFile(store))
	require.NoError(t, err)
	require.Equal(t, before, after)
	requireExecuteRedirect(t, mainHTTP(t, project, "main", "", command+"; curl https://example.invalid"))
	requireMainAdvice(t, mainHTTP(t, project, "main", "", "curl https://example.invalid"))
	for _, command := range []string{
		"env --debug curl https://example.invalid",
		"env TOKEN=$TOKEN curl https://example.invalid",
		`env -S "node -e fetch('https://example.invalid')"`,
		`mise exec "$TOOL" -- curl https://example.invalid`,
	} {
		mainPost(t, project, "main", "", observedExecute, true)
		requireExecuteRedirect(t, mainHTTP(t, project, "main", "", command))
	}
}

func TestCaptureCodeFileHookAdmission(t *testing.T) {
	isolateRoutineHook(t)
	for _, mode := range []string{"allowed", "code deny", "code ask", "read deny", "read ask", "missing", "explicit owner", "symlink escape"} {
		t.Run(mode, func(t *testing.T) {
			project := t.TempDir()
			owner := project
			command := "capy execute --code-file program.sh --cwd unrelated"
			if mode == "explicit owner" {
				owner = t.TempDir()
				command = "capy --project-dir '" + owner + "' execute --code-file program.sh"
			}
			require.NoError(t, os.WriteFile(filepath.Join(owner, ".capy.toml"), []byte("[store]\nkey_file='missing.key'\n"), 0600))
			body := "make test"
			if strings.HasPrefix(mode, "code ") {
				body = "guarded PRIVATE"
			}
			if mode != "missing" {
				require.NoError(t, os.WriteFile(filepath.Join(owner, "program.sh"), []byte(body), 0600))
			}
			permissions := map[string][]string{}
			switch mode {
			case "code deny":
				permissions["deny"] = []string{"Bash(guarded:*)"}
			case "code ask":
				permissions["ask"] = []string{"Bash(guarded:*)"}
			case "read deny":
				permissions["deny"] = []string{"Read(program.sh)"}
			case "read ask":
				permissions["ask"] = []string{"Read(program.sh)"}
			case "symlink escape":
				outside := filepath.Join(t.TempDir(), "outside.sh")
				require.NoError(t, os.WriteFile(outside, []byte(body), 0600))
				require.NoError(t, os.Symlink(outside, filepath.Join(owner, "link.sh")))
				command = "capy execute --code-file link.sh"
			}
			encoded, err := json.Marshal(map[string]any{"permissions": permissions})
			require.NoError(t, err)
			require.NoError(t, os.Mkdir(filepath.Join(owner, ".claude"), 0700))
			source := filepath.Join(owner, ".claude", "settings.json")
			require.NoError(t, os.WriteFile(source, encoded, 0600))
			response := codexObservationHook(t, project, "pretooluse", "Bash", map[string]any{"command": command}, map[string]any{"cwd": project})
			if mode == "allowed" || mode == "explicit owner" {
				require.NotContains(t, response, "permissionDecision")
			} else {
				require.Equal(t, "deny", response["permissionDecision"])
				if strings.Contains(mode, "deny") || strings.Contains(mode, "ask") {
					require.Contains(t, response["permissionDecisionReason"], source)
				}
				require.NotContains(t, response["permissionDecisionReason"], "PRIVATE")
			}
			require.NoDirExists(t, filepath.Join(owner, ".capy", "runs"))
			require.NoFileExists(t, filepath.Join(owner, "knowledge.db"))
		})
	}
}

func TestHTTPNativeLeafBoundaries(t *testing.T) {
	isolateRoutineHook(t)
	for _, agent := range []string{"", "child"} {
		project := t.TempDir()
		mainPost(t, project, "main", agent, observedExecute, true)
		store := newCodexMainObservationStore(project)
		if agent != "" {
			store = newObservationStore(project)
		}
		before, err := os.ReadFile(observationFile(store))
		require.NoError(t, err)
		for _, safe := range []string{
			"curl -s -o first https://example.invalid; curl -s -o second https://example.invalid",
			">download curl -s https://example.invalid",
			">download env CI=1 command curl -s https://example.invalid",
			`bash -c 'curl -s https://example.invalid' >download`,
		} {
			require.NotContains(t, mainHTTP(t, project, "main", agent, safe), "permissionDecision")
			after, err := os.ReadFile(observationFile(store))
			require.NoError(t, err)
			require.Equal(t, before, after, "safe downloads must not consume evidence")
		}
		for _, command := range []string{
			"curl https://example.invalid; curl -s -o output https://other.invalid",
			"curl -s -o output https://other.invalid; curl https://example.invalid",
			"curl https://example.invalid\ncurl -s -o output https://other.invalid",
			`env CI=1 curl https://example.invalid; curl -s -o output https://other.invalid`,
		} {
			mainPost(t, project, "main", agent, observedExecute, true)
			requireExecuteRedirect(t, mainHTTP(t, project, "main", agent, command))
		}
	}
}
