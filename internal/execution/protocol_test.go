package execution

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestPrivateProtocolBounds(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		size       uint32
	}{
		{"zero", "", 0},
		{"oversized", "", maxFrameBytes + 1},
		{"truncated", `{`, 10},
		{"malformed", `{`, 1},
		{"unknown field", `{"action":"start","code":"private"}`, 0},
		{"trailing data", `{"action":"start"}{}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			size := tc.size
			if size == 0 {
				size = uint32(len(tc.body))
			}
			if err := binary.Write(&b, binary.BigEndian, size); err != nil {
				t.Fatal(err)
			}
			b.WriteString(tc.body)
			var c control
			if err := readFrame(&b, &c); err == nil {
				t.Fatal("invalid private input accepted")
			}
		})
	}
	var b bytes.Buffer
	if err := writeFrame(&b, control{Action: "start"}); err != nil {
		t.Fatal(err)
	}
	var got control
	if err := readFrame(&b, &got); err != nil || got.Action != "start" {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	if err := writeFrame(&b, strings.Repeat("x", maxFrameBytes)); err == nil {
		t.Fatal("oversized write accepted")
	}
}

func TestStartupValidation(t *testing.T) {
	valid := Startup{Version: 1, RunID: "11111111-1111-4111-8111-111111111111", Generation: "22222222-2222-4222-8222-222222222222", StorePath: "/test/runs.db"}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Startup)
	}{
		{"version", func(s *Startup) { s.Version++ }},
		{"run", func(s *Startup) { s.RunID = "other" }},
		{"generation", func(s *Startup) { s.Generation = strings.Repeat("x", 36) }},
		{"missing path", func(s *Startup) { s.StorePath = "" }},
		{"path bound", func(s *Startup) { s.StorePath = strings.Repeat("x", 4097) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			tc.mutate(&s)
			if s.validate() == nil {
				t.Fatal("invalid startup accepted")
			}
		})
	}
}
