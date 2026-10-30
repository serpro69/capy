package runstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func recoverySearchOwner(t *testing.T, s *Store) *Owner {
	t.Helper()
	o, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, o.Lease.Close()) })
	return o
}

func assertFrozenSearchHistory(t *testing.T, s *Store, id string, before []byte) {
	t.Helper()
	require.Equal(t, before, applyHistory(t, s.db, id), "indexing must preserve execution, payload and sanitizer checkpoints")
	d, err := readRunResolution(t.Context(), s.db, id)
	require.NoError(t, err)
	var original []byte
	require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", id).Scan(&original))
	digest := sha256.Sum256(original)
	require.Equal(t, original, d.Original)
	require.Equal(t, digest[:], d.Digest)
}

func TestRecoverySearchSafeTailsAndExistingCursors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tail    string
		pending bool
	}{
		{"credential", "password=unfinished", false},
		{"private", "<private>WITHHELD_SECRET", false},
		{"credential prefix", "passw", true},
		{"private prefix", "<priv", true},
		{"split UTF8", "\xe2\x82", true},
		{"binary", "\x00\xff\xfe", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, old, closeStore := planFixture(t)
			r, err := s.Admit(t.Context(), old, executor.CleanupLinuxDescendants, Program{})
			require.NoError(t, err)
			require.NoError(t, s.Pending(t.Context(), r.ID, old.ID, r.Cleanup.Scope))
			require.NoError(t, s.Started(t.Context(), r.ID, "run"))
			var seq uint64
			appendSearchText(t, s, r.ID, "stdout", "safe diagnostic!\n", &seq)
			appendSearchText(t, s, r.ID, "stdout", tc.tail[:len(tc.tail)/2], &seq)
			appendSearchText(t, s, r.ID, "stdout", tc.tail[len(tc.tail)/2:], &seq)
			raw, err := s.ReadRaw(t.Context(), r.ID, "stdout", "", 5)
			require.NoError(t, err)
			text, err := s.ReadProjection(t.Context(), r.ID, "stdout", "", 5)
			require.NoError(t, err)
			oldText, err := s.ReadProjection(t.Context(), r.ID, "stdout", *text.NextCursor, 4096)
			require.NoError(t, err)
			oldRaw, err := s.ReadRaw(t.Context(), r.ID, "stdout", *raw.NextCursor, 4096)
			require.NoError(t, err)
			before := applyHistory(t, s.db, r.ID)
			applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			o := recoverySearchOwner(t, fresh)
			require.Equal(t, old.Slot, o.Slot)
			require.NotEqual(t, old.ID, o.ID)
			gotText, err := fresh.ReadProjection(t.Context(), r.ID, "stdout", *text.NextCursor, 4096)
			require.NoError(t, err)
			require.Equal(t, oldText.Fragments, gotText.Fragments)
			require.Equal(t, oldText.NextCursor, gotText.NextCursor)
			require.Equal(t, oldText.EOF, gotText.EOF)
			require.True(t, gotText.EndOfAvailable)
			require.Equal(t, tc.pending, gotText.RedactionPending)
			require.True(t, gotText.Run.Recovery.CaptureClosed)
			require.False(t, gotText.EOF)
			gotRaw, err := fresh.Read(t.Context(), r.ID, ReadOptions{Raw: true, Stream: "stdout", Cursor: *raw.NextCursor, MaxBytes: 4096})
			require.NoError(t, err)
			require.Equal(t, oldRaw.Fragments, gotRaw.Fragments)
			require.True(t, gotRaw.Raw)
			require.Equal(t, "base64", gotRaw.Encoding)
			require.False(t, gotRaw.EOF)
			require.True(t, gotRaw.EndOfAvailable)
			page, err := fresh.Search(t.Context(), o, SearchOptions{RunID: r.ID, Queries: []string{"diagnostic"}, Limit: 5})
			require.NoError(t, err)
			require.Nil(t, page.Error)
			require.NotEmpty(t, page.Results[0].Hits)
			require.True(t, page.Recovery.CaptureClosed)
			if tc.pending {
				require.True(t, page.Partial)
				require.Equal(t, "partial", page.Index.State)
			}
			for _, table := range []string{"run_search_fts", "run_search_trigram"} {
				var leaked int
				require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM "+table+" WHERE instr(content,'unfinished')>0 OR instr(content,'WIT')>0 OR instr(content,'HHELD_SECRET')>0 OR instr(content,'passw')>0 OR instr(content,'<priv')>0").Scan(&leaked))
				require.Zero(t, leaked)
			}
			assertFrozenSearchHistory(t, fresh, r.ID, before)
		})
	}
}

func TestRecoverySearchResumesAndOverlaysEveryView(t *testing.T) {
	s, old, closeStore := planFixture(t)
	r := planPending(t, s, old)
	seq := uint64(1)
	// Complete the withheld credential, then provide more than one index batch.
	appendSearchText(t, s, r.ID, "stdout", "!\n"+strings.Repeat("passing diagnostic!\n", 6000)+"needle_marker!\n", &seq)
	before := applyHistory(t, s.db, r.ID)
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	o := recoverySearchOwner(t, fresh)
	first, err := fresh.Search(t.Context(), o, SearchOptions{RunID: r.ID, Queries: []string{"needle_marker"}, Limit: 5})
	require.NoError(t, err)
	require.Nil(t, first.Error)
	require.True(t, first.Partial)
	assertFrozenSearchHistory(t, fresh, r.ID, before)
	page := searchToReady(t, fresh, o, r.ID, "needle_marker")
	require.NotEmpty(t, page.Results[0].Hits)
	require.Greater(t, page.Coverage["stdout"], first.Coverage["stdout"])
	row, err := fresh.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.Equal(t, page.Index, row.Result.Index)
	require.Nil(t, row.Result.CompletedAt)
	require.Equal(t, executor.CleanupPending, row.Cleanup.State)
	list, err := fresh.List(t.Context(), ListOptions{Limit: 64})
	require.NoError(t, err)
	require.Len(t, list.Entries, 1)
	require.Equal(t, &page.Index, list.Entries[0].Index)
	hit := page.Results[0].Hits[0]
	read, err := fresh.ReadProjection(t.Context(), r.ID, hit.Stream, hit.ReadCursor, 4096)
	require.NoError(t, err)
	require.Contains(t, pageText(read), "needle_marker")
	require.Equal(t, page.Index, read.Run.Index)
	assertFrozenSearchHistory(t, fresh, r.ID, before)
	assertRecoveryAccounting(t, fresh)

	_, err = fresh.db.Exec("DELETE FROM run_search_state WHERE run_id=?", r.ID)
	require.NoError(t, err)
	_, err = fresh.Load(t.Context(), r.ID)
	requireErrorCode(t, err, "recovery_inconsistent")
	_, err = fresh.List(t.Context(), ListOptions{Limit: 64})
	requireErrorCode(t, err, "recovery_inconsistent")
	_, err = fresh.Search(t.Context(), o, SearchOptions{RunID: r.ID, Queries: []string{"needle_marker"}, Limit: 1})
	requireErrorCode(t, err, "recovery_inconsistent")
	require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
}

func TestRecoverySearchFailureAndWorkerAuthority(t *testing.T) {
	for _, mode := range []string{"quota", "layer", "worker loss", "lease binding", "quarantine"} {
		t.Run(mode, func(t *testing.T) {
			s, old, closeStore := planFixture(t)
			r := planPending(t, s, old)
			before := applyHistory(t, s.db, r.ID)
			applyRetentionFixture(t, s, closeStore)
			fresh := attachBatchFixture(t, s)
			o := recoverySearchOwner(t, fresh)
			claimed, err := fresh.ClaimIndex(t.Context(), o, r.ID)
			require.NoError(t, err)
			require.True(t, claimed)
			switch mode {
			case "quota":
				require.NoError(t, fresh.ReleaseIndex(t.Context(), o, r.ID))
				fresh.policy.MaxRetainedBytes = assertRecoveryAccounting(t, fresh)
			case "layer":
				require.NoError(t, fresh.indexBatch(t.Context(), o, r.ID))
				require.NoError(t, fresh.ReleaseIndex(t.Context(), o, r.ID))
				_, err = fresh.db.Exec("DROP TABLE run_search_trigram")
				require.NoError(t, err)
			case "worker loss":
				require.NoError(t, fresh.indexBatch(t.Context(), o, r.ID))
				// Claim again to model death after claim publication, before work.
				require.NoError(t, fresh.ReleaseIndex(t.Context(), o, r.ID))
				claimed, err = fresh.ClaimIndex(t.Context(), o, r.ID)
				require.NoError(t, err)
				require.True(t, claimed)
				require.NoError(t, o.Lease.Close())
				_, err = fresh.Reconcile(t.Context())
				require.NoError(t, err)
				row, err := fresh.Load(t.Context(), r.ID)
				require.NoError(t, err)
				require.Equal(t, "index_unavailable", row.Result.Index.Error.Code)
				o = recoverySearchOwner(t, fresh)
			case "lease binding":
				forged := *o
				forged.ID = old.ID
				require.Error(t, fresh.indexBatch(t.Context(), &forged, r.ID))
				require.Error(t, fresh.failIndex(t.Context(), &forged, r.ID, &Error{Code: "index_failed"}))
				other, _, _ := planFixture(t)
				require.Error(t, other.indexBatch(t.Context(), o, r.ID))
				require.NoError(t, fresh.ReleaseIndex(t.Context(), o, r.ID))
				require.Error(t, fresh.indexBatch(t.Context(), o, r.ID))
			case "quarantine":
				require.NoError(t, fresh.Quarantine(t.Context()))
				require.Error(t, fresh.indexBatch(t.Context(), o, r.ID))
				require.NoError(t, fresh.failIndex(t.Context(), o, r.ID, &Error{Code: "index_failed"}))
				require.NoError(t, fresh.ReleaseIndex(t.Context(), o, r.ID))
				row, err := fresh.Load(t.Context(), r.ID)
				require.NoError(t, err)
				require.Equal(t, "failed", row.Result.Index.State)
				_, err = fresh.ClaimIndex(t.Context(), o, r.ID)
				requireErrorCode(t, err, "cleanup_unverified")
				page, err := fresh.Search(t.Context(), o, SearchOptions{RunID: r.ID, Queries: []string{"prefix"}, Limit: 1})
				require.NoError(t, err)
				require.Equal(t, "cleanup_unverified", page.Error.Code)
				require.NotNil(t, page.Error.RecoveryHint)
				require.Equal(t, row.Result.Index, page.Index)
			}
			if mode != "quarantine" {
				page, err := fresh.Search(t.Context(), o, SearchOptions{RunID: r.ID, Queries: []string{"prefix"}, Limit: 1})
				require.NoError(t, err)
				if mode == "quota" {
					require.Equal(t, "index_quota_blocked", page.Error.Code)
					require.Equal(t, "index_capacity", page.Index.Error.Code)
				} else if mode == "layer" {
					require.Equal(t, "index_search_failed", page.Error.Code)
					require.Equal(t, "index_unavailable", page.Index.Error.Code)
					require.Empty(t, page.Results)
				} else {
					require.Nil(t, page.Error)
				}
				row, err := fresh.Load(t.Context(), r.ID)
				require.NoError(t, err)
				require.Equal(t, page.Index, row.Result.Index)
			}
			assertFrozenSearchHistory(t, fresh, r.ID, before)
		})
	}
}

func TestIndexFailureAfterOwnerQuarantinePreservesOriginalContract(t *testing.T) {
	s, o, _ := planFixture(t)
	r := planPending(t, s, o)
	claimed, err := s.ClaimIndex(t.Context(), o, r.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	require.NoError(t, s.failIndex(t.Context(), o, r.ID, &Error{Code: "index_failed"}))
	require.NoError(t, s.ReleaseIndex(t.Context(), o, r.ID))
	row, err := s.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", row.Result.Index.State)
	var claims int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM run_work_claims WHERE run_id=?", r.ID).Scan(&claims))
	require.Zero(t, claims)
}

func TestRecoverySearchBatchAvailabilityAndLateRecheck(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 2)
	for _, r := range children {
		startRecoveryChild(t, s, o, r)
	}
	finishRecoveryChild(t, s, children[0], 0)
	require.NoError(t, s.Append(t.Context(), children[1].ID, executor.OutputEvent{Sequence: 2, Phase: "run", Stream: "stdout", Data: []byte("!\n<priv")}))
	before := applyHistory(t, s.db, children[1].ID)
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	worker := recoverySearchOwner(t, fresh)
	page, err := fresh.Search(t.Context(), worker, SearchOptions{BatchID: b.BatchID, Queries: []string{"retained"}, Limit: 5})
	require.NoError(t, err)
	require.Len(t, page.Children, 2)
	require.NotNil(t, page.Recovery)
	require.NotNil(t, page.Children[1].Recovery)
	require.True(t, page.Children[1].OutputAvailable)
	require.True(t, page.Partial)
	require.Equal(t, "partial", page.Children[1].Index.State)
	require.Equal(t, "partial", page.Index.State)
	require.NotEmpty(t, page.Results[0].Hits)
	list, err := fresh.List(t.Context(), ListOptions{BatchID: b.BatchID, Limit: 64})
	require.NoError(t, err)
	require.Equal(t, &page.Children[1].Index, list.Entries[1].Index)
	assertFrozenSearchHistory(t, fresh, children[1].ID, before)
	// Make only the disposed payload expire; retain the independently timed
	// batch history, as can happen when a safe sibling closes it much later.
	ageRetentionFixture(t, fresh, 25*time.Hour)
	require.NoError(t, fresh.recheckBatchSearch(t.Context(), b.BatchID, &page))
	require.True(t, page.Partial)
	require.False(t, page.Children[1].OutputAvailable)
	for _, hit := range page.Results[0].Hits {
		require.NotEqual(t, children[1].ID, hit.RunID)
	}
	_, err = fresh.Search(t.Context(), worker, SearchOptions{RunID: children[1].ID, Queries: []string{"retained"}, Limit: 5})
	requireErrorCode(t, err, "run_unavailable")
	for range 20 {
		report := retainFixture(t, fresh, fresh.policy.MaxRetainedBytes, true)
		if !report.PendingDeletion {
			break
		}
	}
	page, err = fresh.Search(t.Context(), worker, SearchOptions{BatchID: b.BatchID, Queries: []string{"notpresent"}, Limit: 5})
	require.NoError(t, err)
	require.True(t, page.Partial)
	require.False(t, page.Children[1].OutputAvailable)
	require.NotNil(t, page.Children[1].Recovery)
	require.Equal(t, "partial", page.Index.State)
	var count int
	require.NoError(t, fresh.db.QueryRow("SELECT count(*) FROM run_search_state WHERE run_id=?", children[1].ID).Scan(&count))
	require.Zero(t, count)
	assertRecoveryAccounting(t, fresh)
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	require.False(t, bytes.Contains(encoded, []byte("unfinished")))
}

func TestRecoverySearchBatchKeepsRecoveryHint(t *testing.T) {
	s, o, b, children, closeStore := recoveryBatchFixture(t, executor.CleanupLinuxDescendants, 2)
	startRecoveryChild(t, s, o, children[0])
	// Independent projection failure on the later, execution-terminal sibling.
	_, err := s.db.Exec("UPDATE run_streams SET checkpoint=? WHERE run_id=? AND stream='stdout'", []byte(`{"version":999}`), children[1].ID)
	require.NoError(t, err)
	startRecoveryChild(t, s, o, children[1])
	finishRecoveryChild(t, s, children[1], 0)
	applyRetentionFixture(t, s, closeStore)
	fresh := attachBatchFixture(t, s)
	worker := recoverySearchOwner(t, fresh)
	require.NoError(t, fresh.Quarantine(t.Context()))
	page, err := fresh.Search(t.Context(), worker, SearchOptions{BatchID: b.BatchID, Queries: []string{"retained"}, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, "cleanup_unverified", page.Error.Code)
	require.NotNil(t, page.Error.RecoveryHint)
	require.Equal(t, "redaction_unavailable", page.Children[1].Index.Error.Code)
	require.True(t, page.Partial)
}
