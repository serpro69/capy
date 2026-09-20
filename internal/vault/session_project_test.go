package vault

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionProject_EffectiveProject(t *testing.T) {
	label := "~/literal/../project"
	path := "/original/path"
	for _, tt := range []struct {
		name  string
		state *SessionProject
		want  string
	}{
		{name: "unset", want: path},
		{name: "override", state: &SessionProject{CustomProject: &label}, want: label},
		{name: "equal to path", state: &SessionProject{CustomProject: &path}, want: path},
		{name: "clear", state: &SessionProject{}, want: path},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sess := Session{ProjectPath: path, ProjectOverride: tt.state}
			assert.Equal(t, tt.want, sess.EffectiveProject())
			assert.Equal(t, path, sess.ProjectPath)
		})
	}
}

func TestSessionProject_LocalEdits(t *testing.T) {
	t.Run("normalization", func(t *testing.T) {
		secret := "ghp_" + strings.Repeat("a", 36)
		for _, tt := range []struct{ name, input, want, wantErr string }{
			{name: "trim", input: "  Café project  ", want: "Café project"},
			{name: "path literal", input: "~/a/../b", want: "~/a/../b"},
			{name: "secret", input: "token " + secret, want: "token " + sanitize.RedactedSecret},
			{name: "redaction before length", input: strings.Repeat("a", 100) + secret, want: strings.Repeat("a", 100) + sanitize.RedactedSecret},
			{name: "120 code points", input: strings.Repeat("界", 120), want: strings.Repeat("界", 120)},
			{name: "too long", input: strings.Repeat("界", 121), wantErr: "must not exceed 120"},
			{name: "empty", input: " \t\n ", wantErr: "must not be empty"},
			{name: "invalid utf8", input: "bad\xff", wantErr: "must be valid utf-8"},
			{name: "newline", input: "a\nb", wantErr: "must not contain control characters"},
			{name: "escape", input: "a\x1b[31m", wantErr: "must not contain control characters"},
			{name: "null", input: "a\x00b", wantErr: "must not contain control characters"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				s := newTestVault(t)
				const uuid = "aaaaaaaa-1111-2222-3333-444444444444"
				require.NoError(t, s.InsertSession(t.Context(), sampleRecord(uuid)))
				sess, err := s.setSessionProjectAt(t.Context(), uuid, ProjectOptions{Name: tt.input}, time.Unix(0, 1), "writer")
				if tt.wantErr != "" {
					require.ErrorContains(t, err, "session project "+tt.wantErr)
					got, err := s.GetSession(t.Context(), uuid)
					require.NoError(t, err)
					assert.Nil(t, got.ProjectOverride)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tt.want, sess.EffectiveProject())
				got, err := s.GetSession(t.Context(), uuid)
				require.NoError(t, err)
				assert.Equal(t, sess.ProjectOverride, got.ProjectOverride)
			})
		}
	})

	t.Run("set replace clear and independent clocks", func(t *testing.T) {
		s := newTestVault(t)
		ctx := t.Context()
		const uuid = "aaaaaaaa-1111-2222-3333-444444444444"
		require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))
		named, err := s.renameSessionAt(ctx, uuid, RenameOptions{Name: "Independent title"}, time.Unix(0, 900), "title-writer")
		require.NoError(t, err)
		first, err := s.setSessionProjectAt(ctx, uuid[:8], ProjectOptions{Name: "  capy  "}, time.Unix(0, 100), "project-writer")
		require.NoError(t, err)
		require.NotNil(t, first.ProjectOverride)
		assert.Equal(t, "capy", first.EffectiveProject())
		assert.Equal(t, "/home/user/proj", first.ProjectPath)
		assert.Equal(t, "-home-user-proj", first.ClaudeProjectDir)
		assert.Equal(t, int64(100), first.ProjectOverride.UpdatedAtNS)
		assert.Equal(t, "project-writer", first.ProjectOverride.MachineID)
		assert.Equal(t, named.Name, first.Name)
		assert.Nil(t, first.RawJSONL, "mutation reads metadata only")

		listed, err := s.ListSessions(ctx, ListOptions{})
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, first.ProjectOverride, listed[0].ProjectOverride)
		assert.Nil(t, listed[0].RawJSONL)

		second, err := s.setSessionProjectAt(ctx, uuid, ProjectOptions{Name: "replacement"}, time.Unix(0, 90), "other-writer")
		require.NoError(t, err)
		assert.Equal(t, "replacement", second.EffectiveProject())
		assert.Equal(t, int64(101), second.ProjectOverride.UpdatedAtNS)
		// A later imported path must be revealed by clear, not the old path.
		db, err := s.getDB(ctx)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `UPDATE vault_sessions SET project_path = '/latest/path' WHERE uuid = ?`, uuid)
		require.NoError(t, err)
		cleared, err := s.setSessionProjectAt(ctx, uuid, ProjectOptions{Clear: true}, time.Unix(0, 80), "clear-writer")
		require.NoError(t, err)
		assert.Equal(t, "/latest/path", cleared.EffectiveProject())
		require.NotNil(t, cleared.ProjectOverride)
		assert.Nil(t, cleared.ProjectOverride.CustomProject)
		assert.Equal(t, int64(102), cleared.ProjectOverride.UpdatedAtNS)
		assert.Equal(t, named.Name, cleared.Name)
		got, err := s.GetSession(ctx, uuid)
		require.NoError(t, err)
		assert.Equal(t, cleared.ProjectOverride, got.ProjectOverride)

		renamed, err := s.renameSessionAt(ctx, uuid, RenameOptions{Clear: true}, time.Unix(0, 1), "title-clear")
		require.NoError(t, err)
		assert.Equal(t, int64(901), renamed.Name.RenamedAtNS)
		assert.Equal(t, cleared.ProjectOverride, renamed.ProjectOverride)
	})

	t.Run("first clear and parent child independence", func(t *testing.T) {
		s := newTestVault(t)
		ctx := t.Context()
		parent := sampleRecord("aaaaaaaa-1111-2222-3333-444444444444")
		child := sampleRecord("bbbbbbbb-1111-2222-3333-444444444444")
		child.Session.ParentUUID = parent.Session.UUID
		require.NoError(t, s.InsertSession(ctx, parent))
		require.NoError(t, s.InsertSession(ctx, child))
		cleared, err := s.setSessionProjectAt(ctx, parent.Session.UUID, ProjectOptions{Clear: true}, time.Unix(0, 0), "writer")
		require.NoError(t, err)
		require.NotNil(t, cleared.ProjectOverride, "zero timestamp still identifies a tombstone")
		assert.Nil(t, cleared.ProjectOverride.CustomProject)
		assigned, err := s.setSessionProjectAt(ctx, child.Session.UUID, ProjectOptions{Name: "child only"}, time.Unix(0, 2), "writer")
		require.NoError(t, err)
		children, err := s.Children(ctx, parent.Session.UUID)
		require.NoError(t, err)
		require.Len(t, children, 1)
		assert.Equal(t, assigned.ProjectOverride, children[0].ProjectOverride)
		got, err := s.GetSession(ctx, parent.Session.UUID)
		require.NoError(t, err)
		assert.Equal(t, cleared.ProjectOverride, got.ProjectOverride)
	})

	t.Run("lookup and validation", func(t *testing.T) {
		s := newTestVault(t)
		ctx := t.Context()
		ids := []string{"aaaaaaaa-1111-2222-3333-444444444444", "aaaaaaaa-2222-2222-3333-444444444444"}
		for _, uuid := range ids {
			require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))
			_, err := s.setSessionProjectAt(ctx, uuid, ProjectOptions{Name: "duplicate allowed"}, time.Unix(0, 1), "writer")
			require.NoError(t, err)
		}
		_, err := s.setSessionProjectAt(ctx, "aaaaaaaa", ProjectOptions{Name: "wrong"}, time.Unix(0, 2), "writer")
		var ambiguous *AmbiguousUUIDError
		require.ErrorAs(t, err, &ambiguous)
		require.Len(t, ambiguous.Candidates, 2)
		for _, candidate := range ambiguous.Candidates {
			assert.Equal(t, "duplicate allowed", candidate.EffectiveProject())
		}
		for _, prefix := range []string{"missing1", "%%%%%%%%", "________", `aaaaaaaa\`, "aaaaaaaa' OR 1=1--"} {
			_, err := s.setSessionProjectAt(ctx, prefix, ProjectOptions{Clear: true}, time.Unix(0, 2), "writer")
			assert.ErrorIs(t, err, ErrSessionNotFound, "prefix %q", prefix)
		}
		_, err = s.setSessionProjectAt(ctx, "short", ProjectOptions{Clear: true}, time.Unix(0, 2), "writer")
		require.ErrorContains(t, err, "at least 8")
		for _, opts := range []ProjectOptions{{}, {Name: "name", Clear: true}, {Name: " ", Clear: true}} {
			_, err = s.setSessionProjectAt(ctx, ids[0], opts, time.Unix(0, 2), "writer")
			require.Error(t, err)
		}
		for _, uuid := range ids {
			got, err := s.GetSession(ctx, uuid)
			require.NoError(t, err)
			assert.Equal(t, "duplicate allowed", got.EffectiveProject())
			assert.Equal(t, int64(1), got.ProjectOverride.UpdatedAtNS, "invalid edits leave state intact")
		}
	})

	t.Run("overflow and cancellation", func(t *testing.T) {
		s := newTestVault(t)
		ctx := t.Context()
		const uuid = "aaaaaaaa-1111-2222-3333-444444444444"
		require.NoError(t, s.InsertSession(ctx, sampleRecord(uuid)))
		before, err := s.setSessionProjectAt(ctx, uuid, ProjectOptions{Name: "old"}, time.Unix(0, math.MaxInt64), "writer")
		require.NoError(t, err)
		for _, opts := range []ProjectOptions{{Name: "new"}, {Clear: true}} {
			_, err = s.setSessionProjectAt(ctx, uuid, opts, time.Unix(0, 1), "writer")
			require.ErrorContains(t, err, "timestamp overflow")
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = s.setSessionProjectAt(canceled, uuid, ProjectOptions{Clear: true}, time.Unix(0, 1), "writer")
		assert.ErrorIs(t, err, context.Canceled)
		got, err := s.GetSession(ctx, uuid)
		require.NoError(t, err)
		assert.Equal(t, before.ProjectOverride, got.ProjectOverride)
	})
}

func TestSessionProject_ArchivedDataUnchanged(t *testing.T) {
	s := newTestVault(t)
	ctx := t.Context()
	const uuid = "aaaaaaaa-1111-2222-3333-444444444444"
	rec := sampleRecord(uuid)
	rec.Session.IndexVersion = currentIndexVersion
	rec.Chunks = []Chunk{{Title: "Chunk", ContentText: "chunk pterosaur", FirstLineIndex: 0}}
	require.NoError(t, s.InsertSession(ctx, rec))
	_, err := s.renameSessionAt(ctx, uuid, RenameOptions{Name: "Custom title"}, time.Unix(0, 1), "writer")
	require.NoError(t, err)
	before := snapshotArchivedData(t, s, uuid)
	beforeSession, err := s.GetSession(ctx, uuid)
	require.NoError(t, err)
	require.NotEmpty(t, before.ftsRows)
	require.NotEmpty(t, before.chunkRows)
	require.NotEmpty(t, before.trigramRows)
	for _, opts := range []ProjectOptions{{Name: "capy"}, {Name: "replacement"}, {Clear: true}} {
		_, err := s.setSessionProjectAt(ctx, uuid, opts, time.Unix(0, 2), "project-writer")
		require.NoError(t, err)
		assert.Equal(t, before, snapshotArchivedData(t, s, uuid), "stored bytes, files and every index row must remain identical")
		after, err := s.GetSession(ctx, uuid)
		require.NoError(t, err)
		after.ProjectOverride = nil
		assert.Equal(t, beforeSession, after, "all imported metadata and title state must remain identical")
	}
}
