package vault

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportMinSessionBytes(t *testing.T) {
	raw := sampleMainJSONL(t)
	size := int64(len(raw))
	for _, tt := range []struct {
		name    string
		minimum int64
		sidecar bool
		want    string
	}{
		{"disabled", 0, false, StatusNew},
		{"above minimum", size - 1, false, StatusNew},
		{"at minimum", size, false, StatusNew},
		{"below minimum", size + 1, false, StatusExcluded},
		{"sidecars count", size + 10, true, StatusNew},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st, root := newTestVault(t), t.TempDir()
			const uuid = "11111111-2222-3333-4444-555555555555"
			var sidecars map[string][]byte
			wantSize := size
			if tt.sidecar {
				sidecars = map[string][]byte{"tool-results/output.txt": bytes.Repeat([]byte("x"), 10)}
				wantSize += 10
			}
			writeSession(t, root, uuid, raw, sidecars)
			opts := ImportOptions{MinSessionBytes: tt.minimum, DryRun: true}
			dry := importFixture(t, st, root, opts)
			require.Zero(t, dry.Errors)
			require.Len(t, dry.Sessions, 1)
			assert.Equal(t, tt.want, dry.Sessions[0].Status)
			assert.Equal(t, wantSize, dry.Sessions[0].SizeBytes)
			_, err := st.GetSession(context.Background(), uuid)
			require.ErrorIs(t, err, ErrSessionNotFound)

			opts.DryRun = false
			real := importFixture(t, st, root, opts)
			require.Zero(t, real.Errors)
			require.Equal(t, dry.Sessions, real.Sessions)
			got, err := st.GetSession(context.Background(), uuid)
			if tt.want == StatusExcluded {
				require.ErrorIs(t, err, ErrSessionNotFound)
				assert.Contains(t, real.Sessions[0].Reason, "below minimum size")
				hits, err := st.Search(context.Background(), SearchOptions{Query: "timeout"})
				require.NoError(t, err)
				assert.Empty(t, hits)
			} else {
				require.NoError(t, err)
				assert.Equal(t, raw, got.RawJSONL)
			}
		})
	}
}

func TestImportMinSessionBytesGrowthAndExistingArchive(t *testing.T) {
	st, root := newTestVault(t), t.TempDir()
	const uuid = "11111111-2222-3333-4444-555555555555"
	raw := sampleMainJSONL(t)
	writeSession(t, root, uuid, raw, nil)
	opts := ImportOptions{MinSessionBytes: int64(len(raw)) + 1}
	require.Equal(t, 1, importFixture(t, st, root, opts).Excluded)

	grown := append(raw, jsonlBytes(t, userLine("later", "/home/user/proj", "main", "Continue the investigation"))...)
	writeSession(t, root, uuid, grown, nil)
	require.Equal(t, 1, importFixture(t, st, root, opts).Imported)

	// Raising the minimum cannot freeze an existing archive or its index.
	opts.MinSessionBytes = 1 << 20
	require.Equal(t, 1, importFixture(t, st, root, opts).Skipped)
	db, err := st.getDB(context.Background())
	require.NoError(t, err)
	_, err = db.Exec("UPDATE vault_sessions SET index_version=? WHERE uuid=?", currentIndexVersion-1, uuid)
	require.NoError(t, err)
	require.Equal(t, 1, importFixture(t, st, root, opts).Updated)
	got, err := st.GetSession(context.Background(), uuid)
	require.NoError(t, err)
	assert.Equal(t, currentIndexVersion, got.IndexVersion)

	grown = append(grown, jsonlBytes(t, userLine("later2", "/home/user/proj", "main", "One more useful turn"))...)
	writeSession(t, root, uuid, grown, nil)
	require.Equal(t, 1, importFixture(t, st, root, opts).Updated)
	got, err = st.GetSession(context.Background(), uuid)
	require.NoError(t, err)
	assert.Equal(t, grown, got.RawJSONL)
}

func TestImportMinSessionBytesCodexCompression(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("compressed=%t", compressed), func(t *testing.T) {
			st, home := newTestVault(t), t.TempDir()
			raw := codexImportRollout(t, codexLegacy, codexFixtureID, "/proj", "A useful short conversation")
			rel := codexActiveRel(codexFixtureID)
			if compressed {
				rel += ".zst"
			}
			writeCodexRollout(t, home, rel, raw, compressed)
			require.Equal(t, 1, importCodexHome(t, st, home, ImportOptions{MinSessionBytes: int64(len(raw)) + 1}).Excluded)
			res := importCodexHome(t, st, home, ImportOptions{MinSessionBytes: int64(len(raw))})
			require.Zero(t, res.Errors)
			require.Equal(t, 1, res.Imported)
			assert.Equal(t, int64(len(raw)), res.Sessions[0].SizeBytes)
		})
	}
}

func TestImportMinSessionBytesLargerDuplicateQualifies(t *testing.T) {
	st, home := newTestVault(t), t.TempDir()
	small := codexImportRollout(t, codexLegacy, codexFixtureID, "/proj", "hi")
	large := codexImportRollout(t, codexLegacy, codexFixtureID, "/proj", "A substantially longer useful conversation")
	writeCodexRollout(t, home, codexActiveRel(codexFixtureID), small, false)
	writeCodexRollout(t, home, codexArchivedRel(codexFixtureID), large, false)
	opts := ImportOptions{MinSessionBytes: int64(len(large)), DryRun: true}
	dry := importCodexHome(t, st, home, opts)
	opts.DryRun = false
	real := importCodexHome(t, st, home, opts)
	require.Zero(t, real.Errors)
	assert.Equal(t, 1, real.Excluded)
	assert.Equal(t, 1, real.Imported)
	assert.Equal(t, dry.Sessions, real.Sessions)
	got, err := st.GetSession(context.Background(), codexFixtureID)
	require.NoError(t, err)
	assert.Equal(t, large, got.RawJSONL)
}

func TestMergeMinSessionBytes(t *testing.T) {
	ctx := context.Background()
	srcPath, destPath := filepath.Join(t.TempDir(), "source.db"), filepath.Join(t.TempDir(), "dest.db")
	small := mergeRecord(t, "small-session", "tiny", 2, 100, "small", "source", "/proj")
	boundary := mergeRecord(t, "boundary-session", "boundary", 2, 1000, "boundary", "source", "/proj")
	old := mergeRecord(t, "existing-session", "old", 2, 100, "old", "dest", "/proj")
	grown := mergeRecord(t, "existing-session", "grown", 2, 200, "grown", "source", "/proj")
	unchanged := mergeRecord(t, "unchanged-session", "same", 2, 100, "same", "source", "/proj")
	buildVault(t, srcPath, testVaultKey, small, boundary, grown, unchanged)
	buildVault(t, destPath, testVaultKey, old, unchanged)
	dest := openDest(t, destPath, testVaultKey)

	opts := MergeOptions{MinSessionBytes: 1000, DryRun: true}
	dry, err := MergeFrom(ctx, dest, srcPath, testVaultKey, "test", opts)
	require.NoError(t, err)
	require.Zero(t, dry.Errors)
	assert.Equal(t, 1, dry.Imported)
	assert.Equal(t, 1, dry.Updated)
	assert.Equal(t, 1, dry.Skipped)
	assert.Equal(t, 1, dry.Excluded)
	got, err := dest.GetSession(ctx, old.Session.UUID)
	require.NoError(t, err)
	assert.Equal(t, old.Session.RawJSONL, got.RawJSONL)

	opts.DryRun = false
	real, err := MergeFrom(ctx, dest, srcPath, testVaultKey, "test", opts)
	require.NoError(t, err)
	require.Zero(t, real.Errors)
	// Real writes report queued rows at batch flush; compare per-session decisions.
	assert.ElementsMatch(t, dry.Sessions, real.Sessions)
	_, err = dest.GetSession(ctx, small.Session.UUID)
	require.ErrorIs(t, err, ErrSessionNotFound)
	got, err = dest.GetSession(ctx, grown.Session.UUID)
	require.NoError(t, err)
	assert.Equal(t, grown.Session.RawJSONL, got.RawJSONL)

	// A rejected source remains eligible on a later run with the filter disabled.
	real, err = MergeFrom(ctx, dest, srcPath, testVaultKey, "test", MergeOptions{})
	require.NoError(t, err)
	require.Zero(t, real.Errors)
	assert.Equal(t, 1, real.Imported)
}
