package agent

import (
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// No MCPServers → no MCP manager, no MCP tools, no statuses.
func TestNoMCPServersMeansNoMCP(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()
	if a.mcpManager != nil || a.MCPTools() != nil || a.MCPStatuses() != nil {
		t.Fatal("an agent without MCPServers must not start an MCP manager")
	}
}

// Programmatic servers reach the manager directly (no settings involved): a
// server that can't connect is reported in MCPStatuses, never fatal, and an
// invalid config is rejected the same way `harness mcp add` would.
func TestProgrammaticMCPServersReachTheManager(t *testing.T) {
	a := New(AgentOptions{
		Store: store.NewInMemoryStore(),
		MCPServers: map[string]types.MCPServer{
			"missing": {Command: "/nonexistent/harness-mcp-test-binary"},
			"invalid": {},
		},
	})
	defer a.Close()

	if a.mcpManager == nil {
		t.Fatal("MCPServers set but no MCP manager started")
	}
	st := map[string]bool{}
	for _, s := range a.MCPStatuses() {
		if s.Connected {
			t.Errorf("%s: unexpectedly connected", s.Name)
		}
		if s.Error == "" {
			t.Errorf("%s: failure must carry an error", s.Name)
		}
		st[s.Name] = true
	}
	if !st["missing"] || !st["invalid"] {
		t.Fatalf("statuses = %v, want both programmatic servers reported", a.MCPStatuses())
	}
}
