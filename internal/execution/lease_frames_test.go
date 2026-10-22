package execution

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const exchangeTestID = "11111111-1111-4111-8111-111111111111"

func TestLeaseFramesRejectMalformedMessages(t *testing.T) {
	valid := `{"version":2,"exchange_id":"` + exchangeTestID + `","type":"lease_observed","lease":"owner","step":1,"observed":42,"written":43}`
	for _, tc := range []struct{ name, data string }{
		{"duplicate", strings.Replace(valid, `"step":1`, `"step":1,"step":1`, 1)},
		{"unknown", strings.Replace(valid, `"step":1`, `"step":1,"extra":0`, 1)},
		{"missing", strings.Replace(valid, `,"written":43`, ``, 1)},
		{"wrong version", strings.Replace(valid, `"version":2`, `"version":1`, 1)},
		{"wrong direction", strings.Replace(valid, `"type":`, `"action":`, 1)},
		{"swapped role", strings.Replace(valid, `"owner"`, `"other"`, 1)},
		{"wrong step", strings.Replace(valid, `"step":1`, `"step":2`, 1)},
		{"null", strings.Replace(valid, `"written":43`, `"written":null`, 1)},
		{"fraction", strings.Replace(valid, `"written":43`, `"written":43.0`, 1)},
		{"exponent", strings.Replace(valid, `"written":43`, `"written":43e0`, 1)},
		{"negative", strings.Replace(valid, `"written":43`, `"written":-1`, 1)},
		{"zero", strings.Replace(valid, `"written":43`, `"written":0`, 1)},
		{"overflow", strings.Replace(valid, `"written":43`, `"written":9007199254740992`, 1)},
		{"extraneous scope", strings.Replace(valid, `"step":1`, `"step":1,"cleanup_scope":"linux_descendants_v1"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var frame leaseMessage
			require.Error(t, json.Unmarshal([]byte(tc.data), &frame))
		})
	}
	var frame leaseMessage
	require.NoError(t, json.Unmarshal([]byte(valid), &frame))
	require.Equal(t, leaseMarker(42), frame.Observed)
	var framed bytes.Buffer
	require.NoError(t, writeFrame(&framed, frame))
	require.NoError(t, readFrame(&framed, &frame))
}

func TestLeaseStartupStrictFieldsAndBytePaths(t *testing.T) {
	start := exchangeStartup{Version: 2, RunID: exchangeTestID, Generation: exchangeTestID, ExchangeID: exchangeTestID,
		StorePath: "invalid-utf8-\xff", CleanupScope: linuxExchangeScope}
	data, err := json.Marshal(start)
	require.NoError(t, err)
	var decoded exchangeStartup
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, start, decoded)
	for _, bad := range []string{
		strings.Replace(string(data), `"version":2`, `"version":2,"version":2`, 1),
		strings.Replace(string(data), `"bytes":`, `"unknown":0,"bytes":`, 1),
		strings.Replace(string(data), linuxExchangeScope, "unknown", 1),
		strings.Replace(string(data), `"version":2`, `"version":null`, 1),
	} {
		require.Error(t, json.Unmarshal([]byte(bad), &decoded))
	}
}

func TestLeaseMarkerSeekFailuresRemainErrors(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "marker")
	require.NoError(t, err)
	defer f.Close()
	_, err = readLeaseMarker(f)
	require.Error(t, err, "zero offset is not an observation")
	for _, m := range []leaseMarker{0, -1, maxLeaseMarker + 1} {
		require.Error(t, setLeaseMarker(f, m))
	}
	require.NoError(t, setLeaseMarker(f, 123))
	m, err := readLeaseMarker(f)
	require.NoError(t, err)
	require.Equal(t, leaseMarker(123), m)
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	require.Error(t, setLeaseMarker(r, 123))
	_, err = readLeaseMarker(r)
	require.Error(t, err)
	require.NoError(t, f.Close())
	require.Error(t, setLeaseMarker(f, 123))
}
