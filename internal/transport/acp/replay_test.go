package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/gurcuff91/harness/types"
)

func TestReplayHistoryTextMessages(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	messages := []types.Message{
		types.NewUserTextMessage("hi there"),
		types.NewAssistantTextMessage("hello!"),
	}
	replayHistory(c, "sess1", messages)

	notes := decodeNotifications(t, &buf)
	if len(notes) != 2 {
		t.Fatalf("expected 2 notifications, got %d: %+v", len(notes), notes)
	}
	first := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if first["sessionUpdate"] != "user_message_chunk" {
		t.Errorf("first sessionUpdate = %v", first["sessionUpdate"])
	}
	second := notes[1]["params"].(map[string]any)["update"].(map[string]any)
	if second["sessionUpdate"] != "agent_message_chunk" {
		t.Errorf("second sessionUpdate = %v", second["sessionUpdate"])
	}
}

func TestReplayHistoryThinking(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	messages := []types.Message{
		types.NewAssistantToolCallMessage("", "thinking hard", "", nil),
	}
	replayHistory(c, "sess1", messages)

	notes := decodeNotifications(t, &buf)
	if len(notes) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notes))
	}
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "agent_thought_chunk" {
		t.Errorf("sessionUpdate = %v", update["sessionUpdate"])
	}
}

func TestReplayHistoryToolCallWithResult(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	call := types.ToolCall{ID: "t1", Name: "Read", Input: json.RawMessage(`{"path":"x.go"}`)}
	messages := []types.Message{
		types.NewAssistantToolCallMessage("", "", "", []types.ToolCall{call}),
		types.NewToolResultMessage([]types.ToolResult{{ID: "t1", Output: "file contents"}}),
	}
	replayHistory(c, "sess1", messages)

	notes := decodeNotifications(t, &buf)
	if len(notes) != 1 {
		t.Fatalf("expected 1 notification (tool_call carries its final state directly), got %d: %+v", len(notes), notes)
	}
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "tool_call" {
		t.Errorf("sessionUpdate = %v", update["sessionUpdate"])
	}
	if update["status"] != "completed" {
		t.Errorf("status = %v", update["status"])
	}
	if update["kind"] != "read" {
		t.Errorf("kind = %v", update["kind"])
	}
	if update["toolCallId"] != "t1" {
		t.Errorf("toolCallId = %v", update["toolCallId"])
	}
}

func TestReplayHistoryToolCallWithFailedResult(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	call := types.ToolCall{ID: "t1", Name: "Bash", Input: json.RawMessage(`{}`)}
	messages := []types.Message{
		types.NewAssistantToolCallMessage("", "", "", []types.ToolCall{call}),
		types.NewToolResultMessage([]types.ToolResult{{ID: "t1", Output: "boom", IsErr: true}}),
	}
	replayHistory(c, "sess1", messages)

	notes := decodeNotifications(t, &buf)
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["status"] != "failed" {
		t.Errorf("status = %v, want failed", update["status"])
	}
}

func TestReplayHistoryToolCallWithoutResult(t *testing.T) {
	// Defensive case: a call with no matching result (shouldn't normally
	// happen in a persisted history, but must not panic or hang).
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	call := types.ToolCall{ID: "orphan", Name: "Bash", Input: json.RawMessage(`{}`)}
	messages := []types.Message{
		types.NewAssistantToolCallMessage("", "", "", []types.ToolCall{call}),
	}
	replayHistory(c, "sess1", messages)

	notes := decodeNotifications(t, &buf)
	if len(notes) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notes))
	}
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["status"] != "completed" {
		t.Errorf("status = %v, want completed (default when no result found)", update["status"])
	}
}

func TestReplayHistoryEmpty(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)
	replayHistory(c, "sess1", nil)
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty history, got %q", buf.String())
	}
}
