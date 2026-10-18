package executor

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// Plain UTF-8 paths keep the existing JSON representation. Physical filesystem
// paths can contain arbitrary bytes, even when admitted through a UTF-8 symlink.
// An object makes that exceptional representation explicit and causes older
// readers expecting a string to fail closed instead of opening a different file.
type requestFields ExecRequest

type byteFilePath struct {
	Bytes []byte `json:"bytes"`
}

func (r ExecRequest) MarshalJSON() ([]byte, error) {
	if utf8.ValidString(r.FilePath) {
		return json.Marshal(requestFields(r))
	}
	return json.Marshal(struct {
		requestFields
		FilePath byteFilePath
	}{requestFields: requestFields(r), FilePath: byteFilePath{Bytes: []byte(r.FilePath)}})
}

func (r *ExecRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		requestFields
		FilePath json.RawMessage
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var path string
	if len(wire.FilePath) > 0 && wire.FilePath[0] == '{' {
		var encoded byteFilePath
		if err := json.Unmarshal(wire.FilePath, &encoded); err != nil {
			return err
		}
		if encoded.Bytes == nil {
			return errors.New("missing file path bytes")
		}
		path = string(encoded.Bytes)
	} else if len(wire.FilePath) > 0 {
		if err := json.Unmarshal(wire.FilePath, &path); err != nil {
			return err
		}
	}
	*r = ExecRequest(wire.requestFields)
	r.FilePath = path
	return nil
}
