package executor

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// Plain UTF-8 requests keep the existing JSON representation. Code files and
// physical filesystem paths can contain arbitrary bytes.
// An object makes that exceptional representation explicit and causes older
// readers expecting a string to fail closed instead of opening a different file.
type requestFields ExecRequest

type byteFilePath struct {
	Bytes []byte `json:"bytes"`
}

// ByteString preserves arbitrary program/path bytes across private JSON
// persistence. Ordinary UTF-8 stays a string; old string-only readers reject
// the exceptional object representation instead of changing execution inputs.
type ByteString string

func (s ByteString) MarshalJSON() ([]byte, error) {
	if utf8.ValidString(string(s)) {
		return json.Marshal(string(s))
	}
	return json.Marshal(byteFilePath{Bytes: []byte(s)})
}

func (s *ByteString) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '{' {
		var encoded byteFilePath
		if err := json.Unmarshal(data, &encoded); err != nil {
			return err
		}
		if encoded.Bytes == nil {
			return errors.New("missing request field bytes")
		}
		*s = ByteString(encoded.Bytes)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	*s = ByteString(text)
	return nil
}

func (r ExecRequest) MarshalJSON() ([]byte, error) {
	if utf8.ValidString(r.FilePath) && utf8.ValidString(r.Code) && utf8.ValidString(r.Cwd) {
		return json.Marshal(requestFields(r))
	}
	return json.Marshal(struct {
		requestFields
		FilePath ByteString
		Code     ByteString
		Cwd      ByteString
	}{
		requestFields: requestFields(r),
		FilePath:      ByteString(r.FilePath),
		Code:          ByteString(r.Code),
		Cwd:           ByteString(r.Cwd),
	})
}

func (r *ExecRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		requestFields
		FilePath ByteString
		Code     ByteString
		Cwd      ByteString
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	result := ExecRequest(wire.requestFields)
	result.FilePath, result.Code, result.Cwd = string(wire.FilePath), string(wire.Code), string(wire.Cwd)
	*r = result
	return nil
}
