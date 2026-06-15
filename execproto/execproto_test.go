package execproto

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestParseInboundData(t *testing.T) {
	in, err := ParseInbound(false, []byte{0x03, 'a', 'b'}) // Ctrl-C + chars, raw
	if err != nil {
		t.Fatalf("data frame err: %v", err)
	}
	if in.Resize != nil || !bytes.Equal(in.Data, []byte{0x03, 'a', 'b'}) {
		t.Errorf("data frame = %+v", in)
	}
}

func TestParseInboundResize(t *testing.T) {
	in, err := ParseInbound(true, []byte(`{"type":"resize","cols":120,"rows":40}`))
	if err != nil {
		t.Fatalf("resize err: %v", err)
	}
	if in.Data != nil || in.Resize == nil || in.Resize.Cols != 120 || in.Resize.Rows != 40 {
		t.Errorf("resize = %+v", in)
	}
}

func TestParseInboundErrors(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"bad json", `{not json`},
		{"unknown type", `{"type":"frobnicate"}`},
		{"missing type", `{"cols":80,"rows":24}`},
		{"zero dims", `{"type":"resize","cols":0,"rows":24}`},
		{"negative dims", `{"type":"resize","cols":80,"rows":-1}`},
		{"oversized dims", `{"type":"resize","cols":999999,"rows":24}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseInbound(true, []byte(c.payload)); err == nil {
				t.Errorf("expected error for %s", c.payload)
			}
		})
	}
}

func TestExitNotice(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal(ExitNotice(), &m); err != nil {
		t.Fatalf("exit notice not valid JSON: %v", err)
	}
	if m["type"] != "exit" {
		t.Errorf("exit notice = %v", m)
	}
}
