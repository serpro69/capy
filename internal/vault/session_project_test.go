package vault

import (
	"context"
	"fmt"
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

func TestSessionProject_ListFiltering(t *testing.T) {
	s := newTestVault(t)
	ctx := t.Context()
	const (
		newest     = "aaaa0001-1111-2222-3333-444444444444"
		otherTitle = "bbbb0002-1111-2222-3333-444444444444"
		child      = "cccc0003-1111-2222-3333-444444444444"
		claude     = "dddd0004-1111-2222-3333-444444444444"
		oldest     = "eeee0005-1111-2222-3333-444444444444"
	)
	for i, fixture := range []struct {
		uuid     string
		platform Platform
		parent   string
		label    string
		title    string
	}{
		{uuid: newest, platform: PlatformCodex, label: "unrelated", title: "TARGET"},
		{uuid: otherTitle, platform: PlatformCodex, label: "shared", title: "other"},
		{uuid: child, platform: PlatformCodex, parent: newest, label: "shared", title: "TARGET"},
		{uuid: claude, platform: PlatformClaudeCode, label: "shared", title: "TARGET"},
		{uuid: oldest, platform: PlatformCodex, label: "shared", title: "TARGET"},
	} {
		rec := sampleRecord(fixture.uuid)
		rec.Session.ProjectPath = "/old/location"
		rec.Session.Platform = fixture.platform
		rec.Session.ParentUUID = fixture.parent
		rec.Session.EndTime = rec.Session.EndTime.Add(-time.Duration(i) * time.Hour)
		require.NoError(t, s.InsertSession(ctx, rec))
		_, err := s.setSessionProjectAt(ctx, fixture.uuid, ProjectOptions{Name: fixture.label}, time.Unix(0, 1), "m")
		require.NoError(t, err)
		_, err = s.renameSessionAt(ctx, fixture.uuid, RenameOptions{Name: fixture.title}, time.Unix(0, 1), "m")
		require.NoError(t, err)
	}
	for _, tt := range []struct {
		name string
		opts ListOptions
		want []string
	}{
		{name: "project before SQL limit", opts: ListOptions{Project: "SHARED", Limit: 1}, want: []string{otherTitle}},
		{name: "name before Go limit", opts: ListOptions{Project: "shared", Name: "target", Limit: 1}, want: []string{claude}},
		{name: "all filters", opts: ListOptions{Project: "shared", Name: "target", Platform: PlatformCodex, Limit: 1}, want: []string{oldest}},
		{name: "children included", opts: ListOptions{Project: "shared", Name: "target", Platform: PlatformCodex, IncludeChildren: true, Limit: 1}, want: []string{child}},
		{name: "no limit", opts: ListOptions{Project: "shared"}, want: []string{otherTitle, claude, oldest}},
		{name: "raw path replaced", opts: ListOptions{Project: "old/location"}},
		{name: "title is not project alias", opts: ListOptions{Project: "TARGET"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.ListSessions(ctx, tt.opts)
			require.NoError(t, err)
			var ids []string
			for _, sess := range got {
				ids = append(ids, sess.UUID)
				assert.Nil(t, sess.RawJSONL, "listing is metadata-only")
				assert.Equal(t, "/old/location", sess.ProjectPath)
			}
			assert.Equal(t, tt.want, ids)
		})
	}
	_, err := s.setSessionProjectAt(ctx, oldest, ProjectOptions{Clear: true}, time.Unix(0, 2), "m")
	require.NoError(t, err)
	got, err := s.ListSessions(ctx, ListOptions{Project: "old/location", Limit: 1})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, oldest, got[0].UUID)
	assert.Equal(t, "/old/location", got[0].EffectiveProject())
}

func TestSessionProject_SQLResolverAndLiteralQueries(t *testing.T) {
	for _, state := range []string{"unset", "override", "equal to path", "clear"} {
		t.Run(state, func(t *testing.T) {
			s := newTestVault(t)
			ctx := t.Context()
			db, err := s.getDB(ctx)
			require.NoError(t, err)
			labels := []string{`MiXeD CAFÉ %_\'" *`, "ordinary", "mixed café", `MiXeD CAFÉ ax`}
			var ids []string
			for i, label := range labels {
				uuid := fmt.Sprintf("abcd%04d-1111-2222-3333-444444444444", i)
				ids = append(ids, uuid)
				rec := sampleRecord(uuid)
				rec.Session.ProjectPath = label
				if state == "override" {
					rec.Session.ProjectPath = "/replaced/path"
				}
				rec.Session.EndTime = rec.Session.EndTime.Add(-time.Duration(i) * time.Hour)
				require.NoError(t, s.InsertSession(ctx, rec))
				if state != "unset" {
					opts := ProjectOptions{Name: label}
					if state == "clear" {
						opts = ProjectOptions{Clear: true}
					}
					_, err := s.setSessionProjectAt(ctx, uuid, opts, time.Unix(0, 1), "m")
					require.NoError(t, err)
				}
				sess, err := s.GetSession(ctx, uuid)
				require.NoError(t, err)
				var resolved string
				require.NoError(t, db.QueryRowContext(ctx, `SELECT `+effectiveProjectSQL+sessionMetaJoin+` WHERE s.uuid = ?`, uuid).Scan(&resolved))
				assert.Equal(t, label, resolved)
				assert.Equal(t, sess.EffectiveProject(), resolved)
			}
			for _, tt := range []struct {
				name  string
				query string
				want  []int
			}{
				{name: "percent", query: "%", want: []int{0}},
				{name: "underscore", query: "_", want: []int{0}},
				{name: "backslash", query: `\`, want: []int{0}},
				{name: "single quote", query: "'", want: []int{0}},
				{name: "double quote", query: `"`, want: []int{0}},
				{name: "star is literal", query: "*", want: []int{0}},
				{name: "ASCII folding", query: "mIxEd", want: []int{0, 2, 3}},
				{name: "uppercase accented", query: "cafÉ", want: []int{0, 3}},
				{name: "lowercase accented", query: "CAFé", want: []int{2}},
				{name: "compound literal", query: `%_\'"`, want: []int{0}},
				{name: "SQL syntax is literal", query: `' OR 1=1 --`},
				{name: "empty is unrestricted", want: []int{0, 1, 2, 3}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					got, err := s.ListSessions(ctx, ListOptions{Project: tt.query})
					require.NoError(t, err)
					var gotIDs, wantIDs []string
					for _, sess := range got {
						gotIDs = append(gotIDs, sess.UUID)
					}
					for _, i := range tt.want {
						wantIDs = append(wantIDs, ids[i])
					}
					assert.Equal(t, wantIDs, gotIDs)
				})
			}
		})
	}
}
