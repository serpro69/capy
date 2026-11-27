package vault

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A complete session ID that also begins a longer ID, or that LIKE's ASCII
// case folding matches against another ID, resolves to itself in every
// prefix-lookup path. Shorter prefixes keep their ambiguity.
const (
	exactShort = "legacy-session"
	exactLong  = "legacy-session-2"
	exactUpper = "LEGACY-SESSION"
	exactStem  = "legacy-sess"
)

func newExactIDVault(t *testing.T) *VaultStore {
	t.Helper()
	s := newTestVault(t)
	for _, id := range []string{exactShort, exactLong, exactUpper} {
		require.NoError(t, s.InsertSession(t.Context(), sampleRecord(id)))
	}
	return s
}

func TestPreferExactSession(t *testing.T) {
	a, b := Session{UUID: "abcdefgh"}, Session{UUID: "abcdefgh-2"}
	for _, tt := range []struct {
		name    string
		matches []Session
		id      string
		want    []Session
	}{
		{"exact among several", []Session{b, a}, "abcdefgh", []Session{a}},
		{"no exact keeps all", []Session{a, b}, "abcdefg", []Session{a, b}},
		{"single unchanged", []Session{b}, "abcdefgh", []Session{b}},
		{"none", nil, "abcdefgh", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, preferExactSession(tt.matches, tt.id))
		})
	}
}

func TestExactSessionID_GetSession(t *testing.T) {
	s := newExactIDVault(t)
	for _, id := range []string{exactShort, exactUpper, exactLong} {
		got, err := s.GetSession(t.Context(), id)
		require.NoError(t, err, id)
		assert.Equal(t, id, got.UUID)
	}
	_, err := s.GetSession(t.Context(), exactStem)
	var ambiguous *AmbiguousUUIDError
	require.ErrorAs(t, err, &ambiguous)
	assert.Len(t, ambiguous.Candidates, 3, "a shorter prefix stays ambiguous")
}

func TestExactSessionID_RenameAndProject(t *testing.T) {
	s := newExactIDVault(t)
	renamed, err := s.renameSessionAt(t.Context(), exactShort, RenameOptions{Name: "Short one"}, time.Unix(0, 1), "m")
	require.NoError(t, err)
	assert.Equal(t, exactShort, renamed.UUID)
	assigned, err := s.setSessionProjectAt(t.Context(), exactUpper, ProjectOptions{Name: "upper"}, time.Unix(0, 1), "m")
	require.NoError(t, err)
	assert.Equal(t, exactUpper, assigned.UUID)

	for _, id := range []string{exactLong, exactUpper} {
		got, err := s.GetSession(t.Context(), id)
		require.NoError(t, err)
		assert.NotEqual(t, "Short one", got.EffectiveTitle(), "only the exact session is renamed")
	}
	got, err := s.GetSession(t.Context(), exactShort)
	require.NoError(t, err)
	assert.NotEqual(t, "upper", got.EffectiveProject(), "only the exact session is reassigned")

	var ambiguous *AmbiguousUUIDError
	_, err = s.renameSessionAt(t.Context(), exactStem, RenameOptions{Name: "Nope"}, time.Unix(0, 2), "m")
	require.ErrorAs(t, err, &ambiguous)
	_, err = s.setSessionProjectAt(t.Context(), exactStem, ProjectOptions{Name: "nope"}, time.Unix(0, 2), "m")
	require.ErrorAs(t, err, &ambiguous)
}

func TestExactSessionID_PrepareDelete(t *testing.T) {
	s := newExactIDVault(t)
	for _, opts := range []DeleteOptions{{}, {ExactIDs: true}} {
		plan, err := s.PrepareDelete(t.Context(), []string{exactShort}, opts)
		require.NoError(t, err)
		targets := plan.Targets()
		require.Len(t, targets, 1)
		assert.Equal(t, exactShort, targets[0].UUID, "exact IDs %v", opts.ExactIDs)
	}
	_, err := s.PrepareDelete(t.Context(), []string{exactStem}, DeleteOptions{})
	var ambiguous *AmbiguousUUIDError
	require.ErrorAs(t, err, &ambiguous)
}

func TestExactSessionID_ReadBrief(t *testing.T) {
	s := newTestVault(t)
	for _, rec := range []*SessionRecord{
		briefRecord(exactShort, "/work/alpha", briefRow(roleUser, 0, 0, 0, "short session request")),
		briefRecord(exactLong, "/work/alpha", briefRow(roleUser, 0, 0, 0, "long session request")),
		briefRecord(exactUpper, "/work/beta", briefRow(roleUser, 0, 0, 0, "upper session request")),
	} {
		require.NoError(t, s.InsertSession(t.Context(), rec))
	}
	for _, tt := range []struct {
		name, session, want string
		scope               BriefScope
	}{
		{"exact beside a longer ID", exactShort, "short session request", BriefScope{}},
		{"exact beside a case variant", exactUpper, "upper session request", BriefScope{}},
		{"exact inside scope", exactShort, "short session request", BriefScope{ProjectPath: "alpha"}},
		{"longer ID", exactLong, "long session request", BriefScope{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := s.ReadBrief(t.Context(), tt.session, tt.scope)
			require.NoError(t, err)
			require.NotNil(t, b.Latest)
			assert.Equal(t, tt.session, b.ID.Value)
			assert.Equal(t, tt.want, b.Latest.Text.Value)
		})
	}

	_, err := s.ReadBrief(t.Context(), exactStem, BriefScope{})
	require.ErrorIs(t, err, ErrBriefAmbiguous, "a shorter prefix stays ambiguous")
	// Scope still applies first: the exact ID is outside scope, so the input
	// falls back to a prefix lookup, which case-folds onto both alpha IDs.
	_, err = s.ReadBrief(t.Context(), exactUpper, BriefScope{ProjectPath: "alpha"})
	require.ErrorIs(t, err, ErrBriefAmbiguous)
}

// A damaged transcript on another candidate must not fail an exact lookup;
// ambiguity needs only metadata.
func TestExactSessionID_GetSessionIgnoresOtherCandidatesBlobs(t *testing.T) {
	s := newExactIDVault(t)
	db, err := s.getDB(t.Context())
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(),
		`UPDATE vault_sessions SET encoding = 'zstd', raw_jsonl = X'00' WHERE uuid = ?`, exactLong)
	require.NoError(t, err)

	got, err := s.GetSession(t.Context(), exactShort)
	require.NoError(t, err)
	assert.Equal(t, exactShort, got.UUID)
	assert.NotEmpty(t, got.RawJSONL, "the selected transcript is decoded")

	_, err = s.GetSession(t.Context(), exactStem)
	var ambiguous *AmbiguousUUIDError
	require.ErrorAs(t, err, &ambiguous, "ambiguity is reported without decoding candidates")

	_, err = s.GetSession(t.Context(), exactLong)
	require.ErrorContains(t, err, "decoding raw_jsonl for "+exactLong, "the damaged session itself still fails loudly")
}

// With ExactID, a session deleted after its full ID was captured is missing,
// not resolved to a longer ID that it prefixes.
func TestExactSessionID_ExactIDOptionHasNoPrefixFallback(t *testing.T) {
	s := newExactIDVault(t)
	deleted, err := s.DeleteSession(t.Context(), exactShort, false)
	require.NoError(t, err)
	require.True(t, deleted)

	_, err = s.renameSessionAt(t.Context(), exactShort, RenameOptions{Name: "late", ExactID: true}, time.Unix(0, 1), "m")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = s.setSessionProjectAt(t.Context(), exactShort, ProjectOptions{Name: "late", ExactID: true}, time.Unix(0, 1), "m")
	require.ErrorIs(t, err, ErrSessionNotFound)
	got, err := s.GetSession(t.Context(), exactLong)
	require.NoError(t, err)
	assert.NotEqual(t, "late", got.EffectiveTitle())
	assert.NotEqual(t, "late", got.EffectiveProject())

	// Without ExactID the input falls back to a prefix lookup, which now
	// matches the longer ID and, by ASCII case folding, the upper-case one.
	_, err = s.renameSessionAt(t.Context(), exactShort, RenameOptions{Name: "prefix"}, time.Unix(0, 2), "m")
	var ambiguous *AmbiguousUUIDError
	require.ErrorAs(t, err, &ambiguous)
	assert.Len(t, ambiguous.Candidates, 2)

	// ExactID also reaches IDs shorter than the prefix minimum.
	require.NoError(t, s.InsertSession(t.Context(), sampleRecord("notes")))
	named, err := s.renameSessionAt(t.Context(), "notes", RenameOptions{Name: "Notes", ExactID: true}, time.Unix(0, 3), "m")
	require.NoError(t, err)
	assert.Equal(t, "notes", named.UUID)
}

// Reindex enumerates UUIDs before reading each one. A session deleted in
// between must not be rebuilt from a longer ID it prefixes.
func TestExactSessionID_ReindexNeverReadsAnotherSession(t *testing.T) {
	s := newTestVault(t)
	for _, id := range []string{exactShort, exactLong} {
		require.NoError(t, s.InsertSession(t.Context(), sampleRecord(id)))
	}
	deleted, err := s.DeleteSession(t.Context(), exactShort, false)
	require.NoError(t, err)
	require.True(t, deleted)

	_, _, err = rebuildSessionFTS(t.Context(), s, exactShort)
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, _, err = rebuildSessionFTS(t.Context(), s, exactLong)
	require.NoError(t, err, "the surviving session still rebuilds")
}
