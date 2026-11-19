package providers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// A message's CreatedAt is display data for clients: no provider dialect may
// put it on the wire (the model must never see when a message was written).
func TestCreatedAtNeverReachesAProvider(t *testing.T) {
	at := time.Date(2026, 11, 19, 3, 4, 5, 0, time.UTC)
	msgs := []types.Message{
		{Role: types.RoleUser, Parts: []types.ContentPart{{Text: "hello"}}, CreatedAt: at},
		{Role: types.RoleAssistant, Parts: []types.ContentPart{{Text: "hi"}}, CreatedAt: at},
	}
	var wire []string
	for _, m := range msgs {
		for _, raw := range llm.TranslateMessageToAnthropic(m) {
			wire = append(wire, string(raw))
		}
		for _, raw := range llm.TranslateMessageToOpenAIForTest(m) {
			wire = append(wire, string(raw))
		}
	}
	for _, raw := range buildWireMessages(msgs) {
		wire = append(wire, string(raw))
	}
	items, err := buildCodexInput(msgs)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(items)
	wire = append(wire, string(b))
	for _, w := range wire {
		if strings.Contains(w, "created_at") || strings.Contains(w, "2026-11-19") {
			t.Errorf("a timestamp reached the wire: %s", w)
		}
	}
	if len(wire) < 5 {
		t.Fatalf("only %d wire payloads checked", len(wire))
	}
}
