package vault

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionEffectiveTitle(t *testing.T) {
	custom := "Custom title"
	tests := []struct {
		name     string
		session  Session
		expected string
	}{
		{name: "no name row", session: Session{Title: "Imported title"}, expected: "Imported title"},
		{name: "custom override", session: Session{Title: "Imported title", Name: &SessionName{CustomTitle: &custom}}, expected: custom},
		{name: "clear tombstone", session: Session{Title: "Imported title", Name: &SessionName{}}, expected: "Imported title"},
		{name: "empty imported title", session: Session{Name: &SessionName{}}, expected: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.session.EffectiveTitle())
		})
	}
}

func TestNormalizeSessionName(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 36)
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "trim", input: "  Release notes  ", want: "Release notes"},
		{name: "secret redaction", input: "token " + secret, want: "token " + sanitize.RedactedSecret},
		{name: "empty", input: " \t\n ", wantErr: "must not be empty"},
		{name: "invalid utf-8", input: string([]byte{'b', 'a', 'd', 0xff}), wantErr: "valid utf-8"},
		{name: "embedded newline", input: "first\nsecond", wantErr: "control characters"},
		{name: "escape", input: "safe\x1b[31m", wantErr: "control characters"},
		{name: "unicode boundary", input: strings.Repeat("é", maxSessionNameRunes), want: strings.Repeat("é", maxSessionNameRunes)},
		{name: "unicode over limit", input: strings.Repeat("界", maxSessionNameRunes+1), wantErr: "must not exceed"},
		{
			name:  "length checked after redaction",
			input: strings.Repeat("a", 100) + secret,
			want:  strings.Repeat("a", 100) + sanitize.RedactedSecret,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeSessionName(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVaultStore_RenameReplaceClearAndReads(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuid := "aaaaaaaa-1111-2222-3333-444444444444"
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))

	first, err := s.renameSessionAt(ctx, uuid[:8], RenameOptions{Name: "  My vault name  "},
		time.Unix(0, 100), "machine-one")
	require.NoError(t, err)
	assert.Equal(t, "Sample session", first.Title, "imported title provenance must remain intact")
	assert.Equal(t, "My vault name", first.EffectiveTitle())
	require.NotNil(t, first.Name)
	assert.Equal(t, int64(100), first.Name.RenamedAtNS)
	assert.Equal(t, "machine-one", first.Name.MachineID)

	got, err := s.GetSession(ctx, uuid)
	require.NoError(t, err)
	assert.Equal(t, "Sample session", got.Title)
	assert.Equal(t, "My vault name", got.EffectiveTitle())

	listed, err := s.ListSessions(ctx, ListOptions{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "My vault name", listed[0].EffectiveTitle())
	assert.Nil(t, listed[0].RawJSONL)

	replaced, err := s.renameSessionAt(ctx, uuid, RenameOptions{Name: "Replacement"},
		time.Unix(0, 90), "machine-two")
	require.NoError(t, err)
	assert.Equal(t, "Replacement", replaced.EffectiveTitle())
	assert.Equal(t, int64(101), replaced.Name.RenamedAtNS, "backward clock must still advance the tuple")

	cleared, err := s.renameSessionAt(ctx, uuid, RenameOptions{Clear: true},
		time.Unix(0, 80), "machine-three")
	require.NoError(t, err)
	assert.Equal(t, "Sample session", cleared.EffectiveTitle())
	require.NotNil(t, cleared.Name, "clear must retain a tombstone row")
	assert.Nil(t, cleared.Name.CustomTitle)
	assert.Equal(t, int64(102), cleared.Name.RenamedAtNS)

	got, err = s.GetSession(ctx, uuid)
	require.NoError(t, err)
	assert.Equal(t, "Sample session", got.EffectiveTitle())
	require.NotNil(t, got.Name)
	assert.Nil(t, got.Name.CustomTitle)
}

func TestVaultStore_RenameLookupErrorsAndAmbiguousCandidates(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuidA := "dddddddd-1111-aaaa-0000-000000000001"
	uuidB := "dddddddd-1111-bbbb-0000-000000000002"
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuidA)))
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuidB)))
	_, err := s.renameSessionAt(ctx, uuidA, RenameOptions{Name: "Alpha name"}, time.Unix(0, 1), "a")
	require.NoError(t, err)
	_, err = s.renameSessionAt(ctx, uuidB, RenameOptions{Name: "Beta name"}, time.Unix(0, 1), "b")
	require.NoError(t, err)

	_, err = s.renameSessionAt(ctx, "dddddddd", RenameOptions{Name: "Nope"}, time.Unix(0, 2), "c")
	var ambiguous *AmbiguousUUIDError
	require.ErrorAs(t, err, &ambiguous)
	require.Len(t, ambiguous.Candidates, 2)
	titles := map[string]string{}
	for _, candidate := range ambiguous.Candidates {
		titles[candidate.UUID] = candidate.EffectiveTitle()
	}
	assert.Equal(t, "Alpha name", titles[uuidA])
	assert.Equal(t, "Beta name", titles[uuidB])

	_, err = s.renameSessionAt(ctx, "eeeeeeee", RenameOptions{Name: "Missing"}, time.Unix(0, 2), "c")
	assert.ErrorIs(t, err, ErrSessionNotFound)
	_, err = s.renameSessionAt(ctx, "short", RenameOptions{Name: "Invalid"}, time.Unix(0, 2), "c")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrSessionNotFound)
}

func TestVaultStore_RenameTreatsLikeMetacharactersLiterally(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuid := "aaaaaaaa-1111-2222-3333-444444444444"
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))

	tests := []struct {
		name   string
		prefix string
	}{
		{name: "underscore", prefix: "________"},
		{name: "percent", prefix: "%%%%%%%%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.renameSessionAt(
				ctx,
				tt.prefix,
				RenameOptions{Name: "Wrong session"},
				time.Unix(0, 1),
				"machine",
			)
			assert.ErrorIs(t, err, ErrSessionNotFound)
		})
	}

	got, err := s.GetSession(ctx, uuid)
	require.NoError(t, err)
	assert.Nil(t, got.Name, "wildcard-like prefixes must not mutate the only session")
}

func TestVaultStore_RenameValidationAndDuplicateNames(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuidA := "11111111-1111-1111-1111-111111111111"
	uuidB := "22222222-2222-2222-2222-222222222222"
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuidA)))
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuidB)))

	_, err := s.renameSessionAt(ctx, uuidA, RenameOptions{}, time.Unix(0, 1), "machine")
	require.Error(t, err)
	_, err = s.renameSessionAt(ctx, uuidA, RenameOptions{Name: "name", Clear: true}, time.Unix(0, 1), "machine")
	require.Error(t, err)

	for _, uuid := range []string{uuidA, uuidB} {
		_, err = s.renameSessionAt(ctx, uuid, RenameOptions{Name: "Duplicate allowed"}, time.Unix(0, 1), "machine")
		require.NoError(t, err)
	}

	db, err := s.getDB(ctx)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE vault_session_names SET custom_title = '' WHERE session_uuid = ?`, uuidA)
	require.Error(t, err, "schema must reject empty custom titles from every write path")
}

func TestVaultStore_RenameTimestampOverflow(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuid := "33333333-3333-3333-3333-333333333333"
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO vault_session_names
		(session_uuid, custom_title, renamed_at_ns, machine_id) VALUES (?, 'Old', ?, 'machine')`, uuid, int64(math.MaxInt64))
	require.NoError(t, err)

	_, err = s.renameSessionAt(ctx, uuid, RenameOptions{Name: "New"}, time.Unix(0, 1), "machine")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp overflow")

	got, err := s.GetSession(ctx, uuid)
	require.NoError(t, err)
	assert.Equal(t, "Old", got.EffectiveTitle(), "overflow must leave existing state unchanged")
}

func TestContainsFold(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		substr string
		want   bool
	}{
		{name: "ascii mixed case", s: "Release Notes", substr: "release", want: true},
		{name: "non-ascii folding", s: "Café Deploy", substr: "CAFÉ", want: true},
		{name: "needle folds too", s: "CAFÉ DEPLOY", substr: "café", want: true},
		{name: "literal percent", s: "50% done", substr: "%", want: true},
		{name: "no match", s: "Release Notes", substr: "deploy", want: false},
		{name: "wildcards are literal", s: "abc", substr: "a%c", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ContainsFold(tt.s, tt.substr))
		})
	}
}

func TestVaultStore_ListSessionsNameFilter(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()

	// Newest → oldest: alpha keeps its imported title; beta and gamma get custom
	// names sharing a foldable token so limit-after-filter is observable.
	const (
		alpha = "aaaa0001-1111-2222-3333-444444444444"
		beta  = "bbbb0002-1111-2222-3333-444444444444"
		gamma = "cccc0003-1111-2222-3333-444444444444"
	)
	for i, fixture := range []struct {
		uuid    string
		project string
	}{
		{alpha, "/home/user/alpha"},
		{beta, "/home/user/beta"},
		{gamma, "/home/user/gamma"},
	} {
		rec := sampleRecord(fixture.uuid)
		rec.Session.ProjectPath = fixture.project
		rec.Session.EndTime = time.Date(2026, 5, 1, 13-i, 0, 0, 0, time.UTC)
		require.NoError(t, s.InsertSession(ctx, rec))
	}
	_, err := s.renameSessionAt(ctx, beta, RenameOptions{Name: `Shared CAFÉ deploy % _ "notes"`}, time.Unix(0, 1), "m")
	require.NoError(t, err)
	_, err = s.renameSessionAt(ctx, gamma, RenameOptions{Name: "shared café target"}, time.Unix(0, 2), "m")
	require.NoError(t, err)

	tests := []struct {
		name string
		opts ListOptions
		want []string // expected UUIDs, newest first
	}{
		{name: "folded non-ascii needle", opts: ListOptions{Name: "café"}, want: []string{beta, gamma}},
		{name: "mixed case substring", opts: ListOptions{Name: "Café Target"}, want: []string{gamma}},
		{name: "literal percent", opts: ListOptions{Name: "%"}, want: []string{beta}},
		{name: "literal underscore", opts: ListOptions{Name: "_"}, want: []string{beta}},
		{name: "literal quotes", opts: ListOptions{Name: `"notes"`}, want: []string{beta}},
		{name: "imported title fallback", opts: ListOptions{Name: "sample"}, want: []string{alpha}},
		{name: "project and name combine with AND", opts: ListOptions{Project: "beta", Name: "shared"}, want: []string{beta}},
		{name: "no match", opts: ListOptions{Name: "zzz"}, want: nil},
		// alpha is the newest session overall but does not match: a SQL-side
		// LIMIT 1 would fetch only alpha and return nothing.
		{name: "limit applies after the name predicate", opts: ListOptions{Name: "shared", Limit: 1}, want: []string{beta}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.ListSessions(ctx, tt.opts)
			require.NoError(t, err)
			uuids := make([]string, 0, len(got))
			for _, sess := range got {
				uuids = append(uuids, sess.UUID)
			}
			if tt.want == nil {
				assert.Empty(t, uuids)
				return
			}
			assert.Equal(t, tt.want, uuids)
		})
	}
}

func TestVaultStore_SearchResolvesEffectiveTitles(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuid := "abab0001-1111-2222-3333-444444444444"
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))

	before, err := s.Search(ctx, SearchOptions{Query: "brontosaurus"})
	require.NoError(t, err)
	require.Len(t, before, 1)
	assert.Equal(t, "Sample session", before[0].Title)

	_, err = s.renameSessionAt(ctx, uuid, RenameOptions{Name: "Custom search label"}, time.Unix(0, 1), "m")
	require.NoError(t, err)

	after, err := s.Search(ctx, SearchOptions{Query: "brontosaurus"})
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, "Custom search label", after[0].Title)
	// Only the display title changes; matching, anchors, and snippet are untouched.
	got := after[0]
	got.Title = before[0].Title
	assert.Equal(t, before[0], got)

	// The name itself never becomes an FTS match.
	nameHits, err := s.Search(ctx, SearchOptions{Query: "custom search label"})
	require.NoError(t, err)
	assert.Empty(t, nameHits)

	_, err = s.renameSessionAt(ctx, uuid, RenameOptions{Clear: true}, time.Unix(0, 2), "m")
	require.NoError(t, err)
	cleared, err := s.Search(ctx, SearchOptions{Query: "brontosaurus"})
	require.NoError(t, err)
	require.Len(t, cleared, 1)
	assert.Equal(t, "Sample session", cleared[0].Title, "a clear tombstone falls back to the imported title")
}

func TestSearchChunks_ResolvesEffectiveTitles(t *testing.T) {
	s := newChunkSearchVault(t)
	ctx := context.Background()

	before := searchChunks(t, s, SearchOptions{Query: "zorbed"})
	require.NotEmpty(t, before)
	require.Equal(t, chunkSearchUUIDB, before[0].SessionUUID)

	_, err := s.renameSessionAt(ctx, chunkSearchUUIDB, RenameOptions{Name: "Quixotic telemetry hunt"}, time.Unix(0, 1), "m")
	require.NoError(t, err)

	after := searchChunks(t, s, SearchOptions{Query: "zorbed"})
	require.NotEmpty(t, after)
	assert.Equal(t, "Quixotic telemetry hunt", after[0].Title)
	// Only the display title changes; matching and anchors are untouched.
	got := after[0]
	got.Title = before[0].Title
	assert.Equal(t, before[0], got)

	// The name text is not in either chunk FTS layer, so it never matches.
	assert.Empty(t, searchChunks(t, s, SearchOptions{Query: "quixotic"}))

	_, err = s.renameSessionAt(ctx, chunkSearchUUIDB, RenameOptions{Clear: true}, time.Unix(0, 2), "m")
	require.NoError(t, err)
	cleared := searchChunks(t, s, SearchOptions{Query: "zorbed"})
	require.NotEmpty(t, cleared)
	assert.Equal(t, before[0].Title, cleared[0].Title, "a clear tombstone falls back to the imported title")
}

type archivedDataSnapshot struct {
	raw      []byte
	encoding sql.NullString
	hash     string
	size     int64
	ftsCount int
	chunkA   int
	chunkB   int
	files    []File
}

func snapshotArchivedData(t *testing.T, s *VaultStore, uuid string) archivedDataSnapshot {
	t.Helper()
	ctx := context.Background()
	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var snap archivedDataSnapshot
	require.NoError(t, db.QueryRow(`SELECT raw_jsonl, encoding, content_hash, size_bytes
		FROM vault_sessions WHERE uuid = ?`, uuid).Scan(&snap.raw, &snap.encoding, &snap.hash, &snap.size))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_fts WHERE session_uuid = ?`, uuid).Scan(&snap.ftsCount))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_chunks WHERE session_uuid = ?`, uuid).Scan(&snap.chunkA))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_chunks_trigram WHERE session_uuid = ?`, uuid).Scan(&snap.chunkB))
	snap.files, err = s.GetFiles(ctx, uuid)
	require.NoError(t, err)
	return snap
}

func TestVaultStore_RenameDoesNotMutateArchivedData(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuid := "44444444-4444-4444-4444-444444444444"
	rec := sampleRecord(uuid)
	rec.Chunks = []Chunk{{Title: "Chunk", ContentText: "chunk pterosaur", FirstLineIndex: 0}}
	require.NoError(t, s.InsertSession(ctx, rec))
	before := snapshotArchivedData(t, s, uuid)

	_, err := s.renameSessionAt(ctx, uuid, RenameOptions{Name: "Immutable archive"}, time.Unix(0, 10), "machine")
	require.NoError(t, err)
	_, err = s.renameSessionAt(ctx, uuid, RenameOptions{Clear: true}, time.Unix(0, 11), "machine")
	require.NoError(t, err)
	after := snapshotArchivedData(t, s, uuid)

	assert.True(t, bytes.Equal(before.raw, after.raw), "stored raw_jsonl bytes must not change")
	assert.Equal(t, before.encoding, after.encoding)
	assert.Equal(t, before.hash, after.hash)
	assert.Equal(t, before.size, after.size)
	assert.Equal(t, before.ftsCount, after.ftsCount)
	assert.Equal(t, before.chunkA, after.chunkA)
	assert.Equal(t, before.chunkB, after.chunkB)
	assert.Equal(t, before.files, after.files)
}

func TestVaultStore_ConcurrentRenamesAreMonotonic(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	uuid := "55555555-5555-5555-5555-555555555555"
	require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))

	const n = 12
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := s.renameSessionAt(ctx, uuid, RenameOptions{Name: fmt.Sprintf("name-%02d", i)},
				time.Unix(0, 1000), fmt.Sprintf("machine-%02d", i))
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	got, err := s.GetSession(ctx, uuid)
	require.NoError(t, err)
	require.NotNil(t, got.Name)
	assert.Equal(t, int64(1000+n-1), got.Name.RenamedAtNS)
	require.NotNil(t, got.Name.CustomTitle)
	index := strings.TrimPrefix(*got.Name.CustomTitle, "name-")
	assert.Equal(t, "machine-"+index, got.Name.MachineID, "winning title and writer tuple must stay atomic")

	db, err := s.getDB(ctx)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_session_names WHERE session_uuid = ?`, uuid).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestVaultStore_RenameDeleteRaceLeavesNoOrphan(t *testing.T) {
	s := newTestVault(t)
	ctx := context.Background()
	for i := range 20 {
		uuid := fmt.Sprintf("%08d-6666-6666-6666-666666666666", i)
		require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))
		start := make(chan struct{})
		var renameErr, deleteErr error
		var deleted bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, renameErr = s.renameSessionAt(ctx, uuid, RenameOptions{Name: "racing"}, time.Unix(0, 1), "machine")
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
		if renameErr != nil {
			assert.True(t, errors.Is(renameErr, ErrSessionNotFound), "rename error must be actionable: %v", renameErr)
		}
		db, err := s.getDB(ctx)
		require.NoError(t, err)
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM vault_session_names WHERE session_uuid = ?`, uuid).Scan(&count))
		assert.Equal(t, 0, count)
	}
}

func applyNameFixture(t *testing.T, s *VaultStore, uuid string, clear bool) SessionName {
	t.Helper()
	ctx := context.Background()
	got, err := s.renameSessionAt(ctx, uuid, RenameOptions{Name: "Retained custom name"}, time.Unix(0, 500), "retention-machine")
	require.NoError(t, err)
	if clear {
		got, err = s.renameSessionAt(ctx, uuid, RenameOptions{Clear: true}, time.Unix(0, 501), "retention-machine")
		require.NoError(t, err)
	}
	require.NotNil(t, got.Name)
	return *got.Name
}

func assertNameFixture(t *testing.T, s *VaultStore, uuid, importedTitle string, expected SessionName) {
	t.Helper()
	got, err := s.GetSession(context.Background(), uuid)
	require.NoError(t, err)
	assert.Equal(t, importedTitle, got.Title)
	require.NotNil(t, got.Name)
	assert.Equal(t, expected, *got.Name)
	if expected.CustomTitle == nil {
		assert.Equal(t, importedTitle, got.EffectiveTitle())
	} else {
		assert.Equal(t, *expected.CustomTitle, got.EffectiveTitle())
	}
}

func TestSessionNameSurvivesReimportReplacement(t *testing.T) {
	for _, clear := range []bool{false, true} {
		name := "custom"
		if clear {
			name = "clear tombstone"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestVault(t)
			root := t.TempDir()
			projectDir := filepath.Join(root, "-home-user-proj")
			uuid := "77777777-7777-7777-7777-777777777777"
			writeSession(t, projectDir, uuid, sampleMainJSONL(t), nil)
			require.Equal(t, 1, importFixture(t, s, root, ImportOptions{}).Imported)
			expected := applyNameFixture(t, s, uuid, clear)

			grown := append([]byte{}, sampleMainJSONL(t)...)
			grown = append(grown, jsonlBytes(t,
				userLine("u-grown", "/home/user/proj", "feature/x", "A later prompt makes this transcript larger"),
				aiTitleLine("Imported replacement title"),
			)...)
			writeSession(t, projectDir, uuid, grown, nil)
			res := importFixture(t, s, root, ImportOptions{})
			assert.Equal(t, 1, res.Updated)

			assertNameFixture(t, s, uuid, "Imported replacement title", expected)
		})
	}
}

func TestSessionNameSurvivesReindex(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprintf("clear=%t", clear), func(t *testing.T) {
			s := newTestVault(t)
			uuid := "88888888-8888-8888-8888-888888888888"
			rec := sampleRecord(uuid)
			rec.Session.IndexVersion = 1
			require.NoError(t, s.InsertSession(context.Background(), rec))
			expected := applyNameFixture(t, s, uuid, clear)

			res, err := Reindex(context.Background(), s)
			require.NoError(t, err)
			assert.Equal(t, 1, res.Reindexed)
			assertNameFixture(t, s, uuid, "Sample session", expected)
		})
	}
}

func TestSessionNameSurvivesCompact(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprintf("clear=%t", clear), func(t *testing.T) {
			s := newTestVault(t)
			ctx := context.Background()
			uuid := "99999999-9999-9999-9999-999999999999"
			rec := sampleRecord(uuid)
			rec.Session.RawJSONL = largeCompressibleBytes()
			rec.Session.SizeBytes = int64(len(rec.Session.RawJSONL))
			require.NoError(t, s.InsertSession(ctx, rec))
			db, err := s.getDB(ctx)
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE vault_sessions SET raw_jsonl = ?, encoding = NULL WHERE uuid = ?`, rec.Session.RawJSONL, uuid)
			require.NoError(t, err)
			expected := applyNameFixture(t, s, uuid, clear)

			res, err := s.Compact(ctx)
			require.NoError(t, err)
			assert.Equal(t, 1, res.SessionsRewritten)
			require.NoError(t, s.Open(ctx))
			assertNameFixture(t, s, uuid, "Sample session", expected)
		})
	}
}

func TestSessionNameSurvivesBackupAPIRekey(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprintf("clear=%t", clear), func(t *testing.T) {
			const newKey = "new-vault-key-at-least-32-characters-long!!"
			t.Setenv(vaultKeyEnv, testVaultKey)
			path := filepath.Join(t.TempDir(), "vault.db")
			s := NewVaultStore(path)
			ctx := context.Background()
			uuid := "abababab-abab-abab-abab-abababababab"
			require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))
			expected := applyNameFixture(t, s, uuid, clear)
			require.NoError(t, s.Close())

			_, err := sqliteutil.Rekey(path, testVaultKey, newKey)
			require.NoError(t, err)
			t.Setenv(vaultKeyEnv, newKey)
			rotated := NewVaultStore(path)
			t.Cleanup(func() { _ = rotated.Close() })
			require.NoError(t, rotated.Open(ctx))
			assertNameFixture(t, rotated, uuid, "Sample session", expected)
		})
	}
}
