package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// Client is an HTTP client for the Harness API. Identical contract to the v1
// TUI client — tui is a pure frontend over the same HTTP/SSE backend.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a client for the given server address.
func NewClient(addr string) *Client {
	return &Client{
		baseURL: "http://" + addr,
		http:    &http.Client{},
	}
}

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
		return data, fmt.Errorf("%s", strings.TrimSpace(string(data)))
	}
	return data, nil
}

// ── Command types (mirror server) ───────────────────────────────

type CommandDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Params      []ParamDef `json:"params"`
}

type ParamDef struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
}

// ── Methods ──────────────────────────────────────────────────────────────

func (c *Client) GetSettings() ([]byte, error)  { return c.do("GET", "/api/settings", nil) }
func (c *Client) GetProviders() ([]byte, error) { return c.do("GET", "/api/providers", nil) }
func (c *Client) ListModels() ([]byte, error)   { return c.do("GET", "/api/models", nil) }

// PatchSettings partially updates core settings (persists the global default;
// does not touch live sessions). Pass a map with the fields to change.
func (c *Client) PatchSettings(fields map[string]any) ([]byte, error) {
	return c.do("PATCH", "/api/settings", fields)
}

// Provider-config collection.
func (c *Client) GetProviderConfigs() ([]byte, error) {
	return c.do("GET", "/api/settings/providers", nil)
}
func (c *Client) PutProviderConfig(name string, cfg any) ([]byte, error) {
	return c.do("PUT", "/api/settings/providers/"+name, cfg)
}
func (c *Client) DeleteProviderConfig(name string) ([]byte, error) {
	return c.do("DELETE", "/api/settings/providers/"+name, nil)
}

// MCP-server collection.
func (c *Client) GetMCPServers() ([]byte, error) {
	return c.do("GET", "/api/settings/mcp", nil)
}
func (c *Client) PutMCPServer(name string, srv any) ([]byte, error) {
	return c.do("PUT", "/api/settings/mcp/"+name, srv)
}
func (c *Client) DeleteMCPServer(name string) ([]byte, error) {
	return c.do("DELETE", "/api/settings/mcp/"+name, nil)
}

// GetMCPStatus returns the live connection status of configured MCP servers.
func (c *Client) GetMCPStatus() ([]byte, error) {
	return c.do("GET", "/api/mcp/status", nil)
}

// GetSchedules returns the configured cron-scheduled prompts.
func (c *Client) GetSchedules() ([]byte, error) {
	return c.do("GET", "/api/schedules", nil)
}

func (c *Client) ConnectProvider(name, apiKey string) ([]byte, error) {
	body := map[string]any{}
	if apiKey != "" {
		body["api_key"] = apiKey
	}
	return c.do("POST", "/api/providers/"+name+"/connect", body)
}

// ConnectProviderWithCreds sends full OAuth credentials to connect a subscription provider.
func (c *Client) ConnectProviderWithCreds(name string, creds *types.Credentials) ([]byte, error) {
	body := map[string]any{
		"access_token":  creds.AccessToken,
		"refresh_token": creds.RefreshToken,
		"expires_at":    creds.ExpiresAt,
	}
	return c.do("POST", "/api/providers/"+name+"/connect", body)
}

func (c *Client) DisconnectProvider(name string) ([]byte, error) {
	return c.do("POST", "/api/providers/"+name+"/disconnect", nil)
}

func (c *Client) ListSessionsByCWD(cwd string) ([]byte, error) {
	return c.do("GET", "/api/sessions?cwd="+cwd, nil)
}

func (c *Client) ResumeSession(id string) ([]byte, error) {
	// The TUI is single-session: it always claims the scheduled-prompt handler so
	// that, if this agent runs the engine (--scheduler), fired prompts land here.
	// When the engine is off it's a harmless no-op.
	return c.do("POST", "/api/sessions/"+id+"/resume?scheduled_prompts_handler=true", nil)
}

func (c *Client) DeleteSession(id string) ([]byte, error) {
	return c.do("DELETE", "/api/sessions/"+id, nil)
}

func (c *Client) CloseSession(id string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+id+"/close", nil)
}

func (c *Client) GetMessages(sessionID string) ([]byte, error) {
	return c.do("GET", "/api/sessions/"+sessionID+"/messages", nil)
}

func (c *Client) StopSession(sessionID string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+sessionID+"/stop", nil)
}

func (c *Client) CreateSession(model, cwd string) ([]byte, error) {
	// See ResumeSession: the single-session TUI always claims the handler.
	return c.do("POST", "/api/sessions?scheduled_prompts_handler=true", map[string]string{"model": model, "cwd": cwd})
}

func (c *Client) SendPrompt(sessionID, text string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+sessionID+"/prompt", map[string]string{"text": text})
}


func (c *Client) ListCommands(sessionID string) ([]CommandDef, error) {
	data, err := c.do("GET", "/api/sessions/"+sessionID+"/commands", nil)
	if err != nil {
		return nil, err
	}
	var cmds []CommandDef
	if err := json.Unmarshal(data, &cmds); err != nil {
		return nil, err
	}
	return cmds, nil
}

func (c *Client) ExecCommand(sessionID, command string, params map[string]any) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+sessionID+"/commands", map[string]any{
		"command": command,
		"params":  params,
	})
}

// StreamEvents opens an SSE connection and returns a channel of events. The
// reader uses a large buffer to tolerate big single-line deltas.
func (c *Client) StreamEvents(ctx context.Context, sessionID string) (<-chan map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/sessions/"+sessionID+"/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SSE connect: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("SSE: status %d", resp.StatusCode)
	}

	ch := make(chan map[string]any, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var evt map[string]any
			if err := json.Unmarshal([]byte(line[6:]), &evt); err != nil {
				continue
			}
			select {
			case ch <- evt:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
