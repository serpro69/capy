package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/vault"
	"github.com/serpro69/capy/internal/vault/tui"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDateFlag(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		endOfDay bool
		wantErr  bool
		zero     bool
	}{
		{name: "empty is zero, no error", in: "", zero: true},
		{name: "date only", in: "2026-05-01"},
		{name: "rfc3339", in: "2026-05-01T10:30:00Z"},
		{name: "garbage", in: "not-a-date", wantErr: true},
		{name: "out of range", in: "2026-13-99", wantErr: true},
		{name: "endOfDay ignored when empty", in: "", endOfDay: true, zero: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDateFlag(tt.in, tt.endOfDay)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.zero, got.IsZero())
		})
	}
}

func TestParseDateFlag_EndOfDaySemantics(t *testing.T) {
	// A date-only --before must cover the whole target day (inclusive).
	before, err := parseDateFlag("2026-05-01", true)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 23, 59, 59, 0, time.UTC), before)

	// A date-only --after stays at start-of-day (already inclusive of the day).
	after, err := parseDateFlag("2026-05-01", false)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), after)

	// An explicit RFC3339 timestamp is used verbatim regardless of endOfDay.
	exact, err := parseDateFlag("2026-05-01T08:15:00Z", true)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 8, 15, 0, 0, time.UTC), exact)
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "shorter than max", in: "abc", max: 5, want: "abc"},
		{name: "equal to max", in: "abcde", max: 5, want: "abcde"},
		{name: "longer truncates with ellipsis", in: "abcdef", max: 5, want: "abcd…"},
		{name: "multibyte counts runes", in: "héllo wörld", max: 6, want: "héllo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncate(tt.in, tt.max))
		})
	}
}

func TestSubagentDisplayID(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		want string
	}{
		{name: "agent jsonl", rel: "subagents/agent-abc123.jsonl", want: "abc123"},
		{name: "non-agent subagent jsonl", rel: "subagents/other.jsonl", want: "other"},
		{name: "subagent meta json not rendered", rel: "subagents/agent-x.meta.json", want: ""},
		{name: "tool result not a subagent", rel: "tool-results/t1.json", want: ""},
		{name: "loose file", rel: "notes.txt", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, subagentDisplayID(tt.rel))
		})
	}
}

func TestValidRole(t *testing.T) {
	for _, r := range []string{"user", "assistant", "tool", "system"} {
		assert.True(t, validRole(r), r)
	}
	assert.False(t, validRole("bogus"))
	assert.False(t, validRole(""))
}

// TestSessionsToJSON_Platform pins the `capy vault list --json` contract from
// codex-vault-sessions Slice 5: every row carries "platform" (never omitted),
// and "parent_uuid" appears only for a child session.
func TestSessionsToJSON_Platform(t *testing.T) {
	out := sessionsToJSON([]vault.Session{
		{UUID: "claude-1", Platform: vault.PlatformClaudeCode},
		{UUID: "codex-child", Platform: vault.PlatformCodex, ParentUUID: "codex-parent"},
	})
	require.Len(t, out, 2)
	assert.Equal(t, "claude-code", out[0].Platform)
	assert.Empty(t, out[0].ParentUUID)
	assert.Equal(t, "codex", out[1].Platform)
	assert.Equal(t, "codex-parent", out[1].ParentUUID)

	b, err := json.MarshalIndent(out, "", "  ") // same encoding printJSON uses
	require.NoError(t, err)
	assert.Contains(t, string(b), `"platform": "claude-code"`)
	assert.NotContains(t, strings.SplitN(string(b), "codex-child", 2)[0], `"parent_uuid"`,
		"a top-level session must omit parent_uuid entirely")
	assert.Contains(t, string(b), `"parent_uuid": "codex-parent"`)
}

// Codex ids are UUIDv7: two threads minted minutes apart share their first 8 hex
// digits, so the Codex display prefix is 12 characters (design § Identity and
// display). Claude keeps 8.
func TestShortUUID(t *testing.T) {
	assert.Equal(t, "abcd1234", shortUUID("abcd1234-5678-90ab", vault.PlatformClaudeCode))
	assert.Equal(t, "short", shortUUID("short", vault.PlatformClaudeCode))

	a := "019e22ba-4c15-7ae0-a903-255537a6a1b3"
	b := "019e22ba-9f01-7c2d-8e44-0b1c2d3e4f50"
	assert.Equal(t, shortUUID(a, vault.PlatformClaudeCode), shortUUID(b, vault.PlatformClaudeCode), "8 chars collide")
	assert.NotEqual(t, shortUUID(a, vault.PlatformCodex), shortUUID(b, vault.PlatformCodex), "12 chars distinguish them")
	assert.Equal(t, "019e22ba-4c1", shortUUID(a, vault.PlatformCodex))
	assert.Equal(t, "019e22ba", shortUUID(a, vault.PlatformClaudeCode))
	assert.Equal(t, "019e22ba", shortUUID(a, ""), "an unset platform is Claude")
	assert.Equal(t, "short", shortUUID("short", vault.PlatformCodex))
}

func TestParsePlatformFlag(t *testing.T) {
	p, err := parsePlatformFlag("")
	require.NoError(t, err)
	assert.Equal(t, vault.Platform(""), p, "empty means no restriction")

	p, err = parsePlatformFlag("codex")
	require.NoError(t, err)
	assert.Equal(t, vault.PlatformCodex, p)

	_, err = parsePlatformFlag("bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid --platform "bogus" (want claude-code|codex)`)
}

// Codex UUIDv7 ids used across the Slice 9 tests: the two share their first 8
// hex digits, so an 8-character display would render them identically.
const (
	codexParentUUID = "019e22ba-4c15-7ae0-a903-255537a6a1b3"
	codexChildUUID  = "019e22ba-9f01-7c2d-8e44-0b1c2d3e4f50"
)

func TestSessionTitleCell(t *testing.T) {
	top := vault.Session{UUID: codexParentUUID, Platform: vault.PlatformCodex, Title: "Parent work"}
	assert.Equal(t, "Parent work", sessionTitleCell(top), "a top-level row is just its title")

	child := vault.Session{UUID: codexChildUUID, Platform: vault.PlatformCodex, ParentUUID: codexParentUUID, Title: "explorer · researcher"}
	assert.Equal(t, "↳ 019e22ba-4c1  explorer · researcher", sessionTitleCell(child),
		"a child row names its parent with the platform's 12-char id")
}

func TestSearchPlatformCell(t *testing.T) {
	assert.Equal(t, "claude-code", searchPlatformCell(vault.SearchResult{}), "an unset platform is Claude")
	assert.Equal(t, "codex", searchPlatformCell(vault.SearchResult{Platform: vault.PlatformCodex}))
	assert.Equal(t, "codex (child)", searchPlatformCell(vault.SearchResult{Platform: vault.PlatformCodex, ParentUUID: codexParentUUID}))
	assert.LessOrEqual(t, len("claude-code "+childMarker), searchPlatformColumnWidth, "the column fits the widest cell")
}

// TestWriteShowHeader pins the `show` header for a Codex parent (children listed
// in the order Children returns them), a Codex child (parent named) and a Claude
// session (platform line only, no parent/children rows) in both formats.
func TestWriteShowHeader(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	parent := &vault.Session{UUID: codexParentUUID, Platform: vault.PlatformCodex, Title: "Parent work",
		ProjectPath: "/p", GitBranch: "main", StartTime: start, EndTime: start.Add(time.Hour)}
	children := []vault.Session{
		{UUID: codexChildUUID, Platform: vault.PlatformCodex, ParentUUID: codexParentUUID},
		{UUID: "019e22bb-0000-7000-8000-000000000000", Platform: vault.PlatformCodex, ParentUUID: codexParentUUID},
	}
	child := &vault.Session{UUID: codexChildUUID, Platform: vault.PlatformCodex, ParentUUID: codexParentUUID,
		Title: "explorer · researcher", ProjectPath: "/p", StartTime: start, EndTime: start}
	claude := &vault.Session{UUID: "abcd1234-aaaa-bbbb-cccc-1234567890ab", Title: "Claude work",
		ProjectPath: "/p", GitBranch: "main", StartTime: start, EndTime: start}

	render := func(sess *vault.Session, children []vault.Session, markdown bool) string {
		var sb strings.Builder
		writeShowHeader(&sb, sess, children, markdown)
		return sb.String()
	}

	t.Run("codex parent text", func(t *testing.T) {
		got := render(parent, children, false)
		assert.Equal(t, "Parent work\n"+
			"uuid: "+codexParentUUID+"  platform: codex  project: /p  branch: main\n"+
			"dates: 2026-05-01 10:00 – 2026-05-01 11:00\n"+
			"children: 019e22ba-9f0, 019e22bb-000\n\n", got)
	})
	t.Run("codex parent markdown", func(t *testing.T) {
		got := render(parent, children, true)
		assert.Contains(t, got, "- **Platform:** codex\n")
		assert.Contains(t, got, "- **Children:** 019e22ba-9f0, 019e22bb-000\n\n")
		assert.NotContains(t, got, "Parent:")
	})
	t.Run("codex child text", func(t *testing.T) {
		got := render(child, nil, false)
		assert.Contains(t, got, "platform: codex")
		assert.Contains(t, got, "\nparent: 019e22ba-4c1\n\n", "the parent id is the 12-char Codex form")
		assert.NotContains(t, got, "children:")
	})
	t.Run("codex child markdown", func(t *testing.T) {
		got := render(child, nil, true)
		assert.Contains(t, got, "- **Parent:** 019e22ba-4c1\n\n")
	})
	t.Run("claude text", func(t *testing.T) {
		got := render(claude, nil, false)
		assert.Equal(t, "Claude work\n"+
			"uuid: abcd1234-aaaa-bbbb-cccc-1234567890ab  platform: claude-code  project: /p  branch: main\n"+
			"dates: 2026-05-01 10:00 – 2026-05-01 10:00\n\n", got)
	})
	t.Run("claude markdown", func(t *testing.T) {
		got := render(claude, nil, true)
		assert.Contains(t, got, "- **Platform:** claude-code\n")
		assert.NotContains(t, got, "Parent:")
		assert.NotContains(t, got, "Children:")
		assert.True(t, strings.HasSuffix(got, "\n\n"), "the header ends with a blank line before the transcript")
	})
}

// A hit carries its platform always and parent_uuid only when from a child —
// the sessionJSON contract, mirrored for `search --json`.
func TestResultsToJSON_Platform(t *testing.T) {
	out := resultsToJSON([]vault.SearchResult{
		{SessionUUID: "claude-1", Role: "user"},
		{SessionUUID: codexChildUUID, Platform: vault.PlatformCodex, ParentUUID: codexParentUUID, Role: "assistant"},
	})
	require.Len(t, out, 2)
	assert.Equal(t, "claude-code", out[0].Platform, "an unset platform serialises as Claude")
	assert.Empty(t, out[0].ParentUUID)
	assert.Equal(t, "codex", out[1].Platform)
	assert.Equal(t, codexParentUUID, out[1].ParentUUID)

	b, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, strings.SplitN(string(b), codexChildUUID, 2)[0], `"parent_uuid"`)
	assert.Contains(t, string(b), `"parent_uuid":"`+codexParentUUID+`"`)
}

func TestStatsToJSON_PlatformsAndChildren(t *testing.T) {
	out := statsToJSON(&vault.VaultStats{
		Sessions: 3, Children: 1,
		ByPlatform: []vault.PlatformStat{
			{Platform: vault.PlatformClaudeCode, Sessions: 1, Bytes: 10},
			{Platform: vault.PlatformCodex, Sessions: 2, Bytes: 20},
		},
		ByProject: []vault.ProjectStat{{ProjectPath: "/p", Count: 3}},
	}, 0)
	assert.Equal(t, 1, out.Children)
	assert.Equal(t, []projectJSON{{ProjectPath: "/p", Count: 3}}, out.Projects, "the project breakdown is unchanged")
	require.Len(t, out.Platforms, 2)
	assert.Equal(t, platformJSON{Platform: "claude-code", Sessions: 1, Bytes: 10}, out.Platforms[0])
	assert.Equal(t, platformJSON{Platform: "codex", Sessions: 2, Bytes: 20}, out.Platforms[1])

	empty := statsToJSON(&vault.VaultStats{}, 0)
	b, err := json.Marshal(empty)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"platforms":[]`, "an empty vault serialises an empty array, not null")
	assert.Contains(t, string(b), `"children":0`)
}

// Two Codex UUIDv7 candidates that collide at 8 characters must be told apart in
// the ambiguous-prefix listing — otherwise the listing repeats the very prefix
// the user was told is ambiguous.
func TestWriteLookupCandidates_CodexCollidingPrefixes(t *testing.T) {
	amb := &vault.AmbiguousUUIDError{
		Prefix: "019e22ba",
		Candidates: []vault.Session{
			{UUID: codexParentUUID, Platform: vault.PlatformCodex, Title: "one", ProjectPath: "/p"},
			{UUID: codexChildUUID, Platform: vault.PlatformCodex, Title: "two", ProjectPath: "/p"},
		},
	}
	var sb strings.Builder
	writeLookupCandidates(&sb, amb)
	got := sb.String()
	assert.Contains(t, got, `ambiguous session id "019e22ba" matches 2 sessions:`)
	assert.Contains(t, got, "  019e22ba-4c1  ")
	assert.Contains(t, got, "  019e22ba-9f0  ")

	// A Claude candidate keeps the 8-char id, padded to the shared column.
	sb.Reset()
	writeLookupCandidates(&sb, &vault.AmbiguousUUIDError{Prefix: "abcd1234", Candidates: []vault.Session{
		{UUID: "abcd1234-1111", Title: "one"}, {UUID: "abcd1234-2222", Title: "two"},
	}})
	assert.Contains(t, sb.String(), "  abcd1234      ")
}

func TestPrintDeletePreview_WarnsAboutChildren(t *testing.T) {
	parent := &vault.Session{UUID: codexParentUUID, Platform: vault.PlatformCodex, Title: "Parent work", MessageCount: 4}
	children := []vault.Session{
		{UUID: codexChildUUID, Platform: vault.PlatformCodex, ParentUUID: codexParentUUID},
		{UUID: "019e22bb-0000-7000-8000-000000000000", Platform: vault.PlatformCodex, ParentUUID: codexParentUUID},
	}

	var sb strings.Builder
	printDeletePreview(&sb, parent, children)
	got := sb.String()
	assert.Contains(t, got, "Platform: codex\n")
	assert.Contains(t, got, "warning: 2 child session(s) name this session as parent and will NOT be deleted (no cascade): 019e22ba-9f0, 019e22bb-000\n")
	assert.Contains(t, got, "capy vault list --include-children")

	sb.Reset()
	printDeletePreview(&sb, parent, nil)
	assert.NotContains(t, sb.String(), "warning:", "no children, no warning")
	assert.Contains(t, sb.String(), "Messages: 4\n")
}

func TestCheckResumable(t *testing.T) {
	assert.NoError(t, checkResumable(&vault.Session{UUID: "abcd1234-aaaa", Platform: vault.PlatformClaudeCode}))
	assert.NoError(t, checkResumable(&vault.Session{UUID: "abcd1234-aaaa"}), "an unset platform is Claude")

	err := checkResumable(&vault.Session{UUID: codexParentUUID, Platform: vault.PlatformCodex})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session 019e22ba-4c1 is a Codex session")
	assert.Contains(t, err.Error(), "'capy vault restore 019e22ba-4c1'")
	assert.Contains(t, err.Error(), "'codex resume "+codexParentUUID+"'", "the codex command gets the full uuid")
}

// codexRolloutLines is a minimal legacy-mode Codex rollout whose session_meta is
// meta (a JSON object literal): one human turn, one assistant reply.
func codexRolloutLines(meta string) []byte {
	return []byte(strings.Join([]string{
		`{"timestamp":"2026-05-01T10:00:00Z","type":"session_meta","payload":` + meta + `}`,
		`{"timestamp":"2026-05-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Please fix the brontosaurus timeout"}}`,
		`{"timestamp":"2026-05-01T10:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looking into the timeout now."}]}}`,
	}, "\n") + "\n")
}

// buildCodexVault points CODEX_HOME at a temp home holding one Codex rollout and
// imports it into a fresh vault at vaultPath (CAPY_VAULT_KEY set). The rollout's
// uuid is codexParentUUID.
func buildCodexVault(t *testing.T, vaultPath string) {
	t.Helper()
	t.Setenv("CAPY_VAULT_KEY", "test-vault-key-at-least-32-characters-long!!")
	t.Setenv("CAPY_MACHINE_ID", "test-machine")
	home := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", home)
	rel := filepath.Join("sessions", "2026", "05", "01", "rollout-2026-05-01T10-00-00-"+codexParentUUID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(home, rel)), 0o755))
	meta := `{"id":"` + codexParentUUID + `","cwd":"/home/user/proj","cli_version":"0.130.0","source":"cli"}`
	require.NoError(t, os.WriteFile(filepath.Join(home, rel), codexRolloutLines(meta), 0o644))

	ctx := context.Background()
	sessions, _, err := vault.DiscoverCodexSessions(context.Background(), home, vault.CodexDiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	st := vault.NewVaultStore(vaultPath)
	require.NoError(t, st.Open(ctx))
	res := vault.Import(ctx, st, sessions, vault.ImportOptions{})
	require.NoError(t, st.Close())
	require.Equal(t, 1, res.Imported, "codex rollout should import")
}

// resumeVaultSession must refuse a Codex session before it looks for `claude`
// and before it restores anything: with an empty PATH the error is the Codex
// guidance, not "claude not found", and CODEX_HOME is untouched. The TUI's `R`
// action reaches the same function through performTUIAction, so it is covered by
// the same assertion.
func TestResumeVaultSession_CodexFailsLoudWithoutLaunch(t *testing.T) {
	vaultPath := filepath.Join(t.TempDir(), "vault.db")
	buildCodexVault(t, vaultPath)
	t.Setenv("PATH", t.TempDir()) // no `claude` anywhere

	// A fresh home: any restore would recreate sessions/2026/05/01/ here.
	freshHome := t.TempDir()
	t.Setenv("CODEX_HOME", freshHome)

	ctx := context.Background()
	st := vault.NewVaultStore(vaultPath)
	defer st.Close()
	require.NoError(t, st.Open(ctx))
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)

	for name, run := range map[string]func() error{
		"cli": func() error { return resumeVaultSession(cmd, st, codexParentUUID[:12], "") },
		"tui R": func() error {
			return performTUIAction(cmd, st, tui.Action{Kind: tui.ActionResume, SessionUUID: codexParentUUID})
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is a Codex session")
			assert.Contains(t, err.Error(), "capy vault restore 019e22ba-4c1")
			assert.Contains(t, err.Error(), "codex resume "+codexParentUUID)
			assert.NotContains(t, err.Error(), "not found on PATH", "the Codex guard runs before the claude lookup")
			entries, readErr := os.ReadDir(freshHome)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "nothing may be restored for a refused resume")
		})
	}

	// The store is still usable: the Codex guard must not have closed it.
	_, err := st.GetSession(ctx, codexParentUUID)
	require.NoError(t, err)
}

func TestDisplayPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	assert.Equal(t, "-", displayPath(""))
	assert.Equal(t, "~/proj/capy", displayPath("/home/tester/proj/capy"))
	assert.Equal(t, "/var/data/x", displayPath("/var/data/x"))
}

func TestOneLine(t *testing.T) {
	assert.Equal(t, "a b c", oneLine("a\nb  c"))
	assert.Equal(t, "x y", oneLine("  x\t\ny  "))
}

func TestHandleLookupError(t *testing.T) {
	t.Run("ambiguous lists candidates", func(t *testing.T) {
		amb := &vault.AmbiguousUUIDError{
			Prefix: "abcd1234",
			Candidates: []vault.Session{
				{UUID: "abcd1234-1111", Title: "one", ProjectPath: "/p", EndTime: time.Now()},
				{UUID: "abcd1234-2222", Title: "two", ProjectPath: "/p", EndTime: time.Now()},
			},
		}
		err := handleLookupError("abcd1234", amb)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous")
	})

	t.Run("not found", func(t *testing.T) {
		err := handleLookupError("zzzzzzzz", vault.ErrSessionNotFound)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no session matches")
	})

	t.Run("other error passes through", func(t *testing.T) {
		orig := assert.AnError
		err := handleLookupError("x", orig)
		assert.Equal(t, orig, err)
	})
}

func TestRenameOptions(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		clear   bool
		want    vault.RenameOptions
		wantErr string
	}{
		{name: "name argument", args: []string{"abcd1234", "New name"}, want: vault.RenameOptions{Name: "New name"}},
		{name: "clear", args: []string{"abcd1234"}, clear: true, want: vault.RenameOptions{Clear: true}},
		{name: "name and clear are exclusive", args: []string{"abcd1234", "New name"}, clear: true, wantErr: "mutually exclusive"},
		{name: "missing name", args: []string{"abcd1234"}, wantErr: "provide a name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renameOptions(tt.args, tt.clear)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// buildVaultWithSession creates an encrypted vault at vaultPath under key and
// imports one fixture session, returning its UUID. It sets CAPY_VAULT_KEY (which
// VaultStore.Open consumes) to key, so callers flip the env var afterward to
// assert which key opens the rotated DB.
func buildVaultWithSession(t *testing.T, vaultPath, key string) string {
	t.Helper()
	t.Setenv("CAPY_VAULT_KEY", key)
	t.Setenv("CAPY_MACHINE_ID", "test-machine") // avoid touching ~/.config/capy

	root := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(root, 0o755))
	uuid := "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","cwd":"/home/user/proj","gitBranch":"main","message":{"role":"user","content":"Fix the brontosaurus timeout"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-01T10:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"On it."}]}}`,
		`{"type":"ai-title","aiTitle":"Fix the brontosaurus timeout","sessionId":"s1"}`,
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, uuid+".jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644))

	ctx := context.Background()
	sessions, err := vault.DiscoverSessions(context.Background(), root)
	require.NoError(t, err)
	st := vault.NewVaultStore(vaultPath)
	require.NoError(t, st.Open(ctx))
	res := vault.Import(ctx, st, sessions, vault.ImportOptions{})
	require.NoError(t, st.Close())
	require.Equal(t, 1, res.Imported, "fixture session should import")
	return uuid
}

// listVaultWith opens the vault under key and returns its sessions. The returned
// error is the open error, so callers can assert that a given key does/does not
// open the rotated DB.
func listVaultWith(t *testing.T, vaultPath, key string) ([]vault.Session, error) {
	t.Helper()
	t.Setenv("CAPY_VAULT_KEY", key)
	ctx := context.Background()
	st := vault.NewVaultStore(vaultPath)
	defer st.Close()
	if err := st.Open(ctx); err != nil {
		return nil, err
	}
	return st.ListSessions(ctx, vault.ListOptions{})
}

func TestRunVaultRekey_RoundTrip(t *testing.T) {
	const oldKey = "old-vault-key-at-least-32-characters-long!!"
	const newKey = "new-vault-key-at-least-32-characters-long!!"

	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.db")
	uuid := buildVaultWithSession(t, vaultPath, oldKey)

	require.NoError(t, runVaultRekey(vaultPath, oldKey, newKey, false))
	assert.FileExists(t, vaultPath+".bak", ".bak must be preserved by default")

	// The new key opens the rotated vault and the session is intact.
	list, err := listVaultWith(t, vaultPath, newKey)
	require.NoError(t, err, "new key must open the rotated vault")
	require.Len(t, list, 1)
	assert.Equal(t, uuid, list[0].UUID)

	// The old key no longer opens the rotated vault.
	_, err = listVaultWith(t, vaultPath, oldKey)
	require.Error(t, err, "old key must not open the rotated vault")
}

func TestRunVaultRekey_RemoveBackup(t *testing.T) {
	const oldKey = "old-vault-key-at-least-32-characters-long!!"
	const newKey = "new-vault-key-at-least-32-characters-long!!"

	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.db")
	buildVaultWithSession(t, vaultPath, oldKey)

	require.NoError(t, runVaultRekey(vaultPath, oldKey, newKey, true))
	assert.NoFileExists(t, vaultPath+".bak", "--remove-backup must unlink the old-key .bak")

	// The rotation still succeeded: the new key opens the vault.
	list, err := listVaultWith(t, vaultPath, newKey)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestRunVaultRekey_RejectsNewEqualsOld(t *testing.T) {
	const key = "same-vault-key-at-least-32-characters-long!!"

	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.db")
	buildVaultWithSession(t, vaultPath, key)

	before, err := os.ReadFile(vaultPath)
	require.NoError(t, err)

	err = runVaultRekey(vaultPath, key, key, false)
	require.Error(t, err, "rekey must reject a new key identical to the old")
	assert.Contains(t, err.Error(), "identical")
	assert.NoFileExists(t, vaultPath+".bak", "a rejected rekey must not touch the vault")

	after, err := os.ReadFile(vaultPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a rejected rekey must leave the vault byte-identical")

	// The vault still opens with its unchanged key.
	list, err := listVaultWith(t, vaultPath, key)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestResolveMergeKey(t *testing.T) {
	// --key flag wins over both env vars.
	t.Setenv("CAPY_VAULT_MERGE_KEY", "mergeenv")
	t.Setenv("CAPY_VAULT_KEY", "vaultenv")
	key, src := resolveMergeKey("flagkey")
	assert.Equal(t, "flagkey", key)
	assert.Equal(t, "--key", src)

	// No flag → CAPY_VAULT_MERGE_KEY.
	key, src = resolveMergeKey("")
	assert.Equal(t, "mergeenv", key)
	assert.Equal(t, "CAPY_VAULT_MERGE_KEY", src)

	// No flag, no merge env → fall back to CAPY_VAULT_KEY (shared-passphrase case).
	t.Setenv("CAPY_VAULT_MERGE_KEY", "")
	key, src = resolveMergeKey("")
	assert.Equal(t, "vaultenv", key)
	assert.Equal(t, "CAPY_VAULT_KEY", src)
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "vault.db")
	b := filepath.Join(dir, "other.db")

	same, err := samePath(a, a)
	require.NoError(t, err)
	assert.True(t, same, "identical paths are the same vault")

	same, err = samePath(a, b)
	require.NoError(t, err)
	assert.False(t, same, "distinct paths are different vaults")

	// A relative spelling of the same file resolves to the same absolute path.
	rel, err := filepath.Rel(mustGetwd(t), a)
	require.NoError(t, err)
	same, err = samePath(rel, a)
	require.NoError(t, err)
	assert.True(t, same, "a relative path to the same file is the same vault")
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return wd
}
