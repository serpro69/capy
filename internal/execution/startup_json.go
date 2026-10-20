package execution

import (
	"bytes"
	"encoding/json"

	"github.com/serpro69/capy/internal/executor"
)

type startupFields Startup

func (s Startup) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		startupFields
		StorePath executor.ByteString `json:"store_path"`
	}{startupFields: startupFields(s), StorePath: executor.ByteString(s.StorePath)})
}

func (s *Startup) UnmarshalJSON(data []byte) error {
	var wire struct {
		startupFields
		StorePath executor.ByteString `json:"store_path"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*s = Startup(wire.startupFields)
	s.StorePath = string(wire.StorePath)
	return nil
}
