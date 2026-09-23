package vault

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionProject_MaintenanceSurvival(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprintf("clear=%t", clear), func(t *testing.T) {
			s := newTestVault(t)
			ctx := t.Context()
			const uuid = "aaaaaaaa-1010-4000-8000-000000000001"
			root := t.TempDir()
			projectDir := filepath.Join(root, "-home-user-proj")
			sidecars := map[string][]byte{"tool-results/result.txt": bytes.Repeat([]byte("preserved sidecar\n"), 100)}
			raw := sampleMainJSONL(t)
			writeSession(t, projectDir, uuid, raw, sidecars)
			require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)
			// Opposite title/project states prove the two tracks stay independent.
			title := applyNameFixture(t, s, uuid, !clear)
			before := snapshotArchivedData(t, s, uuid)
			edited, err := s.setSessionProjectAt(ctx, uuid, ProjectOptions{Name: "~/literal/../capy"}, time.Unix(0, 700), "project-writer")
			require.NoError(t, err)
			if clear {
				edited, err = s.setSessionProjectAt(ctx, uuid, ProjectOptions{Clear: true}, time.Unix(0, 701), "project-writer")
				require.NoError(t, err)
			}
			expectedProject := edited.ProjectOverride
			assert.Equal(t, before, snapshotArchivedData(t, s, uuid), "edits affect only project metadata")

			// A replacement may change imported cwd/title, but not either override.
			raw = bytes.ReplaceAll(raw, []byte("/home/user/proj"), []byte("/home/user/moved"))
			raw = append(raw, jsonlBytes(t,
				userLine("grown", "/home/user/moved", "feature/x", "A later prompt grows the archived transcript"),
				aiTitleLine("Replacement imported title"),
			)...)
			writeSession(t, projectDir, uuid, raw, sidecars)
			require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Updated)
			grown, err := s.GetSession(ctx, uuid)
			require.NoError(t, err)
			files, err := s.GetFiles(ctx, uuid)
			require.NoError(t, err)
			require.Len(t, files, 1)
			require.Equal(t, sidecars[files[0].RelativePath], files[0].RawContent)
			check := func(stage string) {
				t.Helper()
				got, err := s.GetSession(ctx, uuid)
				require.NoError(t, err, stage)
				assert.Equal(t, expectedProject, got.ProjectOverride, stage)
				assertNameFixture(t, s, uuid, "Replacement imported title", title)
				assert.Equal(t, "/home/user/moved", got.ProjectPath, stage)
				assert.Equal(t, "-home-user-proj", got.ClaudeProjectDir, stage)
				wantEffective := "~/literal/../capy"
				if clear {
					wantEffective = "/home/user/moved"
				}
				assert.Equal(t, wantEffective, got.EffectiveProject(), stage)
				assert.Equal(t, raw, got.RawJSONL, stage)
				assert.Equal(t, grown.ContentHash, got.ContentHash, stage)
				assert.Equal(t, grown.SizeBytes, got.SizeBytes, stage)
				gotFiles, err := s.GetFiles(ctx, uuid)
				require.NoError(t, err, stage)
				assert.Equal(t, files, gotFiles, stage)
				found, err := s.HasSessionsInProject(ctx, "", "/home/user/moved")
				require.NoError(t, err, stage)
				assert.True(t, found, stage)
			}
			check("reimport")

			db, err := s.getDB(ctx)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE vault_sessions SET index_version = 0 WHERE uuid = ?`, uuid)
			require.NoError(t, err)
			reindexed, err := Reindex(ctx, s)
			require.NoError(t, err)
			require.Equal(t, 1, reindexed.Reindexed)
			check("reindex")
			hits, err := s.SearchChunks(ctx, SearchOptions{Query: "timeout", Project: grown.EffectiveProject()})
			require.NoError(t, err)
			require.NotEmpty(t, hits)
			assert.Equal(t, uuid, hits[0].SessionUUID)

			// Exercise actual legacy-blob rewriting, not compact's no-op path.
			_, err = db.ExecContext(ctx, `UPDATE vault_sessions SET raw_jsonl = ?, encoding = NULL WHERE uuid = ?`, raw, uuid)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE vault_files SET raw_content = ?, encoding = NULL WHERE session_uuid = ?`, files[0].RawContent, uuid)
			require.NoError(t, err)
			compacted, err := s.Compact(ctx)
			require.NoError(t, err)
			assert.Equal(t, 1, compacted.SessionsRewritten)
			assert.Equal(t, 1, compacted.FilesRewritten)
			assert.True(t, compacted.Vacuumed)
			check("compact")

			path := s.dbPath
			archive := snapshotArchivedData(t, s, uuid)
			require.NoError(t, s.Close())
			const newKey = "project-maintenance-new-vault-key"
			_, err = sqliteutil.Rekey(path, testVaultKey, newKey)
			require.NoError(t, err)
			t.Setenv(vaultKeyEnv, newKey)
			s = NewVaultStore(path)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			check("backup API rekey")
			assert.Equal(t, archive, snapshotArchivedData(t, s, uuid), "rekey preserves all stored archive/index values")

			// Restore the final archive and compare actual files, including sidecars.
			final, err := s.GetSession(ctx, uuid)
			require.NoError(t, err)
			finalFiles, err := s.GetFiles(ctx, uuid)
			require.NoError(t, err)
			restored, err := RestoreSession(uuid, final.RawJSONL, finalFiles, t.TempDir(), nil)
			require.NoError(t, err)
			require.Len(t, restored.Written, 2)
			gotRaw, err := os.ReadFile(filepath.Join(restored.Root, uuid+".jsonl"))
			require.NoError(t, err)
			assert.Equal(t, raw, gotRaw)
			gotFile, err := os.ReadFile(filepath.Join(restored.Root, uuid, files[0].RelativePath))
			require.NoError(t, err)
			assert.Equal(t, files[0].RawContent, gotFile)
		})
	}
}

func TestSessionProject_EditDeleteRaceLeavesNoOrphan(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprintf("clear=%t", clear), func(t *testing.T) {
			s := newTestVault(t)
			ctx := t.Context()
			for i := range 20 {
				uuid := fmt.Sprintf("%08d-1010-4000-8000-000000000002", i)
				require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))
				applyNameFixture(t, s, uuid, false)
				opts := ProjectOptions{Name: "racing project"}
				if clear {
					opts = ProjectOptions{Clear: true}
				}
				start := make(chan struct{})
				var editErr, deleteErr error
				var deleted bool
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					_, editErr = s.SetSessionProject(ctx, uuid, opts)
				}()
				go func() {
					defer wg.Done()
					<-start
					deleted, deleteErr = s.DeleteSession(ctx, uuid)
				}()
				close(start)
				wg.Wait()
				require.NoError(t, deleteErr)
				assert.True(t, deleted)
				if editErr != nil {
					require.ErrorIs(t, editErr, ErrSessionNotFound)
				}
				db, err := s.getDB(ctx)
				require.NoError(t, err)
				var remaining int
				require.NoError(t, db.QueryRowContext(ctx, `SELECT
					(SELECT COUNT(*) FROM vault_session_projects WHERE session_uuid = ?) +
					(SELECT COUNT(*) FROM vault_session_names WHERE session_uuid = ?)`, uuid, uuid).Scan(&remaining))
				assert.Zero(t, remaining, "neither metadata track may outlive the deleted session")
			}
		})
	}
}
