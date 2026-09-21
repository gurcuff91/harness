package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestSettingsCrossProcessReload is the regression test for the same
// multi-instance staleness bug as credentials.go's
// TestCredentialCrossProcessReload, but for settings.json: a second,
// independent SettingsManager pointed at the same file must pick up another
// manager's write (e.g. /model in one TUI instance) on its very next read,
// without needing to be recreated.
func TestSettingsCrossProcessReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	writer := &SettingsManager{path: path}
	writer.load()
	reader := &SettingsManager{path: path}
	reader.load()

	if got := reader.ActiveModel(); got != "" {
		t.Fatalf("reader should see no active model yet, got %q", got)
	}

	if err := writer.SetActiveModel("anthropic/claude-a"); err != nil {
		t.Fatalf("writer.SetActiveModel: %v", err)
	}
	bumpSettingsMtime(t, path, 2*time.Second)

	if got := reader.ActiveModel(); got != "anthropic/claude-a" {
		t.Fatalf("reader did not pick up the writer's ActiveModel via reloadIfStale: %q", got)
	}

	// A second write must also propagate.
	if err := writer.SetActiveModel("anthropic/claude-b"); err != nil {
		t.Fatalf("writer.SetActiveModel (2nd): %v", err)
	}
	bumpSettingsMtime(t, path, 4*time.Second)

	if got := reader.ActiveModel(); got != "anthropic/claude-b" {
		t.Fatalf("reader did not pick up the SECOND write: %q", got)
	}
}

// bumpSettingsMtime is settings_test.go's copy of credentials_test.go's
// bumpMtime (unexported, package-private helpers can't cross _test.go files
// cleanly without a shared non-test file, and this is trivial enough not to
// warrant one).
func bumpSettingsMtime(t *testing.T, path string, delta time.Duration) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	newTime := info.ModTime().Add(delta)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
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

// TestValidThinkingLevel covers the pure validator exposed so callers can
// check a level WITHOUT persisting it (e.g. a session's /thinking command,
// which must reject an invalid value before applying it to the live session,
// without writing the global default).
func TestValidThinkingLevel(t *testing.T) {
	for _, ok := range []string{"off", "low", "medium", "high", "xhigh"} {
		if !ValidThinkingLevel(ok) {
			t.Errorf("ValidThinkingLevel(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "OFF", "none", "max", "xxhigh", " high"} {
		if ValidThinkingLevel(bad) {
			t.Errorf("ValidThinkingLevel(%q) = true, want false", bad)
		}
	}
}

// ── Custom providers ─────────────────────────────────────────────────────

func TestCustomProviderValidation(t *testing.T) {
	m := newTestSettings(t, "")

	bad := map[string]CustomProvider{
		"unsupported-type": {Type: "anthropic", URL: "https://x"},
		"empty-type":       {URL: "https://x"},
		"missing-url":      {Type: "openai"},
	}
	for name, p := range bad {
		if err := m.SetCustomProvider(name, p); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		} else if !errors.Is(err, ErrInvalidCustomProvider) {
			t.Errorf("%s: expected ErrInvalidCustomProvider, got %v", name, err)
		}
		if _, ok := m.CustomProvider(name); ok {
			t.Errorf("%s: invalid provider was persisted", name)
		}
	}

	if err := m.SetCustomProvider("my-proxy", CustomProvider{Type: "openai", URL: "https://my-proxy.internal/v1"}); err != nil {
		t.Errorf("well-formed provider: expected success, got %v", err)
	}
}

// TestCustomProviderReservedNames confirms every built-in provider name is
// rejected — this set must never drift silently from the real registry
// (internal/providers/*.go's Name() values).
func TestCustomProviderReservedNames(t *testing.T) {
	m := newTestSettings(t, "")
	reserved := []string{"anthropic", "claude-oauth", "codex-oauth", "minimax", "ollama-cloud", "ollama", "openai", "opencode-go"}
	for _, name := range reserved {
		err := m.SetCustomProvider(name, CustomProvider{Type: "openai", URL: "https://x"})
		if err == nil {
			t.Errorf("%s: expected rejection as a reserved built-in name, got nil error", name)
		} else if !errors.Is(err, ErrInvalidCustomProvider) {
			t.Errorf("%s: expected ErrInvalidCustomProvider, got %v", name, err)
		}
	}
	// A name that merely CONTAINS a reserved name isn't reserved itself.
	if err := m.SetCustomProvider("my-openai-proxy", CustomProvider{Type: "openai", URL: "https://x"}); err != nil {
		t.Errorf("non-colliding name should be accepted, got %v", err)
	}
}

// TestCustomProviderCollection verifies custom providers round-trip through
// disk, including all optional fields, and delete.
func TestCustomProviderCollection(t *testing.T) {
	m := newTestSettings(t, "")
	p := CustomProvider{
		Type:      "openai",
		URL:       "https://my-proxy.internal/v1",
		ModelsURL: "https://my-proxy.internal/v1/custom-models",
		Headers:   map[string]string{"X-Org-Id": "acme"},
		Display:   "Acme Proxy",
		Disabled:  false,
	}
	if err := m.SetCustomProvider("my-proxy", p); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Reload from disk.
	m2 := newTestSettings(t, "")
	m2.path = m.path
	m2.load()
	got, ok := m2.CustomProvider("my-proxy")
	if !ok {
		t.Fatal("provider not persisted")
	}
	if got.Type != "openai" || got.URL != p.URL || got.ModelsURL != p.ModelsURL ||
		got.Headers["X-Org-Id"] != "acme" || got.Display != "Acme Proxy" {
		t.Errorf("provider round-trip mismatch: %+v", got)
	}

	all := m2.CustomProviders()
	if len(all) != 1 {
		t.Errorf("CustomProviders() len = %d, want 1", len(all))
	}

	if err := m2.DeleteCustomProvider("my-proxy"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := m2.CustomProvider("my-proxy"); ok {
		t.Error("provider still present after delete")
	}
}

// TestCustomProviderDisabledField confirms Disabled round-trips and
// defaults to false (enabled) when omitted — same omitempty contract as
// MCPServer.Disabled.
func TestCustomProviderDisabledField(t *testing.T) {
	m := newTestSettings(t, "")
	if err := m.SetCustomProvider("p1", CustomProvider{Type: "openai", URL: "https://x", Disabled: true}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok := m.CustomProvider("p1")
	if !ok || !got.Disabled {
		t.Errorf("Disabled did not round-trip: %+v ok=%v", got, ok)
	}

	raw, _ := os.ReadFile(m.path)
	var out map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if out["provider"]["p1"]["disabled"] != true {
		t.Errorf("disabled:true missing from raw JSON: %s", raw)
	}
}
