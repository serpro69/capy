package runstore

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func TestScopedCleanupSQLAndTypedBoundaries(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	seed, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	for _, column := range []string{"cleanup_scope", "cleanup"} {
		var notNull int
		var def sql.NullString
		require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT "notnull",dflt_value FROM pragma_table_info('runs') WHERE name=?`, column).Scan(&notNull, &def))
		require.Equal(t, 1, notNull)
		require.False(t, def.Valid)
		other, value := "cleanup", "not_spawned"
		if column == "cleanup" {
			other, value = "cleanup_scope", string(executor.CleanupLinuxDescendants)
		}
		_, err := s.db.ExecContext(t.Context(), "INSERT INTO runs(id,generation,slot,created_at,program,result,"+other+") SELECT ?,generation,slot,created_at,program,result,? FROM runs WHERE id=?", uuid.NewString(), value, seed.ID)
		require.Error(t, err, "omitted %s must fail SQL", column)
	}
	for _, scope := range []executor.CleanupScope{"", "unknown", "darwin_process_group_v1", executor.CleanupLinuxDescendants, executor.CleanupDarwinObservation} {
		for _, state := range []executor.CleanupState{"", "unknown", executor.CleanupNotSpawned, executor.CleanupPending, executor.CleanupRetired, executor.CleanupObserved} {
			t.Run(string(scope)+"/"+string(state), func(t *testing.T) {
				want := scope == executor.CleanupLinuxDescendants && (state == executor.CleanupNotSpawned || state == executor.CleanupPending || state == executor.CleanupRetired) || scope == executor.CleanupDarwinObservation && (state == executor.CleanupNotSpawned || state == executor.CleanupPending || state == executor.CleanupObserved)
				c := executor.CleanupEvidence{Scope: scope, State: state}
				require.Equal(t, want, ValidateCleanup(c) == nil)
				require.Equal(t, want && state != executor.CleanupPending, CleanupAccepted(c))
				tx, err := s.db.BeginTx(t.Context(), nil)
				require.NoError(t, err)
				defer tx.Rollback()
				_, err = tx.ExecContext(t.Context(), "UPDATE runs SET cleanup_scope=?,cleanup=? WHERE id=?", scope, state, seed.ID)
				require.Equal(t, want, err == nil, "%v", err)
			})
		}
	}
	for _, column := range []string{"cleanup_scope", "cleanup"} {
		_, err := s.db.ExecContext(t.Context(), "UPDATE runs SET "+column+"=NULL WHERE id=?", seed.ID)
		require.Error(t, err)
	}
	_, err = s.Admit(t.Context(), o, "", Program{})
	requireErrorCode(t, err, "cleanup_unverified")
}

func TestScopedCleanupTransitions(t *testing.T) {
	for _, scope := range []executor.CleanupScope{executor.CleanupLinuxDescendants, executor.CleanupDarwinObservation} {
		t.Run(string(scope), func(t *testing.T) {
			s, o := retentionStore(t, 8<<20)
			row, err := s.Admit(t.Context(), o, scope, Program{})
			require.NoError(t, err)
			complete := executor.CleanupRetired
			other := executor.CleanupDarwinObservation
			if scope == executor.CleanupDarwinObservation {
				complete, other = executor.CleanupObserved, executor.CleanupLinuxDescendants
			}
			require.Error(t, s.Pending(t.Context(), row.ID, o.ID, other))
			require.Error(t, s.Pending(t.Context(), row.ID, uuid.NewString(), scope))
			require.Error(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: scope, State: complete}, Started: true}))
			require.NoError(t, s.Pending(t.Context(), row.ID, o.ID, scope))
			require.Error(t, s.Pending(t.Context(), row.ID, o.ID, scope))
			require.NoError(t, s.Started(t.Context(), row.ID, "run"))
			for _, bad := range []executor.CleanupEvidence{{}, {Scope: other, State: executor.CleanupPending}, {Scope: scope, State: executor.CleanupNotSpawned}} {
				require.Error(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: bad, Started: true}))
			}
			code := 7
			proof := executor.CleanupEvidence{Scope: scope, State: complete}
			require.NoError(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: proof, Started: true, ExitCode: &code, CaptureLost: true, CaptureReason: "append_failed"}))
			got, err := s.Load(t.Context(), row.ID)
			require.NoError(t, err)
			require.Equal(t, proof, got.Cleanup)
			require.Equal(t, proof, got.Result.Cleanup)
			require.Equal(t, &code, got.Result.ExitCode)
			require.Equal(t, "incomplete", got.Result.Capture.State)
			require.Error(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: proof}))
			wire, err := json.Marshal(got.Result)
			require.NoError(t, err)
			require.Contains(t, string(wire), `"cleanup":{"scope":"`+string(scope)+`","state":"`+string(complete)+`"}`)
			expireRun(t, s, row.ID)
			_, err = s.Maintain(t.Context())
			require.NoError(t, err)
			_, err = s.Load(t.Context(), row.ID)
			requireErrorCode(t, err, "run_unavailable")
			require.NoError(t, o.Lease.Close())
			next, err := s.ClaimOwner(t.Context())
			require.NoError(t, err)
			require.Equal(t, o.Slot, next.Slot)
			require.NoError(t, next.Lease.Close())
		})
	}
}

func TestScopedCleanupPendingPreservesQuarantine(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	row, err := s.Admit(t.Context(), o, executor.CleanupDarwinObservation, Program{})
	require.NoError(t, err)
	require.NoError(t, s.Pending(t.Context(), row.ID, o.ID, row.Cleanup.Scope))
	require.NoError(t, s.Started(t.Context(), row.ID, "run"))
	require.NoError(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: row.Cleanup.Scope, State: executor.CleanupPending}, Started: true}))
	expireRun(t, s, row.ID)
	require.NoError(t, o.Lease.Close())
	report, err := s.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, report.UnverifiedSlots)
	_, err = s.Maintain(t.Context())
	require.NoError(t, err)
	got, err := s.Load(t.Context(), row.ID)
	require.NoError(t, err)
	require.Equal(t, executor.CleanupPending, got.Cleanup.State)
	require.Nil(t, got.Result.ExpiresAt)
	_, err = s.ClaimOwner(t.Context())
	requireErrorCode(t, err, "cleanup_unverified")
}

func TestScopedDarwinRecoveryAcceptsCompletedObservation(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	for _, state := range []executor.CleanupState{executor.CleanupNotSpawned, executor.CleanupObserved} {
		row, err := s.Admit(t.Context(), o, executor.CleanupDarwinObservation, Program{})
		require.NoError(t, err)
		if state == executor.CleanupObserved {
			require.NoError(t, s.Pending(t.Context(), row.ID, o.ID, row.Cleanup.Scope))
			require.NoError(t, s.Started(t.Context(), row.ID, "run"))
			// Synthetic durable proof, mirroring the Linux recovery fixture.
			// This tests storage policy, not native process observation.
			_, err := s.db.ExecContext(t.Context(), "UPDATE runs SET cleanup='observed' WHERE id=?", row.ID)
			require.NoError(t, err)
		}
	}
	require.NoError(t, o.Lease.Close())
	report, err := s.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, 2, report.RecoveredRuns)
	require.Zero(t, report.UnverifiedSlots)
	next, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	require.NoError(t, next.Lease.Close())
}

func TestScopedBatchProofSurvivesEviction(t *testing.T) {
	for _, scope := range []executor.CleanupScope{executor.CleanupLinuxDescendants, executor.CleanupDarwinObservation} {
		t.Run(string(scope), func(t *testing.T) {
			s, o := retentionStore(t, 8<<20)
			b, children, err := s.AdmitBatch(t.Context(), o, scope, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "first"}, {Command: "second"}}})
			require.NoError(t, err)
			require.Equal(t, executor.CleanupNotSpawned, b.Cleanup.State)
			completed := executor.CleanupRetired
			if scope == executor.CleanupDarwinObservation {
				completed = executor.CleanupObserved
			}
			first := children[0]
			require.NoError(t, s.Pending(t.Context(), first.ID, o.ID, scope))
			b, err = s.BatchStatus(t.Context(), b.BatchID)
			require.NoError(t, err)
			require.Equal(t, executor.CleanupPending, b.Cleanup.State)
			require.NoError(t, s.Started(t.Context(), first.ID, "run"))
			require.NoError(t, s.Finish(t.Context(), first.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: scope, State: completed}, Started: true}))
			b, err = s.BatchStatus(t.Context(), b.BatchID)
			require.NoError(t, err)
			require.Equal(t, executor.CleanupPending, b.Cleanup.State, "queued sibling prevents completed aggregate")
			require.NoError(t, s.Finish(t.Context(), children[1].ID, Completion{Cleanup: children[1].Cleanup, Reason: "queue_timeout"}))
			for _, child := range children {
				expireRun(t, s, child.ID)
			}
			_, err = s.Maintain(t.Context())
			require.NoError(t, err)
			b, err = s.BatchStatus(t.Context(), b.BatchID)
			require.NoError(t, err)
			require.Equal(t, executor.CleanupEvidence{Scope: scope, State: completed}, b.Cleanup)
			requirePublicCleanup(t, b, b.Cleanup)
			list, err := s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 10})
			require.NoError(t, err)
			require.Len(t, list.Entries, 2)
			for _, child := range list.Entries {
				require.False(t, child.OutputAvailable)
				requirePublicCleanup(t, child, child.Cleanup)
				require.Equal(t, scope, child.Cleanup.Scope)
			}
			page, err := s.Search(t.Context(), o, SearchOptions{BatchID: b.BatchID, Queries: []string{"marker"}, Limit: 1})
			require.NoError(t, err, "proof snapshot must release the one-connection pool before indexing")
			require.Len(t, page.Children, 2)
			requirePublicCleanup(t, page, b.Cleanup)
			require.Equal(t, completed, page.Children[0].Cleanup.State)
			require.Equal(t, executor.CleanupNotSpawned, page.Children[1].Cleanup.State)
			for _, child := range page.Children {
				requirePublicCleanup(t, child, child.Cleanup)
				require.Equal(t, scope, child.Cleanup.Scope)
			}
		})
	}
}

func TestScopedBatchSnapshotAcrossCompletion(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	b, children, err := s.AdmitBatch(t.Context(), o, executor.CleanupDarwinObservation, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "first"}}})
	require.NoError(t, err)
	row := children[0]
	require.NoError(t, s.Pending(t.Context(), row.ID, o.ID, row.Cleanup.Scope))
	require.NoError(t, s.Started(t.Context(), row.ID, "run"))
	writer, err := Open(t.Context(), s.path, testKey, false)
	require.NoError(t, err)
	defer writer.Close()
	tx, err := s.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer tx.Rollback()
	before, _, _, err := batchMetadata(t.Context(), tx, b.BatchID)
	require.NoError(t, err)
	require.Nil(t, before.CompletedAt)
	require.NoError(t, writer.Finish(t.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: row.Cleanup.Scope, State: executor.CleanupObserved}, Started: true}))
	copies, aggregate, err := batchChildren(t.Context(), tx, b.BatchID)
	require.NoError(t, err)
	require.Equal(t, executor.CleanupPending, aggregate.State)
	require.False(t, copies[0].terminal)
	require.NoError(t, tx.Commit())
	after, err := s.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, executor.CleanupObserved, after.Cleanup.State)
	require.NotNil(t, after.CompletedAt)
}

func TestScopedBatchRejectsInconsistentCopies(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	b, children, err := s.AdmitBatch(t.Context(), o, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "first"}}})
	require.NoError(t, err)
	row := children[0]
	var original []byte
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT result FROM run_batch_children WHERE run_id=?", row.ID).Scan(&original))
	for _, shape := range []string{`{"scope":"linux_descendants_v1","state":"pending"}`, `{"scope":"darwin_process_group_observation_v1","state":"not_spawned"}`, `{"scope":"darwin_process_group_observation_v1","state":"retired"}`, `null`} {
		t.Run(shape, func(t *testing.T) {
			_, err := s.db.ExecContext(t.Context(), "UPDATE run_batch_children SET result=json_set(CAST(result AS TEXT),'$.cleanup',json(?)) WHERE run_id=?", shape, row.ID)
			require.NoError(t, err)
			defer func() {
				_, err := s.db.ExecContext(t.Context(), "UPDATE run_batch_children SET result=? WHERE run_id=?", original, row.ID)
				require.NoError(t, err)
			}()
			_, err = s.BatchStatus(t.Context(), b.BatchID)
			requireErrorCode(t, err, "cleanup_unverified")
			_, err = s.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 1})
			requireErrorCode(t, err, "cleanup_unverified")
			_, err = s.Search(t.Context(), o, SearchOptions{BatchID: b.BatchID, Queries: []string{"marker"}, Limit: 1})
			requireErrorCode(t, err, "cleanup_unverified")
			_, err = s.Load(t.Context(), row.ID)
			requireErrorCode(t, err, "cleanup_unverified")
			_, err = s.List(t.Context(), ListOptions{Limit: 1})
			requireErrorCode(t, err, "cleanup_unverified")
			requireErrorCode(t, s.Pending(t.Context(), row.ID, o.ID, row.Cleanup.Scope), "cleanup_unverified")
		})
	}
}

func TestScopedBatchRejectsMixedScopesAfterEviction(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	b, children, err := s.AdmitBatch(t.Context(), o, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "one"}, {Command: "two"}}})
	require.NoError(t, err)
	for _, row := range children {
		require.NoError(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: row.Cleanup}))
		expireRun(t, s, row.ID)
	}
	_, err = s.Maintain(t.Context())
	require.NoError(t, err)
	var original []byte
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT result FROM run_batch_children WHERE run_id=?", children[1].ID).Scan(&original))
	_, err = s.db.ExecContext(t.Context(), "UPDATE run_batch_children SET result=json_set(CAST(result AS TEXT),'$.cleanup.scope',?) WHERE run_id=?", executor.CleanupDarwinObservation, children[1].ID)
	require.NoError(t, err)
	defer func() {
		_, err := s.db.ExecContext(t.Context(), "UPDATE run_batch_children SET result=? WHERE run_id=?", original, children[1].ID)
		require.NoError(t, err)
	}()
	_, err = s.BatchStatus(t.Context(), b.BatchID)
	requireErrorCode(t, err, "cleanup_unverified")
	require.ErrorContains(t, err, "mixed cleanup scopes")
	_, err = s.db.ExecContext(t.Context(), "UPDATE run_batches SET expires_at=0 WHERE id=?", b.BatchID)
	require.NoError(t, err)
	_, err = s.Maintain(t.Context())
	requireErrorCode(t, err, "cleanup_unverified")
	var count int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_batch_children WHERE batch_id=?", b.BatchID).Scan(&count))
	require.Equal(t, 2, count, "invalid batch proof must not be evicted")
}

func TestSchemaEightRefusedWithoutRecreation(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	row := retentionRun(t, s, o, "preserve schema-eight history", true)
	originalID := s.ID()
	// Attachment checks the authenticated version before interpreting columns.
	// Task 6 supplies the actual v8 table-copy migration fixture.
	_, err := s.db.ExecContext(t.Context(), "UPDATE run_meta SET version=8")
	require.NoError(t, err)
	defer func() {
		_, err := s.db.ExecContext(t.Context(), "UPDATE run_meta SET version=9")
		require.NoError(t, err)
	}()
	for _, create := range []bool{false, true} {
		opened, err := Open(t.Context(), s.path, testKey, create)
		require.Nil(t, opened)
		requireErrorCode(t, err, "schema_mismatch")
		require.ErrorContains(t, err, "schema 8")
		require.ErrorContains(t, err, "schema 10")
	}
	var version int
	var id string
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT version,store_id FROM run_meta").Scan(&version, &id))
	require.Equal(t, 8, version)
	require.Equal(t, originalID, id)
	page, err := s.ReadRaw(t.Context(), row.ID, "stdout", "", 4096)
	require.NoError(t, err)
	require.Equal(t, "preserve schema-eight history", string(page.Fragments[0].Data))
	require.WithinDuration(t, time.Now(), *page.Run.CompletedAt, time.Minute)
}
