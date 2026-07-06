package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTPTransport speaks the MCP Streamable HTTP transport: every JSON-RPC message
// is a POST to a single endpoint. The server replies either with a single JSON
// object (Content-Type: application/json) or an SSE stream (text/event-stream);
// this transport supports both. Authentication is entirely header-driven (e.g.
// Authorization: Bearer <token>) — no OAuth flow. A server-assigned
// Mcp-Session-Id, if any, is captured and echoed on subsequent requests.
type HTTPTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu        sync.Mutex
	sessionID string
}

// HTTPConfig configures a remote MCP server connection.
type HTTPConfig struct {
	URL     string
	Headers map[string]string // custom headers (auth, etc.)
	Timeout time.Duration     // per-request timeout; 0 = default 30s
}

// NewHTTPTransport builds a remote transport. It performs no I/O until Send.
func NewHTTPTransport(cfg HTTPConfig) (*HTTPTransport, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("mcp http: empty url")
	}
	timeout := 30 * time.Second
	if cfg.Timeout > 0 {
		timeout = cfg.Timeout
	}
	return &HTTPTransport{
		url:     cfg.URL,
		headers: cfg.Headers,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

// Send POSTs a JSON-RPC request and returns its matching response, decoding
// either a direct JSON body or the response embedded in an SSE stream.
func (t *HTTPTransport) Send(ctx context.Context, req request) (response, error) {
	body, err := t.post(ctx, req)
	if err != nil {
		return response{}, err
	}
	defer body.Close()

	ct := body.contentType
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		return t.readSSEResponse(body, req.ID)
	case strings.HasPrefix(ct, "application/json"):
		var resp response
		if err := json.NewDecoder(body).Decode(&resp); err != nil {
			return response{}, fmt.Errorf("mcp http: decode json: %w", err)
		}
		return resp, nil
	default:
		// Some servers omit/mislabel Content-Type; try JSON as a fallback.
		var resp response
		if err := json.NewDecoder(body).Decode(&resp); err != nil {
			return response{}, fmt.Errorf("mcp http: unexpected content-type %q: %w", ct, err)
		}
		return resp, nil
	}
}

// Notify POSTs a one-way notification. The server MUST answer 202 Accepted with
// no body (per the spec); any other 2xx is tolerated.
func (t *HTTPTransport) Notify(ctx context.Context, note request) error {
	body, err := t.post(ctx, note)
	if err != nil {
		return err
	}
	body.Close()
	return nil
}

// Close releases the transport. Remote MCP has no subprocess to kill; session
// teardown (DELETE) is optional and skipped in phase 2.
func (t *HTTPTransport) Close() error { return nil }

// httpBody bundles a response body reader with its content type and status.
type httpBody struct {
	io.ReadCloser
	contentType string
}

// post issues the HTTP POST, applies auth/session headers, checks status, and
// returns the body for the caller to decode. Non-2xx returns an error.
func (t *HTTPTransport) post(ctx context.Context, msg request) (*httpBody, error) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("mcp http: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("mcp http: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	t.mu.Lock()
	if t.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.mu.Unlock()

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp http: request: %w", err)
	}

	// Capture a server-assigned session id (first seen wins).
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		if t.sessionID == "" {
			t.sessionID = sid
		}
		t.mu.Unlock()
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("mcp http: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	return &httpBody{ReadCloser: resp.Body, contentType: resp.Header.Get("Content-Type")}, nil
}

// readSSEResponse parses an SSE stream and returns the JSON-RPC response whose
// id matches wantID. Intermediate messages (progress notifications, logs, or
// server→client requests) are ignored — phase 2 consumes only the final result.
func (t *HTTPTransport) readSSEResponse(r io.Reader, wantID *int64) (response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20) // up to 1MB per event

	var dataBuf strings.Builder
	flush := func() (response, bool, error) {
		if dataBuf.Len() == 0 {
			return response{}, false, nil
		}
		data := dataBuf.String()
		dataBuf.Reset()
		var resp response
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			return response{}, false, nil // not a JSON-RPC message — ignore
		}
		// Only the response carrying our id ends the wait.
		if resp.ID != nil && wantID != nil && *resp.ID == *wantID {
			return resp, true, nil
		}
		return response{}, false, nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "": // event boundary — try to resolve accumulated data
			if resp, done, err := flush(); err != nil {
				return response{}, err
			} else if done {
				return resp, nil
			}
		case strings.HasPrefix(line, "data:"):
			dataBuf.WriteString(strings.TrimSpace(line[len("data:"):]))
		default:
			// Ignore other SSE fields (event:, id:, retry:, comments).
		}
	}
	if err := scanner.Err(); err != nil {
		return response{}, fmt.Errorf("mcp http: read sse: %w", err)
	}
	// Stream ended without an event boundary — try any trailing data.
	if resp, done, err := flush(); err != nil {
		return response{}, err
	} else if done {
		return resp, nil
	}
	return response{}, fmt.Errorf("mcp http: sse stream closed without a matching response")
}
