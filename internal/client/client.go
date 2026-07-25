// Package client is the single HTTP/SSE client for harness's internal API
// (internal/server). Every transport (TUI, Telegram, the CLI's one-shot `-p`
// prompt, and any future one) is a thin frontend over the same backend — this
// is the one client they all share, instead of each reimplementing the same
// do()/StreamEvents() boilerplate.
//
// Return shape: every method returns the raw []byte response body (or an
// error), never a pre-decoded Go value. Decoding into whatever shape a
// transport wants (a struct, a map[string]any, a specific field) is the
// transport's job, one layer up — this package has zero opinion about how a
// caller wants to use the data, only about talking to the API correctly.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gurcuff91/harness/types"
)

// Client is an HTTP/SSE client for the harness internal API
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
// error otherwise.
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

// ── Server info ────────────────────────────────────────────────────────────

// GetServerInfo returns the server info document (version, etc.).
func (c *Client) GetServerInfo() ([]byte, error) { return c.do("GET", "/api/server", nil) }

// ── Settings ─────────────────────────────────────────────────────────────

// GetSettings returns the persisted core settings (active model, thinking).
func (c *Client) GetSettings() ([]byte, error) { return c.do("GET", "/api/settings", nil) }

// PatchSettings partially updates core settings (persists the global default;
// does not touch live sessions). Pass a map with the fields to change.
func (c *Client) PatchSettings(fields map[string]any) ([]byte, error) {
	return c.do("PATCH", "/api/settings", fields)
}

// ── Provider configs (settings collection) ──────────────────────────────

func (c *Client) GetProviderConfigs() ([]byte, error) {
	return c.do("GET", "/api/settings/providers", nil)
}

func (c *Client) PutProviderConfig(name string, cfg any) ([]byte, error) {
	return c.do("PUT", "/api/settings/providers/"+name, cfg)
}

func (c *Client) DeleteProviderConfig(name string) ([]byte, error) {
	return c.do("DELETE", "/api/settings/providers/"+name, nil)
}

// ── Providers (live state) ───────────────────────────────────────────────

func (c *Client) GetProviders() ([]byte, error) { return c.do("GET", "/api/providers", nil) }

func (c *Client) ConnectProvider(name, apiKey string) ([]byte, error) {
	body := map[string]any{}
	if apiKey != "" {
		body["api_key"] = apiKey
	}
	return c.do("POST", "/api/providers/"+name+"/connect", body)
}

// ConnectProviderWithCreds sends full OAuth credentials to connect a
// subscription provider.
func (c *Client) ConnectProviderWithCreds(name string, creds *types.Credentials) ([]byte, error) {
	return c.do("POST", "/api/providers/"+name+"/connect", map[string]any{
		"access_token":  creds.AccessToken,
		"refresh_token": creds.RefreshToken,
		"expires_at":    creds.ExpiresAt,
	})
}

func (c *Client) DisconnectProvider(name string) ([]byte, error) {
	return c.do("POST", "/api/providers/"+name+"/disconnect", nil)
}

// ── Models ───────────────────────────────────────────────────────────────

func (c *Client) ListModels() ([]byte, error) { return c.do("GET", "/api/models", nil) }

// ── MCP servers ──────────────────────────────────────────────────────────

func (c *Client) GetMCPServers() ([]byte, error) { return c.do("GET", "/api/settings/mcp", nil) }

func (c *Client) PutMCPServer(name string, srv any) ([]byte, error) {
	return c.do("PUT", "/api/settings/mcp/"+name, srv)
}

func (c *Client) DeleteMCPServer(name string) ([]byte, error) {
	return c.do("DELETE", "/api/settings/mcp/"+name, nil)
}

// GetMCPStatus returns the live connection status of configured MCP servers.
func (c *Client) GetMCPStatus() ([]byte, error) { return c.do("GET", "/api/mcp/status", nil) }

// ── Memories (read-only) ─────────────────────────────────────────────────

// GetMemories queries the read-only memories endpoint. rawQuery is the URL
// query string (cwd, query, include_content, skip, limit), already encoded —
// pass "" for no filter.
func (c *Client) GetMemories(rawQuery string) ([]byte, error) {
	path := "/api/memories"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	return c.do("GET", path, nil)
}

// ── Schedules (read-only) ────────────────────────────────────────────────

// GetSchedules returns the configured cron-scheduled prompts. A non-empty
// owner filters to the schedules owned by that session (the ones that fire
// in it); pass "" for the full (operator) view.
func (c *Client) GetSchedules(owner string) ([]byte, error) {
	path := "/api/schedules"
	if owner != "" {
		path += "?owner=" + owner
	}
	return c.do("GET", path, nil)
}

// ── Sessions ─────────────────────────────────────────────────────────────

func (c *Client) CreateSession(model, cwd string) ([]byte, error) {
	return c.do("POST", "/api/sessions", map[string]string{"model": model, "cwd": cwd})
}

// ListSessions lists sessions, optionally filtered by cwd ("" = all cwds).
func (c *Client) ListSessions(cwd string) ([]byte, error) {
	path := "/api/sessions"
	if cwd != "" {
		path += "?cwd=" + cwd
	}
	return c.do("GET", path, nil)
}

func (c *Client) GetSession(id string) ([]byte, error) {
	return c.do("GET", "/api/sessions/"+id, nil)
}

func (c *Client) DeleteSession(id string) ([]byte, error) {
	return c.do("DELETE", "/api/sessions/"+id, nil)
}

func (c *Client) CloseSession(id string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+id+"/close", nil)
}

// ResumeSession reopens an existing session by id.
func (c *Client) ResumeSession(id string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+id+"/resume", nil)
}

// StopSession interrupts any in-flight work on a session.
func (c *Client) StopSession(id string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+id+"/stop", nil)
}

func (c *Client) SendPrompt(sessionID, text string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+sessionID+"/prompt", map[string]string{"text": text})
}

// SendPromptWithImages submits a prompt carrying one or more images (base64).
// The server validates that the session's model supports vision.
func (c *Client) SendPromptWithImages(sessionID, text string, images []types.ImageData) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+sessionID+"/prompt", map[string]any{
		"text":   text,
		"images": images,
	})
}

func (c *Client) GetMessages(sessionID string) ([]byte, error) {
	return c.do("GET", "/api/sessions/"+sessionID+"/messages", nil)
}

// ── Session commands ─────────────────────────────────────────────────────

func (c *Client) ListCommands(sessionID string) ([]byte, error) {
	return c.do("GET", "/api/sessions/"+sessionID+"/commands", nil)
}

// ExecCommand runs a session command (e.g. "compact", "model", "thinking").
// On success the response body is {"status": {"code": ...}}.
func (c *Client) ExecCommand(sessionID, command string, params map[string]any) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+sessionID+"/commands", map[string]any{
		"command": command,
		"params":  params,
	})
}

// ── Events (SSE) ─────────────────────────────────────────────────────────

// StreamEvents opens the session's SSE stream and returns a channel of
// decoded events. The channel closes when ctx is cancelled or the server
// ends the stream; see stream.go for the buffer sizing rationale.
func (c *Client) StreamEvents(ctx context.Context, sessionID string) (<-chan map[string]any, error) {
	return c.streamEvents(ctx, sessionID)
}
