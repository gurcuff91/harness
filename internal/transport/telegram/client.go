package telegram

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/gurcuff91/harness/internal/client"
	"github.com/gurcuff91/harness/types"
)

// apiClient talks to the in-process Harness server over HTTP/SSE — the same
// backend the TUI and CLI use — via the shared internal/client package. This
// type is a thin decoding wrapper: internal/client returns raw []byte for
// every endpoint (it has no opinion about the shape a caller wants); this
// wrapper decodes into the specific Go values this transport's call sites
// were already written against, so commands.go/pump.go/images.go/telegram.go
// don't need to change beyond what moved here.
type apiClient struct {
	c *client.Client
}

func newAPIClient(addr string) *apiClient {
	return &apiClient{c: client.New(addr)}
}

// ListModels returns the active models (used to resolve the default model).
func (c *apiClient) ListModels() ([]map[string]any, error) {
	data, err := c.c.ListModels()
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
	data, err := c.c.GetSettings()
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
	data, err := c.c.CreateSession(model, cwd)
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
	_, err := c.c.ResumeSession(id)
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
	_, err := c.c.CloseSession(id)
	return err
}

// StopSession interrupts any in-flight work on a session.
func (c *apiClient) StopSession(id string) error {
	_, err := c.c.StopSession(id)
	return err
}

// ExecCommand runs a session command and returns its status code. The command
// endpoint responds on success with {"status": {"code": ...}}; on conflict it
// returns an error via the shared client's standard error shape. The returned
// code (e.g. "started") lets callers confirm the command took effect.
func (c *apiClient) ExecCommand(id, command string, params map[string]any) (string, error) {
	data, err := c.c.ExecCommand(id, command, params)
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
	data, err := c.c.GetSession(id)
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
	data, err := c.c.GetServerInfo()
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
	data, err := c.c.GetMCPStatus()
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
	data, err := c.c.GetSchedules(owner)
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
	_, err := c.c.SendPrompt(sessionID, text)
	return err
}

// SendPromptWithImages submits a prompt carrying one or more images (base64).
// The server validates that the session's model supports vision.
func (c *apiClient) SendPromptWithImages(sessionID, text string, images []types.ImageData) error {
	_, err := c.c.SendPromptWithImages(sessionID, text, images)
	return err
}

// StreamEvents opens the session's SSE stream and returns a channel of decoded
// events. The stream closes when ctx is cancelled or the server ends it.
func (c *apiClient) StreamEvents(ctx context.Context, sessionID string) (<-chan map[string]any, error) {
	return c.c.StreamEvents(ctx, sessionID)
}
