package mcp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// fakeServer is a local MCPServer that re-executes this test binary as the
// fake MCP server (see TestMain in client_test.go).
func fakeServer() types.MCPServer {
	return types.MCPServer{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}}
}

// Start connects exactly the servers it's given — no settings involved —
// skipping disabled ones, and reports invalid configs in Statuses without
// failing the rest.
func TestStartUsesProgrammaticServers(t *testing.T) {
	m := NewManager()
	defer m.Close()

	disabled := fakeServer()
	disabled.Disabled = true
	got := m.Start(context.Background(), map[string]types.MCPServer{
		"fake":  fakeServer(),
		"off":   disabled,
		"bogus": {Command: "x", URL: "https://both.example"}, // invalid: both set
	})

	names := map[string]bool{}
	for _, tl := range got {
		names[tl.Def.Name] = true
	}
	if !names["mcp__fake__echo"] || !names["mcp__fake__add"] || len(got) != 2 {
		t.Fatalf("tools = %v, want exactly mcp__fake__{echo,add}", names)
	}

	st := map[string]Status{}
	for _, s := range m.Statuses() {
		st[s.Name] = s
	}
	if s := st["fake"]; !s.Connected || s.ToolCount != 2 {
		t.Errorf("fake status = %+v, want connected with 2 tools", s)
	}
	if _, ok := st["off"]; ok {
		t.Error("disabled server must be skipped entirely")
	}
	if s := st["bogus"]; s.Connected || !strings.Contains(s.Error, types.ErrInvalidMCPServer.Error()) {
		t.Errorf("bogus status = %+v, want an invalid-mcp-server error", s)
	}
}

// Start with no servers is a no-op.
func TestStartWithNoServers(t *testing.T) {
	m := NewManager()
	defer m.Close()
	if got := m.Start(context.Background(), nil); len(got) != 0 || len(m.Statuses()) != 0 {
		t.Fatalf("nil servers must connect nothing, got tools=%d statuses=%d", len(got), len(m.Statuses()))
	}
}

// ServersFromSettings returns what `harness mcp add` stored, disabled
// entries included (Start is the one that skips them).
func TestServersFromSettings(t *testing.T) {
	restore := config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore())
	defer restore()
	sm := config.GetSettingsManager()
	if err := sm.SetMCPServer("fs", types.MCPServer{Command: "npx", Args: []string{"-y", "srv"}}); err != nil {
		t.Fatal(err)
	}
	if err := sm.SetMCPServer("api", types.MCPServer{URL: "https://x", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	got := ServersFromSettings()
	if len(got) != 2 || got["fs"].Command != "npx" || !got["api"].Disabled {
		t.Fatalf("ServersFromSettings() = %+v", got)
	}
}

func TestMCPServerValidate(t *testing.T) {
	for name, srv := range map[string]types.MCPServer{
		"both":    {Command: "x", URL: "https://x"},
		"neither": {},
	} {
		if err := srv.Validate(); !errors.Is(err, types.ErrInvalidMCPServer) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidMCPServer", name, err)
		}
	}
	for name, srv := range map[string]types.MCPServer{
		"local":  {Command: "npx"},
		"remote": {URL: "https://x"},
	} {
		if err := srv.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}
}
