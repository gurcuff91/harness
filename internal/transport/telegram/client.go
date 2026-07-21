package telegram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// apiClient talks to the in-process Harness server over HTTP/SSE — the same
// backend the TUI uses. Kept minimal: only the calls this transport needs.
type apiClient struct {
	base string
	http *http.Client
}

func newAPIClient(addr string) *apiClient {
	return &apiClient{base: "http://" + addr, http: &http.Client{}}
}

func (c *apiClient) do(method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		if ae := parseHarnessError(data); ae != nil {
			return data, ae // structured: message + details for rich rendering
		}
		return data, fmt.Errorf("%s", strings.TrimSpace(string(data)))
	}
	return data, nil
}

// ListModels returns the active models (used to resolve the default model).
func (c *apiClient) ListModels() ([]map[string]any, error) {
	data, err := c.do("GET", "/api/models", nil)
	if err != nil {
		return nil, err
	}
	var models []map[string]any
	if err := json.Unmarshal(data, &models); err != nil {
		return nil, err
	}
	return models, nil
}

// GetSettings returns the persisted core settings (active model, thinking).
func (c *apiClient) GetSettings() (map[string]any, error) {
	data, err := c.do("GET", "/api/settings", nil)
	if err != nil {
		return nil, err
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return s, nil
}

// CreateSession opens a new session and returns its id.
func (c *apiClient) CreateSession(model, cwd string) (string, error) {
	data, err := c.do("POST", "/api/sessions", map[string]string{"model": model, "cwd": cwd})
	if err != nil {
		return "", err
	}
	var s map[string]any
	json.Unmarshal(data, &s)
	id, _ := s["id"].(string)
	return id, nil
}

// ResumeSession reopens an existing session by id. Returns false (no error) if
// the session no longer exists.
func (c *apiClient) ResumeSession(id string) (bool, error) {
	_, err := c.do("POST", "/api/sessions/"+id+"/resume", nil)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CloseSession flushes and closes a session (removing it from the active set).
func (c *apiClient) CloseSession(id string) error {
	_, err := c.do("POST", "/api/sessions/"+id+"/close", nil)
	return err
}

// StopSession interrupts any in-flight work on a session.
func (c *apiClient) StopSession(id string) error {
	_, err := c.do("POST", "/api/sessions/"+id+"/stop", nil)
	return err
}

// ExecCommand runs a session command (e.g. "compact", "model", "thinking").
// ExecCommand runs a session command and returns its status code. The command
// endpoint responds on success with {"status": {"code": ...}}; on conflict it
// returns an error via do()'s standard error shape. The returned code (e.g.
// "started") lets callers confirm the command took effect.
func (c *apiClient) ExecCommand(id, command string, params map[string]any) (string, error) {
	data, err := c.do("POST", "/api/sessions/"+id+"/commands",
		map[string]any{"command": command, "params": params})
	if err != nil {
		return "", err
	}
	var resp struct {
		Status struct {
			Code string `json:"code"`
		} `json:"status"`
	}
	_ = json.Unmarshal(data, &resp)
	return resp.Status.Code, nil
}

// GetSession returns a session's metadata (model, thinking, stats, …).
func (c *apiClient) GetSession(id string) (map[string]any, error) {
	data, err := c.do("GET", "/api/sessions/"+id, nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// GetServerInfo returns the server info document (version, etc.).
func (c *apiClient) GetServerInfo() (map[string]any, error) {
	data, err := c.do("GET", "/api/server", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// CountConnectedMCPs returns how many configured MCP servers are connected.
func (c *apiClient) CountConnectedMCPs() int {
	data, err := c.do("GET", "/api/mcp/status", nil)
	if err != nil {
		return 0
	}
	var statuses []struct {
		Connected bool `json:"connected"`
	}
	if json.Unmarshal(data, &statuses) != nil {
		return 0
	}
	n := 0
	for _, s := range statuses {
		if s.Connected {
			n++
		}
	}
	return n
}

// CountSchedules returns how many schedules are owned by the given session (the
// ones that will actually fire in it).
func (c *apiClient) CountSchedules(owner string) int {
	data, err := c.do("GET", "/api/schedules?owner="+url.QueryEscape(owner), nil)
	if err != nil {
		return 0
	}
	var jobs []json.RawMessage
	if json.Unmarshal(data, &jobs) != nil {
		return 0
	}
	return len(jobs)
}

// SendPrompt submits a user prompt to a session.
func (c *apiClient) SendPrompt(sessionID, text string) error {
	_, err := c.do("POST", "/api/sessions/"+sessionID+"/prompt", map[string]string{"text": text})
	return err
}

// SendPromptWithImages submits a prompt carrying one or more images (base64).
// The server validates that the session's model supports vision.
func (c *apiClient) SendPromptWithImages(sessionID, text string, images []types.ImageData) error {
	_, err := c.do("POST", "/api/sessions/"+sessionID+"/prompt", map[string]any{
		"text":   text,
		"images": images,
	})
	return err
}

// StreamEvents opens the session's SSE stream and returns a channel of decoded
// events. The stream closes when ctx is cancelled or the server ends it.
func (c *apiClient) StreamEvents(ctx context.Context, sessionID string) (<-chan map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/api/sessions/"+sessionID+"/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("SSE: status %d", resp.StatusCode)
	}
	ch := make(chan map[string]any, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var evt map[string]any
			if json.Unmarshal([]byte(line[6:]), &evt) != nil {
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

// parseHarnessError parses a standard error response body into a harnessError
// (message + structured details). Returns nil if the body isn't the standard
// {"error": {"message": ...}} shape.

// harnessError is a structured error from the harness API: a human message plus
// optional details (the provider's parsed error payload). do() returns it for
// 4xx/5xx so the transport can render the details richly (formatError) rather
// than collapsing them into a plain string.
type harnessError struct {
	message string
	details map[string]any
}

func (e *harnessError) Error() string {
	if len(e.details) > 0 {
		if d, err := json.Marshal(e.details); err == nil {
			return e.message + ": " + string(d)
		}
	}
	return e.message
}

func parseHarnessError(body []byte) *harnessError {
	var env struct {
		Error struct {
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		return &harnessError{message: env.Error.Message, details: env.Error.Details}
	}
	return nil
}
