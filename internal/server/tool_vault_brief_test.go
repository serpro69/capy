package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 2 (vault-session-brief): capy_vault_brief serves the same brief as
// `capy vault brief` under vault search's project scope, and both keyword
// search paths offer it on eligible main-transcript hits.

func vaultBriefReq(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = vaultBriefToolName
	req.Params.Arguments = args
	return req
}

func vaultBriefCalls(srv *Server) int {
	return srv.stats.Snapshot().Calls[vaultBriefToolName]
}

func trackedVaultBriefBytes(srv *Server) int64 {
	return srv.stats.Snapshot().BytesReturned[vaultBriefToolName]
}

// callVaultBrief calls the handler and checks the tracking contract for a
// validated call: the decoded text is valid UTF-8 and tracked exactly once.
func callVaultBrief(t *testing.T, srv *Server, args map[string]any) (text string, isError bool) {
	t.Helper()
	return callVaultBriefCtx(t.Context(), t, srv, args)
}

func callVaultBriefCtx(ctx context.Context, t *testing.T, srv *Server, args map[string]any) (string, bool) {
	t.Helper()
	calls, before := vaultBriefCalls(srv), trackedVaultBriefBytes(srv)
	res, err := srv.handleVaultBrief(ctx, vaultBriefReq(args))
	require.NoError(t, err)
	text := decodedText(t, res)
	assert.True(t, utf8.ValidString(text))
	assert.Equal(t, calls+1, vaultBriefCalls(srv), "a validated call is tracked once")
	assert.Equal(t, int64(len(text)), trackedVaultBriefBytes(srv)-before, "tracked bytes equal the decoded text")
	return text, res.IsError
}

// cliBrief is what `capy vault brief <session>` prints for the same archive:
// the CLI resolves across the whole selected vault and renders with the same
// renderer. cmd/capy's stdio test compares the real binary's stdout.
func cliBrief(t *testing.T, session string) string {
	t.Helper()
	b, err := vault.NewVaultStore(vault.VaultDBPath()).ReadBrief(t.Context(), session, vault.BriefScope{})
	require.NoError(t, err)
	return vault.RenderBrief(b)
}

func vaultDBAbsent(t *testing.T) {
	t.Helper()
	_, err := os.Stat(vault.VaultDBPath())
	assert.True(t, os.IsNotExist(err), "the vault database must not be created")
}

func TestVaultBrief_Schema(t *testing.T) {
	tool := toolVaultBrief()
	assert.Equal(t, vaultBriefToolName, tool.Name)
	require.Len(t, tool.InputSchema.Properties, 3)
	for _, name := range []string{"session", "project", "all_projects"} {
		assert.Contains(t, tool.InputSchema.Properties, name)
	}
	assert.Equal(t, []string{"session"}, tool.InputSchema.Required)
	assert.Equal(t, false, tool.InputSchema.AdditionalProperties)
	require.NotNil(t, tool.Annotations.ReadOnlyHint)
	assert.True(t, *tool.Annotations.ReadOnlyHint)
	assert.False(t, *tool.Annotations.DestructiveHint)

	srv := newTestServer(t, nil)
	srv.registerToolsForTest()
	registered := srv.mcpServer.GetTool(vaultBriefToolName)
	require.NotNil(t, registered, "capy_vault_brief is registered")
	assert.NotNil(t, registered.Handler)
}

// Invalid requests fail before the vault is touched and stay untracked.
func TestVaultBrief_Validation(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)

	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"no arguments", nil, "provide session"},
		{"session missing", map[string]any{"project": "x"}, "provide session"},
		{"session number", map[string]any{"session": 12345678}, `"session": expected a string`},
		{"session null", map[string]any{"session": nil}, `"session": expected a string`},
		{"session too short", map[string]any{"session": "sweep-s"}, "at least 8 characters"},
		{"session NUL", map[string]any{"session": "sweep-se\x00ss"}, "must not contain NUL"},
		{"project number", map[string]any{"session": uuid1, "project": 7}, `"project": expected a string`},
		{"project NUL", map[string]any{"session": uuid1, "project": "pro\x00ject"}, "project selector must not contain NUL"},
		{"all_projects malformed", map[string]any{"session": uuid1, "all_projects": "yes"}, `"all_projects"`},
		{"through_line", map[string]any{"session": uuid1, "through_line": 3}, "through_line is not supported"},
		{"through_line beside unknown", map[string]any{"session": uuid1, "through_line": 3, "zzz": 1}, "through_line is not supported"},
		{"unknown argument", map[string]any{"session": uuid1, "line": 3}, `unsupported parameter "line"`},
		{"search argument", map[string]any{"session": uuid1, "queries": []any{"x"}}, `unsupported parameter "queries"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := srv.handleVaultBrief(t.Context(), vaultBriefReq(tt.args))
			require.NoError(t, err)
			require.True(t, res.IsError)
			assert.Contains(t, decodedText(t, res), tt.want)
			assert.Zero(t, vaultBriefCalls(srv), "validation errors stay untracked")
			vaultDBAbsent(t)
		})
	}
}

func TestVaultBrief_DisabledAndAbsent(t *testing.T) {
	t.Run("disabled vault", func(t *testing.T) {
		projectDir, uuid1, _ := setupVaultSweepProject(t)
		t.Setenv("CAPY_VAULT_KEY", "")
		srv := newTestServerWithProjectDir(t, nil, projectDir)
		text, isErr := callVaultBrief(t, srv, map[string]any{"session": uuid1})
		assert.True(t, isErr)
		assert.Contains(t, text, "CAPY_VAULT_KEY")
		vaultDBAbsent(t)
	})
	t.Run("absent vault", func(t *testing.T) {
		projectDir, uuid1, _ := setupVaultSweepProject(t)
		t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
		srv := newTestServerWithProjectDir(t, nil, projectDir)
		text, isErr := callVaultBrief(t, srv, map[string]any{"session": uuid1})
		assert.True(t, isErr)
		assert.Contains(t, text, "no vault database")
		vaultDBAbsent(t)
	})
}

func TestVaultBrief_RendersCLIText(t *testing.T) {
	projectDir, uuid1, uuid2 := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(t.Context())
	want := cliBrief(t, uuid1)

	text, isErr := callVaultBrief(t, srv, map[string]any{"session": uuid1})
	require.False(t, isErr, text)
	assert.Equal(t, want, text, "MCP text equals the CLI rendering")
	assert.LessOrEqual(t, len(text), vault.BriefMaxBytes)
	assert.Contains(t, text, `Session "`+uuid1+`" · claude-code`)
	assert.Contains(t, text, "Opening user · message starts at line 0: \"How do I configure the database? (turn 1)\"")
	assert.Contains(t, text, "Latest user · message starts at line 4: \"How do I configure the database? (turn 3)\"")
	assert.Contains(t, text, "Latest assistant · message starts at line 5:")

	t.Run("literal prefix", func(t *testing.T) {
		text, isErr := callVaultBrief(t, srv, map[string]any{"session": uuid1[:12]})
		require.False(t, isErr, text)
		assert.Equal(t, want, text)
	})
	t.Run("ambiguous prefix names no candidate", func(t *testing.T) {
		text, isErr := callVaultBrief(t, srv, map[string]any{"session": "sweep-se"})
		require.True(t, isErr)
		assert.Contains(t, text, "matches more than one session in the requested scope — use a longer ID")
		assert.NotContains(t, text, uuid1)
		assert.NotContains(t, text, uuid2)
	})
	t.Run("serves the same brief twice", func(t *testing.T) {
		text, isErr := callVaultBrief(t, srv, map[string]any{"session": uuid1})
		require.False(t, isErr)
		assert.Equal(t, want, text, "the writer handle opened by the sweep does not change the reader")
	})
}

// The caller's deadline shortens the read; a timed-out read returns no brief.
func TestVaultBrief_Deadline(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(t.Context())

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	text, isErr := callVaultBriefCtx(canceled, t, srv, map[string]any{"session": uuid1})
	assert.True(t, isErr)
	assert.Contains(t, text, "canceled")
	assert.NotContains(t, text, "Session \"")

	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	text, isErr = callVaultBriefCtx(expired, t, srv, map[string]any{"session": uuid1})
	assert.True(t, isErr)
	assert.Contains(t, text, "deadline exceeded")
	assert.NotContains(t, text, "Session \"")
}

// Without a project directory the default scope has nothing to match; it must
// fail rather than silently widen to the whole vault.
func TestVaultBrief_DefaultScopeNeedsProjectDir(t *testing.T) {
	projectDir, uuid1, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	srv.vaultSweep(t.Context())
	srv.projectDir = ""

	for _, args := range []map[string]any{
		{"session": uuid1},
		{"session": uuid1, "project": ""},
		{"session": uuid1, "all_projects": false},
	} {
		text, isErr := callVaultBrief(t, srv, args)
		assert.True(t, isErr)
		assert.Contains(t, text, "no project directory")
	}
	for _, args := range []map[string]any{
		{"session": uuid1, "all_projects": true},
		{"session": uuid1, "project": "*"},
	} {
		text, isErr := callVaultBrief(t, srv, args)
		require.False(t, isErr, text)
		assert.Contains(t, text, `Session "`+uuid1+`"`)
	}
}

// Task 2.2: lookup is scoped before the prefix decides zero, one or many.
func TestVaultBrief_ProjectScope(t *testing.T) {
	projectA, uuidA, projectB, uuidB := setupVaultSweepMultiProject(t)
	for i := 1; i <= 3; i++ {
		writeProjectSession(t, projectA, fmt.Sprintf("sweep-tri-%04d", i), "Triplet question", "Triplet answer.")
	}
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")
	srv := newTestServerWithProjectDir(t, nil, projectA)
	srv.vaultSweep(t.Context())

	const missHint = "needs a matching project selector or all_projects: true"
	type outcome int
	const (
		found outcome = iota
		ambiguous
		miss
	)
	cases := []struct {
		name    string
		args    map[string]any
		want    outcome
		session string
	}{
		{"prefix shared with an out-of-scope session", map[string]any{"session": "sweep-pr"}, found, uuidA},
		{"same prefix across all projects", map[string]any{"session": "sweep-pr", "all_projects": true}, ambiguous, ""},
		{"same prefix with star", map[string]any{"session": "sweep-pr", "project": "*"}, ambiguous, ""},
		{"string true widens", map[string]any{"session": "sweep-pr", "all_projects": " TrUe "}, ambiguous, ""},
		{"three-way local ambiguity", map[string]any{"session": "sweep-tr"}, ambiguous, ""},
		{"out-of-scope full ID", map[string]any{"session": uuidB}, miss, ""},
		{"out-of-scope full ID, unrelated project", map[string]any{"session": uuidB, "project": "unrelated"}, miss, ""},
		{"explicit other project", map[string]any{"session": uuidB, "project": projectB}, found, uuidB},
		{"all projects", map[string]any{"session": uuidB, "all_projects": true}, found, uuidB},
		{"widening overrides explicit", map[string]any{"session": uuidB, "all_projects": true, "project": "unrelated"}, found, uuidB},
		{"underscore is literal", map[string]any{"session": "sweep_proj-b-bbbb", "all_projects": true}, miss, ""},
		{"percent is literal", map[string]any{"session": "sweep-pro%", "all_projects": true}, miss, ""},
		{"backslash is literal", map[string]any{"session": `sweep-pr\`, "all_projects": true}, miss, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callVaultBrief(t, srv, tt.args)
			switch tt.want {
			case found:
				require.False(t, isErr, text)
				assert.Contains(t, text, `Session "`+tt.session+`"`)
			case ambiguous:
				require.True(t, isErr)
				assert.Contains(t, text, "use a longer ID")
				for _, id := range []string{uuidA, uuidB, "sweep-tri-0001", "sweep-tri-0002", "sweep-tri-0003"} {
					assert.NotContains(t, text, id, "ambiguity reveals no candidate")
				}
				assert.NotRegexp(t, `\b[23] sessions?\b`, text, "ambiguity reveals no count")
			case miss:
				require.True(t, isErr)
				assert.Contains(t, text, "no session in the requested scope matches")
				widened := tt.args["all_projects"] == true
				assert.Equal(t, !widened, strings.Contains(text, missHint), text)
			}
		})
	}

	t.Run("renamed project", func(t *testing.T) {
		const label = "labelonlyquasar"
		_, err := srv.getVault().SetSessionProject(t.Context(), uuidA, vault.ProjectOptions{Name: label})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := srv.getVault().SetSessionProject(context.Background(), uuidA, vault.ProjectOptions{Clear: true})
			require.NoError(t, err)
		})
		for _, args := range []map[string]any{
			{"session": uuidA},                         // default keeps the imported path
			{"session": uuidA, "project": "LABELONLY"}, // ASCII case-insensitive label
		} {
			text, isErr := callVaultBrief(t, srv, args)
			require.False(t, isErr, text)
			assert.Contains(t, text, `Project "`+label+`"`, "the brief shows the current effective project")
		}
		text, isErr := callVaultBrief(t, srv, map[string]any{"session": uuidA, "project": projectA})
		assert.True(t, isErr, "a replaced imported path no longer matches an explicit selector")
		assert.Contains(t, text, "no session in the requested scope matches")
	})
	assert.Equal(t, projectA, srv.projectDir, "scope resolution does not change the server directory")
}

// An independently archived Codex child is its own session: its UUID opens its
// own main transcript, not the parent's.
func TestVaultBrief_IndependentChild(t *testing.T) {
	home := setupCodexSweepEnv(t)
	project := t.TempDir()
	parent, child := codexSweepMatch, codexSweepOther
	writeCodexRollout(t, home, codexRolloutRel(parent), codexRolloutLines(t, parent, project, "plan the migration rollout"))
	writeCodexRollout(t, home, codexRolloutRel(child),
		codexChildRolloutLines(t, child, parent, project, "inspect the goroutine scheduler internals"))
	srv := newTestServerWithProjectDir(t, nil, project)
	require.Equal(t, 2, srv.vaultSweep(t.Context()).codex.Imported)

	text, isErr := callVaultBrief(t, srv, map[string]any{"session": child})
	require.False(t, isErr, text)
	assert.Contains(t, text, `Session "`+child+`" · codex`)
	assert.Contains(t, text, "inspect the goroutine scheduler internals")
	assert.NotContains(t, text, "plan the migration rollout")
	assert.Equal(t, cliBrief(t, child), text)

	text, isErr = callVaultBrief(t, srv, map[string]any{"session": parent})
	require.False(t, isErr, text)
	assert.Contains(t, text, "plan the migration rollout")
	assert.NotContains(t, text, "goroutine scheduler")

	// The shared 8-byte prefix is ambiguous between parent and child.
	text, isErr = callVaultBrief(t, srv, map[string]any{"session": parent[:8]})
	assert.True(t, isErr)
	assert.Contains(t, text, "use a longer ID")
}

// writeTurnsSession writes a Claude transcript with one user/assistant pair per
// turn, padded past the import size gate like writeSessionJSONL.
func writeTurnsSession(t *testing.T, dir, uuid string, turns [][2]string) {
	t.Helper()
	ts := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	var lines []string
	for i, turn := range turns {
		user, err := json.Marshal(map[string]any{
			"type": "user", "uuid": fmt.Sprintf("u%d", i), "timestamp": ts, "sessionId": uuid,
			"message": map[string]any{"id": fmt.Sprintf("um%d", i), "role": "user", "content": turn[0]},
		})
		require.NoError(t, err)
		asst, err := json.Marshal(map[string]any{
			"type": "assistant", "uuid": fmt.Sprintf("a%d", i), "timestamp": ts, "sessionId": uuid,
			"message": map[string]any{
				"id": fmt.Sprintf("am%d", i), "role": "assistant",
				"content": []map[string]any{{"type": "text", "text": turn[1] + " " + strings.Repeat("More context here. ", 5)}},
			},
		})
		require.NoError(t, err)
		lines = append(lines, string(user), string(asst))
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, uuid+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// briefActionSessions returns the session IDs of every full hit carrying the
// latest-exchange instruction, as a reader of the reply would find them.
var briefActionHit = regexp.MustCompile(`--- \[session:([^\]\n]+)\] ---\n### [^\n]*\n[^\n]*\n` + regexp.QuoteMeta(vaultBriefAction) + "\n")

func briefActionSessions(reply string) []string {
	var ids []string
	for _, m := range briefActionHit.FindAllStringSubmatch(reply, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// Task 2.3: following the displayed instruction from either search tool,
// with the search's own selectors, yields the session's latest exchange even
// though the matched term lies in an earlier turn.
func TestVaultBrief_FollowsSearchHit(t *testing.T) {
	projectA, _, projectB, _ := setupVaultSweepMultiProject(t)
	const local, remote = "xturn-local-0001", "xturn-remote-0001"
	turns := [][2]string{
		{"Calibrate the quasar pulsar timing model", "Calibrated the quasar pulsar timing model against the reference clock."},
		{"Tidy the changelog entries", "Tidied the changelog entries."},
		{"Which release notes remain open?", "Two release notes remain open."},
	}
	sessionDir := func(project string) string {
		dir, err := vault.ProjectSessionDir(project)
		require.NoError(t, err)
		return dir
	}
	writeTurnsSession(t, sessionDir(projectA), local, turns)
	writeTurnsSession(t, sessionDir(projectB), remote, turns)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")
	srv := newTestServerWithProjectDir(t, nil, projectA)
	srv.vaultSweep(t.Context())
	seedDurableDBGuide(t, srv) // a populated knowledge pass beside the vault

	search := map[string]func(args map[string]any) string{
		"capy_vault_search": func(args map[string]any) string {
			res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(args))
			require.NoError(t, err)
			require.False(t, res.IsError, resultText(res))
			return resultText(res)
		},
		"capy_search": func(args map[string]any) string {
			resetSearchThrottle(srv)
			res := callSearch(t, srv, args)
			require.False(t, res.IsError, resultText(res))
			return resultText(res)
		},
	}
	for tool, run := range search {
		for _, tt := range []struct {
			name      string
			selectors map[string]any
			session   string
		}{
			{"default scope", nil, local},
			{"explicit other project", map[string]any{"project": projectB}, remote},
			{"all projects", map[string]any{"all_projects": true}, ""},
		} {
			t.Run(tool+"/"+tt.name, func(t *testing.T) {
				args := map[string]any{"queries": []any{"quasar pulsar calibration"}}
				for k, v := range tt.selectors {
					args[k] = v
				}
				reply := run(args)
				ids := briefActionSessions(reply)
				require.NotEmpty(t, ids, "an eligible main hit carries the instruction:\n%s", reply)
				if tt.session != "" {
					require.Contains(t, ids, tt.session, reply)
					ids = []string{tt.session}
				}
				for _, id := range ids {
					// Follow the instruction: the displayed ID and the same
					// project/all_projects arguments as the search.
					briefArgs := map[string]any{"session": id}
					for k, v := range tt.selectors {
						briefArgs[k] = v
					}
					text, isErr := callVaultBrief(t, srv, briefArgs)
					require.False(t, isErr, text)
					assert.Contains(t, text, `Session "`+id+`"`)
					assert.Contains(t, text, `Latest user · message starts at line 4: "Which release notes remain open?"`)
					assert.Contains(t, text, `Latest assistant · message starts at line 5: "Two release notes remain open.`)
					assert.Contains(t, text, `Opening user · message starts at line 0: "Calibrate the quasar pulsar timing model"`,
						"the matched term is only in the opening turn")
				}
			})
		}
	}
}

func TestRenderVaultHit_BriefAction(t *testing.T) {
	main := vault.SearchResult{
		SessionUUID: "11111111-0000-4000-8000-000000000001", Title: "Release checks",
		LineIndex: 12, ChunkIndex: 3, HasChunkIndex: true, Snippet: "unrelated passage text",
	}
	withID := func(id string) vault.SearchResult { r := main; r.SessionUUID = id; return r }
	sidecar := main
	sidecar.SubagentID = "agent-explorer"
	child := main
	child.ParentUUID = "11111111-0000-4000-8000-000000000000"

	for _, tt := range []struct {
		name  string
		hit   vault.SearchResult
		offer bool
	}{
		{"main hit, term only in the title", main, true},
		{"standalone child uses its own ID", child, true},
		{"legacy filename ID", withID("sweep-sess-aaaa"), true},
		{"ID at the display limit", withID(strings.Repeat("a", vaultSessionIDMaxBytes)), true},
		{"sidecar at the same line", sidecar, false},
		{"ID above the display limit", withID(strings.Repeat("a", vaultSessionIDMaxBytes+1)), false},
		{"redacted ID", withID("sk-ant-" + strings.Repeat("Q", 40)), false},
		{"escaped ID", withID("abcdefgh\nijk"), false},
		{"ID shorter than a brief selector", withID("abc1234"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			block := renderVaultHit(tt.hit).block
			assert.Equal(t, tt.offer, strings.Contains(block, vaultBriefAction), block)
			assert.Contains(t, block, "chunk starts at line 12 · ")
			if tt.hit.SubagentID == "" {
				assert.Contains(t, block, " · main transcript · ")
			} else {
				assert.Contains(t, block, " · sidecar agent-explorer · ")
				assert.NotContains(t, block, "main transcript")
			}
			if tt.offer {
				assert.Contains(t, block, "--- [session:"+tt.hit.SessionUUID+"] ---", "the instruction refers to the displayed full ID")
			}
		})
	}

	t.Run("split windows of one chunk start both offer it", func(t *testing.T) {
		next := main
		next.ChunkIndex, next.SnippetStart = 4, 900
		assert.Contains(t, renderVaultHit(next).block, vaultBriefAction)
	})

	t.Run("largest eligible hit fits its allowance", func(t *testing.T) {
		huge := strings.Repeat("ű", 2000)
		r := vault.SearchResult{
			SessionUUID: strings.Repeat("a", vaultSessionIDMaxBytes), Title: huge, Project: huge,
			EndTime: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), LineIndex: 1_000_000_000,
			ChunkIndex: 1_000_000, HasChunkIndex: true, Platform: vault.Platform(huge), ParentUUID: huge,
			Snippet: strings.Repeat("passage ", 400),
		}
		block := renderVaultHit(r).block
		require.Contains(t, block, vaultBriefAction)
		assert.LessOrEqual(t, len(hitSeparator)+len("[999] ")+len(block), searchHitMaxBytes)
	})

	t.Run("knowledge hit with a session label", func(t *testing.T) {
		k := renderKnowledgeHit(store.SearchResult{
			Label: "session:11111111-0000-4000-8000-000000000001", Title: "legacy", Content: "body",
		}, "body")
		assert.Contains(t, k.block, "(knowledge)")
		assert.NotContains(t, k.block, vaultBriefAction, "legacy session-labelled knowledge carries knowledge attribution only")
		assert.NotContains(t, k.block, "chunk starts at line")
	})
}

// With compact replies, a repeated passage is a reference; the instruction
// lives in the full entry it cites.
func TestVaultSearch_BriefActionReachableViaReference(t *testing.T) {
	projectDir, _, _ := setupVaultSweepProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	srv := newTestServerWithProjectDir(t, nil, projectDir)
	hit := vault.SearchResult{
		SessionUUID: "22222222-0000-4000-8000-000000000002", Title: "Release checks",
		LineIndex: 0, HasChunkIndex: true, Snippet: "release [checks]",
	}
	stubVaultChunks(srv, map[string][]vault.SearchResult{"release checks": {hit}, "checks release": {hit}})

	res, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{
		"queries": []any{"release checks", "checks release"},
	}))
	require.NoError(t, err)
	out := resultText(res)
	assert.Equal(t, []string{"[1]", "↑1"}, entryMarkers(out))
	assert.Equal(t, 1, strings.Count(out, vaultBriefAction))
	assert.Equal(t, []string{hit.SessionUUID}, briefActionSessions(out), "the cited entry [1] carries the instruction")
}
