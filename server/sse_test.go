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

// TestFormatEventTokensCarriesBothSemantics pins the tokens payload contract:
// live-context fields AND session-history fields, each with its own meaning
// (see types.TokenUsage). total_input in particular used to have no wire
// representation at all, even though SessionStats persisted it — which is
// why a resuming client showed session totals and then had them replaced by
// per-turn values on the first event.
func TestFormatEventTokensCarriesBothSemantics(t *testing.T) {
	line := formatEvent(types.Event{
		Type: types.EventTokens,
		Tokens: types.TokenUsage{
			// live context
			Input:         827_602,
			ContextUsage:  0.8276,
			ContextWindow: 1_000_000,
			// session history (mirrors SessionStats)
			TotalInput:  112_626_989,
			TotalOutput: 2_529_943,
			CacheRead:   2_216_313_730,
			CacheWrite:  64_706_276,
			CostUSD:     1120.66,
		},
	})
	if line == nil {
		t.Fatal("formatEvent returned nil for EventTokens")
	}
	payload := decodeSSE(t, line)

	want := map[string]float64{
		"input":          827_602,
		"context_usage":  0.8276,
		"context_window": 1_000_000,
		"total_input":    112_626_989,
		"total_output":   2_529_943,
		"cache_read":     2_216_313_730,
		"cache_write":    64_706_276,
		"cost_usd":       1120.66,
	}
	for field, expected := range want {
		got, ok := payload[field]
		if !ok {
			t.Errorf("tokens payload is missing %q", field)
			continue
		}
		num, ok := got.(float64)
		if !ok {
			t.Errorf("%s: got %T, want a number", field, got)
			continue
		}
		if num != expected {
			t.Errorf("%s = %v, want %v", field, num, expected)
		}
	}
}

// TestFormatEventTokensSendsZeroesExplicitly verifies a zero is never dropped
// from the tokens payload. Zeroes are MEANINGFUL here — 0 cache reads on a
// turn, and especially context_usage 0.0 right after a compaction reclaimed
// the context. Omitting them would be indistinguishable on the wire from
// "not reported", which is exactly the trap the "loop" field hit before.
func TestFormatEventTokensSendsZeroesExplicitly(t *testing.T) {
	// The shape emitted right after a compaction: live context zeroed,
	// history intact.
	line := formatEvent(types.Event{
		Type: types.EventTokens,
		Tokens: types.TokenUsage{
			Input:         0,
			ContextUsage:  0,
			ContextWindow: 1_000_000,
			TotalInput:    5_000,
			TotalOutput:   1_000,
			CacheRead:     0,
			CacheWrite:    0,
			CostUSD:       0,
		},
	})
	payload := decodeSSE(t, line)

	for _, field := range []string{"input", "context_usage", "cache_read", "cache_write", "cost_usd"} {
		if _, ok := payload[field]; !ok {
			t.Errorf("%q was omitted when zero — a zero here is meaningful, not absent", field)
		}
	}
}

// decodeSSE strips the "data: " framing and decodes the JSON payload.
func decodeSSE(t *testing.T, line []byte) map[string]any {
	t.Helper()
	const prefix = "data: "
	s := string(line)
	if len(s) <= len(prefix) {
		t.Fatalf("malformed SSE line: %q", s)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(s[len(prefix):len(s)-2]), &payload); err != nil {
		t.Fatalf("decoding SSE payload %q: %v", s, err)
	}
	return payload
}
