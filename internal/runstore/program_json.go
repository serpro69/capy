package runstore

import (
	"encoding/json"

	"github.com/serpro69/capy/internal/executor"
)

type programFields Program

func (p Program) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		programFields
		Owner executor.ByteString `json:"owner"`
	}{programFields: programFields(p), Owner: executor.ByteString(p.Owner)})
}

func (p *Program) UnmarshalJSON(data []byte) error {
	var wire struct {
		programFields
		Owner executor.ByteString `json:"owner"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*p = Program(wire.programFields)
	p.Owner = string(wire.Owner)
	return nil
}
