package execution

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func terminalFixture() notice {
	code := 7
	return notice{Version: protocolVersion, ExchangeID: exchangeTestID, Type: "terminal", Outcome: &Outcome{
		Result: executor.StreamResult{Started: true, ExitCode: &code, Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}}, TerminalPersisted: true,
	}}
}

func TestTerminalFramesRejectInvalidProof(t *testing.T) {
	frame := terminalFixture()
	data, err := json.Marshal(frame)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"retired":`)
	var decoded notice
	require.NoError(t, json.Unmarshal(data, &decoded))
	for _, tc := range []struct{ name, old, new string }{
		{"missing cleanup", `"cleanup":`, `"missing":`},
		{"legacy flag", `"cleanup":`, `"retired":`},
		{"unknown scope", `linux_descendants_v1`, `unknown`},
		{"cross scope", `linux_descendants_v1`, `darwin_process_group_observation_v1`},
		{"unknown state", `"state":"retired"`, `"state":"unknown"`},
		{"contradictory not spawned", `"state":"retired"`, `"state":"not_spawned"`},
		{"contradictory unstarted", `"started":true`, `"started":false`},
		{"duplicate scope", `"scope":"linux_descendants_v1"`, `"scope":"linux_descendants_v1","scope":"linux_descendants_v1"`},
		{"duplicate result", `"started":true`, `"started":true,"started":true`},
		{"duplicate outcome", `"terminal_persisted":true`, `"terminal_persisted":true,"terminal_persisted":true`},
		{"null acknowledgement", `"terminal_persisted":true`, `"terminal_persisted":null`},
		{"missing acknowledgement", `"terminal_persisted":true`, `"other":true`},
		{"negative bytes", `"observed_bytes":0`, `"observed_bytes":-1`},
		{"capture exceeds observed", `"captured_bytes":0`, `"captured_bytes":1`},
		{"wrong direction", `"type":"terminal"`, `"action":"terminal"`},
		{"wrong version", `"version":2`, `"version":1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := strings.Replace(string(data), tc.old, tc.new, 1)
			require.NotEqual(t, string(data), bad)
			require.Error(t, json.Unmarshal([]byte(bad), &decoded))
		})
	}
}

func TestTerminalBindsExchangeAndBackendScope(t *testing.T) {
	for _, mode := range []string{"matching", "wrong exchange", "wrong backend", "out of order"} {
		t.Run(mode, func(t *testing.T) {
			r, w, err := os.Pipe()
			require.NoError(t, err)
			defer r.Close()
			in, out, err := os.Pipe()
			require.NoError(t, err)
			defer in.Close()
			defer out.Close()
			frame := terminalFixture()
			switch mode {
			case "wrong exchange":
				frame.ExchangeID = "22222222-2222-4222-8222-222222222222"
			case "wrong backend":
				frame.Outcome.Result.Cleanup = executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupObserved}
			case "out of order":
				frame = notice{Version: protocolVersion, ExchangeID: exchangeTestID, Type: "ready", CleanupScope: linuxExchangeScope}
			}
			sent := make(chan error, 1)
			go func() { defer w.Close(); sent <- writeFrame(w, frame) }()
			s := Supervisor{input: out, events: r, exchangeID: exchangeTestID, scope: executor.CleanupLinuxDescendants, join: func() error { return nil }}
			got, err := s.Wait()
			require.NoError(t, <-sent)
			if mode == "matching" {
				require.NoError(t, err)
				require.Equal(t, 7, *got.Result.ExitCode)
			} else {
				require.Error(t, err)
				require.False(t, got.Result.Cleanup.Complete())
				require.False(t, got.TerminalPersisted)
			}
		})
	}
}
