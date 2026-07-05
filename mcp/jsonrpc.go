// Package mcp implements a minimal client for the Model Context Protocol (MCP).
//
// Phase 1 scope: the stdio transport and the tools primitive only. The protocol
// is JSON-RPC 2.0 (newline-delimited over stdin/stdout), implemented with the
// standard library alone — no external SDK — to keep the harness dependency
// footprint minimal. The Transport interface leaves the door open for a future
// Streamable-HTTP transport without touching the client or manager.
package mcp

import "encoding/json"

// jsonrpcVersion is the only JSON-RPC version MCP uses.
const jsonrpcVersion = "2.0"

// request is a JSON-RPC 2.0 request. ID is omitted for notifications.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is a JSON-RPC 2.0 response. Exactly one of Result/Error is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// newRequest builds a request with the given id, method and params. params may
// be nil.
func newRequest(id int64, method string, params any) (request, error) {
	r := request{JSONRPC: jsonrpcVersion, ID: &id, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return request{}, err
		}
		r.Params = b
	}
	return r, nil
}

// newNotification builds a notification (a request with no id, no response
// expected).
func newNotification(method string, params any) (request, error) {
	r := request{JSONRPC: jsonrpcVersion, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return request{}, err
		}
		r.Params = b
	}
	return r, nil
}
