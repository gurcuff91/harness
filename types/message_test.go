package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMessageMeta_IsSystemGenerated_JSON verifies the new metadata field
// serializes with the expected snake_case key so frontends can detect
// system-injected user messages (e.g. the max-iterations summary request).
func TestMessageMeta_IsSystemGenerated_JSON(t *testing.T) {
	m := NewUserTextMessage("system prompt")
	m.Meta = &MessageMeta{IsSystemGenerated: true}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"is_system_generated":true`) {
		t.Errorf("expected is_system_generated field in JSON, got %s", b)
	}

	var decoded Message
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Meta == nil || !decoded.Meta.IsSystemGenerated {
		t.Errorf("IsSystemGenerated round-trip failed: meta=%v", decoded.Meta)
	}
}

// Meta.Display is persisted with the message (so a reloaded history shows what
// the live echo showed), omitted when empty, and Shown is the one place a
// frontend asks what to display.
func TestMessageMetaDisplayRoundTripsAndShownIsNilSafe(t *testing.T) {
	m := NewUserTextMessage("the whole skill body")
	m.Meta = &MessageMeta{Origin: "subagent:kano", Display: "subagent:kano reply (3 lines)"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"display":"subagent:kano reply (3 lines)"`) {
		t.Fatalf("display is not persisted: %s", b)
	}
	var back Message
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Meta == nil || back.Meta.Display != "subagent:kano reply (3 lines)" || back.Meta.Origin != "subagent:kano" {
		t.Fatalf("round trip lost the meta: %+v", back.Meta)
	}

	if got := back.Meta.Shown("full"); got != "subagent:kano reply (3 lines)" {
		t.Errorf("Shown with a display = %q", got)
	}
	var none *MessageMeta
	if got := none.Shown("full"); got != "full" {
		t.Errorf("Shown on a nil meta = %q, want the text", got)
	}
	if got := (&MessageMeta{Origin: "slack:C1"}).Shown("full"); got != "full" {
		t.Errorf("Shown without a display = %q, want the text", got)
	}
	// An older message, with no display field, is shown in full and stays compact on disk.
	old := NewUserTextMessage("hi")
	old.Meta = &MessageMeta{Origin: "slack:C1"}
	if ob, _ := json.Marshal(old); strings.Contains(string(ob), "display") {
		t.Errorf("an empty display must be omitted: %s", ob)
	}
}
