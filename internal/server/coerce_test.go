package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func invalidBoolInputs() []struct {
	name  string
	value any
} {
	return []struct {
		name  string
		value any
	}{
		{name: "null", value: nil},
		{name: "integer zero", value: 0},
		{name: "integer one", value: 1},
		{name: "json number zero", value: float64(0)},
		{name: "json number one", value: float64(1)},
		{name: "fraction", value: 0.5},
		{name: "number token", value: json.Number("1")},
		{name: "array", value: []any{true}},
		{name: "object", value: map[string]any{"value": true}},
		{name: "empty string", value: ""},
		{name: "whitespace", value: " \t\n"},
		{name: "numeric string", value: "1"},
		{name: "short true", value: "t"},
		{name: "short false", value: "F"},
		{name: "other string", value: "yes"},
		{name: "quoted string", value: `"false"`},
	}
}

func TestBoolArg(t *testing.T) {
	for _, tt := range []struct {
		name         string
		args         map[string]any
		defaultValue bool
		want         bool
	}{
		{name: "omitted false"},
		{name: "omitted true", args: map[string]any{}, defaultValue: true, want: true},
		{name: "native true", args: map[string]any{"flag": true}, want: true},
		{name: "native false", args: map[string]any{"flag": false}, defaultValue: true},
		{name: "string true", args: map[string]any{"flag": "true"}, want: true},
		{name: "string false", args: map[string]any{"flag": "false"}, defaultValue: true},
		{name: "trim and fold true", args: map[string]any{"flag": " \tTrUe\n"}, want: true},
		{name: "trim and fold false", args: map[string]any{"flag": "\u2003FaLsE\u00a0"}, defaultValue: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := boolArg(tt.args, "flag", tt.defaultValue)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
	for _, tt := range invalidBoolInputs() {
		t.Run(tt.name, func(t *testing.T) {
			for _, fallback := range []bool{false, true} {
				_, err := boolArg(map[string]any{"flag": tt.value}, "flag", fallback)
				require.EqualError(t, err, `invalid parameter "flag": expected a boolean or a true/false string`)
			}
		})
	}
}
