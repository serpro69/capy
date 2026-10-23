package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

const (
	exchangeProtocolVersion = protocolVersion
	linuxExchangeScope      = "linux_descendants_v1"
	darwinExchangeScope     = "darwin_process_group_observation_v1"
	maxLeaseMarker          = 1<<53 - 1
)

// The proof fixture and production supervisor use one startup representation.
type exchangeStartup = Startup

func exchangeScope(scope string) bool {
	return scope == linuxExchangeScope || scope == darwinExchangeScope
}

type leaseMarker int64

func (m *leaseMarker) UnmarshalJSON(data []byte) error {
	// JSON's numerical equivalence is insufficient: exponents, fractions,
	// signs, zero, null and values outside the exact integer range are invalid.
	if len(data) == 0 || data[0] < '1' || data[0] > '9' {
		return errors.New("invalid lease marker")
	}
	for _, c := range data {
		if c < '0' || c > '9' {
			return errors.New("invalid lease marker")
		}
	}
	n, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil || n > maxLeaseMarker {
		return errors.New("invalid lease marker")
	}
	*m = leaseMarker(n)
	return nil
}

type leaseMessage struct {
	Version      int         `json:"version"`
	ExchangeID   string      `json:"exchange_id"`
	Action       string      `json:"action,omitempty"`
	Type         string      `json:"type,omitempty"`
	Lease        string      `json:"lease,omitempty"`
	Step         int         `json:"step,omitempty"`
	Observed     leaseMarker `json:"observed,omitempty"`
	Written      leaseMarker `json:"written,omitempty"`
	Expected     leaseMarker `json:"expected,omitempty"`
	CleanupScope string      `json:"cleanup_scope,omitempty"`
	Outcome      *Outcome    `json:"outcome,omitempty"`
}

func (m *leaseMessage) UnmarshalJSON(data []byte) error {
	type plain leaseMessage
	var wire plain
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	fields := []string{"version", "exchange_id"}
	if wire.Version != exchangeProtocolVersion || !validID(wire.ExchangeID) || (wire.Action == "") == (wire.Type == "") {
		return errors.New("invalid lease exchange message")
	}
	kind := wire.Action
	if kind != "" {
		fields = append(fields, "action")
	} else {
		kind = wire.Type
		fields = append(fields, "type")
	}
	wantStep := 0
	switch kind {
	case "lease_observe":
		if wire.Action == "" {
			return errors.New("invalid lease message direction")
		}
		wantStep = 1
	case "lease_observed":
		if wire.Type == "" {
			return errors.New("invalid lease message direction")
		}
		wantStep = 1
		fields = append(fields, "observed", "written")
	case "lease_confirm":
		if wire.Action == "" {
			return errors.New("invalid lease message direction")
		}
		wantStep = 2
		fields = append(fields, "expected")
	case "lease_confirmed":
		if wire.Type == "" {
			return errors.New("invalid lease message direction")
		}
		wantStep = 2
		fields = append(fields, "observed")
	case "terminal":
		if wire.Type == "" || wire.Outcome == nil {
			return errors.New("invalid terminal notice")
		}
		fields = append(fields, "outcome")
	case "leases_confirmed", "cancel":
		if wire.Action == "" {
			return errors.New("invalid lease message direction")
		}
	case "ready", "start":
		if (kind == "ready") != (wire.Type != "") || !exchangeScope(wire.CleanupScope) {
			return errors.New("invalid lease readiness")
		}
		fields = append(fields, "cleanup_scope")
	default:
		return errors.New("unexpected lease exchange message")
	}
	if wantStep != 0 {
		if wire.Step != wantStep || (wire.Lease != "owner" && wire.Lease != "lifecycle") {
			return errors.New("invalid lease exchange role or step")
		}
		fields = append(fields, "lease", "step")
	}
	if _, err := exactFields(data, fields...); err != nil {
		return err
	}
	*m = leaseMessage(wire)
	return nil
}

// exactFields rejects duplicates as well as unknown/omitted/null fields. The
// surrounding readFrame still enforces one JSON value and the bounded frame.
func exactFields(data []byte, names ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("expected lease frame object")
	}
	fields := make(map[string]json.RawMessage, len(names))
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || fields[name] != nil {
			return nil, errors.New("duplicate lease frame field")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(value, []byte("null")) {
			return nil, errors.New("null lease frame field")
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing lease frame data")
	}
	if len(fields) != len(names) {
		return nil, errors.New("invalid lease frame fields")
	}
	for _, name := range names {
		if fields[name] == nil {
			return nil, errors.New("missing lease frame field")
		}
	}
	return fields, nil
}
