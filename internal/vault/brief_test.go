package vault

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// briefRecord builds a session whose main-transcript FTS rows are rows. Each row
// is {role, turn, message, line, text}; a sidecar row sets SubagentID itself.
func briefRecord(uuid, project string, rows ...FTSRow) *SessionRecord {
	rec := sampleRecord(uuid)
	rec.Session.ProjectPath = project
	rec.Session.IndexVersion = currentIndexVersion
	rec.Files = nil
	rec.FTS = nil
	for _, r := range rows {
		r.SessionUUID = uuid
		rec.FTS = append(rec.FTS, r)
	}
	return rec
}

func briefRow(role string, turn, message, line int, text string) FTSRow {
	return FTSRow{Role: role, TurnIndex: turn, MessageIndex: message, LineIndex: line, ContentText: text}
}

func fileDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return sha256.Sum256(b)
}

func TestReadBrief_SelectsLatestExchange(t *testing.T) {
	const uuid = "11111111-aaaa-bbbb-cccc-000000000001"
	sidecar := briefRow(roleUser, 9, 0, 40, "sidecar request")
	sidecar.SubagentID = "agent-1"
	sidecarReply := briefRow(roleAssistant, 9, 1, 41, "sidecar reply")
	sidecarReply.SubagentID = "agent-1"

	tests := []struct {
		name          string
		rows          []FTSRow
		wantOpening   string
		wantLatest    string
		wantSame      bool
		wantAssistant string // "" == none
		noUser        bool
	}{
		{
			name: "latest exchange with distinct opening",
			rows: []FTSRow{
				briefRow(roleUser, 0, 0, 0, "open"),
				briefRow(roleAssistant, 0, 1, 1, "first reply"),
				briefRow(roleUser, 1, 0, 4, "latest"),
				briefRow(roleAssistant, 1, 1, 5, "reply a"),
				briefRow(roleTool, 1, 2, 6, "tool output"),
				briefRow(roleAssistant, 1, 3, 7, "reply b"),
				briefRow(roleSystem, 1, 4, 8, "system note"),
			},
			wantOpening: "open", wantLatest: "latest", wantAssistant: "reply b",
		},
		{
			name:        "single user row is opening and latest",
			rows:        []FTSRow{briefRow(roleUser, 0, 0, 0, "only"), briefRow(roleAssistant, 0, 1, 1, "answer")},
			wantOpening: "only", wantLatest: "only", wantSame: true, wantAssistant: "answer",
		},
		{
			name: "pending reply clears the previous assistant",
			rows: []FTSRow{
				briefRow(roleUser, 0, 0, 0, "open"),
				briefRow(roleAssistant, 0, 1, 1, "old reply"),
				briefRow(roleUser, 1, 0, 3, "still waiting"),
				briefRow(roleTool, 1, 1, 4, "tool row is not an assistant"),
			},
			wantOpening: "open", wantLatest: "still waiting",
		},
		{
			name:        "tool-only assistant qualifies",
			rows:        []FTSRow{briefRow(roleUser, 0, 0, 0, "run it"), briefRow(roleAssistant, 0, 1, 1, "Bash: go test")},
			wantOpening: "run it", wantLatest: "run it", wantSame: true, wantAssistant: "Bash: go test",
		},
		{
			name:   "no indexed user",
			rows:   []FTSRow{briefRow(roleAssistant, 0, 0, 0, "assistant-only"), briefRow(roleSystem, 0, 1, 1, "sys")},
			noUser: true,
		},
		{
			name: "assistant before any user is ignored",
			rows: []FTSRow{
				briefRow(roleAssistant, 0, 0, 0, "preamble"),
				briefRow(roleUser, 1, 0, 2, "ask"),
			},
			wantOpening: "ask", wantLatest: "ask", wantSame: true,
		},
		{
			name: "positions order numerically, not as text",
			rows: []FTSRow{
				briefRow(roleUser, 10, 0, 100, "turn ten"),
				briefRow(roleUser, 9, 0, 90, "turn nine"),
				briefRow(roleAssistant, 10, 1, 101, "ten reply"),
				briefRow(roleUser, 2, 0, 20, "turn two"),
			},
			wantOpening: "turn two", wantLatest: "turn ten", wantAssistant: "ten reply",
		},
		{
			name: "sidecar rows with overlapping lines are ignored",
			rows: []FTSRow{
				briefRow(roleUser, 0, 0, 0, "main ask"),
				briefRow(roleAssistant, 0, 1, 1, "main reply"),
				sidecar, sidecarReply,
			},
			wantOpening: "main ask", wantLatest: "main ask", wantSame: true, wantAssistant: "main reply",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestVault(t)
			require.NoError(t, s.InsertSession(context.Background(), briefRecord(uuid, "/home/u/p", tt.rows...)))

			b, err := s.ReadBrief(context.Background(), uuid[:8], BriefScope{})
			require.NoError(t, err)
			assert.Equal(t, uuid, b.ID.Value)
			assert.Equal(t, PlatformClaudeCode, b.Platform)
			if tt.noUser {
				assert.Nil(t, b.Opening)
				assert.Nil(t, b.Latest)
				assert.Nil(t, b.Assistant)
				return
			}
			require.NotNil(t, b.Opening)
			assert.Equal(t, tt.wantOpening, b.Opening.Text.Value)
			assert.Equal(t, tt.wantLatest, b.Latest.Text.Value)
			assert.Equal(t, tt.wantSame, b.LatestIsOpening)
			if tt.wantAssistant == "" {
				assert.Nil(t, b.Assistant)
			} else {
				require.NotNil(t, b.Assistant)
				assert.Equal(t, roleAssistant, b.Assistant.Role)
				assert.Equal(t, tt.wantAssistant, b.Assistant.Text.Value)
			}
		})
	}
}

func TestReadBrief_CitesMessageStartLines(t *testing.T) {
	s := newTestVault(t)
	const uuid = "22222222-aaaa-bbbb-cccc-000000000002"
	require.NoError(t, s.InsertSession(context.Background(), briefRecord(uuid, "/p",
		briefRow(roleUser, 0, 0, 1, "Check the release."),
		briefRow(roleAssistant, 0, 1, 3, "On it."),
		briefRow(roleUser, 1, 0, 18, "Which checks are still running?"),
	)))
	b, err := s.ReadBrief(context.Background(), uuid, BriefScope{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), b.Opening.Line)
	assert.Equal(t, int64(18), b.Latest.Line)
	assert.Equal(t, roleUser, b.Latest.Role)
}

func TestReadBrief_EffectiveMetadataAndStaleIndex(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	const uuid = "33333333-aaaa-bbbb-cccc-000000000003"
	rec := briefRecord(uuid, "/home/u/imported", briefRow(roleUser, 0, 0, 0, "hi"))
	rec.Session.IndexVersion = 3
	require.NoError(t, s.InsertSession(ctx, rec))

	b, err := s.ReadBrief(ctx, uuid[:8], BriefScope{})
	require.NoError(t, err)
	assert.Equal(t, "Sample session", b.Title.Value)
	assert.Equal(t, "/home/u/imported", b.Project.Value)
	assert.True(t, b.StaleIndex(), "an accepted older index version is served with a hint")
	assert.Equal(t, "hi", b.Opening.Text.Value)

	_, err = s.RenameSession(ctx, uuid[:8], RenameOptions{Name: "Renamed"})
	require.NoError(t, err)
	_, err = s.SetSessionProject(ctx, uuid[:8], ProjectOptions{Name: "billing"})
	require.NoError(t, err)
	b, err = s.ReadBrief(ctx, uuid[:8], BriefScope{})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", b.Title.Value)
	assert.Equal(t, "billing", b.Project.Value)
}

func TestReadBrief_LookupScopeAndSelectors(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	for _, rec := range []*SessionRecord{
		briefRecord("aaaaaaaa-0001-0000-0000-000000000000", "/work/alpha", briefRow(roleUser, 0, 0, 0, "a1")),
		briefRecord("aaaaaaaa-0002-0000-0000-000000000000", "/work/beta", briefRow(roleUser, 0, 0, 0, "b1")),
		briefRecord("aaaaaaaa-0003-0000-0000-000000000000", "/work/beta", briefRow(roleUser, 0, 0, 0, "b2")),
	} {
		require.NoError(t, s.InsertSession(ctx, rec))
	}
	_, err := s.SetSessionProject(ctx, "aaaaaaaa-0001", ProjectOptions{Name: "Billing"})
	require.NoError(t, err)

	tests := []struct {
		name    string
		session string
		scope   BriefScope
		want    string // expected opening text; "" when an error is expected
		wantErr error
		errText string
	}{
		{name: "vault-wide prefix is ambiguous", session: "aaaaaaaa", wantErr: ErrBriefAmbiguous},
		{name: "scope resolves a shared prefix", session: "aaaaaaaa", scope: BriefScope{ProjectPath: "alpha"}, want: "a1"},
		{name: "effective label scope, ASCII case-insensitive", session: "aaaaaaaa", scope: BriefScope{Project: "billing"}, want: "a1"},
		{name: "ambiguous inside scope", session: "aaaaaaaa", scope: BriefScope{ProjectPath: "beta"}, wantErr: ErrBriefAmbiguous},
		{name: "out-of-scope match is a miss", session: "aaaaaaaa-0002", scope: BriefScope{ProjectPath: "alpha"}, wantErr: ErrSessionNotFound},
		{name: "wildcards are literal", session: "aaaaaaa%", wantErr: ErrSessionNotFound},
		{name: "underscore is literal", session: "aaaaaaaa_0002", wantErr: ErrSessionNotFound},
		{name: "short prefix rejected", session: "aaaa", errText: "at least 8"},
		{name: "NUL session rejected", session: "aaaaaaaa\x00", errText: "NUL"},
		{name: "NUL project rejected", session: "aaaaaaaa", scope: BriefScope{Project: "a\x00"}, errText: "NUL"},
		{name: "mixed scopes rejected", session: "aaaaaaaa", scope: BriefScope{Project: "x", ProjectPath: "y"}, errText: "mutually exclusive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := s.ReadBrief(ctx, tt.session, tt.scope)
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
				assert.NotContains(t, err.Error(), "0002", "errors must not reveal candidates")
				assert.Nil(t, b)
			case tt.errText != "":
				require.ErrorContains(t, err, tt.errText)
				assert.Nil(t, b)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.want, b.Opening.Text.Value)
			}
		})
	}
}

// commitDuringBrief makes the next ReadBrief run write after it has resolved the
// session and before it reads metadata, while the reader's snapshot is open. The returned
// function reports whether write ran and its error.
func commitDuringBrief(t *testing.T, write func() error) func() (bool, error) {
	t.Helper()
	var ran bool
	var err error
	briefAfterResolve = func() {
		briefAfterResolve = func() {}
		ran, err = true, write()
	}
	t.Cleanup(func() { briefAfterResolve = func() {} })
	return func() (bool, error) { return ran, err }
}

// A brief reads one committed snapshot: a writer commit landing after it has
// resolved the session changes neither its metadata nor its excerpts, and a
// fresh call sees the new state.
func TestReadBrief_OneSnapshotAcrossWriterCommit(t *testing.T) {
	const uuid = "eeeeeeee-aaaa-bbbb-cccc-00000000000e"
	original := func() *SessionRecord {
		return briefRecord(uuid, "/work/alpha",
			briefRow(roleUser, 0, 0, 0, "old open"),
			briefRow(roleAssistant, 0, 1, 1, "old first reply"),
			briefRow(roleUser, 1, 0, 4, "old latest"),
			briefRow(roleAssistant, 1, 1, 5, "old reply"),
		)
	}
	assertOriginal := func(t *testing.T, b *Brief) {
		t.Helper()
		assert.Equal(t, "Sample session", b.Title.Value)
		assert.Equal(t, "/work/alpha", b.Project.Value)
		require.NotNil(t, b.Opening)
		assert.Equal(t, "old open", b.Opening.Text.Value)
		assert.Equal(t, "old latest", b.Latest.Text.Value)
		assert.Equal(t, int64(4), b.Latest.Line)
		require.NotNil(t, b.Assistant)
		assert.Equal(t, "old reply", b.Assistant.Text.Value)
	}

	tests := []struct {
		name  string
		write func(ctx context.Context, s *VaultStore) error
		scope BriefScope // used by both the interrupted and the fresh call
		fresh func(t *testing.T, b *Brief, err error)
	}{
		{
			name: "divergent replacement",
			write: func(ctx context.Context, s *VaultStore) error {
				rec := briefRecord(uuid, "/work/alpha",
					briefRow(roleUser, 0, 0, 0, "new open"),
					briefRow(roleUser, 1, 0, 2, "new latest"),
					briefRow(roleAssistant, 1, 1, 3, "new reply"),
				)
				rec.Session.Title = "Replaced"
				rec.Session.ContentHash = "replaced"
				return s.ReplaceSession(ctx, rec)
			},
			fresh: func(t *testing.T, b *Brief, err error) {
				require.NoError(t, err)
				assert.Equal(t, "Replaced", b.Title.Value)
				assert.Equal(t, "new open", b.Opening.Text.Value)
				assert.Equal(t, "new latest", b.Latest.Text.Value)
				assert.Equal(t, int64(2), b.Latest.Line)
				assert.Equal(t, "new reply", b.Assistant.Text.Value)
			},
		},
		{
			name: "rename",
			write: func(ctx context.Context, s *VaultStore) error {
				_, err := s.RenameSession(ctx, uuid, RenameOptions{Name: "Renamed"})
				return err
			},
			fresh: func(t *testing.T, b *Brief, err error) {
				require.NoError(t, err)
				assert.Equal(t, "Renamed", b.Title.Value)
				assert.Equal(t, "old latest", b.Latest.Text.Value)
			},
		},
		{
			name: "reassignment out of the requested scope",
			write: func(ctx context.Context, s *VaultStore) error {
				_, err := s.SetSessionProject(ctx, uuid, ProjectOptions{Name: "billing"})
				return err
			},
			scope: BriefScope{Project: "alpha"},
			fresh: func(t *testing.T, b *Brief, err error) {
				require.ErrorIs(t, err, ErrSessionNotFound)
				assert.Nil(t, b)
			},
		},
		{
			name: "remembered deletion",
			write: func(ctx context.Context, s *VaultStore) error {
				deleted, err := s.DeleteSession(ctx, uuid, true)
				if err == nil && !deleted {
					err = errors.New("nothing deleted")
				}
				return err
			},
			fresh: func(t *testing.T, b *Brief, err error) {
				require.ErrorIs(t, err, ErrSessionNotFound)
				assert.Nil(t, b)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestVault(t)
			require.NoError(t, s.InsertSession(ctx, original()))

			committed := commitDuringBrief(t, func() error { return tt.write(ctx, s) })
			b, err := s.ReadBrief(ctx, uuid[:8], tt.scope)
			require.NoError(t, err)
			ran, writeErr := committed()
			require.True(t, ran, "the writer must commit inside the brief's read")
			require.NoError(t, writeErr, "a WAL writer commits while the reader holds its snapshot")
			assertOriginal(t, b)

			b, err = s.ReadBrief(ctx, uuid[:8], tt.scope)
			tt.fresh(t, b, err)
		})
	}
}

func TestReadBrief_ChildUsesOwnMainTranscript(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	const parent = "44444444-aaaa-bbbb-cccc-000000000004"
	const child = "55555555-aaaa-bbbb-cccc-000000000005"
	require.NoError(t, s.InsertSession(ctx, briefRecord(parent, "/p", briefRow(roleUser, 0, 0, 0, "parent ask"))))
	rec := briefRecord(child, "/p", briefRow(roleAssistant, 0, 0, 0, "child work"))
	rec.Session.Platform = PlatformCodex
	rec.Session.ParentUUID = parent
	require.NoError(t, s.InsertSession(ctx, rec))

	b, err := s.ReadBrief(ctx, child[:8], BriefScope{})
	require.NoError(t, err)
	assert.Equal(t, PlatformCodex, b.Platform)
	assert.Nil(t, b.Opening, "a child with no indexed human request reports that fact")
}

func TestReadBrief_OversizedFieldIsOmitted(t *testing.T) {
	s := newTestVault(t)
	const uuid = "66666666-aaaa-bbbb-cccc-000000000006"
	huge := strings.Repeat("x", briefFieldReadLimit) + "\x00tail"
	require.NoError(t, s.InsertSession(context.Background(), briefRecord(uuid, "/p",
		briefRow(roleUser, 0, 0, 0, huge),
		briefRow(roleAssistant, 0, 1, 1, strings.Repeat("y", briefFieldReadLimit)),
	)))
	b, err := s.ReadBrief(context.Background(), uuid[:8], BriefScope{})
	require.NoError(t, err)
	// huge exceeds the limit only through the bytes after its embedded NUL.
	assert.True(t, b.Latest.Text.Omitted, "size counts bytes past an embedded NUL")
	assert.Empty(t, b.Latest.Text.Value)
	assert.False(t, b.Assistant.Text.Omitted, "a value exactly at the limit is read")
}

func TestReadBrief_VaultStates(t *testing.T) {
	ctx := context.Background()

	t.Run("missing key", func(t *testing.T) {
		t.Setenv(vaultKeyEnv, "")
		_, err := NewVaultStore(filepath.Join(t.TempDir(), "vault.db")).ReadBrief(ctx, "12345678", BriefScope{})
		require.ErrorContains(t, err, vaultKeyEnv)
	})

	t.Run("absent vault stays absent", func(t *testing.T) {
		t.Setenv(vaultKeyEnv, testVaultKey)
		dir := t.TempDir()
		path := filepath.Join(dir, "vault.db")
		_, err := NewVaultStore(path).ReadBrief(ctx, "12345678", BriefScope{})
		require.ErrorIs(t, err, ErrVaultNotFound)
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("read-only open never creates a database", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "vault.db")
		_, err := sqliteutil.OpenWithCanary(ctx, briefDSN(path, testVaultKey), path, vaultKeyEnv)
		require.Error(t, err)
		_, statErr := os.Stat(path)
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	})

	t.Run("authenticated empty vault is a miss", func(t *testing.T) {
		s := newTestVault(t)
		require.NoError(t, s.Open(ctx))
		_, err := s.ReadBrief(ctx, "12345678", BriefScope{})
		require.ErrorIs(t, err, ErrSessionNotFound)
	})

	t.Run("committed row still in WAL is visible", func(t *testing.T) {
		s := newTestVault(t)
		const uuid = "77777777-aaaa-bbbb-cccc-000000000007"
		require.NoError(t, s.InsertSession(ctx, briefRecord(uuid, "/p", briefRow(roleUser, 0, 0, 0, "in wal"))))
		info, err := os.Stat(s.dbPath + "-wal")
		require.NoError(t, err)
		require.Positive(t, info.Size(), "the writer must still hold the commit in its WAL")
		b, err := s.ReadBrief(ctx, uuid[:8], BriefScope{})
		require.NoError(t, err)
		assert.Equal(t, "in wal", b.Opening.Text.Value)
	})

	t.Run("wrong key or damage preserves the archive", func(t *testing.T) {
		s := newTestVault(t)
		const uuid = "88888888-aaaa-bbbb-cccc-000000000008"
		require.NoError(t, s.InsertSession(ctx, briefRecord(uuid, "/p", briefRow(roleUser, 0, 0, 0, "secret text"))))
		require.NoError(t, s.Close())
		before := fileDigest(t, s.dbPath)

		t.Setenv(vaultKeyEnv, "a-different-key-of-sufficient-length!!")
		_, err := s.ReadBrief(ctx, uuid[:8], BriefScope{})
		require.Error(t, err)
		assert.True(t, sqliteutil.IsWrongPassphrase(err), "got %v", err)
		assert.NotContains(t, err.Error(), "secret text")
		assert.NotContains(t, err.Error(), "a-different-key")
		assert.Equal(t, before, fileDigest(t, s.dbPath))
		matches, _ := filepath.Glob(s.dbPath + ".corrupt*")
		assert.Empty(t, matches, "the brief never backs up or recreates a vault")
	})

	t.Run("garbage file is not recovered", func(t *testing.T) {
		t.Setenv(vaultKeyEnv, testVaultKey)
		path := filepath.Join(t.TempDir(), "vault.db")
		require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("garbage!", 1024)), 0o600))
		before := fileDigest(t, path)
		_, err := NewVaultStore(path).ReadBrief(ctx, "12345678", BriefScope{})
		require.Error(t, err)
		assert.Equal(t, before, fileDigest(t, path))
	})
}

// rawVault creates an encrypted database with only the given DDL, bypassing
// the vault's schema and migrations, and returns its path.
func rawVault(t *testing.T, ddl string) string {
	t.Helper()
	t.Setenv(vaultKeyEnv, testVaultKey)
	path := filepath.Join(t.TempDir(), "vault.db")
	db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, testVaultKey))
	require.NoError(t, err)
	_, err = db.Exec(ddl)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path
}

const legacyVaultDDL = `
CREATE TABLE vault_sessions (uuid TEXT PRIMARY KEY, title TEXT, project_path TEXT NOT NULL, raw_jsonl BLOB NOT NULL);
CREATE VIRTUAL TABLE vault_fts USING fts5(content_text, session_uuid UNINDEXED, subagent_id UNINDEXED,
  turn_index UNINDEXED, message_index UNINDEXED, line_index UNINDEXED, role UNINDEXED);
CREATE TABLE vault_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO vault_sessions VALUES ('99999999-0000-0000-0000-000000000000', 't', '/p', x'00');
`

func TestReadBrief_IncompatibleLayouts(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		ddl  string
		want string
	}{
		{name: "predecessor missing migrations", ddl: legacyVaultDDL, want: "predates migration 0003_add_index_version"},
		{
			name: "ledger claims a missing structure",
			ddl: legacyVaultDDL + `ALTER TABLE vault_sessions ADD COLUMN index_version INTEGER NOT NULL DEFAULT 1;
				CREATE TABLE vault_migrations (name TEXT PRIMARY KEY);
				INSERT INTO vault_migrations VALUES ('0003_add_index_version'), ('0006_platform');`,
			want: "inconsistent vault layout: migration 0006_platform is recorded but vault_sessions.platform is missing",
		},
		{name: "not a vault", ddl: `CREATE TABLE other (x);`, want: "not a recognized vault layout"},
		{
			name: "newer reader marker wins",
			ddl:  legacyVaultDDL + `INSERT INTO vault_meta VALUES ('min_reader_version', '99');`,
			want: "requires reader version 99",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := rawVault(t, tt.ddl)
			before := fileDigest(t, path)
			_, err := NewVaultStore(path).ReadBrief(ctx, "99999999", BriefScope{})
			var incompatible *IncompatibleVaultError
			require.True(t, errors.As(err, &incompatible), "got %v", err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, before, fileDigest(t, path), "no migration or repair ran")
		})
	}

	t.Run("newer marker on a current vault", func(t *testing.T) {
		s := newTestVault(t)
		const uuid = "aaaaaaaa-aaaa-bbbb-cccc-00000000000a"
		require.NoError(t, s.InsertSession(ctx, briefRecord(uuid, "/p", briefRow(roleUser, 0, 0, 0, "x"))))
		db, err := s.getDB(ctx)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT OR REPLACE INTO vault_meta (key, value) VALUES (?, '99')`, minReaderVersionKey)
		require.NoError(t, err)
		_, err = s.ReadBrief(ctx, uuid[:8], BriefScope{})
		var incompatible *IncompatibleVaultError
		require.True(t, errors.As(err, &incompatible), "got %v", err)
	})

	t.Run("unrecognized stored platform", func(t *testing.T) {
		s := newTestVault(t)
		const uuid = "bbbbbbbb-aaaa-bbbb-cccc-00000000000b"
		require.NoError(t, s.InsertSession(ctx, briefRecord(uuid, "/p", briefRow(roleUser, 0, 0, 0, "x"))))
		db, err := s.getDB(ctx)
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE vault_sessions SET platform = 'future-cli' WHERE uuid = ?`, uuid)
		require.NoError(t, err)
		_, err = s.ReadBrief(ctx, uuid[:8], BriefScope{})
		require.ErrorIs(t, err, ErrUnknownPlatform)
	})
}

func TestReadBrief_CallerDeadline(t *testing.T) {
	s := newTestVault(t)
	const uuid = "cccccccc-aaaa-bbbb-cccc-00000000000c"
	require.NoError(t, s.InsertSession(context.Background(), briefRecord(uuid, "/p", briefRow(roleUser, 0, 0, 0, "x"))))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, err := s.ReadBrief(ctx, uuid[:8], BriefScope{})
	assert.Nil(t, b)
	require.ErrorIs(t, err, context.Canceled)
}

func TestBriefTimeoutHint(t *testing.T) {
	tests := []struct {
		name string
		id   BriefField
		want string
	}{
		{name: "uuid", id: text("48285c38-32fd-46f3-b18f-121cba7471ce"), want: "48285c38-32fd-46f3-b18f-121cba7471ce"},
		{name: "command substitution", id: text("aaaaaaaa$(touch x)")},
		{name: "spaces", id: text("aaaaaaaa bbbb")},
		{name: "semicolon", id: text("aaaaaaaa;rm")},
		{name: "leading hyphen", id: text("-aaaaaaaa")},
		{name: "needs escaping", id: text("aaaaaaaa\x1b")},
		{name: "omitted", id: BriefField{Present: true, Omitted: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, briefHintID(tt.id))
		})
	}

	deadline := &BriefTimeoutError{SessionID: "48285c38-32fd", Err: context.DeadlineExceeded}
	assert.Equal(t, "reading the session brief timed out; run capy vault show 48285c38-32fd "+
		"with the same vault selection for the full transcript", deadline.Error())
	assert.ErrorIs(t, deadline, context.DeadlineExceeded)
	canceled := &BriefTimeoutError{Err: context.Canceled}
	assert.Equal(t, "reading the session brief was canceled", canceled.Error())
}

func TestReadBrief_MarkerReadFailureIsNotIncompatible(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	const uuid = "dddddddd-aaaa-bbbb-cccc-00000000000d"
	require.NoError(t, s.InsertSession(ctx, briefRecord(uuid, "/p", briefRow(roleUser, 0, 0, 0, "x"))))
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT OR REPLACE INTO vault_meta (key, value) VALUES (?, 'not-a-number')`, minReaderVersionKey)
	require.NoError(t, err)

	_, err = s.ReadBrief(ctx, uuid[:8], BriefScope{})
	require.ErrorContains(t, err, "invalid min_reader_version")
	var incompatible *IncompatibleVaultError
	assert.False(t, errors.As(err, &incompatible), "a damaged marker is a storage problem, not a newer format")
}

func TestReadBrief_MigrationHintKeepsVaultSelection(t *testing.T) {
	path := rawVault(t, legacyVaultDDL)
	_, err := NewVaultStore(path).ReadBrief(context.Background(), "99999999", BriefScope{})
	require.ErrorContains(t, err, "using the same vault selection (--path or CAPY_VAULT_PATH)")
}
