package executor

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecRequestJSONPreservesFilePathBytes(t *testing.T) {
	for name, path := range map[string]string{"empty": "", "unicode": "/tmp/input-é.txt", "raw bytes": "/tmp/input-\xff.txt"} {
		t.Run(name, func(t *testing.T) {
			request := ExecRequest{Language: Shell, Code: "printf test", FilePath: path, Cwd: "/tmp", TimeoutSec: 17}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			var decoded ExecRequest
			require.NoError(t, json.Unmarshal(data, &decoded))
			require.Equal(t, request, decoded)
			// A reader predating byte paths must refuse that request. Existing
			// ordinary/string-path requests keep their exact wire representation.
			var old requestFields
			err = json.Unmarshal(data, &old)
			if name == "raw bytes" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				legacy, err := json.Marshal(requestFields(request))
				require.NoError(t, err)
				require.Equal(t, legacy, data)
			}
		})
	}
	for name, data := range map[string]string{
		"missing bytes": `{"FilePath":{}}`, "null bytes": `{"FilePath":{"bytes":null}}`,
		"invalid base64": `{"FilePath":{"bytes":"!"}}`, "wrong path type": `{"FilePath":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			var decoded ExecRequest
			require.Error(t, json.Unmarshal([]byte(data), &decoded))
		})
	}
}

func TestExecRequestJSONPreservesCodeAndCwdBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		cwd  string
	}{
		{"code", "printf '\xff'", "/tmp"},
		{"cwd", "pwd", "/tmp/work-\xff"},
		{"both", "printf '\xff'", "/tmp/work-\xff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := ExecRequest{Language: Shell, Code: tc.code, Cwd: tc.cwd}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			var decoded ExecRequest
			require.NoError(t, json.Unmarshal(data, &decoded))
			require.Equal(t, request, decoded)
			var old requestFields
			require.Error(t, json.Unmarshal(data, &old), "old readers must reject byte objects")
		})
	}
}
