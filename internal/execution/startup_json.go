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
	fields, err := exactFields(data, "version", "run_id", "generation", "store_path", "deadline", "exchange_id", "cleanup_scope")
	if err != nil {
		return err
	}
	if bytes.HasPrefix(fields["store_path"], []byte("{")) {
		if _, err := exactFields(fields["store_path"], "bytes"); err != nil {
			return err
		}
	}
	var wire struct {
		startupFields
		StorePath executor.ByteString `json:"store_path"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*s = Startup(wire.startupFields)
	s.StorePath = string(wire.StorePath)
	return s.validate()
}
