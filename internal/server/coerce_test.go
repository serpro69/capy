package server

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func invalidFetchTTLs() []struct {
	name  string
	value any
} {
	return []struct {
		name  string
		value any
	}{
		{name: "null", value: nil},
		{name: "negative", value: -1},
		{name: "fractional", value: 0.5},
		{name: "nonfinite", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "nan", value: math.NaN()},
		{name: "duration overflow", value: float64(maxFetchTTLMS + 1)},
		{name: "integer overflow", value: int64(math.MaxInt64)},
		{name: "string overflow", value: "9223372036854775808"},
		{name: "string duration overflow", value: strconv.FormatInt(maxFetchTTLMS+1, 10)},
		{name: "fractional string", value: "500.5"},
		{name: "decimal string", value: "500.0"},
		{name: "exponent string", value: "5e2"},
		{name: "signed string", value: "+500"},
		{name: "negative string", value: "-1"},
		{name: "hex string", value: "0x10"},
		{name: "separator", value: "1_000"},
		{name: "empty", value: ""},
		{name: "whitespace", value: " \t"},
		{name: "boolean", value: true},
		{name: "array", value: []any{500}},
		{name: "object", value: map[string]any{"ttl": 500}},
		{name: "number null", value: json.Number("null")},
		{name: "number fraction", value: json.Number("500.1")},
	}
}

func TestFetchTTLArg(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  time.Duration
	}{
		{name: "zero", value: float64(0)},
		{name: "integer", value: 500, want: 500 * time.Millisecond},
		{name: "int64", value: int64(500), want: 500 * time.Millisecond},
		{name: "json number", value: float64(500), want: 500 * time.Millisecond},
		{name: "decimal token", value: json.Number("500.0"), want: 500 * time.Millisecond},
		{name: "exponent token", value: json.Number("5e2"), want: 500 * time.Millisecond},
		{name: "string", value: "500", want: 500 * time.Millisecond},
		{name: "trimmed string", value: " \t00500\n", want: 500 * time.Millisecond},
		{name: "maximum number", value: float64(maxFetchTTLMS), want: time.Duration(maxFetchTTLMS) * time.Millisecond},
		{name: "maximum string", value: strconv.FormatInt(maxFetchTTLMS, 10), want: time.Duration(maxFetchTTLMS) * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fetchTTLArg(map[string]any{"ttl": tc.value}, 24)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	for _, tc := range invalidFetchTTLs() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fetchTTLArg(map[string]any{"ttl": tc.value}, 24)
			require.ErrorContains(t, err, `invalid parameter "ttl"`)
		})
	}
	got, err := fetchTTLArg(nil, 7)
	require.NoError(t, err)
	assert.Equal(t, 7*time.Hour, got)
	got, err = fetchTTLArg(nil, 0)
	require.NoError(t, err)
	assert.Zero(t, got)
	for _, hours := range []int{-1, int(math.MaxInt64/int64(time.Hour)) + 1} {
		_, err := fetchTTLArg(nil, hours)
		require.ErrorContains(t, err, "store.cache.fetch_ttl_hours")
		got, err := fetchTTLArg(map[string]any{"ttl": 500}, hours)
		require.NoError(t, err, "explicit TTL does not consume configured hours")
		assert.Equal(t, 500*time.Millisecond, got)
	}
}

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
