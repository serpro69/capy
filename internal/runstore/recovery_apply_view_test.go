package runstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecoveryApplyViewUnknownCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "not committed"
		if committed {
			name = "committed response lost"
		}
		t.Run(name, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			planPending(t, s, o)
			closeStore()
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			commits := 0
			hooks := &recoveryApplyHooks{Commit: func(tx *sql.Tx) error {
				commits++
				if committed {
					require.NoError(t, tx.Commit())
				}
				return errors.New("PRIVATE_ERROR")
			}}
			view, err := applyRecoveryView(t.Context(), s.path, testKey, applyOptions(plan.Token), hooks)
			applyErrorCode(t, err, "recovery_outcome_unknown")
			require.Equal(t, 1, commits)
			require.Nil(t, view.Admission)
			require.Nil(t, view.Recovery)
			require.Equal(t, plan.Token, view.Plan)
			require.Contains(t, view.NextStep, "Receipt absence does not prove nonapplication")
			for _, n := range []int{32768, 1024, 512, 0} {
				bounded := BoundRecoveryApplication(view, n)
				encoded, err := json.Marshal(bounded)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "PRIVATE_ERROR")
				if n == 0 {
					require.LessOrEqual(t, len(encoded)+1, 2048)
				}
				require.Equal(t, plan.Token, bounded.Plan)
			}
			lookup, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Plan: view.Plan, Limit: 16})
			if committed {
				require.NoError(t, err)
				require.NotNil(t, lookup.Receipt)
			} else {
				applyErrorCode(t, err, "receipt_unavailable")
			}
			again, err := ApplyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token))
			require.NoError(t, err)
			if committed {
				require.Equal(t, "already_applied", again.Status)
				require.Equal(t, lookup.Receipt, again.Recovery)
			} else {
				require.Equal(t, "applied", again.Status)
			}
		})
	}
}

func TestRecoveryApplyViewTimeoutCommands(t *testing.T) {
	s, o, closeStore := planFixture(t)
	planPending(t, s, o)
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	options := applyOptions(plan.Token)
	options.Plan = plan.Token[:10] + "\n" + plan.Token[10:]
	options.Explanation = "Stopped 'groups'; $(do-not-execute) password=abcdefghijklmnopqrstuvwx"
	options.Timeout = time.Nanosecond
	view, err := ApplyRecovery(t.Context(), s.path, testKey, options)
	applyErrorCode(t, err, "recovery_timeout")
	require.Nil(t, view.Admission)
	require.Equal(t, plan.Token, view.Plan)
	require.NotContains(t, view.NextCommand, "\n")
	require.NotContains(t, view.NextCommand, "abcdefghijklmnopqrstuvwx")
	require.Contains(t, view.NextCommand, "--plan "+plan.Token)
	require.Contains(t, view.NextCommand, "--reason 'Stopped '\"'\"'groups'\"'\"'; $(do-not-execute)")
	require.Equal(t, "15m0s", view.NextTimeout)
	view.NextCommand = strings.Repeat("optional command", 1000)
	encoded, err := json.Marshal(BoundRecoveryApplication(view, 0))
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded)+1, 2048)
}

func TestRecoveryApplyViewRetainedReceiptFailure(t *testing.T) {
	s, o, closeStore := planFixture(t)
	planPending(t, s, o)
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	options := applyOptions(plan.Token)
	result, err := applyRecovery(t.Context(), s.path, testKey, options, nil)
	require.NoError(t, err)
	for _, code := range []string{"recovery_timeout", "recovery_outcome_unknown", "recovery_receipt_conflict"} {
		t.Run(code, func(t *testing.T) {
			view := recoveryApplicationView(s.path, options, result, failure(code, "current state unavailable"))
			require.Nil(t, view.Admission)
			require.Equal(t, result.Receipt.ID, view.Recovery.ID)
			require.Empty(t, view.Plan)
			require.Empty(t, view.NextTimeout)
			require.Contains(t, view.NextStep, "receipt records an applied recovery")
			require.Contains(t, view.NextStep, "Current blockers are unavailable")
			require.NotContains(t, view.NextStep, "No recovery committed")
			require.NotContains(t, view.NextStep, "Receipt absence")
			require.Contains(t, view.NextCommand, "--receipt-id "+result.Receipt.ID)
			if code == "recovery_outcome_unknown" {
				require.Contains(t, view.Error.Message, "recovery was applied")
			}
		})
	}
}

func TestRecoveryApplyViewCapacitySample(t *testing.T) {
	s, o, closeStore := planFixture(t)
	planPending(t, s, o)
	_, err := s.db.Exec("CREATE TRIGGER exhaust_view_audit AFTER INSERT ON run_resolutions BEGIN UPDATE run_meta SET accounted_bytes=(SELECT max_retained_bytes FROM run_policy)+1; END")
	require.NoError(t, err)
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	view, err := ApplyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token))
	applyErrorCode(t, err, "recovery_capacity")
	require.NotNil(t, view.Capacity)
	require.Positive(t, view.Capacity.MetadataBytes)
	require.Equal(t, plan.Capacity.MetadataBytes, view.Capacity.MetadataBytes)
	require.Equal(t, plan.Capacity.AvailableBytes, view.Capacity.AvailableBytes)
	require.True(t, view.Capacity.Sufficient, "sample precedes injected transactional quota exhaustion; it is not a reservation")
	require.Nil(t, view.Recovery)
}
