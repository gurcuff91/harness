package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMessageMeta_IsSystemGenerated_JSON verifies the new metadata field
// serializes with the expected snake_case key so transports can detect
// system-injected user messages (e.g. the max-turns summary request).
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
