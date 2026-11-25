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

	outcome := pumpEvents(c, "sess1", events, false)
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

	pumpEvents(c, "sess1", events, false)

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

	pumpEvents(c, "sess1", events, false)

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

	pumpEvents(c, "sess1", events, false)

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

	pumpEvents(c, "sess1", events, false)

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
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		c := newConn(nil, &buf)
		events := make(chan client.Event, 1)
		events <- tc.evt
		close(events)

		outcome := pumpEvents(c, "sess1", events, false)
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

	outcome := pumpEvents(c, "sess1", events, false)
	if outcome.err == nil || outcome.err.Message != "provider exploded" {
		t.Fatalf("err = %+v", outcome.err)
	}
	if outcome.stopReason != "" {
		t.Errorf("stopReason should be empty on error, got %q", outcome.stopReason)
	}
	if outcome.err.Data != nil {
		t.Errorf("Data = %v, want nil when the event carries no Details", outcome.err.Data)
	}
}

// TestPumpEventsErrorCarriesProviderDetailsAsData is the regression test for
// silently dropping a provider's own structured error payload (e.g. a rate
// limit response body, parsed into types.ProviderAPIError.Details) when
// translating EventError to ACP's Error object. The TUI already surfaces this
// as a pretty-printed JSON block under the error line
// (internal/tui/events.go) — ACP has a purpose-built field for the
// same thing, "data?: unknown" on the Error type, so it must not be dropped
// down to just the generic message ("openai API error 429" alone, with no
// indication of WHY).
func TestPumpEventsErrorCarriesProviderDetailsAsData(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	details := map[string]any{"error": map[string]any{"message": "Rate limit exceeded", "type": "rate_limit_error"}}
	events := make(chan client.Event, 1)
	events <- client.Event{Type: "error", Message: "openai API error 429", Details: details}
	close(events)

	outcome := pumpEvents(c, "sess1", events, false)
	if outcome.err == nil {
		t.Fatal("expected a non-nil error")
	}
	if outcome.err.Message != "openai API error 429" {
		t.Errorf("Message = %q", outcome.err.Message)
	}
	gotData, ok := outcome.err.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %v (%T), want the provider's Details map", outcome.err.Data, outcome.err.Data)
	}
	if _, ok := gotData["error"]; !ok {
		t.Errorf("Data lost the provider payload: %v", gotData)
	}
}

// TestPumpEventsMaxIterationsReachedDoesNotStopTheTurn is the regression test
// for the core bug this fix addresses: agent/session.go reserves ONE MORE
// model call after EventMaxIterationsReached to summarize progress
// (requestProgressUpdate) — that summary streams in as ordinary "text"
// events, followed by a REAL "turn_end". If pumpEvents returned right on
// max_iterations_reached (as it used to), the pump would cut the turn before
// any of that arrived and the user would silently lose exactly the summary
// this whole mechanism exists to deliver. This asserts the full sequence:
// the warning notification fires, the pump keeps reading, the post-limit
// summary text still comes through, and the eventual stopReason is the
// normal "end_turn" — never a max-iterations-specific one.
func TestPumpEventsMaxIterationsReachedDoesNotStopTheTurn(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 4)
	events <- client.Event{Type: "max_iterations_reached", MaxIterations: 50}
	events <- client.Event{Type: "text", Delta: "Here's a summary of what I did..."}
	events <- client.Event{Type: "turn_end"}
	close(events)

	outcome := pumpEvents(c, "sess1", events, false)
	if outcome.stopReason != stopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q (Harness always reaches a real turn_end after the summary, never a max-iterations-specific stop)", outcome.stopReason, stopReasonEndTurn)
	}

	notes := decodeNotifications(t, &buf)
	if len(notes) != 2 {
		t.Fatalf("expected 2 notifications (warning + summary text), got %d: %+v", len(notes), notes)
	}
	warning := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	warningText := warning["content"].(map[string]any)["text"]
	if warningText != "⚠ reached the 50-iteration limit — summarizing progress\n" {
		t.Errorf("warning text = %q", warningText)
	}
	summary := notes[1]["params"].(map[string]any)["update"].(map[string]any)
	summaryText := summary["content"].(map[string]any)["text"]
	if summaryText != "Here's a summary of what I did..." {
		t.Errorf("summary text = %q, want it to still arrive after the warning", summaryText)
	}
}

func TestPumpEventsIgnoredEventTypes(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 16)
	for _, ty := range []string{
		"loop_start", "loop_end", "tool_args", "text_end", "thinking_end",
		"turn_start", "received_prompt", "follow_up_start",
	} {
		events <- client.Event{Type: ty}
	}
	events <- client.Event{Type: "turn_end"}
	close(events)

	pumpEvents(c, "sess1", events, false)

	notes := decodeNotifications(t, &buf)
	if len(notes) != 0 {
		t.Fatalf("expected no notifications for ignored event types, got %d: %+v", len(notes), notes)
	}
}

// TestPumpEventsCompactStartAndEnd is the regression test for compaction —
// whether triggered by an explicit /compact command or fired AUTOMATICALLY
// mid-turn (agent/session.go compacts on its own past 98% context usage) —
// producing visible feedback in the chat, since it's the only channel ACP
// gives a client to learn the context was just rewritten out from under the
// conversation it's rendering.
func TestPumpEventsCompactStartAndEnd(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 4)
	events <- client.Event{Type: "compact_start"}
	events <- client.Event{Type: "compact_end", Summary: "the actual summary text is not surfaced"}
	events <- client.Event{Type: "turn_end"}
	close(events)

	pumpEvents(c, "sess1", events, false)

	notes := decodeNotifications(t, &buf)
	if len(notes) != 2 {
		t.Fatalf("expected 2 notifications (compact_start, compact_end), got %d: %+v", len(notes), notes)
	}
	start := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if start["sessionUpdate"] != "agent_message_chunk" {
		t.Errorf("compact_start sessionUpdate = %v", start["sessionUpdate"])
	}
	startText := start["content"].(map[string]any)["text"]
	if startText != "⏳ Compacting context...\n" {
		t.Errorf("compact_start text = %q", startText)
	}

	end := notes[1]["params"].(map[string]any)["update"].(map[string]any)
	endText := end["content"].(map[string]any)["text"]
	if endText != "✓ Context compacted.\n" {
		t.Errorf("compact_end text = %q", endText)
	}
}

// TestPumpEventsStopOnCompactEndForStandaloneCompact is the regression test
// for the /compact command specifically: Session.Compact() never emits
// turn_end (it's not a ReAct turn — see the stopOnCompactEnd doc comment on
// pumpEvents), so with stopOnCompactEnd=true the pump must return right
// after compact_end instead of blocking forever waiting for a turn_end that
// will never arrive.
func TestPumpEventsStopOnCompactEndForStandaloneCompact(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 2)
	events <- client.Event{Type: "compact_start"}
	events <- client.Event{Type: "compact_end"}
	// Deliberately NOT closing the channel and NOT sending turn_end — a real
	// standalone compact never sends one either. If stopOnCompactEnd didn't
	// work, pumpEvents would hang here and the test would time out.
	outcome := pumpEvents(c, "sess1", events, true)
	if outcome.stopReason != stopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q", outcome.stopReason, stopReasonEndTurn)
	}
}

// TestPumpEventsCompactEndDoesNotStopForNormalTurns confirms the opposite
// side of the same behavior: with stopOnCompactEnd=false (every prompt path
// except /compact), a compact_end firing mid-turn — which is exactly what
// automatic compaction does, between ReAct iterations of an otherwise normal
// turn — must NOT end the pump early; it has to keep reading until the
// turn's real turn_end.
func TestPumpEventsCompactEndDoesNotStopForNormalTurns(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event, 4)
	events <- client.Event{Type: "compact_start"}
	events <- client.Event{Type: "compact_end"}
	events <- client.Event{Type: "text", Delta: "still working after auto-compaction"}
	events <- client.Event{Type: "turn_end"}
	close(events)

	outcome := pumpEvents(c, "sess1", events, false)
	if outcome.stopReason != stopReasonEndTurn {
		t.Fatalf("stopReason = %q, want %q", outcome.stopReason, stopReasonEndTurn)
	}
	notes := decodeNotifications(t, &buf)
	if len(notes) != 3 { // compact_start, compact_end, and the text delta after it
		t.Fatalf("expected 3 notifications, got %d: %+v", len(notes), notes)
	}
}

func TestPumpEventsClosedChannelWithoutTerminalEvent(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	events := make(chan client.Event)
	close(events)

	outcome := pumpEvents(c, "sess1", events, false)
	if outcome.stopReason != stopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q (clean fallback)", outcome.stopReason, stopReasonEndTurn)
	}
}
