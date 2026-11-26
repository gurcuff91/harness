package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWireEventOneCasePerEventType(t *testing.T) {
	cases := map[EventType]struct {
		e    Event
		typ  string
		keys []string
	}{
		EventTurnStart:            {Event{}, "turn_start", nil},
		EventTurnEnd:              {Event{}, "turn_end", nil},
		EventLoopStart:            {Event{Loop: 0}, "loop_start", []string{"loop"}},
		EventLoopEnd:              {Event{Loop: 2}, "loop_end", []string{"loop"}},
		EventStreamTextDelta:      {Event{Delta: "x"}, "text", []string{"delta"}},
		EventStreamTextEnd:        {Event{}, "text_end", nil},
		EventStreamThinkingDelta:  {Event{Delta: "x"}, "thinking", []string{"delta"}},
		EventStreamThinkingEnd:    {Event{}, "thinking_end", nil},
		EventToolStart:            {Event{ToolName: "Bash", ToolID: "1"}, "tool_start", []string{"tool_name", "tool_id"}},
		EventToolArgsDelta:        {Event{ToolName: "Bash", ToolID: "1", Delta: "{"}, "tool_args", []string{"tool_name", "tool_id", "delta"}},
		EventToolCall:             {Event{ToolName: "Bash", ToolID: "1", ToolArgs: "{}"}, "tool_call", []string{"tool_name", "tool_id", "tool_args"}},
		EventToolResult:           {Event{ToolName: "Bash", ToolID: "1", Output: "o", Duration: 1500 * time.Microsecond, IsError: true}, "tool_result", []string{"tool_name", "tool_id", "output", "duration", "is_error"}},
		EventTokens:               {Event{}, "tokens", []string{"input", "context_usage", "context_window", "total_input", "total_output", "cache_read", "cache_write", "cost_usd"}},
		EventError:                {Event{Message: "m"}, "error", []string{"message"}},
		EventMaxIterationsReached: {Event{MaxIterations: 5}, "max_iterations_reached", []string{"max_iterations"}},
		EventFollowUpStart:        {Event{Output: "t", Origin: "o"}, "follow_up_start", []string{"text", "origin", "message_index"}},
		EventReceivedPrompt:       {Event{Output: "t", Origin: "o"}, "received_prompt", []string{"text", "origin", "message_index"}},
		EventCompactStart:         {Event{}, "compact_start", nil},
		EventCompactEnd:           {Event{Summary: "s"}, "compact_end", []string{"summary"}},
		EventStop:                 {Event{}, "stop", nil},
		EventRetry:                {Event{Attempt: 1, MaxAttempts: 3, Message: "m"}, "retry", []string{"attempt", "max", "error"}},
	}
	for et := EventTurnStart; et <= EventRetry; et++ {
		tc, ok := cases[et]
		if !ok {
			t.Fatalf("event type %d has no case: add it here and to rpc's event table", et)
		}
		e := tc.e
		e.Type = et
		p, ok := WireEvent(e)
		if !ok {
			t.Fatalf("%s not exposed", tc.typ)
		}
		if p["type"] != tc.typ {
			t.Errorf("type = %v, want %s", p["type"], tc.typ)
		}
		if len(p) != len(tc.keys)+1 {
			t.Errorf("%s: keys = %v, want %v", tc.typ, p, tc.keys)
		}
		for _, k := range tc.keys {
			if _, has := p[k]; !has {
				t.Errorf("%s: missing key %q", tc.typ, k)
			}
		}
		if _, err := json.Marshal(p); err != nil {
			t.Errorf("%s: %v", tc.typ, err)
		}
	}
}

func TestWireEventDetails(t *testing.T) {
	p, _ := WireEvent(Event{Type: EventToolResult, Duration: 1500 * time.Microsecond})
	if p["duration"] != 1.5 {
		t.Errorf("duration = %v, want fractional ms", p["duration"])
	}
	if p, _ := WireEvent(Event{Type: EventError, Message: "m"}); p["details"] != nil {
		t.Errorf("details present with none")
	}
	if p, _ := WireEvent(Event{Type: EventError, Message: "m", Details: map[string]any{"a": "b"}}); p["details"] == nil {
		t.Errorf("details dropped")
	}
	if p, _ := WireEvent(Event{Type: EventLoopStart}); p["loop"] != 0 {
		t.Errorf("loop 0 must be sent explicitly: %v", p)
	}
	if _, ok := WireEvent(Event{Type: EventType(9999)}); ok {
		t.Error("unknown type must not be exposed")
	}
}
