package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// defaultConnectTimeout bounds the initialize + tools/list phase per server. It is generous on
// purpose: a remote server may be slow to boot (a container, a cold npx install), and a connection
// that is cut off early is a server that never gets a chance to answer.
const defaultConnectTimeout = 30 * time.Second

// defaultCallTimeout bounds one tool call. It is deliberately larger than the connect timeout:
// connecting is quick, but a tool may legitimately run for minutes (a remote agent answering a
// question, a build), and 5 minutes is a ceiling for a call that has hung, not a budget.
const defaultCallTimeout = 5 * time.Minute

// httpTimeoutMargin is how much longer the HTTP client's own timeout is than a call's deadline. The
// call's context is what ends a call (and produces the error that says which setting to raise); the
// client's timeout is only a backstop behind it, so it must never fire first.
const httpTimeoutMargin = time.Minute

// newHTTPTransport is NewHTTPTransport, replaceable by a test that wants to see how connectServer
// configures it.
var newHTTPTransport = NewHTTPTransport

// connectTimeout is the server's connect timeout (initialize + tools/list), or the default.
func connectTimeout(cfg types.MCPServer) time.Duration {
	if cfg.ConnectionTimeout > 0 {
		return time.Duration(cfg.ConnectionTimeout) * time.Millisecond
	}
	return defaultConnectTimeout
}

// callTimeout is the server's per-call timeout, or the default.
func callTimeout(cfg types.MCPServer) time.Duration {
	if cfg.Timeout > 0 {
		return time.Duration(cfg.Timeout) * time.Millisecond
	}
	return defaultCallTimeout
}

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

// ServersFromSettings returns the MCP servers configured in harness's
// settings (what `harness mcp add` writes — the "mcp" namespace of the
// SettingsStore), disabled ones included (Start skips them). This is the
// explicit default source: the harness CLI passes it to every agent it
// builds, and an SDK consumer can do the same — optionally merging in its
// own servers first — via harness.AgentWithMCPs(mcp.ServersFromSettings()).
// Returns a fresh map the caller owns; empty (never an error) when nothing is
// configured or the store can't be read.
func ServersFromSettings() map[string]types.MCPServer {
	return config.GetSettingsManager().MCPServers()
}

// Start connects every enabled server in servers, initializes each, fetches
// its tools, and returns the aggregated tool list (namespaced
// mcp__<server>__<tool>). The manager never reads configuration itself — the
// caller decides where servers come from (see ServersFromSettings). Errors
// degrade: an invalid config (types.MCPServer.Validate) or a failed connect
// skips that server and records the failure in Statuses(). Safe to call once
// at agent creation.
func (m *Manager) Start(ctx context.Context, servers map[string]types.MCPServer) []tools.Tool {

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
		if cfg.Disabled {
			continue
		}

		if err := cfg.Validate(); err != nil {
			m.statuses = append(m.statuses, Status{Name: name, Connected: false, Error: err.Error()})
			continue
		}
		client, toolDefs, err := connectServer(ctx, cfg)
		if err != nil {
			m.statuses = append(m.statuses, Status{Name: name, Connected: false, Error: err.Error()})
			continue
		}
		m.clients = append(m.clients, client)
		for _, t := range toolDefs {
			m.toolList = append(m.toolList, wrapTool(name, t, client, callTimeout(cfg)))
		}
		m.statuses = append(m.statuses, Status{Name: name, Connected: true, ToolCount: len(toolDefs)})
	}
	return m.toolList
}

// connectServer builds the right frontend for the server's type (local=stdio,
// remote=HTTP), initializes it, and lists its tools — bounded by the server's
// connect timeout (or the default). The connect timeout bounds ONLY this phase, through
// connectCtx; what bounds a later tool call is the call timeout (see wrapTool), which is also the
// HTTP client's own ceiling — the connect timeout must never become one, or a slow tool would be
// cut off at the seconds that are right for connecting.
func connectServer(ctx context.Context, cfg types.MCPServer) (*Client, []Tool, error) {
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout(cfg))
	defer cancel()

	var tr Transport
	var err error
	if cfg.IsRemote() {
		tr, err = newHTTPTransport(HTTPConfig{URL: cfg.URL, Headers: cfg.Headers, Timeout: callTimeout(cfg) + httpTimeoutMargin})
	} else {
		tr, err = NewStdioTransport(StdioConfig{Command: cfg.Argv(), Env: cfg.Env, Cwd: cfg.Cwd})
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
// reuse the parent's process rather than spawning their own. Every call is bounded by timeout (the
// server's "timeout"), on top of the caller's own cancellation (Stop still ends it at once).
func wrapTool(server string, t Tool, client *Client, timeout time.Duration) tools.Tool {
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
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			res, err := client.CallTool(callCtx, t.Name, input)
			if err != nil {
				// Our deadline, not the caller's: say so, and say what to change. A caller that
				// stopped the turn keeps its own error.
				if ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
					return "", fmt.Errorf("mcp server %q: %q did not answer within %v — raise \"timeout\" (ms) for this server if the tool is meant to run that long: %w", server, t.Name, timeout, err)
				}
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
