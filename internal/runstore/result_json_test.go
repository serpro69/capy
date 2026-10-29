package runstore

import (
	"encoding/json"
	"testing"

	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func publicObject(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var object map[string]any
	require.NoError(t, json.Unmarshal(data, &object))
	return object
}

func requirePublicCleanup(t *testing.T, value any, proof executor.CleanupEvidence) {
	t.Helper()
	object := publicObject(t, value)
	require.Equal(t, map[string]any{"scope": string(proof.Scope), "state": string(proof.State)}, object["cleanup"])
}

func TestPublicRecoveryViewsNormalizeHistoricalProof(t *testing.T) {
	for _, scope := range []executor.CleanupScope{executor.CleanupLinuxDescendants, executor.CleanupDarwinObservation} {
		t.Run(string(scope), func(t *testing.T) {
			s, owner := retentionStore(t, 8<<20)
			row, err := s.Admit(t.Context(), owner, scope, Program{})
			require.NoError(t, err)
			requirePublicCleanup(t, row.Result, row.Cleanup)
			require.NoError(t, s.Pending(t.Context(), row.ID, owner.ID, scope))
			require.NoError(t, s.Started(t.Context(), row.ID, "run"))
			const output = "historical marker\nsecond line\n"
			require.NoError(t, s.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte(output)}))
			// Actual nonzero/signal outcomes survive incomplete cleanup.
			code := 7
			proof := executor.CleanupEvidence{Scope: scope, State: executor.CleanupPending}
			require.NoError(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: proof, Started: true, ExitCode: &code, ObservedBytes: int64(len(output))}))
			for _, cached := range []string{"null", `{"scope":"unknown","state":"retired"}`, `"malformed cached proof"`} {
				_, err := s.db.ExecContext(t.Context(), `UPDATE runs SET result=json_set(CAST(result AS TEXT),'$.version',1,'$.cleanup',json(?)) WHERE id=?`, cached, row.ID)
				require.NoError(t, err)
				loaded, err := s.Load(t.Context(), row.ID)
				require.NoError(t, err)
				require.Equal(t, 2, loaded.Result.Version)
				requirePublicCleanup(t, loaded.Result, proof)
				require.Equal(t, &code, loaded.Result.ExitCode)
				require.Nil(t, loaded.Result.Signal)
				require.Equal(t, "incomplete", loaded.Result.Capture.State)
				for _, raw := range []bool{false, true} {
					page, err := s.Read(t.Context(), row.ID, ReadOptions{Stream: "stdout", MaxBytes: 4096, Raw: raw})
					require.NoError(t, err)
					require.Equal(t, 2, page.Version)
					requirePublicCleanup(t, page.Run, proof)
					require.NotContains(t, publicObject(t, page), "cleanup")
				}
				list, err := s.List(t.Context(), ListOptions{Limit: 4})
				require.NoError(t, err)
				require.Equal(t, 2, list.Version)
				require.Len(t, list.Entries, 1)
				requirePublicCleanup(t, list.Entries[0], proof)
				require.NotContains(t, publicObject(t, list), "cleanup")
				page, err := s.Search(t.Context(), owner, SearchOptions{RunID: row.ID, Queries: []string{"marker"}, Limit: 1})
				require.NoError(t, err)
				require.Equal(t, 2, page.Version)
				requirePublicCleanup(t, page, proof)
				for _, group := range page.Results {
					for _, hit := range group.Hits {
						require.NotContains(t, publicObject(t, hit), "cleanup")
					}
				}
			}
		})
	}
}

func TestPublicRejectionHasNullCleanup(t *testing.T) {
	for _, result := range []any{Rejection("invalid_argument", "invalid"), BatchResult{Version: EnvelopeVersion, Kind: "rejection"}} {
		object := publicObject(t, result)
		require.Equal(t, float64(2), object["version"])
		require.Contains(t, object, "cleanup")
		require.Nil(t, object["cleanup"])
	}
}

func TestPublicPendingCleanupPreservesSignal(t *testing.T) {
	for _, scope := range []executor.CleanupScope{executor.CleanupLinuxDescendants, executor.CleanupDarwinObservation} {
		t.Run(string(scope), func(t *testing.T) {
			s, owner := retentionStore(t, 8<<20)
			row, err := s.Admit(t.Context(), owner, scope, Program{})
			require.NoError(t, err)
			require.NoError(t, s.Pending(t.Context(), row.ID, owner.ID, scope))
			require.NoError(t, s.Started(t.Context(), row.ID, "run"))
			proof := executor.CleanupEvidence{Scope: scope, State: executor.CleanupPending}
			require.NoError(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: proof, Started: true, Signal: 9}))
			loaded, err := s.Load(t.Context(), row.ID)
			require.NoError(t, err)
			requirePublicCleanup(t, loaded.Result, proof)
			require.Nil(t, loaded.Result.ExitCode)
			require.Equal(t, 9, *loaded.Result.Signal)
			require.Equal(t, "interrupted", loaded.Result.State)
		})
	}
}
