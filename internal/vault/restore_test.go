package vault

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreSession_WritesMainAndSidecars(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj") // does not exist yet — RestoreSession creates it
	uuid := "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	main := []byte(`{"type":"user","text":"hello"}` + "\n")
	files := []File{
		{RelativePath: "subagents/agent-1.jsonl", RawContent: []byte(`{"type":"assistant"}`)},
		{RelativePath: "tool-results/t1.json", RawContent: []byte(`{"ok":true}`)},
	}

	res, err := RestoreSession(uuid, main, files, root, nil)
	require.NoError(t, err)
	assert.Len(t, res.Written, 3)
	assert.Empty(t, res.Skipped)
	assert.Empty(t, res.Unsafe)

	gotMain, err := os.ReadFile(filepath.Join(root, uuid+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, main, gotMain)

	gotSub, err := os.ReadFile(filepath.Join(root, uuid, "subagents", "agent-1.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, files[0].RawContent, gotSub)

	gotTool, err := os.ReadFile(filepath.Join(root, uuid, "tool-results", "t1.json"))
	require.NoError(t, err)
	assert.Equal(t, files[1].RawContent, gotTool)
}

func TestRestoreSession_OverwritePolicy(t *testing.T) {
	root := t.TempDir()
	uuid := "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	target := filepath.Join(root, uuid+".jsonl")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0o644))

	// Decline overwrite → existing content survives, recorded as skipped.
	res, err := RestoreSession(uuid, []byte("new"), nil, root, func(string) bool { return false })
	require.NoError(t, err)
	assert.Empty(t, res.Written)
	assert.Equal(t, []string{target}, res.Skipped)
	got, _ := os.ReadFile(target)
	assert.Equal(t, "original", string(got))

	// Approve overwrite → content replaced.
	res, err = RestoreSession(uuid, []byte("new"), nil, root, func(string) bool { return true })
	require.NoError(t, err)
	assert.Equal(t, []string{target}, res.Written)
	got, _ = os.ReadFile(target)
	assert.Equal(t, "new", string(got))
}

func TestRestoreSession_PathSafety(t *testing.T) {
	const uuid = "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	tests := []struct {
		name string
		rel  string
		safe bool
	}{
		{"plain nested", "subagents/agent-1.jsonl", true},
		{"tool result", "tool-results/t1.json", true},
		{"parent traversal", "../../../etc/evil", false},
		{"absolute path", "/etc/passwd", false},
		{"interior traversal", "subagents/../../escape", false},
		{"leading dotdot", "../escape", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			res, err := RestoreSession(uuid, []byte("main"),
				[]File{{RelativePath: tt.rel, RawContent: []byte("x")}}, root, nil)
			require.NoError(t, err) // an unsafe sidecar is skipped, never fatal

			// The main JSONL is always restored, regardless of a bad sidecar.
			_, statErr := os.Stat(filepath.Join(res.Root, uuid+".jsonl"))
			require.NoError(t, statErr, "main JSONL must always be written")

			if tt.safe {
				assert.Len(t, res.Written, 2) // main + sidecar
				assert.Empty(t, res.Unsafe)
			} else {
				assert.Equal(t, []string{filepath.Join(res.Root, uuid+".jsonl")}, res.Written, "only the main JSONL")
				assert.Equal(t, []string{tt.rel}, res.Unsafe)
			}

			// Nothing escaped the restore root: its parent holds only the root.
			entries, _ := os.ReadDir(filepath.Dir(root))
			for _, e := range entries {
				assert.Equal(t, filepath.Base(root), e.Name(), "no file may escape the restore root")
			}
		})
	}
}

// A symlinked restore root must not let a sidecar escape: writes land inside the
// symlink's real target, and the containment check is performed against the
// resolved root.
func TestRestoreSession_SymlinkRootResolved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	require.NoError(t, os.MkdirAll(realRoot, 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(realRoot, link))

	uuid := "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	res, err := RestoreSession(uuid, []byte("main"), nil, link, nil)
	require.NoError(t, err)
	assert.Equal(t, realRoot, res.Root, "root must be resolved through the symlink")

	got, err := os.ReadFile(filepath.Join(realRoot, uuid+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "main", string(got))
}

// A pre-planted symlinked *directory* inside the root must not let a sidecar
// write escape — the lexical containment check passes (os.WriteFile follows the
// symlink), so RestoreSession re-resolves the parent dir after MkdirAll.
func TestRestoreSession_SymlinkDirEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))

	uuid := "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	require.NoError(t, os.MkdirAll(filepath.Join(root, uuid), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, uuid, "sub"))) // root/<uuid>/sub -> outside

	files := []File{{RelativePath: "sub/evil.jsonl", RawContent: []byte("PWNED")}}
	res, err := RestoreSession(uuid, []byte("main"), files, root, func(string) bool { return true })
	require.NoError(t, err)

	assert.Equal(t, []string{"sub/evil.jsonl"}, res.Unsafe, "write through a symlinked dir must be rejected")
	_, statErr := os.Stat(filepath.Join(outside, "evil.jsonl"))
	assert.True(t, os.IsNotExist(statErr), "write must not escape the root through a symlinked directory")
}

// RestoreSessionAt is the Codex form: the main file lands at root/<mainRel>
// (the stored relative rollout path), byte-identical to the archived bytes, and
// the framed content hash over {"<uuid>.jsonl": written} matches the one import
// stored — the round-trip gate (design § Success Criteria 1).
func TestRestoreSessionAt_WritesAtRelativePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "codex-home") // does not exist yet
	const uuid = "019fcc39-ffbb-7212-ba7b-4291df4b05f6"
	mainRel := "sessions/2026/08/04/rollout-2026-08-04T12-03-00-" + uuid + ".jsonl"
	raw := codexMinimalRollout(t, codexLegacy, uuid, "/p/a")
	wantHash, wantSize := computeContentHash(map[string][]byte{uuid + ".jsonl": raw})

	res, err := RestoreSessionAt(uuid, mainRel, raw, nil, root, nil)
	require.NoError(t, err)

	target := filepath.Join(res.Root, filepath.FromSlash(mainRel))
	assert.Equal(t, []string{target}, res.Written)
	assert.Empty(t, res.Skipped)
	assert.Empty(t, res.Unsafe)
	assert.Empty(t, res.Notes, "no compressed twin → no note")

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, raw, got, "restored bytes must be byte-identical to the archived JSONL")
	gotHash, gotSize := computeContentHash(map[string][]byte{uuid + ".jsonl": got})
	assert.Equal(t, wantHash, gotHash)
	assert.Equal(t, wantSize, gotSize)

	// Nothing else was created: no root/<uuid>.jsonl, no root/<uuid>/ dir.
	_, statErr := os.Stat(filepath.Join(res.Root, uuid+".jsonl"))
	assert.True(t, os.IsNotExist(statErr))
	_, statErr = os.Stat(filepath.Join(res.Root, uuid))
	assert.True(t, os.IsNotExist(statErr))
}

// RestoreSession is RestoreSessionAt with "<uuid>.jsonl": the Claude layout is
// unchanged by the generalisation.
func TestRestoreSession_DelegatesToRestoreSessionAt(t *testing.T) {
	root := t.TempDir()
	uuid := "abcd1234-aaaa-bbbb-cccc-1234567890ab"

	viaSession, err := RestoreSession(uuid, []byte("main"), nil, root, nil)
	require.NoError(t, err)
	viaAt, err := RestoreSessionAt(uuid, uuid+".jsonl", []byte("main"), nil, root, func(string) bool { return true })
	require.NoError(t, err)
	assert.Equal(t, viaSession.Written, viaAt.Written)
	assert.Equal(t, []string{filepath.Join(viaSession.Root, uuid+".jsonl")}, viaSession.Written)
}

// mainRel is the session's own location — an unsafe one is fatal, and nothing
// (not even the root) is created for a path rejected up front.
func TestRestoreSessionAt_RejectsUnsafeMainRel(t *testing.T) {
	const uuid = "019fcc39-ffbb-7212-ba7b-4291df4b05f6"
	tests := []struct {
		name    string
		mainRel string
	}{
		{"parent traversal", "../evil.jsonl"},
		{"interior traversal", "sessions/../../evil.jsonl"},
		{"absolute", "/etc/evil.jsonl"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			res, err := RestoreSessionAt(uuid, tt.mainRel, []byte("main"), nil, root, nil)
			require.Error(t, err)
			assert.Nil(t, res)
			assert.Contains(t, err.Error(), "main file path")

			_, statErr := os.Stat(root)
			assert.True(t, os.IsNotExist(statErr), "root must not be created for a rejected mainRel")
			entries, _ := os.ReadDir(base)
			assert.Empty(t, entries, "nothing may be written anywhere")
		})
	}
}

// A compressed twin (root/<mainRel>.zst) is Codex's own state and is never
// touched — the plain file is written beside it and the twin is reported.
func TestRestoreSessionAt_LeavesZstTwinUntouched(t *testing.T) {
	root := t.TempDir()
	const uuid = "019fcc39-ffbb-7212-ba7b-4291df4b05f6"
	mainRel := "sessions/2026/08/04/rollout-2026-08-04T12-03-00-" + uuid + ".jsonl"
	twin := filepath.Join(root, filepath.FromSlash(mainRel)+".zst")
	twinBytes := []byte("compressed bytes that must survive")
	require.NoError(t, os.MkdirAll(filepath.Dir(twin), 0o755))
	require.NoError(t, os.WriteFile(twin, twinBytes, 0o644))

	res, err := RestoreSessionAt(uuid, mainRel, []byte("plain"), nil, root, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(res.Root, filepath.FromSlash(mainRel))}, res.Written)
	assert.Empty(t, res.Skipped, "the twin is not the target, so no overwrite decision is asked")

	gotTwin, err := os.ReadFile(twin)
	require.NoError(t, err)
	assert.Equal(t, twinBytes, gotTwin)

	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], filepath.Join(res.Root, filepath.FromSlash(mainRel)+".zst"))
	assert.Contains(t, res.Notes[0], "untouched")
}

// A dangling leaf symlink at the target must not be followed: the old os.Stat
// path would have missed it (stat fails on a dangling link), skipped the
// overwrite prompt, and written *through* the link to an outside path.
func TestRestoreSession_DanglingLeafSymlinkNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	require.NoError(t, os.MkdirAll(root, 0o755))
	outside := filepath.Join(base, "outside.jsonl") // dangling target, does not exist

	uuid := "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	require.NoError(t, os.Symlink(outside, filepath.Join(root, uuid+".jsonl")))

	res, err := RestoreSession(uuid, []byte("REAL"), nil, root, func(string) bool { return true })
	require.NoError(t, err)
	assert.Len(t, res.Written, 1)

	// The dangling symlink was broken and a regular file written in place.
	got, err := os.ReadFile(filepath.Join(root, uuid+".jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "REAL", string(got))
	// Nothing was written through the link to the outside target.
	_, statErr := os.Stat(outside)
	assert.True(t, os.IsNotExist(statErr), "must not write through a dangling symlink to outside the root")
}
