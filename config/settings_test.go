package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestSettings builds a SettingsManager backed by a temp file with the given
// initial JSON contents (empty string = no file).
func newTestSettings(t *testing.T, initial string) *SettingsManager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if initial != "" {
		if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
			t.Fatalf("seed settings: %v", err)
		}
	}
	m := &SettingsManager{path: path}
	m.load()
	return m
}

// TestRoundTrip verifies settings load and persist with the unified names.
func TestRoundTrip(t *testing.T) {
	m := newTestSettings(t, `{"active_model":"anthropic/claude","thinking_level":"high"}`)
	if got := m.ActiveModel(); got != "anthropic/claude" {
		t.Errorf("ActiveModel = %q", got)
	}
	if got := m.ThinkingLevel(); got != "high" {
		t.Errorf("ThinkingLevel = %q", got)
	}

	// Save and confirm only the unified names are written.
	if err := m.SetActiveModel("minimax/MiniMax-M3"); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, _ := os.ReadFile(m.path)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if out["active_model"] != "minimax/MiniMax-M3" {
		t.Errorf("active_model missing/wrong after save: %s", raw)
	}
	if out["thinking_level"] != "high" {
		t.Errorf("thinking_level missing/wrong after save: %s", raw)
	}
}

// TestProvidersCollection verifies provider configs round-trip and delete.
func TestProvidersCollection(t *testing.T) {
	m := newTestSettings(t, `{"active_model":"m","thinking_level":"low","providers":{"ollama":{"url":"http://x"}}}`)
	if cfg, ok := m.Provider("ollama"); !ok || cfg.URL != "http://x" {
		t.Errorf("provider load lost: %+v ok=%v", cfg, ok)
	}
	// Set a new provider (exercises lazy-init when the map already exists).
	if err := m.SetProvider("ollama-cloud", ProviderConfig{URL: "http://y"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Reload from disk and confirm both persist.
	m2 := newTestSettings(t, "")
	m2.path = m.path
	m2.load()
	if cfg, _ := m2.Provider("ollama"); cfg.URL != "http://x" {
		t.Errorf("ollama not persisted: %q", cfg.URL)
	}
	if cfg, _ := m2.Provider("ollama-cloud"); cfg.URL != "http://y" {
		t.Errorf("ollama-cloud not persisted: %q", cfg.URL)
	}
	// Delete and confirm gone.
	if err := m2.DeleteProvider("ollama"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := m2.Provider("ollama"); ok {
		t.Errorf("ollama still present after delete")
	}
}

// TestProvidersDefensiveCopy verifies Providers() returns a copy, not the map.
func TestProvidersDefensiveCopy(t *testing.T) {
	m := newTestSettings(t, `{"providers":{"ollama":{"url":"http://x"}}}`)
	copy := m.Providers()
	copy["ollama"] = ProviderConfig{URL: "MUTATED"}
	copy["injected"] = ProviderConfig{URL: "z"}
	if cfg, _ := m.Provider("ollama"); cfg.URL != "http://x" {
		t.Errorf("internal map mutated via Providers() copy: %q", cfg.URL)
	}
	if _, ok := m.Provider("injected"); ok {
		t.Errorf("injected key leaked into internal map")
	}
}

// TestThinkingLevelValidation verifies SetThinkingLevel accepts only canonical
// levels and rejects anything else without persisting.
func TestThinkingLevelValidation(t *testing.T) {
	m := newTestSettings(t, `{"thinking_level":"medium"}`)
	for _, lvl := range []string{"off", "low", "medium", "high", "xhigh"} {
		if err := m.SetThinkingLevel(lvl); err != nil {
			t.Errorf("level %q: expected accepted, got %v", lvl, err)
		}
	}
	for _, lvl := range []string{"", "disable", "medim", "MEDIUM", "max"} {
		if err := m.SetThinkingLevel(lvl); err == nil {
			t.Errorf("level %q: expected rejected, got nil", lvl)
		} else if !errors.Is(err, ErrInvalidThinkingLevel) {
			t.Errorf("level %q: expected ErrInvalidThinkingLevel, got %v", lvl, err)
		}
	}
	// After the rejected writes, the last accepted value must still be intact.
	if got := m.ThinkingLevel(); got != "xhigh" {
		t.Errorf("invalid write mutated state: ThinkingLevel = %q, want xhigh", got)
	}
}

// TestMCPValidation verifies SetMCPServer rejects malformed configs and accepts
// valid local/remote shapes.
// TestMCPTypeAliases verifies friendly transport aliases are canonicalized.
func TestMCPTypeAliases(t *testing.T) {
	m := newTestSettings(t, "")
	// "http" alias for a remote server should be accepted and stored as "remote".
	if err := m.SetMCPServer("api", MCPServer{Type: "http", URL: "https://x"}); err != nil {
		t.Fatalf("http alias rejected: %v", err)
	}
	if srv, _ := m.MCPServer("api"); srv.Type != "remote" {
		t.Errorf("http not canonicalized: got %q", srv.Type)
	}
	// "stdio" alias for a local server → "local".
	if err := m.SetMCPServer("fs", MCPServer{Type: "stdio", Command: []string{"x"}}); err != nil {
		t.Fatalf("stdio alias rejected: %v", err)
	}
	if srv, _ := m.MCPServer("fs"); srv.Type != "local" {
		t.Errorf("stdio not canonicalized: got %q", srv.Type)
	}
	// load() must canonicalize hand-edited files too.
	m2 := newTestSettings(t, `{"mcp":{"k":{"type":"http","url":"https://y","enabled":true}}}`)
	if srv, _ := m2.MCPServer("k"); srv.Type != "remote" {
		t.Errorf("load did not canonicalize: got %q", srv.Type)
	}
}

func TestMCPValidation(t *testing.T) {
	m := newTestSettings(t, "")
	bad := map[string]MCPServer{
		"unknown-type":   {Type: "bogus"},
		"empty-type":     {Type: ""},
		"local-no-cmd":   {Type: "local"},
		"remote-no-url":  {Type: "remote"},
	}
	for name, srv := range bad {
		if err := m.SetMCPServer(name, srv); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		} else if !errors.Is(err, ErrInvalidMCPServer) {
			t.Errorf("%s: expected ErrInvalidMCPServer, got %v", name, err)
		}
		if _, ok := m.MCPServer(name); ok {
			t.Errorf("%s: invalid server was persisted", name)
		}
	}
	good := map[string]MCPServer{
		"local":  {Type: "local", Command: []string{"npx"}},
		"remote": {Type: "remote", URL: "https://x"},
	}
	for name, srv := range good {
		if err := m.SetMCPServer(name, srv); err != nil {
			t.Errorf("%s: expected success, got %v", name, err)
		}
	}
}

// TestMCPCollection verifies MCP servers round-trip, including local (Env) and
// remote (Headers) shapes, and delete.
func TestMCPCollection(t *testing.T) {
	m := newTestSettings(t, "")
	local := MCPServer{Type: "local", Command: []string{"npx", "-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}, Enabled: true}
	remote := MCPServer{Type: "remote", URL: "https://mcp.example", Headers: map[string]string{"Authorization": "Bearer t"}, Enabled: true}
	if err := m.SetMCPServer("fs", local); err != nil {
		t.Fatalf("set local: %v", err)
	}
	if err := m.SetMCPServer("api", remote); err != nil {
		t.Fatalf("set remote: %v", err)
	}
	// Reload from disk.
	m2 := newTestSettings(t, "")
	m2.path = m.path
	m2.load()
	gotLocal, ok := m2.MCPServer("fs")
	if !ok || gotLocal.Type != "local" || len(gotLocal.Command) != 3 || gotLocal.Env["K"] != "V" {
		t.Errorf("local mcp not persisted: %+v ok=%v", gotLocal, ok)
	}
	gotRemote, ok := m2.MCPServer("api")
	if !ok || gotRemote.Type != "remote" || gotRemote.URL != "https://mcp.example" || gotRemote.Headers["Authorization"] != "Bearer t" {
		t.Errorf("remote mcp not persisted: %+v ok=%v", gotRemote, ok)
	}
	if err := m2.DeleteMCPServer("fs"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := m2.MCPServer("fs"); ok {
		t.Errorf("fs still present after delete")
	}
}
