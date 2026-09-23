package vault

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const projectMergeKey = "project-merge-test-key"
const projectMergeUUID = "abcd1234-0000-0000-0000-000000000001"

// Merge's established source opener enables WAL and checkpoints it. Prepare
// legacy fixtures in that mode before comparing physical bytes, so the check
// detects content/schema writes rather than the existing journal-mode change.
func prepareProjectSourceSnapshot(t *testing.T, path string) []byte {
	t.Helper()
	mutateSource(t, path, projectMergeKey, func(t *testing.T, db *sql.DB) {
		_, err := db.Exec(`PRAGMA journal_mode=WAL`)
		require.NoError(t, err)
	})
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func projectState(value *string, ns int64, machine string) *SessionProject {
	return &SessionProject{CustomProject: value, UpdatedAtNS: ns, MachineID: machine}
}

func buildProjectVault(t *testing.T, path string, rec *SessionRecord, name *SessionName, project *SessionProject) {
	t.Helper()
	t.Setenv(vaultKeyEnv, projectMergeKey)
	s := NewVaultStore(path)
	require.NoError(t, s.InsertSession(t.Context(), rec))
	if name != nil {
		opts := RenameOptions{Clear: name.CustomTitle == nil}
		if name.CustomTitle != nil {
			opts.Name = *name.CustomTitle
		}
		_, err := s.renameSessionAt(t.Context(), rec.Session.UUID, opts, time.Unix(0, name.RenamedAtNS), name.MachineID)
		require.NoError(t, err)
	}
	if project != nil {
		opts := ProjectOptions{Clear: project.CustomProject == nil}
		if project.CustomProject != nil {
			opts.Name = *project.CustomProject
		}
		_, err := s.setSessionProjectAt(t.Context(), rec.Session.UUID, opts, time.Unix(0, project.UpdatedAtNS), project.MachineID)
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
}

func TestSessionProject_MergeOrder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		src, dest *SessionProject
		wins      bool
	}{
		{"absent source", nil, projectState(namePtr("dest"), 1, "m"), false},
		{"absent destination", projectState(namePtr("src"), -1, "m"), nil, true},
		{"clear into absent", projectState(nil, -1, "m"), nil, true},
		{"newer", projectState(namePtr("src"), 2, "a"), projectState(namePtr("dest"), 1, "z"), true},
		{"older", projectState(namePtr("src"), 1, "z"), projectState(namePtr("dest"), 2, "a"), false},
		{"greater machine", projectState(namePtr("src"), 1, "b"), projectState(namePtr("dest"), 1, "a"), true},
		{"smaller machine", projectState(namePtr("src"), 1, "a"), projectState(namePtr("dest"), 1, "b"), false},
		{"duplicate machine greater value", projectState(namePtr("z"), 1, "m"), projectState(namePtr("a"), 1, "m"), true},
		{"duplicate machine smaller value", projectState(namePtr("a"), 1, "m"), projectState(namePtr("z"), 1, "m"), false},
		{"equal value", projectState(namePtr("same"), 1, "m"), projectState(namePtr("same"), 1, "m"), false},
		{"value beats null", projectState(namePtr("src"), 1, "m"), projectState(nil, 1, "m"), true},
		{"null loses tie", projectState(nil, 1, "m"), projectState(namePtr("dest"), 1, "m"), false},
		{"equal nulls", projectState(nil, 1, "m"), projectState(nil, 1, "m"), false},
		{"newer clear", projectState(nil, 2, "m"), projectState(namePtr("dest"), 1, "m"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.src != nil {
				assert.Equal(t, tc.wins, sessionProjectSupersedes(*tc.src, tc.dest))
			}
			dir := t.TempDir()
			srcPath, destPath := filepath.Join(dir, "src.db"), filepath.Join(dir, "dest.db")
			rec := mergeRecord(t, projectMergeUUID, "token", 2, 1000, "same", "importer", "/raw")
			buildProjectVault(t, srcPath, rec, nil, tc.src)
			buildProjectVault(t, destPath, rec, nil, tc.dest)
			dest := openDest(t, destPath, projectMergeKey)
			res, err := MergeFrom(t.Context(), dest, srcPath, projectMergeKey, "test", MergeOptions{})
			require.NoError(t, err)
			require.Zero(t, res.Errors)
			want := tc.dest
			if tc.wins {
				want = tc.src
				assert.Equal(t, 1, res.Updated)
			} else {
				assert.Equal(t, 1, res.Skipped)
			}
			sess, err := dest.GetSession(t.Context(), projectMergeUUID)
			require.NoError(t, err)
			assert.Equal(t, want, sess.ProjectOverride, "winner tuple is stored verbatim")
			again, err := MergeFrom(t.Context(), dest, srcPath, projectMergeKey, "test", MergeOptions{})
			require.NoError(t, err)
			assert.Equal(t, 1, again.Skipped)
			assert.Zero(t, again.Errors)
		})
	}
}

func TestMergeFrom_ProjectStateMatrix(t *testing.T) {
	newName := &SessionName{CustomTitle: namePtr("new title"), RenamedAtNS: 2000, MachineID: "name-writer"}
	oldName := &SessionName{CustomTitle: namePtr("old title"), RenamedAtNS: 1000, MachineID: "name-writer"}
	newProject := projectState(namePtr("new project"), 2000, "project-writer")
	oldProject := projectState(namePtr("old project"), 1000, "project-writer")
	clear := projectState(nil, 3000, "clear-writer")
	for _, branch := range []string{"new", "replacement", "same hash", "smaller", "empty existing", "empty missing"} {
		for _, tc := range []struct {
			name                                 string
			srcName, destName, wantName          *SessionName
			srcProject, destProject, wantProject *SessionProject
		}{
			{"source project destination title", oldName, newName, newName, newProject, oldProject, newProject},
			{"source title destination project", newName, oldName, newName, oldProject, newProject, newProject},
			{"both source fields win", newName, oldName, newName, newProject, oldProject, newProject},
			{"clear with older title", oldName, newName, newName, clear, newProject, clear},
			{"absent source project", newName, oldName, newName, nil, newProject, newProject},
		} {
			t.Run(branch+"/"+tc.name, func(t *testing.T) {
				ctx := t.Context()
				dir := t.TempDir()
				srcPath, destPath := filepath.Join(dir, "src.db"), filepath.Join(dir, "dest.db")
				size, count, hash := int64(1000), 2, "source-hash"
				if branch == "same hash" {
					hash = "dest-hash"
				}
				if branch == "smaller" {
					size = 100
				}
				if branch == "empty existing" || branch == "empty missing" {
					count = 0
				}
				srcRec := mergeRecord(t, projectMergeUUID, "source", count, size, hash, "src", "/source/raw")
				buildProjectVault(t, srcPath, srcRec, tc.srcName, tc.srcProject)
				existing := branch != "new" && branch != "empty missing"
				if existing {
					buildProjectVault(t, destPath, mergeRecord(t, projectMergeUUID, "destination", 2, 500, "dest-hash", "dest", "/destination/raw"), tc.destName, tc.destProject)
				}
				dest := openDest(t, destPath, projectMergeKey)
				before, err := dest.ListSessions(ctx, ListOptions{})
				require.NoError(t, err)
				var snapshot archivedDataSnapshot
				if existing {
					snapshot = snapshotArchivedData(t, dest, projectMergeUUID)
				}
				dry, err := MergeFrom(ctx, dest, srcPath, projectMergeKey, "test", MergeOptions{DryRun: true})
				require.NoError(t, err)
				require.Zero(t, dry.Errors)
				afterDry, err := dest.ListSessions(ctx, ListOptions{})
				require.NoError(t, err)
				assert.Equal(t, before, afterDry, "dry-run leaves metadata untouched")
				if existing {
					assert.Equal(t, snapshot, snapshotArchivedData(t, dest, projectMergeUUID))
				}
				res, err := MergeFrom(ctx, dest, srcPath, projectMergeKey, "test", MergeOptions{})
				require.NoError(t, err)
				require.Zero(t, res.Errors)
				assert.Equal(t, dry, res, "dry-run reports the same prospective destination")
				require.Len(t, res.Sessions, 1)
				if branch == "empty missing" {
					assert.Equal(t, 1, res.Excluded)
					_, err := dest.GetSession(ctx, projectMergeUUID)
					require.ErrorIs(t, err, ErrSessionNotFound)
					db, err := dest.getDB(ctx)
					require.NoError(t, err)
					var rows int
					require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM vault_session_projects`).Scan(&rows))
					assert.Zero(t, rows)
					return
				}
				wantName, wantProject, raw := tc.wantName, tc.wantProject, "/destination/raw"
				if branch == "new" {
					wantName, wantProject = tc.srcName, tc.srcProject
					assert.Equal(t, 1, res.Imported)
				} else {
					assert.Equal(t, 1, res.Updated, "both fields count as one session update")
				}
				if branch == "new" || branch == "replacement" {
					raw = "/source/raw"
				}
				sess, err := dest.GetSession(ctx, projectMergeUUID)
				require.NoError(t, err)
				assert.Equal(t, wantName, sess.Name)
				assert.Equal(t, wantProject, sess.ProjectOverride)
				assert.Equal(t, raw, sess.ProjectPath)
				entry := res.Sessions[0]
				assert.Equal(t, raw, entry.ProjectPath)
				assert.Equal(t, sess.EffectiveProject(), entry.Project)
				assert.Equal(t, sess.EffectiveTitle(), entry.Title)
				if wantProject != nil {
					assert.Equal(t, wantProject.CustomProject, entry.CustomProject)
				}
				if existing && branch != "replacement" {
					assert.Equal(t, snapshot, snapshotArchivedData(t, dest, projectMergeUUID), "metadata-only merge preserves archive and indexes")
				}
			})
		}
	}
}

func TestMergeFrom_ProjectExcludedDestinationReport(t *testing.T) {
	for _, sourceState := range []string{"older", "absent"} {
		t.Run(sourceState, func(t *testing.T) {
			ctx := t.Context()
			srcPath, destPath := filepath.Join(t.TempDir(), "src.db"), filepath.Join(t.TempDir(), "dest.db")
			var srcName *SessionName
			var srcProject *SessionProject
			if sourceState == "older" {
				srcName = &SessionName{CustomTitle: namePtr("old title"), RenamedAtNS: 1, MachineID: "src"}
				srcProject = projectState(namePtr("old project"), 1, "src")
			}
			buildProjectVault(t, srcPath, mergeRecord(t, projectMergeUUID, "empty", 0, 100, "source", "src", "/source/raw"), srcName, srcProject)
			buildProjectVault(t, destPath, mergeRecord(t, projectMergeUUID, "dest", 2, 1000, "dest", "dest", "/destination/raw"),
				&SessionName{CustomTitle: namePtr("destination title"), RenamedAtNS: 2, MachineID: "dest"},
				projectState(namePtr("destination project"), 2, "dest"))
			dest := openDest(t, destPath, projectMergeKey)
			before, err := dest.GetSession(ctx, projectMergeUUID)
			require.NoError(t, err)
			snapshot := snapshotArchivedData(t, dest, projectMergeUUID)
			for _, dryRun := range []bool{true, false} {
				res, err := MergeFrom(ctx, dest, srcPath, projectMergeKey, "test", MergeOptions{DryRun: dryRun})
				require.NoError(t, err)
				require.Zero(t, res.Errors)
				assert.Equal(t, 1, res.Excluded)
				assert.Zero(t, res.Updated)
				require.Len(t, res.Sessions, 1)
				entry := res.Sessions[0]
				assert.Equal(t, "no messages", entry.Reason)
				assert.Equal(t, "destination title", entry.Title)
				assert.Equal(t, "destination project", entry.Project)
				assert.Equal(t, namePtr("destination project"), entry.CustomProject)
				assert.Equal(t, "/destination/raw", entry.ProjectPath)
				after, err := dest.GetSession(ctx, projectMergeUUID)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				assert.Equal(t, snapshot, snapshotArchivedData(t, dest, projectMergeUUID))
			}
		})
	}
}

func TestSessionProject_MergeMatcherParity(t *testing.T) {
	s := newTestVault(t)
	db, err := s.getDB(t.Context())
	require.NoError(t, err)
	for _, value := range []string{`MiXeD CAFÉ %_\'" *`, "mixed café", "ordinary", "Αλφα", "東京", ""} {
		for _, query := range []string{"mixed", "CAFÉ", "CAFé", "%", "_", `\`, "'", `"`, "*", "αλ", "東京", "", "' OR 1=1 --"} {
			t.Run(value+"/"+query, func(t *testing.T) {
				var want bool
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT ? LIKE ? ESCAPE '\'`, value, likeContains(query)).Scan(&want))
				assert.Equal(t, want, containsProjectASCII(value, query))
			})
		}
	}
}

func TestMergeFrom_ProjectSelectionBeforeBlobs(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "current"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			srcPath := filepath.Join(t.TempDir(), "source.db")
			if legacy {
				buildV1Source(t, srcPath, projectMergeKey, projectMergeUUID, "source")
			} else {
				buildProjectVault(t, srcPath, mergeRecord(t, projectMergeUUID, "source", 2, 1000, "hash", "m", "/replaced/path"), nil, projectState(namePtr("CAPY %_\\'"), 1, "m"))
			}
			// A malformed blob would fail decoding if a rejected row were loaded.
			mutateSource(t, srcPath, projectMergeKey, func(t *testing.T, db *sql.DB) {
				var err error
				if legacy {
					_, err = db.Exec(`UPDATE vault_sessions SET raw_jsonl = ?`, []byte("invalid json"))
				} else {
					_, err = db.Exec(`UPDATE vault_sessions SET raw_jsonl = ?, encoding = 'zstd'`, []byte("invalid compressed bytes"))
				}
				require.NoError(t, err)
			})
			before := prepareProjectSourceSnapshot(t, srcPath)
			dest := openDest(t, filepath.Join(t.TempDir(), "dest.db"), projectMergeKey)
			queries := []string{"missing", "-src-proj", "/replaced/path", "capy X"}
			if legacy {
				queries = []string{"missing", "-v1-proj"}
			}
			for _, query := range queries {
				res, err := MergeFrom(t.Context(), dest, srcPath, projectMergeKey, "test", MergeOptions{Project: query})
				require.NoError(t, err)
				assert.Empty(t, res.Sessions, "rejected metadata must not load malformed blobs: %q", query)
			}
			after, err := os.ReadFile(srcPath)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(before, after), "source contents and schema remain unchanged")
		})
	}
}

func TestMergeFrom_ProjectForeignStateAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		custom       *string
		filter, want string
		legacy       bool
	}{
		{"empty is clear", namePtr(""), "/source/raw", "/source/raw", false},
		{"whitespace is clear", namePtr(" \t\u2003 "), "/source/raw", "/source/raw", false},
		{"verbatim nonempty", namePtr("  MiXeD %_\\'  "), "mixed %_\\'", "  MiXeD %_\\'  ", false},
		{"legacy imported path", nil, "/v1/proj", "destination label", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			srcPath := filepath.Join(t.TempDir(), "src.db")
			if tc.legacy {
				buildV1Source(t, srcPath, projectMergeKey, projectMergeUUID, "legacy")
			} else {
				buildProjectVault(t, srcPath, mergeRecord(t, projectMergeUUID, "src", 2, 1000, "source", "m", "/source/raw"), nil, nil)
				mutateSource(t, srcPath, projectMergeKey, func(t *testing.T, db *sql.DB) {
					// Model a foreign writer without our non-empty CHECK. Local
					// validation/schema must keep rejecting this unsupported state.
					_, err := db.Exec(`DROP TABLE vault_session_projects;
						CREATE TABLE vault_session_projects (
							session_uuid TEXT PRIMARY KEY REFERENCES vault_sessions(uuid) ON DELETE CASCADE,
							custom_project TEXT, updated_at_ns INTEGER NOT NULL, machine_id TEXT NOT NULL
						)`)
					require.NoError(t, err)
					_, err = db.Exec(`INSERT INTO vault_session_projects VALUES (?, ?, ?, ?)`, projectMergeUUID, pointerValue(tc.custom), 9000, "foreign")
					require.NoError(t, err)
				})
			}
			before := prepareProjectSourceSnapshot(t, srcPath)
			destPath := filepath.Join(t.TempDir(), "dest.db")
			buildProjectVault(t, destPath, mergeRecord(t, projectMergeUUID, "dest", 2, 1, "dest", "m", "/dest/raw"), nil, projectState(namePtr("destination label"), 1, "m"))
			dest := openDest(t, destPath, projectMergeKey)
			res, err := MergeFrom(ctx, dest, srcPath, projectMergeKey, "test", MergeOptions{Project: tc.filter})
			require.NoError(t, err)
			require.Zero(t, res.Errors)
			require.Equal(t, 1, res.Updated)
			assert.Equal(t, tc.want, res.Sessions[0].Project)
			sess, err := dest.GetSession(ctx, projectMergeUUID)
			require.NoError(t, err)
			require.NotNil(t, sess.ProjectOverride)
			assert.Equal(t, tc.want, sess.EffectiveProject())
			if !tc.legacy {
				assert.Equal(t, int64(9000), sess.ProjectOverride.UpdatedAtNS)
				assert.Equal(t, "foreign", sess.ProjectOverride.MachineID)
			}
			after, err := os.ReadFile(srcPath)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(before, after), "source contents and schema remain unchanged")
		})
	}
}

func TestMergeFrom_ProjectMetadataAtomicity(t *testing.T) {
	for _, branch := range []string{"new", "same hash", "replacement"} {
		t.Run(branch, func(t *testing.T) {
			ctx := t.Context()
			srcPath, destPath := filepath.Join(t.TempDir(), "src.db"), filepath.Join(t.TempDir(), "dest.db")
			hash := "newhash"
			if branch == "same hash" {
				hash = "oldhash"
			}
			buildProjectVault(t, srcPath, mergeRecord(t, projectMergeUUID, "source", 2, 1000, hash, "m", "/src"),
				&SessionName{CustomTitle: namePtr("source title"), RenamedAtNS: 2, MachineID: "m"}, projectState(namePtr("source project"), 2, "m"))
			if branch != "new" {
				buildProjectVault(t, destPath, mergeRecord(t, projectMergeUUID, "dest", 2, 500, "oldhash", "m", "/dest"),
					&SessionName{CustomTitle: namePtr("dest title"), RenamedAtNS: 1, MachineID: "m"}, projectState(namePtr("dest project"), 1, "m"))
			}
			dest := openDest(t, destPath, projectMergeKey)
			before, err := dest.ListSessions(ctx, ListOptions{})
			require.NoError(t, err)
			db, err := dest.getDB(ctx)
			require.NoError(t, err)
			// Fault injection only: reject the second metadata field after the
			// title has been written, proving the transaction rolls both back.
			_, err = db.ExecContext(ctx, `CREATE TRIGGER reject_project BEFORE INSERT ON vault_session_projects BEGIN SELECT RAISE(ABORT, 'project write rejected'); END`)
			require.NoError(t, err)
			res, err := MergeFrom(ctx, dest, srcPath, projectMergeKey, "test", MergeOptions{})
			require.NoError(t, err)
			require.Equal(t, 1, res.Errors)
			require.ErrorContains(t, res.Sessions[0].Err, "project write rejected")
			after, err := dest.ListSessions(ctx, ListOptions{})
			require.NoError(t, err)
			assert.Equal(t, before, after, "parent and both metadata fields commit atomically")
			if branch == "new" {
				var count int
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM vault_session_names`).Scan(&count))
				assert.Zero(t, count)
			}
		})
	}
}

func TestMergeFrom_ProjectConvergesAndConcurrentEdits(t *testing.T) {
	for _, mode := range []string{"reverse merge", "local project", "local title and project"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			srcPath, destPath := filepath.Join(t.TempDir(), "src.db"), filepath.Join(t.TempDir(), "dest.db")
			rec := mergeRecord(t, projectMergeUUID, "token", 2, 1000, "same", "m", "/raw")
			name := &SessionName{CustomTitle: namePtr("source title"), RenamedAtNS: 2000, MachineID: "src"}
			project := projectState(namePtr("destination project"), 3000, "dest")
			buildProjectVault(t, srcPath, rec, name, projectState(namePtr("old project"), 1000, "src"))
			var initialProject *SessionProject
			if mode == "reverse merge" {
				initialProject = project
			}
			buildProjectVault(t, destPath, rec, nil, initialProject)
			dest := openDest(t, destPath, projectMergeKey)
			if mode == "reverse merge" {
				res, err := MergeFrom(ctx, dest, srcPath, projectMergeKey, "test", MergeOptions{})
				require.NoError(t, err)
				require.Zero(t, res.Errors)
				require.NoError(t, dest.Close())
				src := openDest(t, srcPath, projectMergeKey)
				res, err = MergeFrom(ctx, src, destPath, projectMergeKey, "test", MergeOptions{})
				require.NoError(t, err)
				require.Zero(t, res.Errors)
				got, err := src.GetSession(ctx, projectMergeUUID)
				require.NoError(t, err)
				assert.Equal(t, name, got.Name)
				assert.Equal(t, project, got.ProjectOverride)
				return
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			var mergeErr, projectErr, titleErr error
			var res ImportResult
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				res, mergeErr = MergeFrom(ctx, dest, srcPath, projectMergeKey, "test", MergeOptions{})
			}()
			go func() {
				defer wg.Done()
				<-start
				_, projectErr = dest.setSessionProjectAt(ctx, projectMergeUUID, ProjectOptions{Name: "local project"}, time.Unix(0, 5000), "local")
			}()
			if mode == "local title and project" {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, titleErr = dest.renameSessionAt(ctx, projectMergeUUID, RenameOptions{Name: "local title"}, time.Unix(0, 6000), "local")
				}()
			}
			close(start)
			wg.Wait()
			require.NoError(t, mergeErr)
			require.Zero(t, res.Errors, "per-session errors must not be hidden by MergeFrom's setup-only error")
			require.NoError(t, projectErr)
			require.NoError(t, titleErr)
			got, err := dest.GetSession(ctx, projectMergeUUID)
			require.NoError(t, err)
			assert.Equal(t, projectState(namePtr("local project"), 5000, "local"), got.ProjectOverride)
			if mode == "local title and project" {
				name = &SessionName{CustomTitle: namePtr("local title"), RenamedAtNS: 6000, MachineID: "local"}
			}
			assert.Equal(t, name, got.Name)
		})
	}
}
