package admin

import (
	"errors"

	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// MCP administers the configured MCP servers (the settings collection). Live
// connection status stays on the agent (mcp.Manager).
type MCP struct{ ad *Admin }

func newMCP(ad *Admin) *MCP { return &MCP{ad: ad} }

// List returns the whole collection (a fresh map).
func (m *MCP) List() map[string]types.MCPServer {
	return config.GetSettingsManager().MCPServers()
}

// Get returns one server (ErrNotFound if absent).
func (m *MCP) Get(name string) (types.MCPServer, error) {
	srv, ok := config.GetSettingsManager().MCPServer(name)
	if !ok {
		return types.MCPServer{}, markf(ErrNotFound, "mcp server not found: %s", name)
	}
	return srv, nil
}

// Put validates and stores (or replaces) one server. An invalid config wraps
// ErrInvalid, ErrUnprocessable (422) and types.ErrInvalidMCPServer.
func (m *MCP) Put(name string, srv types.MCPServer) error {
	err := config.GetSettingsManager().SetMCPServer(name, srv)
	if errors.Is(err, types.ErrInvalidMCPServer) {
		return &marked2{kinds: []error{ErrInvalid, ErrUnprocessable}, err: err}
	}
	return err
}

// Delete removes one server (ErrNotFound if absent).
func (m *MCP) Delete(name string) error {
	sm := config.GetSettingsManager()
	if _, ok := sm.MCPServer(name); !ok {
		return markf(ErrNotFound, "mcp server not found: %s", name)
	}
	return sm.DeleteMCPServer(name)
}

// SetEnabled flips a server's disabled flag, preserving the rest of its
// config. ErrNotFound if absent.
func (m *MCP) SetEnabled(name string, enabled bool) error {
	srv, err := m.Get(name)
	if err != nil {
		return err
	}
	srv.Disabled = !enabled
	return m.Put(name, srv)
}
