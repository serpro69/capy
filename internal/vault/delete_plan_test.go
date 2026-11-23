package vault

import (
	"fmt"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
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

func addDeleteTestSession(t *testing.T, s *VaultStore, id, parent, project string) {
	t.Helper()
	rec := sampleRecord(id)
	rec.Session.ParentUUID, rec.Session.ProjectPath = parent, project
	require.NoError(t, s.InsertSession(t.Context(), rec))
}

func TestDeletePlanDescendants(t *testing.T) {
	for _, expand := range []bool{false, true} {
		t.Run(fmt.Sprintf("expand=%t", expand), func(t *testing.T) {
			s := newTestVault(t)
			ctx := t.Context()
			// The selected family is still traversable when its parent is absent.
			addDeleteTestSession(t, s, "parent01", "absent01", "root")
			addDeleteTestSession(t, s, "child001", "parent01", "root")
			addDeleteTestSession(t, s, "grand001", "child001", "other")
			addDeleteTestSession(t, s, "unrelated", "", "other")
			plan, err := s.PrepareDelete(ctx, []string{"child001", "parent01", "child001"}, DeleteOptions{WithDescendants: expand})
			require.NoError(t, err)
			require.Equal(t, 2, plan.ExplicitCount())
			targets := plan.Targets()
			require.Equal(t, "child001", targets[0].UUID)
			require.Equal(t, "parent01", targets[1].UUID)
			if expand {
				require.Len(t, targets, 3)
				require.Equal(t, "grand001", targets[2].UUID)
				require.Equal(t, "other", targets[2].Project)
				require.Equal(t, "child001", targets[2].ParentUUID)
				require.Empty(t, plan.RetainedChildren)
			} else {
				require.Len(t, targets, 2)
				require.Len(t, plan.RetainedChildren, 1)
				require.Equal(t, "grand001", plan.RetainedChildren[0].UUID)
			}
			addDeleteTestSession(t, s, "late0001", "parent01", "root")
			require.NoError(t, s.ApplyDelete(ctx, plan))
			rows, err := s.ListSessions(ctx, ListOptions{IncludeChildren: true})
			require.NoError(t, err)
			var ids []string
			for _, row := range rows {
				ids = append(ids, row.UUID)
			}
			want := []string{"late0001", "unrelated"}
			if !expand {
				want = append(want, "grand001")
			}
			require.ElementsMatch(t, want, ids)
		})
	}
}

func TestDeletePlanDescendantCycles(t *testing.T) {
	s := newTestVault(t)
	for _, pair := range [][2]string{{"cycle001", "cycle002"}, {"cycle002", "cycle001"}, {"self0001", "self0001"}} {
		addDeleteTestSession(t, s, pair[0], pair[1], "project")
	}
	p, err := s.PrepareDelete(t.Context(), []string{"cycle001", "self0001"}, DeleteOptions{WithDescendants: true})
	require.NoError(t, err)
	require.Equal(t, 2, p.ExplicitCount())
	targets := p.Targets()
	require.Len(t, targets, 3)
	require.Equal(t, "cycle002", targets[2].UUID)
	require.Empty(t, p.RetainedChildren)
}

func TestDeletePlanDescendantSnapshot(t *testing.T) {
	s := newTestVault(t)
	ctx := t.Context()
	addDeleteTestSession(t, s, "parent01", "", "p")
	addDeleteTestSession(t, s, "child001", "parent01", "p")
	addDeleteTestSession(t, s, "grand001", "child001", "p")
	other := NewVaultStore(s.dbPath)
	defer other.Close()
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	_, err = s.RenameSession(ctx, "parent01", RenameOptions{Name: "Parent"})
	require.NoError(t, err)
	require.NoError(t, other.Open(ctx))
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	// Commit a child on a second connection while PrepareDelete reads the
	// parent's stored title. The next parent-link lookup must share that read.
	inserted := false
	require.NoError(t, conn.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("during_title_read", func(title string) (string, error) {
			if inserted {
				return title, nil
			}
			rec := sampleRecord("late0001")
			rec.Session.ParentUUID = "child001"
			err := other.InsertSession(ctx, rec)
			inserted = err == nil
			return title, err
		}, false)
	}))
	_, err = conn.ExecContext(ctx, `CREATE TEMP VIEW vault_session_names AS SELECT session_uuid,
		during_title_read(custom_title) AS custom_title, renamed_at_ns, machine_id FROM main.vault_session_names`)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	plan, err := s.PrepareDelete(ctx, []string{"parent01"}, DeleteOptions{WithDescendants: true})
	require.NoError(t, err)
	require.True(t, inserted)
	_, err = db.Exec("DROP VIEW temp.vault_session_names")
	require.NoError(t, err)
	addDeleteTestSession(t, other, "late0002", "grand001", "p")
	require.Len(t, plan.Targets(), 3)
	require.NoError(t, s.ApplyDelete(ctx, plan))
	rows, err := s.ListSessions(ctx, ListOptions{IncludeChildren: true})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.ElementsMatch(t, []string{"late0001", "late0002"}, []string{rows[0].UUID, rows[1].UUID})
}
