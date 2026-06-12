package cli

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

// httpClient is the CLI transport's HTTP client for the internal API.
type httpClient struct {
	baseURL string
	http    *http.Client
}

func newClient(addr string) *httpClient {
	return &httpClient{
		baseURL: "http://" + addr,
		http:    &http.Client{},
	}
}

func (c *httpClient) do(method, path string, body any) ([]byte, error) {
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

func (c *httpClient) GetSettings() ([]byte, error) {
	return c.do("GET", "/api/settings", nil)
}

func (c *httpClient) ListModels() ([]byte, error) {
	return c.do("GET", "/api/models", nil)
}

func (c *httpClient) CreateSession(model, cwd string) ([]byte, error) {
	return c.do("POST", "/api/sessions", map[string]string{"model": model, "cwd": cwd})
}

func (c *httpClient) SendPrompt(sessionID, text string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+sessionID+"/prompt", map[string]string{"text": text})
}

func (c *httpClient) CloseSession(id string) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+id+"/close", nil)
}

func (c *httpClient) ExecCommand(sessionID string, command string, params map[string]any) ([]byte, error) {
	return c.do("POST", "/api/sessions/"+sessionID+"/commands", map[string]any{
		"command": command,
		"params":  params,
	})
}

func (c *httpClient) StreamEvents(ctx context.Context, sessionID string) (<-chan map[string]any, error) {
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
