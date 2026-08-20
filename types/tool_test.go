package types

import (
	"encoding/json"
	"testing"
)

func TestIsRawWrapper(t *testing.T) {
	cases := []struct {
		name    string
		input   json.RawMessage
		wantOK  bool
		wantRaw string
	}{
		{
			name:    "the wrapper itself",
			input:   json.RawMessage(`{"_raw_":"{\"command\":\"a\"}{\"command\":\"b\"}"}`),
			wantOK:  true,
			wantRaw: `{"command":"a"}{"command":"b"}`,
		},
		{
			name:   "a normal, well-formed tool call",
			input:  json.RawMessage(`{"command":"ls -la"}`),
			wantOK: false,
		},
		{
			name:   "empty object",
			input:  json.RawMessage(`{}`),
			wantOK: false,
		},
		{
			name:   "invalid JSON outright",
			input:  json.RawMessage(`{not json`),
			wantOK: false,
		},
		{
			name:   "_raw_ present but with a SECOND key — not the wrapper shape",
			input:  json.RawMessage(`{"_raw_":"x","other":"y"}`),
			wantOK: false,
		},
		{
			name:   "_raw_ present but value is NOT a string (e.g. a number)",
			input:  json.RawMessage(`{"_raw_":123}`),
			wantOK: false,
		},
		{
			name:   "_raw_ present but value is an object, not a string",
			input:  json.RawMessage(`{"_raw_":{"nested":true}}`),
			wantOK: false,
		},
		{
			name:   "a tool that legitimately has a single unrelated field",
			input:  json.RawMessage(`{"path":"/tmp/f.txt"}`),
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, ok := IsRawWrapper(tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (raw=%q)", ok, tc.wantOK, raw)
			}
			if tc.wantOK && raw != tc.wantRaw {
				t.Errorf("raw = %q, want %q", raw, tc.wantRaw)
			}
		})
	}
}
