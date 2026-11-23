package vault

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeletePlanStale(t *testing.T) {
	for _, change := range []string{"remove", "reassign"} {
		t.Run(change, func(t *testing.T) {
			s := newTestVault(t)
			ctx := t.Context()
			ids := []string{codexDiscUUIDA, codexDiscUUIDB}
			for _, id := range ids {
				require.NoError(t, s.InsertSession(ctx, sampleRecord(id)))
			}
			plan, err := s.PrepareDelete(ctx, ids, DeleteOptions{ExactIDs: true, Remember: true})
			require.NoError(t, err)
			other := NewVaultStore(s.dbPath)
			defer other.Close()
			if change == "remove" {
				ok, err := other.DeleteSession(ctx, ids[1], false)
				require.NoError(t, err)
				require.True(t, ok)
			} else {
				_, err := other.SetSessionProject(ctx, ids[1], ProjectOptions{Name: "changed"})
				require.NoError(t, err)
			}
			before := minReaderMarker(t, s)
			require.ErrorIs(t, s.ApplyDelete(ctx, plan), ErrStaleDeletePlan)
			_, err = s.GetSession(ctx, ids[0])
			require.NoError(t, err)
			for _, id := range ids {
				deleted, err := s.sessionDeleted(ctx, id)
				require.NoError(t, err)
				require.False(t, deleted)
			}
			require.Equal(t, before, minReaderMarker(t, s))
		})
	}
}

func TestDeleteSessionOneTimeCleansMissingArchiveIndexes(t *testing.T) {
	s := newTestVault(t)
	ctx := t.Context()
	rec := sampleRecord(codexDiscUUIDA)
	rec.Chunks = []Chunk{{ContentText: "orphaned chunk"}}
	require.NoError(t, s.InsertSession(ctx, rec))
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	_, err = db.Exec("DELETE FROM vault_sessions WHERE uuid=?", codexDiscUUIDA)
	require.NoError(t, err)
	marker := minReaderMarker(t, s)
	found, err := s.DeleteSession(ctx, codexDiscUUIDA, false)
	require.NoError(t, err)
	require.False(t, found)
	for _, table := range []string{"vault_fts", "vault_chunks", "vault_chunks_trigram", "vault_session_deletions"} {
		var n int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table+" WHERE session_uuid=?", codexDiscUUIDA).Scan(&n))
		require.Zero(t, n, table)
	}
	require.Equal(t, marker, minReaderMarker(t, s))
}

func TestDeletePlanAcrossGroups(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			s := newTestVault(t)
			ctx := t.Context()
			db, err := s.getDB(ctx)
			require.NoError(t, err)
			var ids []string
			for i := range maxBatchSessions + 2 {
				id := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i+1)
				ids = append(ids, id)
				rec := sampleRecord(id)
				rec.Chunks = []Chunk{{ContentText: "indexed chunk"}}
				require.NoError(t, s.InsertSession(ctx, rec))
				_, err = s.RenameSession(ctx, id, RenameOptions{Name: "renamed"})
				require.NoError(t, err)
				_, err = s.SetSessionProject(ctx, id, ProjectOptions{Name: "project"})
				require.NoError(t, err)
			}
			last := len(ids) - 1 // unrelated survivor
			marker := minReaderMarker(t, s)
			plan, err := s.PrepareDelete(ctx, ids[:last], DeleteOptions{ExactIDs: true, Remember: true})
			require.NoError(t, err)
			if rollback {
				_, err = db.Exec("CREATE TRIGGER reject_last BEFORE DELETE ON vault_sessions WHEN OLD.uuid='" + ids[last-1] + "' BEGIN SELECT RAISE(ABORT,'trailing group'); END")
				require.NoError(t, err)
				require.ErrorContains(t, s.ApplyDelete(ctx, plan), "trailing group")
				require.Equal(t, marker, minReaderMarker(t, s))
			} else {
				require.NoError(t, s.ApplyDelete(ctx, plan))
				require.Equal(t, "4", minReaderMarker(t, s))
			}
			for i, id := range ids {
				live, pair := 0, 1
				if rollback || i == last {
					live, pair = 1, 0
				}
				for _, check := range []struct {
					table, key string
					want       int
				}{
					{"vault_sessions", "uuid", live}, {"vault_files", "session_uuid", 2 * live},
					{"vault_session_names", "session_uuid", live}, {"vault_session_projects", "session_uuid", live},
					{"vault_fts", "session_uuid", 3 * live}, {"vault_chunks", "session_uuid", live},
					{"vault_chunks_trigram", "session_uuid", live}, {"vault_session_deletions", "session_uuid", pair},
				} {
					var n int
					require.NoError(t, db.QueryRow("SELECT count(*) FROM "+check.table+" WHERE "+check.key+"=?", id).Scan(&n))
					require.Equal(t, check.want, n, "%s %s", check.table, id)
				}
			}
		})
	}
}
