package telegram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// SendPrompt submits a user prompt to a session.
func (c *apiClient) SendPrompt(sessionID, text string) error {
	_, err := c.do("POST", "/api/sessions/"+sessionID+"/prompt", map[string]string{"text": text})
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
