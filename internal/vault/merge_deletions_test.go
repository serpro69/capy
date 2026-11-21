package vault

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mergeVaults(t *testing.T, dest, src *VaultStore, opts MergeOptions) ImportResult {
	t.Helper()
	res, err := MergeFrom(t.Context(), dest, src.dbPath, testVaultKey, vaultKeyEnv, opts)
	require.NoError(t, err)
	require.Zero(t, res.Errors)
	return res
}

func TestForwardSelectedDeletions(t *testing.T) {
	a, b, c := newTestVault(t), newTestVault(t), newTestVault(t)
	ctx := t.Context()
	const id = codexDiscUUIDA
	for i, s := range []*VaultStore{a, b, c} {
		rec := sampleRecord(id)
		rec.Session.ProjectPath = []string{"/a/p", "/b/p", "/c/p"}[i]
		require.NoError(t, s.InsertSession(ctx, rec))
		_, err := s.SetSessionProject(ctx, id, ProjectOptions{Name: []string{"Personal", "Work", "elsewhere"}[i]})
		require.NoError(t, err)
	}
	require.NoError(t, c.InsertSession(ctx, sampleRecord(codexDiscUUIDB)))
	rememberForTest(t, a, id)
	want := []MergedDeletion{{UUID: id, Removed: true, Added: 1}}
	require.Empty(t, mergeVaults(t, b, a, MergeOptions{Project: "work"}).Deletions)
	require.Equal(t, want, mergeVaults(t, b, a, MergeOptions{Project: "PERSON", MinSessionBytes: 1 << 40}).Deletions)
	require.NoError(t, b.Close())
	require.NoError(t, b.Open(ctx))
	require.Empty(t, mergeVaults(t, c, b, MergeOptions{Project: "work"}).Deletions)
	require.Equal(t, want, mergeVaults(t, c, b, MergeOptions{Project: "personal"}).Deletions)
	_, err := c.GetSession(ctx, codexDiscUUIDB)
	require.NoError(t, err)

	// An empty receiver also forwards the source pair after reopening.
	empty := newTestVault(t)
	require.Equal(t, []MergedDeletion{{UUID: id, Added: 1}}, mergeVaults(t, empty, b, MergeOptions{}).Deletions)
	require.NoError(t, empty.Close())
	require.Equal(t, []MergedDeletion{{UUID: id, Known: 1}}, mergeVaults(t, c, empty, MergeOptions{}).Deletions)

	independent := newTestVault(t)
	require.NoError(t, independent.InsertSession(ctx, sampleRecord(id)))
	_, err = independent.SetSessionProject(ctx, id, ProjectOptions{Name: "Work"})
	require.NoError(t, err)
	rememberForTest(t, independent, id)
	require.Equal(t, []MergedDeletion{{UUID: id, Added: 1}}, mergeVaults(t, b, independent, MergeOptions{}).Deletions)
	require.Equal(t, []MergedDeletion{{UUID: id, Added: 1}}, mergeVaults(t, c, b, MergeOptions{Project: "work"}).Deletions)
}

func TestMergeDeletionFirstMarkerCleansOrphanIndexesOnce(t *testing.T) {
	ctx := t.Context()
	s := newTestVault(t)
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	tables := []string{"vault_fts", "vault_chunks", "vault_chunks_trigram"}
	for _, table := range tables {
		_, err := db.Exec("INSERT INTO "+table+" (session_uuid,content_text) VALUES (?, 'orphan'), (?, 'neighbor')", codexDiscUUIDA, codexDiscUUIDB)
		require.NoError(t, err)
	}
	batch := []deletionGroup{{uuid: codexDiscUUIDA, projects: []string{"a", "b"}}}
	check := func(preview bool, added, known int) {
		t.Helper()
		out, err := s.mergeDeletionBatch(ctx, batch, preview)
		require.NoError(t, err)
		require.Equal(t, []MergedDeletion{{UUID: codexDiscUUIDA, Added: added, Known: known}}, out)
	}
	check(true, 2, 0)
	require.Empty(t, minReaderMarker(t, s))
	for _, table := range tables {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Equal(t, 2, count)
	}
	check(false, 2, 0)
	require.Equal(t, "4", minReaderMarker(t, s))
	for _, table := range tables {
		var id string
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*), min(session_uuid) FROM "+table).Scan(&count, &id))
		require.Equal(t, 1, count)
		require.Equal(t, codexDiscUUIDB, id)
		// Any subsequent FTS DELETE, even one matching zero rows, must now fail.
		_, err := db.Exec("DROP TABLE " + table)
		require.NoError(t, err)
	}
	check(false, 0, 2)
	batch[0].projects = []string{"c"}
	check(false, 1, 0)
}

type cancelAfterDeletionBatch struct {
	*VaultStore
	cancel context.CancelFunc
}

func (s cancelAfterDeletionBatch) mergeDeletionBatch(ctx context.Context, groups []deletionGroup, dry bool) ([]MergedDeletion, error) {
	result, err := s.VaultStore.mergeDeletionBatch(ctx, groups, dry)
	if err == nil {
		s.cancel()
	}
	return result, err
}

func TestMergeCancelledAfterCommittedDeletionBatch(t *testing.T) {
	src, dest := newTestVault(t), newTestVault(t)
	db, err := src.getDB(t.Context())
	require.NoError(t, err)
	for i := 0; i <= maxBatchSessions; i++ {
		_, err := db.Exec(insertDeletionSQL, fmt.Sprintf("abcd1234-0000-0000-0000-%012d", i), "project")
		require.NoError(t, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	res, err := mergeSnapshot(ctx, cancelAfterDeletionBatch{dest, cancel}, tx, MergeOptions{})
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, res.Deletions, maxBatchSessions)
	retry := mergeVaults(t, dest, src, MergeOptions{})
	require.Len(t, retry.Deletions, maxBatchSessions+1)
	for _, d := range retry.Deletions[:maxBatchSessions] {
		require.Equal(t, 1, d.Known)
	}
	require.Equal(t, 1, retry.Deletions[maxBatchSessions].Added)
}

// Commit a source deletion after marker enumeration, immediately before live enumeration.
type deletingSource struct {
	*sql.Tx
	beforeLive func()
}

func (s *deletingSource) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if strings.HasPrefix(q, "SELECT s.uuid FROM vault_sessions") {
		s.beforeLive()
	}
	return s.Tx.QueryContext(ctx, q, args...)
}

func TestMergeSourceDeletionUsesOneSnapshot(t *testing.T) {
	ctx := t.Context()
	src, dest := newTestVault(t), newTestVault(t)
	rec := mergeRecord(t, codexDiscUUIDA, "snapshot", 3, 100, "h", "machine", "/p")
	rec.Files = sampleRecord(codexDiscUUIDA).Files
	require.NoError(t, src.InsertSession(ctx, rec))
	_, err := src.SetSessionProject(ctx, codexDiscUUIDA, ProjectOptions{Name: "before delete"})
	require.NoError(t, err)
	db, err := src.getDB(ctx)
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	source := &deletingSource{Tx: tx, beforeLive: func() { rememberForTest(t, src, codexDiscUUIDA) }}
	res, err := mergeSnapshot(ctx, dest, source, MergeOptions{})
	require.NoError(t, err)
	require.Empty(t, res.Deletions)
	require.Equal(t, 1, res.Imported)
	require.Equal(t, "before delete", res.Sessions[0].Project)
	files, err := dest.GetFiles(ctx, codexDiscUUIDA)
	require.NoError(t, err)
	require.Equal(t, rec.Files, files)
	require.NoError(t, tx.Rollback())
	require.Equal(t, []MergedDeletion{{UUID: codexDiscUUIDA, Removed: true, Added: 1}}, mergeVaults(t, dest, src, MergeOptions{}).Deletions)
}

func TestMergeLegacyLiveAndMissingDeletionTable(t *testing.T) {
	ctx := t.Context()
	src, dest := newTestVault(t), newTestVault(t)
	require.NoError(t, src.InsertSession(ctx, mergeCodexRecord(t, codexDiscUUIDA, "live", "")))
	require.NoError(t, dest.InsertSession(ctx, sampleRecord(codexDiscUUIDA)))
	rememberForTest(t, dest, codexDiscUUIDA)
	db, err := src.getDB(ctx)
	require.NoError(t, err)
	_, err = db.Exec("DROP TABLE vault_session_deletions; UPDATE vault_sessions SET encoding='invalid'")
	require.NoError(t, err)
	require.Equal(t, "3", minReaderMarker(t, src))
	res := mergeVaults(t, dest, src, MergeOptions{})
	require.Equal(t, 1, res.Excluded)
	require.Equal(t, reasonDeleted, res.Sessions[0].Reason)
	require.Equal(t, PlatformCodex, res.Sessions[0].Platform)
	_, err = db.Exec("UPDATE vault_meta SET value='4' WHERE key=?", minReaderVersionKey)
	require.NoError(t, err)
	res, err = MergeFrom(ctx, dest, src.dbPath, testVaultKey, vaultKeyEnv, MergeOptions{})
	require.ErrorContains(t, err, "lacks vault_session_deletions")
	require.Empty(t, res.Sessions)
	require.Empty(t, res.Deletions)
}

type interleavedDestination struct {
	*VaultStore
	afterPlan   func()
	afterWrite  func()
	beforeWrite func()
}

func (s *interleavedDestination) mergeState(ctx context.Context, id string) (*Session, bool, error) {
	sess, marked, err := s.VaultStore.mergeState(ctx, id)
	if s.afterPlan != nil {
		s.afterPlan()
	}
	return sess, marked, err
}

func (s *interleavedDestination) WriteBatch(ctx context.Context, w []SessionWrite) ([]writeOutcome, error) {
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	out, err := s.VaultStore.WriteBatch(ctx, w)
	if err == nil && s.afterWrite != nil {
		s.afterWrite()
	}
	return out, err
}

func mergeWithDestination(t *testing.T, dest mergeDestination, src *VaultStore, opts MergeOptions) ImportResult {
	t.Helper()
	db, err := src.getDB(t.Context())
	require.NoError(t, err)
	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	result, err := mergeSnapshot(t.Context(), dest, tx, opts)
	require.NoError(t, err)
	require.Zero(t, result.Errors)
	return result
}

func TestMergeDestinationDeletionDuringPlanningOrReporting(t *testing.T) {
	for _, phase := range []string{"preview", "replacement", "metadata", "metadata one-time", "after commit"} {
		t.Run(phase, func(t *testing.T) {
			ctx := t.Context()
			src, dst := newTestVault(t), newTestVault(t)
			require.NoError(t, dst.InsertSession(ctx, mergeRecord(t, codexDiscUUIDA, "old", 2, 10, "old", "b", "/b")))
			rec := mergeRecord(t, codexDiscUUIDA, "new", 3, 20, "new", "a", "/a")
			if strings.HasPrefix(phase, "metadata") {
				rec.Session.ContentHash = "old"
			}
			require.NoError(t, src.InsertSession(ctx, rec))
			_, err := src.SetSessionProject(ctx, codexDiscUUIDA, ProjectOptions{Name: "winning label"})
			require.NoError(t, err)
			dest := &interleavedDestination{VaultStore: dst}
			del := func() {
				ok, err := dst.deleteSession(ctx, codexDiscUUIDA, phase != "metadata one-time")
				require.NoError(t, err)
				require.True(t, ok)
			}
			switch phase {
			case "after commit":
				dest.afterWrite = del
			case "replacement":
				dest.beforeWrite = del
			default:
				dest.afterPlan = del
			}
			res := mergeWithDestination(t, dest, src, MergeOptions{DryRun: phase == "preview"})
			require.Len(t, res.Sessions, 1)
			switch phase {
			case "preview", "after commit":
				require.Equal(t, 1, res.Updated)
				require.Equal(t, "winning label", res.Sessions[0].Project)
			case "metadata one-time":
				require.Equal(t, 1, res.Skipped)
				require.Equal(t, reasonDisappeared, res.Sessions[0].Reason)
			default:
				require.Equal(t, 1, res.Excluded)
				require.Equal(t, reasonDeleted, res.Sessions[0].Reason)
			}
		})
	}
}
