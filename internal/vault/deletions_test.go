package vault

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func rememberForTest(t *testing.T, s *VaultStore, id string) {
	t.Helper()
	ok, err := s.deleteSession(t.Context(), id, true)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestDeletionMigrationAndAtomicRemoval(t *testing.T) {
	s := newTestVault(t)
	ctx := t.Context()
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var fresh, migrated string
	const ddl = "SELECT sql FROM sqlite_master WHERE name='vault_session_deletions'"
	require.NoError(t, db.QueryRow(ddl).Scan(&fresh))
	_, err = db.Exec("DROP TABLE vault_session_deletions; DELETE FROM vault_migrations WHERE name='0008_session_deletions'")
	require.NoError(t, err)
	require.NoError(t, migrateVault(ctx, db))
	require.NoError(t, db.QueryRow(ddl).Scan(&migrated))
	require.Equal(t, fresh, migrated)
	require.Empty(t, minReaderMarker(t, s))

	parent := sampleRecord(codexDiscUUIDA)
	parent.Chunks = []Chunk{{ContentText: "searchable parent"}}
	child := sampleRecord(codexDiscUUIDB)
	child.Session.ParentUUID = parent.Session.UUID
	require.NoError(t, s.InsertSession(ctx, parent))
	require.NoError(t, s.InsertSession(ctx, child))
	_, err = s.SetSessionProject(ctx, parent.Session.UUID, ProjectOptions{Name: "custom"})
	require.NoError(t, err)
	before := minReaderMarker(t, s)
	_, err = db.Exec("CREATE TRIGGER reject_delete BEFORE DELETE ON vault_sessions BEGIN SELECT RAISE(ABORT, 'rollback'); END")
	require.NoError(t, err)
	_, err = s.deleteSession(ctx, parent.Session.UUID, true)
	require.ErrorContains(t, err, "rollback")
	deleted, err := s.sessionDeleted(ctx, parent.Session.UUID)
	require.NoError(t, err)
	require.False(t, deleted)
	require.Equal(t, before, minReaderMarker(t, s))
	_, err = db.Exec("DROP TRIGGER reject_delete")
	require.NoError(t, err)
	rememberForTest(t, s, parent.Session.UUID)
	var project string
	require.NoError(t, db.QueryRow("SELECT effective_project FROM vault_session_deletions WHERE session_uuid=?", parent.Session.UUID).Scan(&project))
	require.Equal(t, "custom", project)
	require.Equal(t, "4", minReaderMarker(t, s))
	for _, table := range []string{"vault_files", "vault_session_projects", "vault_fts", "vault_chunks", "vault_chunks_trigram"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table+" WHERE session_uuid=?", parent.Session.UUID).Scan(&count))
		require.Zero(t, count, table)
	}
	_, err = s.GetSession(ctx, child.Session.UUID)
	require.NoError(t, err)
	ok, err := s.deleteSession(ctx, "missing", true)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestImportDeletionBeforeFileRead(t *testing.T) {
	ctx := t.Context()
	s := newTestVault(t)
	require.NoError(t, s.InsertSession(ctx, sampleRecord(codexDiscUUIDA)))
	rememberForTest(t, s, codexDiscUUIDA)
	home := t.TempDir()
	raw := codexImportRollout(t, codexLegacy, codexDiscUUIDB, "/project", "survivor")
	writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDB), raw, false)
	files, _, err := DiscoverCodexSessions(ctx, home, CodexDiscoverOptions{})
	require.NoError(t, err)
	files = append(files, SessionFile{UUID: codexDiscUUIDA, Path: home + "/absent"})
	for _, dry := range []bool{true, false} {
		r := Import(ctx, s, files, ImportOptions{DryRun: dry})
		require.Equal(t, 1, r.Imported)
		require.Equal(t, 1, r.Excluded)
		require.Zero(t, r.Errors)
		require.Len(t, r.Sessions, 2)
		for _, entry := range r.Sessions {
			if entry.UUID == codexDiscUUIDA {
				require.Equal(t, reasonDeleted, entry.Reason)
			}
		}
	}
}

func TestDeletedWriteSurvivesBatchRollbackAndRetry(t *testing.T) {
	s := newTestVault(t)
	ctx := t.Context()
	a, b := sampleRecord(codexDiscUUIDA), sampleRecord(codexDiscUUIDB)
	require.NoError(t, s.InsertSession(ctx, a))
	rememberForTest(t, s, a.Session.UUID)
	// The duplicate insert fails after the first insert, rolling back the batch.
	writes := []SessionWrite{{Record: a, Replace: true}, {Record: b}, {Record: b}}
	outcomes, err := s.WriteBatch(ctx, writes)
	require.Error(t, err)
	require.Nil(t, outcomes)
	_, err = s.GetSession(ctx, b.Session.UUID)
	require.ErrorIs(t, err, ErrSessionNotFound)
	pending := []ImportedSession{{UUID: a.Session.UUID, Status: StatusUpdated}, {UUID: b.Session.UUID, Status: StatusNew}, {UUID: b.Session.UUID, Status: StatusNew}}
	flushImportBatch(ctx, writes, pending, s.WriteBatch)
	require.Equal(t, reasonDeleted, pending[0].Reason)
	require.Equal(t, StatusExcluded, pending[0].Status)
	require.Equal(t, StatusNew, pending[1].Status)
	require.Equal(t, StatusError, pending[2].Status)
}

type interleavedImportWriter struct {
	*VaultStore
	beforeDigest  func()
	beforeHint    func()
	beforeRebuild func()
	failBatch     bool
}

func (w *interleavedImportWriter) importDigest(ctx context.Context, id string) (archiveDigest, error) {
	if w.beforeDigest != nil {
		w.beforeDigest()
	}
	return w.VaultStore.importDigest(ctx, id)
}

func TestImportDeletionDuringFileRead(t *testing.T) {
	for _, dry := range []bool{true, false} {
		s := newTestVault(t)
		home := t.TempDir()
		writeCodexRollout(t, home, codexActiveRel(codexDiscUUIDA), codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/a", "test"), false)
		require.Equal(t, 1, importCodexHome(t, s, home, ImportOptions{}).Imported)
		files, _, err := DiscoverCodexSessions(t.Context(), home, CodexDiscoverOptions{})
		require.NoError(t, err)
		w := &interleavedImportWriter{VaultStore: s, beforeDigest: func() { rememberForTest(t, s, codexDiscUUIDA) }}
		res := importSessions(t.Context(), w, files, ImportOptions{DryRun: dry, MinSessionBytes: 1 << 30})
		require.Len(t, res.Sessions, 1)
		require.Equal(t, StatusExcluded, res.Sessions[0].Status)
		require.Equal(t, reasonDeleted, res.Sessions[0].Reason)
	}
}

func (w *interleavedImportWriter) updateLocationHint(ctx context.Context, id, hint string) (writeOutcome, error) {
	if w.beforeHint != nil {
		w.beforeHint()
	}
	return w.VaultStore.updateLocationHint(ctx, id, hint)
}

func (w *interleavedImportWriter) rebuildFTSBatch(ctx context.Context, items []FTSRebuild) ([]writeOutcome, error) {
	if w.beforeRebuild != nil {
		w.beforeRebuild()
		w.beforeRebuild = nil
	}
	if w.failBatch && len(items) > 1 {
		return nil, errors.New("force individual retry")
	}
	return w.VaultStore.rebuildFTSBatch(ctx, items)
}

func TestDeletionDuringMovedAndStaleImport(t *testing.T) {
	for _, phase := range []string{"before hint", "after hint", "after hint with batch retry"} {
		t.Run(phase, func(t *testing.T) {
			ctx := t.Context()
			s := newTestVault(t)
			home := t.TempDir()
			for _, id := range []string{codexDiscUUIDA, codexDiscUUIDB} {
				writeCodexRollout(t, home, codexActiveRel(id), codexImportRollout(t, codexLegacy, id, "/project", "searchable"), false)
			}
			require.Equal(t, 2, importCodexHome(t, s, home, ImportOptions{}).Imported)
			db, err := s.getDB(ctx)
			require.NoError(t, err)
			_, err = db.Exec("UPDATE vault_sessions SET index_version=0")
			require.NoError(t, err)
			require.NoError(t, os.Remove(home+"/"+codexActiveRel(codexDiscUUIDA)))
			writeCodexRollout(t, home, codexArchivedRel(codexDiscUUIDA), codexImportRollout(t, codexLegacy, codexDiscUUIDA, "/project", "searchable"), false)
			files, _, err := DiscoverCodexSessions(ctx, home, CodexDiscoverOptions{})
			require.NoError(t, err)
			w := &interleavedImportWriter{VaultStore: s}
			del := func() { rememberForTest(t, s, codexDiscUUIDA) }
			if phase == "before hint" {
				w.beforeHint = del
			} else {
				w.beforeRebuild = func() {
					_, _, _, hint, _, err := s.SessionDigest(ctx, codexDiscUUIDA)
					require.NoError(t, err)
					require.Equal(t, codexArchivedRel(codexDiscUUIDA), hint)
					del()
				}
				w.failBatch = phase == "after hint with batch retry"
			}
			r := importSessions(ctx, w, files, ImportOptions{})
			require.Len(t, r.Sessions, 2)
			require.Equal(t, map[string][]string{codexDiscUUIDA: {StatusExcluded}, codexDiscUUIDB: {StatusUpdated}}, statusByUUID(r))
			for _, entry := range r.Sessions {
				if entry.UUID == codexDiscUUIDA {
					require.Equal(t, reasonDeleted, entry.Reason)
				}
			}
			n, err := s.RebuildFTSBatch(ctx, []FTSRebuild{{UUID: codexDiscUUIDA}, {UUID: codexDiscUUIDB}})
			require.NoError(t, err)
			require.Equal(t, 1, n)
		})
	}
}
