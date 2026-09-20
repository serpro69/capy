package vault

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionProject_ChunkFiltering(t *testing.T) {
	s := newTestVault(t)
	ctx := t.Context()
	const target = "dddddddd-1111-2222-3333-444444444444"
	const parent = "eeeeeeee-1111-2222-3333-444444444444"
	// Twelve better-ranked sessions exceed the retrieval engine's minimum
	// candidate pool of ten for Limit: 1. Filtering after retrieval loses target.
	for i := range 13 {
		uuid := fmt.Sprintf("abcd%04d-1111-2222-3333-444444444444", i)
		path, label := "/physical/elsewhere", "unrelated"
		if i == 12 {
			uuid, path, label = target, "/physical/worktree", "labelonlyquasar"
		}
		rec := sampleRecord(uuid)
		rec.Session.ProjectPath = path
		rec.Chunks = []Chunk{{Title: "Conversation", ContentText: "zorbed pterodactyl " + strings.Repeat("filler ", i*20),
			SubagentID: "agent-7", FirstLineIndex: 42}}
		if uuid == target {
			rec.Session.Platform = PlatformCodex
			rec.Session.ParentUUID = parent
		}
		require.NoError(t, s.InsertSession(ctx, rec))
		_, err := s.SetSessionProject(ctx, uuid, ProjectOptions{Name: label})
		require.NoError(t, err)
	}
	_, err := s.RenameSession(ctx, target, RenameOptions{Name: "Independent title"})
	require.NoError(t, err)

	for _, layer := range []struct{ name, query string }{{"porter", "zorbing"}, {"trigram", "terodact"}} {
		t.Run(layer.name, func(t *testing.T) {
			unscoped := searchChunks(t, s, SearchOptions{Query: layer.query, Limit: 10})
			require.Len(t, unscoped, 10)
			for _, hit := range unscoped {
				require.NotEqual(t, target, hit.SessionUUID, "fixture excludes target from unscoped top ten")
				require.Equal(t, layer.name, hit.MatchLayer)
			}
			for _, tt := range []struct {
				name string
				opts SearchOptions
				want bool
			}{
				{"effective before candidate limit", SearchOptions{Project: "LABELONLY"}, true},
				{"raw before candidate limit", SearchOptions{ProjectPath: "worktree"}, true},
				{"explicit replaced path", SearchOptions{Project: "physical"}, false},
				{"label is not raw path", SearchOptions{ProjectPath: "labelonly"}, false},
				{"combined filters retain child", SearchOptions{Project: "labelonly", Platform: PlatformCodex,
					After: time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC), Before: time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)}, true},
				{"wrong platform", SearchOptions{Project: "labelonly", Platform: PlatformClaudeCode}, false},
				{"too late", SearchOptions{Project: "labelonly", After: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)}, false},
				{"too early", SearchOptions{Project: "labelonly", Before: time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)}, false},
			} {
				t.Run(tt.name, func(t *testing.T) {
					tt.opts.Query, tt.opts.Limit = layer.query, 1
					hits := searchChunks(t, s, tt.opts)
					if !tt.want {
						assert.Empty(t, hits)
						return
					}
					require.Len(t, hits, 1)
					hit := hits[0]
					assert.Equal(t, target, hit.SessionUUID)
					assert.Equal(t, "labelonlyquasar", hit.Project)
					require.NotNil(t, hit.CustomProject)
					assert.Equal(t, hit.Project, *hit.CustomProject)
					assert.Equal(t, "/physical/worktree", hit.ProjectPath)
					assert.Equal(t, "Independent title", hit.Title)
					assert.Equal(t, PlatformCodex, hit.Platform)
					assert.Equal(t, parent, hit.ParentUUID)
					assert.Equal(t, "agent-7", hit.SubagentID)
					assert.Equal(t, 42, hit.LineIndex)
					assert.Equal(t, layer.name, hit.MatchLayer)
				})
			}
		})
	}
	for _, query := range []string{"zorbing", "", " "} {
		_, err := s.SearchChunks(ctx, SearchOptions{Query: query, Project: "labelonly", ProjectPath: "physical"})
		require.ErrorContains(t, err, "mutually exclusive")
	}
	assert.Empty(t, searchChunks(t, s, SearchOptions{Query: "labelonlyquasar"}), "project labels never enter MATCH")
	_, err = s.SetSessionProject(ctx, target, ProjectOptions{Clear: true})
	require.NoError(t, err)
	for _, query := range []string{"zorbing", "terodact"} {
		hits := searchChunks(t, s, SearchOptions{Query: query, Project: "worktree", Limit: 1})
		require.Len(t, hits, 1)
		assert.Equal(t, target, hits[0].SessionUUID)
		assert.Equal(t, hits[0].ProjectPath, hits[0].Project)
		assert.Nil(t, hits[0].CustomProject)
		assert.Empty(t, searchChunks(t, s, SearchOptions{Query: query, Project: "labelonly"}))
	}
}

func TestSessionProject_ChunkOrderingAndLiteralQueries(t *testing.T) {
	s := newChunkSearchVault(t)
	ctx := t.Context()
	before := searchChunks(t, s, SearchOptions{Query: "gizmoflux"})
	require.Len(t, before, 2)
	const label = `Équipe %_\'"`
	for _, uuid := range []string{chunkSearchUUIDB, chunkSearchUUIDC} {
		_, err := s.SetSessionProject(ctx, uuid, ProjectOptions{Name: label})
		require.NoError(t, err)
	}
	after := searchChunks(t, s, SearchOptions{Query: "gizmoflux"})
	require.Len(t, after, len(before), "joins do not duplicate results")
	for i, hit := range after {
		assert.Equal(t, label, hit.Project)
		require.NotNil(t, hit.CustomProject)
		assert.Equal(t, label, *hit.CustomProject)
		hit.Project, hit.CustomProject = before[i].Project, before[i].CustomProject
		assert.Equal(t, before[i], hit, "order, titles, snippets and navigation metadata stay unchanged")
	}
	for _, tt := range []struct {
		name, query string
		want        bool
	}{
		{"full label", label, true}, {"ASCII case fold", "QUIPE", true},
		{"percent", "%", true}, {"underscore", "_", true}, {"backslash", `\`, true},
		{"quotes", `'"`, true}, {"non-ASCII case distinct", "équipe", false},
		{"literal percent miss", "missing%", false}, {"literal underscore miss", "quipe_", false},
		{"replaced path", "projB", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hits := searchChunks(t, s, SearchOptions{Query: "gizmoflux", Project: tt.query})
			if tt.want {
				assert.Equal(t, after, hits, "equivalent candidate sets keep their order")
			} else {
				assert.Empty(t, hits)
			}
		})
	}
	for _, uuid := range []string{chunkSearchUUIDB, chunkSearchUUIDC} {
		_, err := s.SetSessionProject(ctx, uuid, ProjectOptions{Clear: true})
		require.NoError(t, err)
	}
	assert.Equal(t, before, searchChunks(t, s, SearchOptions{Query: "gizmoflux"}))
	_, err := s.SetSessionProject(ctx, chunkSearchUUIDB, ProjectOptions{Name: "/home/user/projB"})
	require.NoError(t, err)
	hits := searchChunks(t, s, SearchOptions{Query: "zorbing", Project: "projB"})
	require.Len(t, hits, 1)
	assert.Equal(t, hits[0].ProjectPath, hits[0].Project)
	require.NotNil(t, hits[0].CustomProject, "path-equal labels retain custom provenance")
}
