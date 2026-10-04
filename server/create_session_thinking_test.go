package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/logx"
)

// TestCreateSessionHonorsAgentThinkingLevelOverGlobalSetting is the
// regression test for the bug reported against v0.76.44: handleCreateSession
// used to unconditionally call sess.SwitchThinking(globalSetting) right after
// NewSession had already correctly resolved the session's thinking level from
// the calling Agent's own a.thinkingLevel (AgentWithThinking(...)), silently
// discarding an SDK embedder's deliberately different level with no error or
// log anywhere. The fix removed that second, unconditional re-application —
// this test asserts a session created via POST /api/sessions keeps the
// AGENT's level even when the global ~/.harness/settings.json disagrees.
//
// The global settings store is an in-memory one for this whole test binary
// (see TestMain in configisolation_test.go), so writing the global level here
// never touches the real developer/CI settings.json. The skip below is a
// last-resort guard in case that isolation is ever removed.
func TestCreateSessionHonorsAgentThinkingLevelOverGlobalSetting(t *testing.T) {
	sm := config.GetSettingsManager()
	if err := sm.SetThinkingLevel("high"); err != nil {
		t.Fatalf("seed global thinking level: %v", err)
	}
	if got := sm.ThinkingLevel(); got != "high" {
		t.Skipf("global settings.json isn't isolated in this test binary (got %q) — some other test already initialized the singleton against a different HOME; skipping to avoid mutating real state", got)
	}

	a := agent.New(agent.AgentOptions{
		Store:         store.NewInMemoryStore(),
		ThinkingLevel: "low", // deliberately differs from the global "high" above
	})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	srv := NewServer(a, ServerOptions{Logger: logx.NewNilLogger()})
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{
		"model": models[0].Model,
		"cwd":   t.TempDir(),
	})
	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	var got sessionDetailDTO
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Thinking != "low" {
		t.Errorf("session thinking = %q, want the Agent's own %q (global %q must NOT override it)", got.Thinking, "low", "high")
	}
}
