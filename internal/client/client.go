// Package client is the single, typed HTTP/SSE client for harness's internal
// API (internal/server). Every transport (TUI, Telegram, the CLI's one-shot
// `-p` prompt, and any future one) is a thin frontend over the same backend —
// this is the one client they all share, instead of each reimplementing the
// same request/decode/stream boilerplate.
//
// It is a real SDK over the API: every method returns a decoded Go value (see
// types.go / event.go), never a raw []byte or map[string]any. Callers get a
// []Session, a *Status, a typed Event stream — decoding is done here, once,
// against wire types that match the server's, so no transport re-invents it and
// no two of them drift.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/gurcuff91/harness/types"
)

// Client is a typed HTTP/SSE client for the harness internal API
// (internal/server). One instance per connection to a running server
// (typically the in-process server a transport starts on a loopback port via
// startInternalServer).
type Client struct {
	baseURL string
	http    *http.Client
}

// New creates a client for the given server address (host:port, no scheme).
func New(addr string) *Client {
	return &Client{
		baseURL: "http://" + addr,
		http:    &http.Client{},
	}
}

// do sends one request and returns the raw response body. A 4xx/5xx status
// returns the body alongside an *Error (see error.go) when the response is
// harness's standard {"error": {"message", "details"}} shape, or a plain
// error otherwise. It is the private transport primitive; the exported methods
// decode its result into typed values.
func (c *Client) do(method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode >= 400 {
		if ae := parseError(data); ae != nil {
			return data, ae
		}
		return data, fmt.Errorf("%s", string(bytes.TrimSpace(data)))
	}
	return data, nil
}

// decode runs do() and unmarshals the successful body into *T. A transport
// error is returned as-is; a decode error is wrapped.
func decode[T any](c *Client, method, path string, body any) (T, error) {
	var out T
	data, err := c.do(method, path, body)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return out, nil
}

// decodeStatus runs an action endpoint that returns {"status": {...}} and
// unwraps the inner Status. Used by connect/disconnect/close/stop/prompt/exec.
func (c *Client) decodeStatus(method, path string, body any) (*Status, error) {
	var env struct {
		Status Status `json:"status"`
	}
	data, err := c.do(method, path, body)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(data, &env)
	return &env.Status, nil
}

// ── Server info ────────────────────────────────────────────────────────────

// GetServerInfo returns the server info document (version, pid, cwd, …).
func (c *Client) GetServerInfo() (*ServerInfo, error) {
	return ptr(decode[ServerInfo](c, "GET", "/api/server", nil))
}

// ── Settings ─────────────────────────────────────────────────────────────

// GetSettings returns the persisted core settings (active model, thinking).
func (c *Client) GetSettings() (*Settings, error) {
	return ptr(decode[Settings](c, "GET", "/api/settings", nil))
}

// PatchSettings partially updates core settings (persists the global default;
// does not touch live sessions). Pass a map with the fields to change; the
// updated Settings is returned.
func (c *Client) PatchSettings(fields map[string]any) (*Settings, error) {
	return ptr(decode[Settings](c, "PATCH", "/api/settings", fields))
}

// ── Provider configs (settings collection) ──────────────────────────────

// GetProviderConfigs returns the whole provider-config collection, keyed by
// provider name.
func (c *Client) GetProviderConfigs() (map[string]ProviderConfig, error) {
	return decode[map[string]ProviderConfig](c, "GET", "/api/settings/providers", nil)
}

// PutProviderConfig stores (or replaces) one provider's config.
func (c *Client) PutProviderConfig(name string, cfg ProviderConfig) (*ProviderConfig, error) {
	return ptr(decode[ProviderConfig](c, "PUT", "/api/settings/providers/"+name, cfg))
}

// DeleteProviderConfig removes one provider's config.
func (c *Client) DeleteProviderConfig(name string) (*Status, error) {
	return c.decodeStatus("DELETE", "/api/settings/providers/"+name, nil)
}

// ── Providers (live state) ───────────────────────────────────────────────

// GetProviders returns all registered providers and their live activation state.
func (c *Client) GetProviders() ([]Provider, error) {
	return decode[[]Provider](c, "GET", "/api/providers", nil)
}

// ConnectProvider connects an api-key (or credential-less) provider. Pass an
// empty apiKey for auto-detected providers (e.g. ollama).
func (c *Client) ConnectProvider(name, apiKey string) (*Status, error) {
	body := map[string]any{}
	if apiKey != "" {
		body["api_key"] = apiKey
	}
	return c.decodeStatus("POST", "/api/providers/"+name+"/connect", body)
}

// ConnectProviderWithCreds sends full OAuth credentials to connect a
// subscription provider.
func (c *Client) ConnectProviderWithCreds(name string, creds *types.Credentials) (*Status, error) {
	return c.decodeStatus("POST", "/api/providers/"+name+"/connect", map[string]any{
		"access_token":  creds.AccessToken,
		"refresh_token": creds.RefreshToken,
		"expires_at":    creds.ExpiresAt,
	})
}

// DisconnectProvider disconnects an active provider.
func (c *Client) DisconnectProvider(name string) (*Status, error) {
	return c.decodeStatus("POST", "/api/providers/"+name+"/disconnect", nil)
}

// ── Models ───────────────────────────────────────────────────────────────

// ListModels returns every model of every active provider, with metadata.
func (c *Client) ListModels() ([]Model, error) {
	return decode[[]Model](c, "GET", "/api/models", nil)
}

// ── MCP servers ──────────────────────────────────────────────────────────

// GetMCPServers returns the whole MCP-server collection, keyed by name.
func (c *Client) GetMCPServers() (map[string]MCPServer, error) {
	return decode[map[string]MCPServer](c, "GET", "/api/settings/mcp", nil)
}

// PutMCPServer stores (or replaces) one MCP server.
func (c *Client) PutMCPServer(name string, srv MCPServer) (*MCPServer, error) {
	return ptr(decode[MCPServer](c, "PUT", "/api/settings/mcp/"+name, srv))
}

// DeleteMCPServer removes one MCP server.
func (c *Client) DeleteMCPServer(name string) (*Status, error) {
	return c.decodeStatus("DELETE", "/api/settings/mcp/"+name, nil)
}

// GetMCPStatus returns the live connection status of configured MCP servers.
func (c *Client) GetMCPStatus() ([]MCPStatus, error) {
	return decode[[]MCPStatus](c, "GET", "/api/mcp/status", nil)
}

// ── Memories (read-only) ─────────────────────────────────────────────────

// GetMemories queries the read-only memories endpoint. rawQuery is the URL
// query string (cwd, query, include_content, skip, limit), already encoded —
// pass "" for no filter.
func (c *Client) GetMemories(rawQuery string) (*MemorySearchResult, error) {
	path := "/api/memories"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	return ptr(decode[MemorySearchResult](c, "GET", path, nil))
}

// ── Schedules (read-only) ────────────────────────────────────────────────

// GetSchedules returns the configured cron-scheduled prompts. A non-empty
// owner filters to the schedules owned by that session (the ones that fire
// in it); pass "" for the full (operator) view.
func (c *Client) GetSchedules(owner string) ([]Schedule, error) {
	path := "/api/schedules"
	if owner != "" {
		path += "?owner=" + url.QueryEscape(owner)
	}
	return decode[[]Schedule](c, "GET", path, nil)
}

// ── Sessions ─────────────────────────────────────────────────────────────

// CreateSession opens a new session and returns it.
// name is optional — pass "" for the default "New Session <date>" naming.
func (c *Client) CreateSession(model, cwd, name string) (*Session, error) {
	body := map[string]string{"model": model, "cwd": cwd}
	if name != "" {
		body["name"] = name
	}
	return ptr(decode[Session](c, "POST", "/api/sessions", body))
}

// ListSessions lists sessions, optionally filtered by cwd ("" = all cwds).
func (c *Client) ListSessions(cwd string) ([]Session, error) {
	path := "/api/sessions"
	if cwd != "" {
		path += "?cwd=" + url.QueryEscape(cwd)
	}
	return decode[[]Session](c, "GET", path, nil)
}

// GetSession returns one session's metadata (model, thinking, stats, …).
func (c *Client) GetSession(id string) (*Session, error) {
	return ptr(decode[Session](c, "GET", "/api/sessions/"+id, nil))
}

// DeleteSession deletes a session permanently (204 No Content on success).
func (c *Client) DeleteSession(id string) error {
	_, err := c.do("DELETE", "/api/sessions/"+id, nil)
	return err
}

// CloseSession flushes and closes an active session.
func (c *Client) CloseSession(id string) (*Status, error) {
	return c.decodeStatus("POST", "/api/sessions/"+id+"/close", nil)
}

// ForkSession creates a new session that is an exact copy of id at this moment.
// Returns the forked session's metadata (new ID, fresh timestamps, same history).
func (c *Client) ForkSession(id string) (*Session, error) {
	s, err := decode[Session](c, "POST", "/api/sessions/"+id+"/fork", nil)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ResumeSession reopens an existing session by id.
func (c *Client) ResumeSession(id string) (*Session, error) {
	return ptr(decode[Session](c, "POST", "/api/sessions/"+id+"/resume", nil))
}

// StopSession interrupts any in-flight work on a session.
func (c *Client) StopSession(id string) (*Status, error) {
	return c.decodeStatus("POST", "/api/sessions/"+id+"/stop", nil)
}

// SendPrompt submits a user prompt to a session. The returned Status.Code is
// "started" (processing now) or "queued" (session was busy).
func (c *Client) SendPrompt(sessionID, text string) (*Status, error) {
	return c.decodeStatus("POST", "/api/sessions/"+sessionID+"/prompt", map[string]string{"text": text})
}

// Ask sends a prompt and blocks until the agent's turn completes, returning
// the final assistant text. This is the synchronous counterpart to SendPrompt.
// Errors (4xx/5xx) are returned as *Error with the standard {"error":{"message"}}
// shape — same as every other method in this client.
func (c *Client) Ask(sessionID, text string) (string, error) {
	resp, err := decode[struct {
		Text string `json:"text"`
	}](c, "POST", "/api/sessions/"+sessionID+"/ask", map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// SendPromptWithImages submits a prompt carrying one or more images (base64).
// The server validates that the session's model supports vision.
func (c *Client) SendPromptWithImages(sessionID, text string, images []types.ImageData) (*Status, error) {
	return c.decodeStatus("POST", "/api/sessions/"+sessionID+"/prompt", map[string]any{
		"text":   text,
		"images": images,
	})
}

// GetSessionContext returns the token-usage breakdown for an active session's
// context window, estimated per component (system prompt, tools, conversation).
func (c *Client) GetSessionContext(sessionID string) (*ContextBreakdown, error) {
	return ptr(decode[ContextBreakdown](c, "GET", "/api/sessions/"+sessionID+"/context", nil))
}

// GetSessionInfo returns the consolidated session-info snapshot for an active
// session: server version, full session metadata, runtime state (busy, queue
// depth), and environment counts (MCP connected, schedule count). Single
// round-trip replacing the three-to-four calls each transport previously made
// to assemble the same picture.
func (c *Client) GetSessionInfo(sessionID string) (*SessionInfo, error) {
	return ptr(decode[SessionInfo](c, "GET", "/api/sessions/"+sessionID+"/info", nil))
}

// GetMessages returns a session's full message history in the neutral
// types.Message format (the same shape the live stream produces).
func (c *Client) GetMessages(sessionID string) ([]types.Message, error) {
	return decode[[]types.Message](c, "GET", "/api/sessions/"+sessionID+"/messages", nil)
}

// ── Session commands ─────────────────────────────────────────────────────

// ListCommands returns the dynamic command set the session accepts (built-ins
// plus its skills).
func (c *Client) ListCommands(sessionID string) ([]CommandDef, error) {
	return decode[[]CommandDef](c, "GET", "/api/sessions/"+sessionID+"/commands", nil)
}

// ExecCommand runs a session command (e.g. "compact", "model", "thinking") and
// returns its resulting Status.
func (c *Client) ExecCommand(sessionID, command string, params map[string]any) (*Status, error) {
	return c.decodeStatus("POST", "/api/sessions/"+sessionID+"/commands", map[string]any{
		"command": command,
		"params":  params,
	})
}

// ── Events (SSE) ─────────────────────────────────────────────────────────

// StreamEvents opens the session's SSE stream and returns a channel of typed
// events. The channel closes when ctx is cancelled or the server ends the
// stream; see stream.go for the buffer sizing rationale.
func (c *Client) StreamEvents(ctx context.Context, sessionID string) (<-chan Event, error) {
	return c.streamEvents(ctx, sessionID)
}

// ptr adapts a (T, error) decode result into (*T, error): nil pointer on error,
// else the address of the decoded value. Keeps the single-object accessors
// one-liners without a named local on every call.
func ptr[T any](v T, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	return &v, nil
}
