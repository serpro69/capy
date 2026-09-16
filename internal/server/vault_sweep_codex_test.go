package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vault_sweep_codex_test.go covers the Codex half of vaultSweep (Slice 8.1):
// independent per-platform discovery (Codex is swept with no Claude root at
// all), the canonical-cwd project filter (design Assumption 10), and the
// accepted per-startup bound (Assumption 12) — a rollout archived at its
// current (path, size) is never opened, an appended rollout is re-read, and a
// rollout for another project is re-read on every sweep. The last point is
// pinned deliberately so a future change to the bound is a conscious one.
//
// Opens are observed through sweepSummary.codexReport.FirstLineReads: the
// vault's open hook is unexported, and the walker increments that counter
// immediately before its only open (see codexDiscoverer.walkRoot).

// setupCodexSweepEnv isolates HOME (no Claude root), the vault DB, the machine
// id and CODEX_HOME (a fresh home with an empty sessions/ root), enables the
// vault, and returns the Codex home.
func setupCodexSweepEnv(t *testing.T) (codexHome string) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CAPY_VAULT_PATH", filepath.Join(t.TempDir(), "vault.db"))
	t.Setenv("CAPY_MACHINE_ID", "test-machine")
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)

	codexHome = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(codexHome, "sessions"), 0o755))
	t.Setenv("CODEX_HOME", codexHome)
	return codexHome
}

// codexRolloutRel is the active rollout path for uuid (a UUIDv7-shaped id).
func codexRolloutRel(uuid string) string {
	return "sessions/2026/05/01/rollout-2026-05-01T10-00-00-" + uuid + ".jsonl"
}

// codexRolloutLines renders a minimal archivable rollout: session_meta with
// cwd, one legacy user_message event carrying prompt, one assistant
// response_item. The shapes mirror internal/vault's Codex fixtures
// (research.md Appendix A); they are re-declared here because that package's
// builders are test-private.
func codexRolloutLines(t *testing.T, uuid, cwd, prompt string) []byte {
	t.Helper()
	return codexRolloutLinesWithMeta(t, codexSessionMetaPayload(uuid, cwd), prompt)
}

// codexChildRolloutLines renders an archivable sub-agent rollout (CLI ≤ 0.137
// shape: the spawn prompt is recorded as a user_message event) whose
// session_meta names parent as its parent thread, both lifted and under
// source.subagent.thread_spawn, so the decoder sets Meta.ParentUUID.
func codexChildRolloutLines(t *testing.T, uuid, parent, cwd, prompt string) []byte {
	t.Helper()
	meta := codexSessionMetaPayload(uuid, cwd)
	meta["parent_thread_id"] = parent
	meta["agent_nickname"] = "Rook"
	meta["agent_role"] = "explorer"
	meta["source"] = map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{
		"parent_thread_id": parent, "depth": 1,
	}}}
	return codexRolloutLinesWithMeta(t, meta, prompt)
}

// codexSessionMetaPayload is the session_meta payload of a plain CLI rollout.
func codexSessionMetaPayload(uuid, cwd string) map[string]any {
	return map[string]any{
		"id": uuid, "timestamp": "2026-05-01T09:58:30Z", "cwd": cwd, "originator": "codex-tui",
		"cli_version": "0.130.0", "source": "cli",
		"git": map[string]any{"branch": "main"},
	}
}

func codexRolloutLinesWithMeta(t *testing.T, meta map[string]any, prompt string) []byte {
	t.Helper()
	lines := []map[string]any{
		{"timestamp": "2026-05-01T10:00:00Z", "type": "session_meta", "payload": meta},
		{"timestamp": "2026-05-01T10:00:01Z", "type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": prompt,
		}},
		{"timestamp": "2026-05-01T10:00:02Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "assistant", "phase": "commentary",
			"content": []map[string]any{{"type": "output_text", "text": "Sure — " + prompt}},
		}},
	}
	var b strings.Builder
	for _, l := range lines {
		raw, err := json.Marshal(l)
		require.NoError(t, err)
		b.Write(raw)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// writeCodexRollout writes lines at <home>/<rel>, zstd-compressed when rel ends
// in .zst, and returns the absolute path.
func writeCodexRollout(t *testing.T, home, rel string, lines []byte) string {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data := lines
	if strings.HasSuffix(rel, ".zst") {
		enc, err := zstd.NewWriter(nil)
		require.NoError(t, err)
		data = enc.EncodeAll(lines, nil)
		require.NoError(t, enc.Close())
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

// appendCodexTurn appends one more human turn to a plain rollout — the
// append-only growth the skip predicate must notice through the size change.
func appendCodexTurn(t *testing.T, path, prompt string) {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"timestamp": "2026-05-01T10:05:00Z", "type": "event_msg",
		"payload": map[string]any{"type": "user_message", "message": prompt},
	})
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.Write(append(line, '\n'))
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// archivedUUIDs lists the uuids in the sweep's vault, children included.
func archivedUUIDs(t *testing.T) map[string]vault.Session {
	t.Helper()
	st := vault.NewVaultStore(vault.VaultDBPath())
	t.Cleanup(func() { _ = st.Close() })
	sessions, err := st.ListSessions(context.Background(), vault.ListOptions{IncludeChildren: true})
	require.NoError(t, err)
	got := map[string]vault.Session{}
	for _, s := range sessions {
		got[s.UUID] = s
	}
	return got
}

const (
	codexSweepMatch = "019e2200-0000-7000-8000-000000000001"
	codexSweepOther = "019e2200-0000-7000-8000-000000000002"
	codexSweepZst   = "019e2200-0000-7000-8000-000000000003"
)

// The default sweep imports the rollouts whose recorded cwd IS the server's
// project — compared canonically, so a project dir reached through a symlink
// still matches the real path Codex recorded — and nothing from other projects.
// HOME holds no Claude root at all, so this also proves Codex is discovered
// independently of Claude.
func TestVaultSweep_Codex_ScopedByCanonicalCwd(t *testing.T) {
	home := setupCodexSweepEnv(t)
	realProject := t.TempDir()
	link := filepath.Join(t.TempDir(), "project-link")
	require.NoError(t, os.Symlink(realProject, link))
	otherProject := t.TempDir()

	writeCodexRollout(t, home, codexRolloutRel(codexSweepMatch),
		codexRolloutLines(t, codexSweepMatch, realProject, "configure the codex database"))
	writeCodexRollout(t, home, codexRolloutRel(codexSweepOther),
		codexRolloutLines(t, codexSweepOther, otherProject, "explain goroutine scheduling"))

	// The server is handed the SYMLINK; the rollout recorded the real path.
	srv := newTestServerWithProjectDir(t, nil, link)
	sum := srv.vaultSweep(context.Background())

	_, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude"))
	require.True(t, os.IsNotExist(err), "fixture: no Claude root exists")
	assert.Equal(t, 0, sum.claudeDiscovered)
	assert.Equal(t, 2, sum.codexDiscovered, "both rollouts survive the (empty) predicate and are first-line read")
	assert.Equal(t, 2, sum.codexReport.FirstLineReads)
	assert.Equal(t, 1, sum.codexMatched, "only the rollout whose cwd is this project")
	assert.Equal(t, 1, sum.codex.Imported)
	assert.Equal(t, 0, sum.codex.Errors)

	got := archivedUUIDs(t)
	require.Len(t, got, 1)
	sess, ok := got[codexSweepMatch]
	require.True(t, ok, "the matching rollout is archived")
	assert.Equal(t, vault.PlatformCodex, sess.Platform)
	assert.Equal(t, codexRolloutRel(codexSweepMatch), sess.ClaudeProjectDir, "location hint is the relative rollout path")
	assert.Equal(t, realProject, sess.ProjectPath)
	_, other := got[codexSweepOther]
	assert.False(t, other, "another project's rollout must not be archived by the default sweep")
}

// With CAPY_VAULT_SWEEP_ALL the cwd filter is off: every rollout is imported,
// whatever project it belongs to.
func TestVaultSweep_Codex_AllProjectsImportsEveryRollout(t *testing.T) {
	home := setupCodexSweepEnv(t)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")
	writeCodexRollout(t, home, codexRolloutRel(codexSweepMatch),
		codexRolloutLines(t, codexSweepMatch, t.TempDir(), "first project"))
	writeCodexRollout(t, home, codexRolloutRel(codexSweepOther),
		codexRolloutLines(t, codexSweepOther, t.TempDir(), "second project"))

	srv := newTestServerWithProjectDir(t, nil, t.TempDir())
	sum := srv.vaultSweep(context.Background())

	assert.Equal(t, 2, sum.codexDiscovered)
	assert.Equal(t, 2, sum.codexMatched, "all-projects keeps every rollout")
	assert.Equal(t, 2, sum.codex.Imported)
	assert.Len(t, archivedUUIDs(t), 2)
}

// The accepted per-startup bound, pinned: a second sweep over an unchanged
// corpus opens NO rollout this vault archived at its current (path, size) —
// plain or .zst — but DOES re-read a rollout it never archived (another
// project's), every time; appending to an archived rollout makes exactly that
// one re-read and re-imported as an update.
func TestVaultSweep_Codex_SkipPredicateBound(t *testing.T) {
	home := setupCodexSweepEnv(t)
	project := t.TempDir()
	otherProject := t.TempDir()

	matchPath := writeCodexRollout(t, home, codexRolloutRel(codexSweepMatch),
		codexRolloutLines(t, codexSweepMatch, project, "configure the codex database"))
	writeCodexRollout(t, home, codexRolloutRel(codexSweepOther),
		codexRolloutLines(t, codexSweepOther, otherProject, "explain goroutine scheduling"))
	writeCodexRollout(t, home, codexRolloutRel(codexSweepZst)+".zst",
		codexRolloutLines(t, codexSweepZst, project, "compressed rollout about compaction"))

	srv := newTestServerWithProjectDir(t, nil, project)
	ctx := context.Background()

	first := srv.vaultSweep(ctx)
	assert.Equal(t, 3, first.codexReport.FirstLineReads, "cold sweep: every rollout is first-line read")
	assert.Equal(t, 0, first.codexReport.SkippedByPredicate, "nothing archived yet")
	assert.Equal(t, 2, first.codexMatched)
	assert.Equal(t, 2, first.codex.Imported)
	assert.Equal(t, 0, first.codex.Errors)
	require.Len(t, archivedUUIDs(t), 2)

	second := srv.vaultSweep(ctx)
	assert.Equal(t, 2, second.codexReport.SkippedByPredicate,
		"the plain rollout at its archived size and the .zst rollout at its path are dropped unopened")
	assert.Equal(t, 1, second.codexReport.FirstLineReads,
		"the other project's rollout is not in the archived map and is re-read: the accepted bound")
	assert.Equal(t, 1, second.codexDiscovered)
	assert.Equal(t, 0, second.codexMatched)
	assert.Equal(t, vault.ImportResult{}, second.codex, "nothing to import, so Import is not called")

	appendCodexTurn(t, matchPath, "a second question about velociraptors")
	third := srv.vaultSweep(ctx)
	assert.Equal(t, 1, third.codexReport.SkippedByPredicate, "only the untouched .zst rollout is skipped")
	assert.Equal(t, 2, third.codexReport.FirstLineReads, "the grown rollout and the other project's are read")
	assert.Equal(t, 1, third.codexMatched)
	assert.Equal(t, 1, third.codex.Updated, "the appended rollout is re-archived as an update")
	assert.Equal(t, 0, third.codex.Imported)
	assert.Equal(t, 0, third.codex.Errors)

	fourth := srv.vaultSweep(ctx)
	assert.Equal(t, 2, fourth.codexReport.SkippedByPredicate, "the grown rollout is archived at its new size")
	assert.Equal(t, 1, fourth.codexReport.FirstLineReads, "the other project's rollout is read again — every sweep")
	assert.Equal(t, vault.ImportResult{}, fourth.codex)
}

// A Codex home whose rollout root exists but is empty is swept without error
// and without importing anything; the vault is still opened (the predicate
// needs it), which is the accepted cost of having Codex installed.
func TestVaultSweep_Codex_EmptyRootIsQuiet(t *testing.T) {
	setupCodexSweepEnv(t)
	srv := newTestServerWithProjectDir(t, nil, t.TempDir())
	var sum sweepSummary
	require.NotPanics(t, func() { sum = srv.vaultSweep(context.Background()) })
	assert.Equal(t, 0, sum.codexDiscovered)
	assert.Equal(t, 0, sum.codexReport.FirstLineReads)
	assert.Equal(t, vault.ImportResult{}, sum.codex)
}
