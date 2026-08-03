package server

import (
	"encoding/json"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// TestFormatEventIncludesLoopIndexOnLoopStartAndEnd is the regression test
// for a real gap: the wire payload for loop_start/loop_end used to omit the
// agent's own 0-based ReAct iteration index entirely (types.Event.Loop),
// forcing every client to maintain its own separate counter by hand instead
// of trusting the agent's real one (which the TUI used to do — a client-side
// currTurn++ that could drift from what the agent actually did, and never
// advanced at all for the reserved max-iterations summary iteration, whose
// exit path never incremented it). formatEvent must now include "loop" on
// both event types, using the exact value from types.Event.Loop — the SAME
// value for a loop_start/loop_end pair belonging to one iteration.
func TestFormatEventIncludesLoopIndexOnLoopStartAndEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		evt  types.Event
		want string
	}{
		{"loop_start index 0", types.Event{Type: types.EventLoopStart, Loop: 0}, "loop_start"},
		{"loop_start index 5", types.Event{Type: types.EventLoopStart, Loop: 5}, "loop_start"},
		{"loop_end index 0", types.Event{Type: types.EventLoopEnd, Loop: 0}, "loop_end"},
		{"loop_end index 49", types.Event{Type: types.EventLoopEnd, Loop: 49}, "loop_end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := formatEvent(tc.evt)
			if line == nil {
				t.Fatal("formatEvent returned nil")
			}
			// line is "data: <json>\n\n" — extract the JSON payload.
			raw := line[len("data: ") : len(line)-2]
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("decode payload: %v (raw: %s)", err, raw)
			}
			if decoded["type"] != tc.want {
				t.Errorf("type = %v, want %q", decoded["type"], tc.want)
			}
			gotLoop, ok := decoded["loop"]
			if !ok {
				t.Fatalf("payload missing \"loop\" field entirely: %v", decoded)
			}
			if int(gotLoop.(float64)) != tc.evt.Loop {
				t.Errorf("loop = %v, want %d", gotLoop, tc.evt.Loop)
			}
		})
	}
}

// TestFormatEventLoopZeroIsNotOmitted is the specific regression the
// omitempty pitfall would cause: Loop: 0 (the very first ReAct iteration)
// must still produce an explicit "loop":0 in the JSON, never a missing
// field — omitempty on a plain int would silently drop exactly this common
// case, making "iteration 0" indistinguishable on the wire from "no loop
// field sent at all".
func TestFormatEventLoopZeroIsNotOmitted(t *testing.T) {
	line := formatEvent(types.Event{Type: types.EventLoopStart, Loop: 0})
	raw := line[len("data: ") : len(line)-2]
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := decoded["loop"]; !ok {
		t.Fatalf(`"loop":0 was omitted from the payload entirely: %s`, raw)
	}
}
