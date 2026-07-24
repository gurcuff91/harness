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

// TestMCPTransportInference verifies the transport is inferred from which of
// command/url is set, and that a disabled server round-trips.
func TestMCPTransportInference(t *testing.T) {
	m := newTestSettings(t, "")
	// A command → local (IsRemote false).
	if err := m.SetMCPServer("fs", MCPServer{Command: "npx", Args: []string{"-y", "srv"}}); err != nil {
		t.Fatalf("local rejected: %v", err)
	}
	if srv, _ := m.MCPServer("fs"); srv.IsRemote() {
		t.Errorf("command server should be local, got remote")
	}
	// A url → remote (IsRemote true).
	if err := m.SetMCPServer("api", MCPServer{URL: "https://x"}); err != nil {
		t.Fatalf("remote rejected: %v", err)
	}
	if srv, _ := m.MCPServer("api"); !srv.IsRemote() {
		t.Errorf("url server should be remote, got local")
	}
	// Hand-edited file with the canonical shape + disabled loads correctly.
	m2 := newTestSettings(t, `{"mcp":{"k":{"command":"npx","args":["-y","srv"],"disabled":true}}}`)
	srv, ok := m2.MCPServer("k")
	if !ok {
		t.Fatal("server did not load")
	}
	if srv.IsRemote() {
		t.Errorf("command server should be local")
	}
	if srv.Command != "npx" || len(srv.Args) != 2 || srv.Args[0] != "-y" || srv.Args[1] != "srv" {
		t.Errorf("command/args not decoded: cmd=%q args=%v", srv.Command, srv.Args)
	}
	if !srv.Disabled {
		t.Errorf("disabled:true should load as Disabled=true")
	}
}

func TestMCPValidation(t *testing.T) {
	m := newTestSettings(t, "")
	bad := map[string]MCPServer{
		"empty": {},                                 // neither command nor url
		"both":  {Command: "npx", URL: "https://x"}, // ambiguous
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
		"local":  {Command: "npx"},
		"remote": {URL: "https://x"},
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
	local := MCPServer{Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}}
	remote := MCPServer{URL: "https://mcp.example", Headers: map[string]string{"Authorization": "Bearer t"}}
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
	if !ok || gotLocal.IsRemote() || gotLocal.Command != "npx" || len(gotLocal.Args) != 2 || gotLocal.Env["K"] != "V" {
		t.Errorf("local mcp not persisted: %+v ok=%v", gotLocal, ok)
	}
	if argv := gotLocal.Argv(); len(argv) != 3 || argv[0] != "npx" || argv[2] != "@mcp/fs" {
		t.Errorf("Argv() wrong: %v", argv)
	}
	gotRemote, ok := m2.MCPServer("api")
	if !ok || !gotRemote.IsRemote() || gotRemote.URL != "https://mcp.example" || gotRemote.Headers["Authorization"] != "Bearer t" {
		t.Errorf("remote mcp not persisted: %+v ok=%v", gotRemote, ok)
	}
	if err := m2.DeleteMCPServer("fs"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := m2.MCPServer("fs"); ok {
		t.Errorf("fs still present after delete")
	}
}

// TestMCPArgv verifies the canonical `command` string + `args` array shape
// flattens to the full argv (executable + args) via Argv().
func TestMCPArgv(t *testing.T) {
	cases := []struct {
		name string
		json string
		want []string
	}{
		{
			name: "command_plus_args",
			json: `{"command":"uvx","args":["minimax-coding-plan-mcp","-y"]}`,
			want: []string{"uvx", "minimax-coding-plan-mcp", "-y"},
		},
		{
			name: "command_only",
			json: `{"command":"echo"}`,
			want: []string{"echo"},
		},
		{
			name: "remote_no_command",
			json: `{"url":"https://x"}`,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var srv MCPServer
			if err := json.Unmarshal([]byte(c.json), &srv); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := srv.Argv()
			if len(got) != len(c.want) {
				t.Errorf("argv length: got %d (%v), want %d (%v)", len(got), got, len(c.want), c.want)
				return
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("argv[%d]: got %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}
