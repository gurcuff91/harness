package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/gurcuff91/harness/client"
)

func TestToolKind(t *testing.T) {
	cases := map[string]string{
		"Bash":         toolKindExec,
		"Read":         toolKindRead,
		"Write":        toolKindEdit,
		"Edit":         toolKindEdit,
		"Fetch":        toolKindFetch,
		"Skill":        toolKindOther,
		"Subagent":     toolKindOther,
		"MemoWrite":    toolKindOther,
		"ColleagueAsk": toolKindOther,
		"Unknown":      toolKindOther,
	}
	for name, want := range cases {
		if got := toolKind(name); got != want {
			t.Errorf("toolKind(%q) = %q, want %q", name, got, want)
		}
	}
}

// decodeNotifications reads every line the pump wrote as a
// sessionUpdateNotification — used to assert on what pumpEvents actually
// sent without depending on exact key ordering.
func decodeNotifications(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func TestPumpEventsTextDelta(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 4)
	events <- client.Event{Type: "text", Delta: "Hello"}
	events <- client.Event{Type: "turn_end"}
	close(events)

	outcome := pumpEvents(c, "sess1", events)
	if outcome.stopReason != stopReasonEndTurn {
		t.Fatalf("stopReason = %q, want %q", outcome.stopReason, stopReasonEndTurn)
	}

	notes := decodeNotifications(t, &buf)
	if len(notes) != 1 {
		t.Fatalf("expected 1 notification, got %d: %+v", len(notes), notes)
	}
	params := notes[0]["params"].(map[string]any)
	if params["sessionId"] != "sess1" {
		t.Errorf("sessionId = %v", params["sessionId"])
	}
	update := params["update"].(map[string]any)
	if update["sessionUpdate"] != "agent_message_chunk" {
		t.Errorf("sessionUpdate = %v", update["sessionUpdate"])
	}
	content := update["content"].(map[string]any)
	if content["type"] != "text" || content["text"] != "Hello" {
		t.Errorf("content = %v", content)
	}
}

func TestPumpEventsThinkingDelta(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 4)
	events <- client.Event{Type: "thinking", Delta: "pondering..."}
	events <- client.Event{Type: "turn_end"}
	close(events)

	pumpEvents(c, "sess1", events)

	notes := decodeNotifications(t, &buf)
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "agent_thought_chunk" {
		t.Errorf("sessionUpdate = %v", update["sessionUpdate"])
	}
}

func TestPumpEventsToolCallLifecycle(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 8)
	events <- client.Event{Type: "tool_start", ToolID: "t1", ToolName: "Bash"}
	events <- client.Event{Type: "tool_call", ToolID: "t1", ToolName: "Bash", ToolArgs: `{"command":"ls"}`}
	events <- client.Event{Type: "tool_result", ToolID: "t1", ToolName: "Bash", Output: "file.go", IsError: false}
	events <- client.Event{Type: "turn_end"}
	close(events)

	pumpEvents(c, "sess1", events)

	notes := decodeNotifications(t, &buf)
	if len(notes) != 3 {
		t.Fatalf("expected 3 notifications (tool_call, tool_call_update x2), got %d: %+v", len(notes), notes)
	}

	start := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if start["sessionUpdate"] != "tool_call" || start["status"] != "pending" || start["kind"] != "execute" {
		t.Errorf("start update = %v", start)
	}

	inProgress := notes[1]["params"].(map[string]any)["update"].(map[string]any)
	if inProgress["sessionUpdate"] != "tool_call_update" || inProgress["status"] != "in_progress" {
		t.Errorf("in_progress update = %v", inProgress)
	}

	completed := notes[2]["params"].(map[string]any)["update"].(map[string]any)
	if completed["sessionUpdate"] != "tool_call_update" || completed["status"] != "completed" {
		t.Errorf("completed update = %v", completed)
	}
	content := completed["content"].([]any)[0].(map[string]any)
	if content["type"] != "content" {
		t.Errorf("content type = %v, want content (no diff — Bash has no path)", content["type"])
	}
}

func TestPumpEventsToolResultError(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 4)
	events <- client.Event{Type: "tool_result", ToolID: "t1", ToolName: "Bash", Output: "boom", IsError: true}
	events <- client.Event{Type: "turn_end"}
	close(events)

	pumpEvents(c, "sess1", events)

	notes := decodeNotifications(t, &buf)
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["status"] != "failed" {
		t.Errorf("status = %v, want failed", update["status"])
	}
}

func TestPumpEventsTokens(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 4)
	events <- client.Event{Type: "tokens", Input: 100, ContextWindow: 200000, CostUSD: 0.05}
	events <- client.Event{Type: "turn_end"}
	close(events)

	pumpEvents(c, "sess1", events)

	notes := decodeNotifications(t, &buf)
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "usage_update" {
		t.Fatalf("sessionUpdate = %v", update["sessionUpdate"])
	}
	if update["used"] != float64(100) || update["size"] != float64(200000) {
		t.Errorf("used/size = %v/%v", update["used"], update["size"])
	}
	cost := update["cost"].(map[string]any)
	if cost["currency"] != "USD" {
		t.Errorf("cost = %v", cost)
	}
}

func TestPumpEventsStopReasons(t *testing.T) {
	cases := []struct {
		evt  client.Event
		want string
	}{
		{client.Event{Type: "turn_end"}, stopReasonEndTurn},
		{client.Event{Type: "stop"}, stopReasonCancelled},
		{client.Event{Type: "max_iterations_reached"}, stopReasonMaxTurnRequests},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		c := newConn(nil, &buf)
		events := make(chan client.Event, 1)
		events <- tc.evt
		close(events)

		outcome := pumpEvents(c, "sess1", events)
		if outcome.stopReason != tc.want {
			t.Errorf("event %q: stopReason = %q, want %q", tc.evt.Type, outcome.stopReason, tc.want)
		}
	}
}

func TestPumpEventsError(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 1)
	events <- client.Event{Type: "error", Message: "provider exploded"}
	close(events)

	outcome := pumpEvents(c, "sess1", events)
	if outcome.err == nil || outcome.err.Message != "provider exploded" {
		t.Fatalf("err = %+v", outcome.err)
	}
	if outcome.stopReason != "" {
		t.Errorf("stopReason should be empty on error, got %q", outcome.stopReason)
	}
}

func TestPumpEventsIgnoredEventTypes(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 16)
	for _, ty := range []string{
		"loop_start", "loop_end", "tool_args", "text_end", "thinking_end",
		"turn_start", "received_prompt", "follow_up_start", "compact_start", "compact_end",
	} {
		events <- client.Event{Type: ty}
	}
	events <- client.Event{Type: "turn_end"}
	close(events)

	pumpEvents(c, "sess1", events)

	notes := decodeNotifications(t, &buf)
	if len(notes) != 0 {
		t.Fatalf("expected no notifications for ignored event types, got %d: %+v", len(notes), notes)
	}
}

func TestPumpEventsClosedChannelWithoutTerminalEvent(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event)
	close(events)

	outcome := pumpEvents(c, "sess1", events)
	if outcome.stopReason != stopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q (clean fallback)", outcome.stopReason, stopReasonEndTurn)
	}
}
