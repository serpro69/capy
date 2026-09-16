package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codex_discovery_test.go covers the Codex half of discovery.go (Slice 7.2):
// the rollout filename parser, the two-root walker and its fixed order, the
// skip predicate (a skipped file is never opened), the bounded streaming .zst
// first-line read, revert-variant skipping, layout autodetection and
// DiscoverAll. Fixture lines come from codex_fixtures_test.go.

const (
	codexDiscUUIDA = "019fcc39-ffbb-7212-ba7b-4291df4b05f6"
	codexDiscUUIDB = "01a04876-0d7d-7fa1-a645-e63187ab1fea"
	codexDiscUUIDC = "019f144d-199d-7bb3-a692-b3791b0b0e45"
	codexDiscUUIDD = "019e0000-0000-7000-8000-000000000000"
	codexDiscUUIDE = "019e0000-0000-7000-8000-000000000001"

	codexDiscRelA = "sessions/2026/05/01/rollout-2026-05-01T10-00-00-" + codexDiscUUIDA + ".jsonl"
	codexDiscRelB = "sessions/2026/05/02/rollout-2026-05-02T11-00-00-" + codexDiscUUIDB + ".jsonl"
	codexDiscRelC = "sessions/2026/05/03/rollout-2026-05-03T12-00-00-" + codexDiscUUIDC + ".jsonl" // written as .zst
	codexDiscRelD = "archived_sessions/rollout-2026-04-01T09-00-00-" + codexDiscUUIDD + ".jsonl"
	codexDiscRelE = "sessions/2026/05/01/rollout-2026-05-01T10-30-00-" + codexDiscUUIDE + "_a1b2c3.jsonl" // revert variant
)

// codexMinimalRollout is a two-line rollout (session_meta + task_started) with
// the given id and cwd — enough for discovery, which reads only line 0.
func codexMinimalRollout(t testing.TB, mode codexMode, id, cwd string) []byte {
	t.Helper()
	return codexRollout(t, mode,
		codexSessionMetaLine(at(0), mode, codexMetaOpts{id: id, cwd: cwd, payloadTS: codexPayloadTS}),
		codexTaskStartedLine(at(0)),
	)
}

// writeCodexDiscoveryHome lays out a Codex home exercising every discovery
// branch: legacy, paginated, compressed, archived, a revert variant, a misnamed
// file and (where supported) a symlink masquerading as a rollout.
func writeCodexDiscoveryHome(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	writeCodexRollout(t, home, codexDiscRelA, codexMinimalRollout(t, codexLegacy, codexDiscUUIDA, "/p/a"), false)
	writeCodexRollout(t, home, codexDiscRelB, codexMinimalRollout(t, codexPaginated, codexDiscUUIDB, "/p/b"), false)
	writeCodexRollout(t, home, codexDiscRelC+".zst", codexMinimalRollout(t, codexPaginated, codexDiscUUIDC, "/p/c"), true)
	writeCodexRollout(t, home, codexDiscRelD, codexMinimalRollout(t, codexLegacy, codexDiscUUIDD, "/p/d"), false)
	writeCodexRollout(t, home, codexDiscRelE, codexMinimalRollout(t, codexLegacy, codexDiscUUIDE, "/p/e"), false)
	require.NoError(t, os.WriteFile(filepath.Join(home, "sessions", "2026", "05", "01", "notes.txt"), []byte("not a rollout"), 0o644))

	outside := filepath.Join(home, "outside.jsonl")
	require.NoError(t, os.WriteFile(outside, codexMinimalRollout(t, codexLegacy, "019e0000-0000-7000-8000-00000000000f", "/p/f"), 0o644))
	link := filepath.Join(home, "sessions", "2026", "05", "01", "rollout-2026-05-01T10-45-00-019e0000-0000-7000-8000-00000000000f.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Logf("symlinks unsupported on this platform, symlink branch not exercised: %v", err)
	}
	return home
}

func TestParseRolloutFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want rolloutName
		ok   bool
	}{
		{"plain", "rollout-2026-08-04T12-03-00-" + codexDiscUUIDA + ".jsonl",
			rolloutName{LocalTS: "2026-08-04T12-03-00", UUID: codexDiscUUIDA}, true},
		{"compressed", "rollout-2026-08-04T12-03-00-" + codexDiscUUIDA + ".jsonl.zst",
			rolloutName{LocalTS: "2026-08-04T12-03-00", UUID: codexDiscUUIDA, Compressed: true}, true},
		{"revert variant", "rollout-2026-08-04T12-03-00-" + codexDiscUUIDA + "_a1b2c3.jsonl",
			rolloutName{LocalTS: "2026-08-04T12-03-00", UUID: codexDiscUUIDA, RolloutID: "a1b2c3"}, true},
		{"revert variant compressed", "rollout-2026-08-04T12-03-00-" + codexDiscUUIDA + "_a1b2c3.jsonl.zst",
			rolloutName{LocalTS: "2026-08-04T12-03-00", UUID: codexDiscUUIDA, RolloutID: "a1b2c3", Compressed: true}, true},
		{"uppercase uuid rejected", "rollout-2026-08-04T12-03-00-" + strings.ToUpper(codexDiscUUIDA) + ".jsonl", rolloutName{}, false},
		{"missing prefix", "2026-08-04T12-03-00-" + codexDiscUUIDA + ".jsonl", rolloutName{}, false},
		{"claude session name", codexDiscUUIDA + ".jsonl", rolloutName{}, false},
		{"other suffix", "rollout-2026-08-04T12-03-00-" + codexDiscUUIDA + ".json", rolloutName{}, false},
		{"trailing garbage", "rollout-2026-08-04T12-03-00-" + codexDiscUUIDA + ".jsonl.bak", rolloutName{}, false},
		{"empty", "", rolloutName{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRolloutFilename(tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDiscoverCodex_Layouts(t *testing.T) {
	home := writeCodexDiscoveryHome(t)
	h := captureSlog(t)

	sessions, report, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
	require.NoError(t, err)

	// sessions/ (sorted by path) strictly before archived_sessions/.
	require.Len(t, sessions, 4)
	assert.Equal(t, []string{codexDiscRelA, codexDiscRelB, codexDiscRelC, codexDiscRelD}, relPaths(sessions))

	a := sessions[0]
	assert.Equal(t, PlatformCodex, a.Platform)
	assert.Equal(t, codexDiscUUIDA, a.UUID)
	assert.Equal(t, filepath.Join(home, filepath.FromSlash(codexDiscRelA)), a.Path)
	assert.Equal(t, "/p/a", a.ProjectPath)
	assert.False(t, a.Compressed)
	assert.Empty(t, a.ProjectDir, "ProjectDir is a Claude field")
	assert.Empty(t, a.AssociatedFiles, "Codex rollouts have no sidecars")
	info, err := os.Stat(a.Path)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), a.OnDiskSize)

	assert.Equal(t, "/p/b", sessions[1].ProjectPath, "paginated first line read")

	c := sessions[2]
	assert.True(t, c.Compressed)
	assert.Equal(t, codexDiscRelC, c.RelativePath, ".zst stripped from the location hint")
	assert.True(t, strings.HasSuffix(c.Path, ".zst"), "Path keeps the real on-disk name")
	assert.Equal(t, "/p/c", c.ProjectPath, "first line read through the streaming decoder")
	info, err = os.Stat(c.Path)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), c.OnDiskSize, "OnDiskSize is the compressed size")

	assert.Equal(t, "/p/d", sessions[3].ProjectPath)

	assert.Equal(t, []string{codexDiscRelE}, report.SkippedRevertVariants)
	assert.Equal(t, 4, report.FirstLineReads)
	assert.Equal(t, 0, report.SkippedByPredicate)

	revert := h.recordsWithMessage("vault discovery: skipping codex revert rollout variant (not archived in v1)")
	require.Len(t, revert, 1)
	assert.Equal(t, slog.LevelWarn, revert[0].Level)
	attrs := recordAttrs(revert[0])
	assert.Equal(t, codexDiscUUIDE, attrs["thread"])
	assert.Equal(t, "a1b2c3", attrs["rollout_id"])

	misnamed := h.recordsWithMessage("vault discovery: skipping file with an unrecognised codex rollout name")
	require.Len(t, misnamed, 1)
	assert.Equal(t, slog.LevelWarn, misnamed[0].Level)
	assert.True(t, strings.HasSuffix(recordAttrs(misnamed[0])["path"].(string), "notes.txt"))
}

// The same thread under both roots (Codex archive is a move; a stale copy can
// linger) — the sessions/ copy is always first so import keeps its path.
func TestDiscoverCodex_ActiveBeforeArchived(t *testing.T) {
	home := t.TempDir()
	raw := codexMinimalRollout(t, codexLegacy, codexDiscUUIDA, "/p/a")
	// Archived path sorts BEFORE the active one lexically ("archived_sessions" <
	// "sessions") — the fixed root order must still win.
	archived := "archived_sessions/rollout-2026-05-01T10-00-00-" + codexDiscUUIDA + ".jsonl"
	writeCodexRollout(t, home, archived, raw, false)
	writeCodexRollout(t, home, codexDiscRelA, raw, false)

	sessions, _, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	assert.Equal(t, []string{codexDiscRelA, archived}, relPaths(sessions))
	assert.Equal(t, codexDiscUUIDA, sessions[0].UUID)
	assert.Equal(t, codexDiscUUIDA, sessions[1].UUID)
}

// A file the Skip predicate accepts is dropped from the result and NEVER opened.
func TestDiscoverCodex_SkipPredicateLeavesFileUnopened(t *testing.T) {
	home := writeCodexDiscoveryHome(t)
	opened := hookRolloutOpens(t)

	type call struct {
		rel        string
		size       int64
		compressed bool
	}
	var calls []call
	skip := func(rel string, size int64, compressed bool) bool {
		calls = append(calls, call{rel, size, compressed})
		return rel == codexDiscRelC // the .zst one — its rel must already be .zst-stripped
	}

	sessions, report, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{Skip: skip})
	require.NoError(t, err)
	assert.Equal(t, []string{codexDiscRelA, codexDiscRelB, codexDiscRelD}, relPaths(sessions))
	assert.Equal(t, 1, report.SkippedByPredicate)
	assert.Equal(t, 3, report.FirstLineReads)

	// The predicate saw every non-revert rollout with stripped rel, on-disk
	// size and the compressed flag — from metadata, before any open.
	require.Len(t, calls, 4)
	byRel := map[string]call{}
	for _, c := range calls {
		byRel[c.rel] = c
	}
	zst := byRel[codexDiscRelC]
	assert.True(t, zst.compressed)
	info, err := os.Stat(filepath.Join(home, filepath.FromSlash(codexDiscRelC+".zst")))
	require.NoError(t, err)
	assert.Equal(t, info.Size(), zst.size)
	assert.False(t, byRel[codexDiscRelA].compressed)

	// The skipped file was never opened; the three survivors were opened once each.
	assert.NotContains(t, opened.paths(), filepath.Join(home, filepath.FromSlash(codexDiscRelC+".zst")))
	assert.Len(t, opened.paths(), 3)
}

// The .zst first-line read must cost one frame block, not a full decompression:
// over a multi-MB incompressible rollout the bytes pulled from the file stay far
// below its size.
func TestDiscoverCodex_ZstFirstLineReadIsBounded(t *testing.T) {
	home := t.TempDir()
	rng := rand.New(rand.NewSource(1))
	var buf bytes.Buffer
	first := codexRollout(t, codexLegacy,
		codexSessionMetaLine(at(0), codexLegacy, codexMetaOpts{id: codexDiscUUIDA, cwd: "/p/big", payloadTS: codexPayloadTS}))
	buf.Write(first)
	blob := make([]byte, 3072)
	for buf.Len() < 4<<20 {
		rng.Read(blob)
		fmt.Fprintf(&buf, `{"timestamp":%q,"type":"noise","payload":{"blob":%q}}`+"\n", at(1), base64.StdEncoding.EncodeToString(blob))
	}
	path := writeCodexRollout(t, home, codexDiscRelC+".zst", buf.Bytes(), true)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(2<<20), "fixture must stay multi-MB after compression")

	opened := hookRolloutOpens(t)
	sessions, _, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "/p/big", sessions[0].ProjectPath)

	read := opened.bytesRead()
	assert.Less(t, read, info.Size()/4, "first-line read pulled %d of %d bytes", read, info.Size())
	assert.Greater(t, read, int64(0))
}

func TestDiscoverCodex_UnreadableFirstLineSkipped(t *testing.T) {
	home := t.TempDir()
	writeCodexRollout(t, home, codexDiscRelA, codexMinimalRollout(t, codexLegacy, codexDiscUUIDA, "/p/a"), false)
	garbage := "sessions/2026/05/01/rollout-2026-05-01T10-10-00-" + codexDiscUUIDB + ".jsonl"
	writeCodexRollout(t, home, garbage, []byte("this is not json\n"), false)
	empty := "sessions/2026/05/01/rollout-2026-05-01T10-20-00-" + codexDiscUUIDD + ".jsonl"
	writeCodexRollout(t, home, empty, nil, false)
	// A JSON first line that is not session_meta is kept with no project hint.
	noMeta := "sessions/2026/05/01/rollout-2026-05-01T10-25-00-" + codexDiscUUIDE + ".jsonl"
	writeCodexRollout(t, home, noMeta, codexRollout(t, codexLegacy, codexTaskStartedLine(at(0))), false)
	h := captureSlog(t)

	sessions, report, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{codexDiscRelA, noMeta}, relPaths(sessions))
	assert.Equal(t, "", sessions[1].ProjectPath)
	assert.Equal(t, 4, report.FirstLineReads, "every survivor is read, even ones then skipped")

	assert.Len(t, h.recordsWithMessage("vault discovery: skipping codex rollout with an unparseable first line"), 1)
	assert.Len(t, h.recordsWithMessage("vault discovery: skipping codex rollout with an unreadable first line"), 1)
	assert.Len(t, h.recordsWithMessage("vault discovery: codex rollout does not open with session_meta; no project hint"), 1)
}

// OS metadata files under a rollout root are skipped at debug, never warned —
// a .DS_Store would otherwise warn on every server-startup sweep.
func TestDiscoverCodex_HiddenFilesSkippedSilently(t *testing.T) {
	home := t.TempDir()
	writeCodexRollout(t, home, codexDiscRelA, codexMinimalRollout(t, codexLegacy, codexDiscUUIDA, "/p/a"), false)
	require.NoError(t, os.WriteFile(filepath.Join(home, "sessions", ".DS_Store"), []byte{0}, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "sessions", "2026", "05", "01", ".directory"), []byte("[x]"), 0o644))
	h := captureSlog(t)

	sessions, _, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{codexDiscRelA}, relPaths(sessions))
	for _, r := range allRecords(h) {
		assert.NotEqual(t, slog.LevelWarn, r.Level, "hidden files must not warn: %s", r.Message)
	}
	assert.Len(t, h.recordsWithMessage("vault discovery: skipping hidden file under a codex rollout root"), 2)
}

// The layout probe never descends past the Codex depth (sessions/YYYY/MM/DD/),
// so a --source over an unrelated tree cannot trigger a whole-tree walk.
func TestIsCodexHome_DepthBounded(t *testing.T) {
	t.Run("live rollout at the real depth is detected", func(t *testing.T) {
		home := t.TempDir()
		writeCodexRollout(t, home, codexDiscRelA, codexMinimalRollout(t, codexLegacy, codexDiscUUIDA, "/p/a"), false)
		assert.True(t, isCodexHome(home))
	})
	t.Run("flat archived rollout is detected", func(t *testing.T) {
		home := t.TempDir()
		writeCodexRollout(t, home, codexDiscRelD, codexMinimalRollout(t, codexLegacy, codexDiscUUIDD, "/p/d"), false)
		assert.True(t, isCodexHome(home))
	})
	t.Run("rollout nested deeper than the layout is not probed", func(t *testing.T) {
		home := t.TempDir()
		deep := "sessions/a/b/c/d/rollout-2026-05-01T10-00-00-" + codexDiscUUIDA + ".jsonl"
		writeCodexRollout(t, home, deep, codexMinimalRollout(t, codexLegacy, codexDiscUUIDA, "/p/a"), false)
		assert.False(t, isCodexHome(home))
	})
	t.Run("claude layout is never codex", func(t *testing.T) {
		root := t.TempDir()
		writeSession(t, filepath.Join(root, "-home-user-proj"), "aaaaaaaa-1111-2222-3333-444444444444", sampleMainJSONL(t), nil)
		assert.False(t, isCodexHome(root))
	})
}

func TestDiscoverCodex_RootsMissingOrEmpty(t *testing.T) {
	t.Run("no rollout roots is an error", func(t *testing.T) {
		_, _, err := DiscoverCodexSessions(context.Background(), t.TempDir(), CodexDiscoverOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no codex rollout roots")
	})
	t.Run("an empty root is an empty result, not an error", func(t *testing.T) {
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, "sessions"), 0o755))
		sessions, report, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
		require.NoError(t, err)
		assert.Empty(t, sessions)
		assert.Equal(t, DiscoveryReport{}, report)
	})
}

// `--source <codex home>` goes through DiscoverSessionsReport, which must
// autodetect the layout and return exactly what the Codex walker returns.
func TestDiscoverSessionsReport_AutodetectsCodexHome(t *testing.T) {
	home := writeCodexDiscoveryHome(t)

	direct, directReport, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
	require.NoError(t, err)
	viaSource, sourceReport, err := DiscoverSessionsReport(context.Background(), home)
	require.NoError(t, err)

	assert.Equal(t, direct, viaSource)
	assert.Equal(t, directReport, sourceReport)
	for _, sf := range viaSource {
		assert.Equal(t, PlatformCodex, sf.Platform)
	}
}

func TestDiscoverSessionsReport_ClaudeLayoutUnchanged(t *testing.T) {
	root := t.TempDir()
	main := sampleMainJSONL(t)
	writeSession(t, filepath.Join(root, "-home-user-proj"), "aaaaaaaa-1111-2222-3333-444444444444", main, nil)

	sessions, report, err := DiscoverSessionsReport(context.Background(), root)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, DiscoveryReport{}, report, "the Claude walker never skips anything the report counts")
	assert.Equal(t, PlatformClaudeCode, sessions[0].Platform)
	assert.Equal(t, int64(len(main)), sessions[0].OnDiskSize)
	assert.Empty(t, sessions[0].RelativePath)
	assert.Empty(t, sessions[0].ProjectPath)
	assert.False(t, sessions[0].Compressed)
}

func TestDiscoverAll(t *testing.T) {
	t.Run("both roots, Claude first then Codex, reports merged", func(t *testing.T) {
		cfg := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", cfg)
		writeSession(t, filepath.Join(cfg, "projects", "-home-user-proj"), "eeeeeeee-1111-2222-3333-444444444444",
			sampleMainJSONL(t), nil)
		home := writeCodexDiscoveryHome(t)
		t.Setenv("CODEX_HOME", home)

		sessions, report, err := DiscoverAll(context.Background(), nil)
		require.NoError(t, err)
		require.Len(t, sessions, 5)
		assert.Equal(t, PlatformClaudeCode, sessions[0].Platform)
		assert.Equal(t, "eeeeeeee-1111-2222-3333-444444444444", sessions[0].UUID)
		for _, sf := range sessions[1:] {
			assert.Equal(t, PlatformCodex, sf.Platform)
		}
		assert.Equal(t, []string{codexDiscRelA, codexDiscRelB, codexDiscRelC, codexDiscRelD}, relPaths(sessions[1:]))
		assert.Equal(t, []string{codexDiscRelE}, report.SkippedRevertVariants)
		assert.Equal(t, 4, report.FirstLineReads)
	})

	t.Run("only one platform walks only that root", func(t *testing.T) {
		cfg := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", cfg)
		// An oversize Claude sidecar would WARN if the Claude root were walked.
		writeSession(t, filepath.Join(cfg, "projects", "-home-user-proj"), "eeeeeeee-1111-2222-3333-444444444444",
			sampleMainJSONL(t), map[string][]byte{"tool-results/big.bin": bytes.Repeat([]byte{0}, maxSidecarBytes+1)})
		home := writeCodexDiscoveryHome(t)
		t.Setenv("CODEX_HOME", home)
		h := captureSlog(t)

		sessions, report, err := DiscoverAll(context.Background(), nil, PlatformCodex)
		require.NoError(t, err)
		assert.Equal(t, []string{codexDiscRelA, codexDiscRelB, codexDiscRelC, codexDiscRelD}, relPaths(sessions))
		for _, sf := range sessions {
			assert.Equal(t, PlatformCodex, sf.Platform)
		}
		assert.Equal(t, 4, report.FirstLineReads)
		assert.Empty(t, h.recordsWithMessage("vault discovery: skipping oversize sidecar file"), "the Claude root must not be walked")

		claudeOnly, _, err := DiscoverAll(context.Background(), nil, PlatformClaudeCode)
		require.NoError(t, err)
		require.Len(t, claudeOnly, 1)
		assert.Equal(t, PlatformClaudeCode, claudeOnly[0].Platform)
	})

	t.Run("codex options reach the walker", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // no projects/ → Claude root absent
		home := writeCodexDiscoveryHome(t)
		t.Setenv("CODEX_HOME", home)

		sessions, report, err := DiscoverAll(context.Background(), &CodexDiscoverOptions{
			Skip: func(rel string, _ int64, _ bool) bool { return rel != codexDiscRelD },
		})
		require.NoError(t, err)
		assert.Equal(t, []string{codexDiscRelD}, relPaths(sessions))
		assert.Equal(t, 3, report.SkippedByPredicate)
		assert.Equal(t, 1, report.FirstLineReads)
	})

	t.Run("no platform root on disk is an empty result", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "absent"))
		h := captureSlog(t)

		sessions, report, err := DiscoverAll(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, sessions)
		assert.Equal(t, DiscoveryReport{}, report)
		for _, r := range allRecords(h) {
			assert.Equal(t, slog.LevelDebug, r.Level, "absent roots are debug, never warnings: %s", r.Message)
		}
	})

	t.Run("an empty Codex root is debug, not silent", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, "sessions"), 0o755))
		t.Setenv("CODEX_HOME", home)
		h := captureSlog(t)

		sessions, _, err := DiscoverAll(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, sessions)
		recs := h.recordsWithMessage("vault discovery: platform root holds no sessions")
		require.Len(t, recs, 1)
		assert.Equal(t, slog.LevelDebug, recs[0].Level)
		assert.Equal(t, string(PlatformCodex), fmt.Sprint(recordAttrs(recs[0])["platform"]))
	})

	t.Run("a Claude root with no sessions yet is debug, not a warning", func(t *testing.T) {
		cfg := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", cfg)
		require.NoError(t, os.MkdirAll(filepath.Join(cfg, "projects"), 0o755))
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		writeCodexRollout(t, home, codexDiscRelA, codexMinimalRollout(t, codexLegacy, codexDiscUUIDA, "/p/a"), false)
		h := captureSlog(t)

		sessions, _, err := DiscoverAll(context.Background(), nil)
		require.NoError(t, err)
		assert.Equal(t, []string{codexDiscRelA}, relPaths(sessions), "an empty Claude root never hides Codex")
		for _, r := range allRecords(h) {
			assert.NotEqual(t, slog.LevelWarn, r.Level, "unexpected warning: %s", r.Message)
		}
	})
}

func TestReadFirstLine(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		cap     int
		want    string
		wantErr string
	}{
		{"newline terminated", "abc\nrest\n", 10, "abc", ""},
		{"crlf terminated", "abc\r\nrest\n", 10, "abc", ""},
		{"no trailing newline", "abc", 10, "abc", ""},
		{"exactly at cap", "abcdefghij\nmore", 10, "abcdefghij", ""},
		{"exactly at cap without newline", "abcdefghij", 10, "abcdefghij", ""},
		{"over cap", "abcdefghijk\nmore", 10, "", "exceeds 10 bytes"},
		{"over cap without newline", "abcdefghijk", 10, "", "exceeds 10 bytes"},
		{"empty input", "", 10, "", "empty first line"},
		{"blank first line", "\nsecond\n", 10, "", "empty first line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readFirstLine(strings.NewReader(tt.in), tt.cap)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

// allRecords snapshots every record the capture handler has seen.
func allRecords(h *recordingHandler) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

// relPaths projects the RelativePath of each Codex session, in order.
func relPaths(sessions []SessionFile) []string {
	out := make([]string, 0, len(sessions))
	for _, sf := range sessions {
		out = append(out, sf.RelativePath)
	}
	return out
}

// rolloutOpenRecorder replaces openRollout for one test, recording every path
// opened and every byte read through the returned readers.
type rolloutOpenRecorder struct {
	opened []string
	read   int64
}

func hookRolloutOpens(t *testing.T) *rolloutOpenRecorder {
	t.Helper()
	rec := &rolloutOpenRecorder{}
	prev := openRollout
	openRollout = func(path string) (io.ReadCloser, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		rec.opened = append(rec.opened, path)
		return &countingReadCloser{ReadCloser: f, n: &rec.read}, nil
	}
	t.Cleanup(func() { openRollout = prev })
	return rec
}

func (r *rolloutOpenRecorder) paths() []string  { return r.opened }
func (r *rolloutOpenRecorder) bytesRead() int64 { return r.read }

type countingReadCloser struct {
	io.ReadCloser
	n *int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	*c.n += int64(n)
	return n, err
}
