package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/types"
)

// defaultConnectTimeout bounds the initialize + tools/list phase per server.
const defaultConnectTimeout = 5 * time.Second

// Status reports the connection outcome of one configured MCP server. It is
// surfaced (e.g. via the HTTP API) so clients can render connection state
// without the manager ever writing to stdout — keeping the TUI uncontaminated.
type Status struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"tool_count"`
	Error     string `json:"error,omitempty"`
}

// Manager owns the live MCP client connections for the process. It connects the
// enabled servers eagerly (once), exposes their tools to the agent, and tracks
// per-server status. A failed server is skipped silently (degraded) and recorded
// in its Status.Error.
type Manager struct {
	mu       sync.Mutex
	clients  []*Client
	toolList []tools.Tool
	statuses []Status
}

// NewManager returns an empty manager. Call Start to connect.
func NewManager() *Manager {
	return &Manager{}
}

// Start connects every enabled MCP server from settings, initializes each,
// fetches its tools, and returns the aggregated tool list (namespaced
// mcp__<server>__<tool>). Errors degrade: the offending server is skipped and
// its failure is recorded in Statuses(). Safe to call once at agent creation.
func (m *Manager) Start(ctx context.Context) []tools.Tool {
	servers := config.GetSettingsManager().MCPServers()

	// Deterministic order for stable tool listing.
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, name := range names {
		cfg := servers[name]
		if !cfg.Enabled {
			continue
		}

		client, toolDefs, err := connectServer(ctx, cfg)
		if err != nil {
			m.statuses = append(m.statuses, Status{Name: name, Connected: false, Error: err.Error()})
			continue
		}
		m.clients = append(m.clients, client)
		for _, t := range toolDefs {
			m.toolList = append(m.toolList, wrapTool(name, t, client))
		}
		m.statuses = append(m.statuses, Status{Name: name, Connected: true, ToolCount: len(toolDefs)})
	}
	return m.toolList
}

// connectServer builds the right transport for the server's type (local=stdio,
// remote=HTTP), initializes it, and lists its tools — bounded by the server's
// timeout (or the default).
func connectServer(ctx context.Context, cfg config.MCPServer) (*Client, []Tool, error) {
	timeout := defaultConnectTimeout
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Millisecond
	}
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var tr Transport
	var err error
	switch cfg.Type {
	case "remote":
		tr, err = NewHTTPTransport(HTTPConfig{URL: cfg.URL, Headers: cfg.Headers, Timeout: timeout})
	default: // "local"
		tr, err = NewStdioTransport(StdioConfig{Command: cfg.Command, Env: cfg.Env, Cwd: cfg.Cwd})
	}
	if err != nil {
		return nil, nil, err
	}
	client := NewClient(tr)
	if err := client.Initialize(connectCtx); err != nil {
		client.Close()
		return nil, nil, err
	}
	toolDefs, err := client.ListTools(connectCtx)
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	return client, toolDefs, nil
}

// wrapTool adapts an MCP tool into a harness tool. The name is namespaced
// mcp__<server>__<tool> (Claude Code style) to avoid collisions; Execute routes
// the call to the owning server's client — so subagents that inherit this tool
// reuse the parent's process rather than spawning their own.
func wrapTool(server string, t Tool, client *Client) tools.Tool {
	name := fmt.Sprintf("mcp__%s__%s", server, t.Name)
	schema := t.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	desc := t.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool %q from server %q", t.Name, server)
	}
	return tools.Tool{
		Def: types.ToolDef{Name: name, Description: desc, InputSchema: schema},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			res, err := client.CallTool(ctx, t.Name, input)
			if err != nil {
				return "", err
			}
			text := res.Text()
			if res.IsError {
				return "", fmt.Errorf("%s", text)
			}
			return text, nil
		},
	}
}

// Tools returns the aggregated MCP tools (for sharing with subagents).
func (m *Manager) Tools() []tools.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tools.Tool, len(m.toolList))
	copy(out, m.toolList)
	return out
}

// Statuses returns a copy of the per-server connection statuses.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, len(m.statuses))
	copy(out, m.statuses)
	return out
}

// Close terminates all MCP client connections and their subprocesses.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		_ = c.Close()
	}
	m.clients = nil
	return nil
}
