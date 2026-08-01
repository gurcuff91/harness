// Package acp implements the Agent Client Protocol (agentclientprotocol.com)
// "agent" role transport: harness runs as a sub-process of an ACP client
// (e.g. the Zed editor), speaking JSON-RPC 2.0 over stdio. Framing is
// newline-delimited — one complete JSON object per line, both directions;
// there is no Content-Length header (unlike LSP).
//
// This package is a pure protocol bridge: it never touches agent/,
// agent/tools/, or client/ internals directly for business logic — it talks
// to the SAME in-process HTTP/SSE server every other transport uses, via the
// same client.Client, exactly like internal/transport/telegram. Only the
// wire format on the outside (JSON-RPC/stdio instead of a chat API) differs.
//
// stdout discipline: once running as an ACP agent, stdout carries ONLY
// JSON-RPC messages — never a stray log line, never pretty-printed JSON
// (each message is exactly one line). All logging goes to stderr, which ACP
// clients are free to capture or ignore.
package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// jsonrpcVersion is the fixed "jsonrpc" field value on every message.
const jsonrpcVersion = "2.0"

// rpcRequest is an incoming or outgoing JSON-RPC request/notification. A
// notification omits ID entirely (encoding/json drops a nil *json.RawMessage
// with omitempty); a request always carries one.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is an outgoing reply to a request (never sent for
// notifications, which have no ID to correlate a reply to).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the standard JSON-RPC error shape.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes used by this package (agentclientprotocol.com
// reserves the standard JSON-RPC range plus a few ACP-specific ones; only the
// ones this transport actually emits are named here).
const (
	errCodeParseError     = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternalError  = -32603
)

// conn is the stdio JSON-RPC transport: reads newline-delimited requests from
// r, writes newline-delimited responses/notifications to w. Writes are
// serialized by mu, since multiple goroutines (one per concurrent ACP
// session's event pump) write concurrently — interleaving two partial JSON
// lines would corrupt the stream for a client reading line-by-line.
type conn struct {
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex
}

func newConn(r io.Reader, w io.Writer) *conn {
	return &conn{reader: bufio.NewReaderSize(r, 64*1024), writer: w}
}

// readMessage blocks for the next newline-delimited JSON-RPC message. Returns
// io.EOF when the client closed stdin (normal shutdown signal).
func (c *conn) readMessage() (*rpcRequest, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return nil, fmt.Errorf("acp: invalid JSON-RPC message: %w", err)
	}
	return &req, nil
}

// writeLine serializes v as one compact JSON line (no pretty-printing —
// required by the newline-delimited framing) and writes it atomically with
// respect to other writers on this conn.
func (c *conn) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.writer.Write(b); err != nil {
		return err
	}
	_, err = c.writer.Write([]byte{'\n'})
	return err
}

// sendResponse writes a successful reply to a request with the given id.
func (c *conn) sendResponse(id json.RawMessage, result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return c.sendError(id, errCodeInternalError, err.Error(), nil)
	}
	return c.writeLine(rpcResponse{JSONRPC: jsonrpcVersion, ID: id, Result: b})
}

// sendError writes an error reply to a request with the given id.
func (c *conn) sendError(id json.RawMessage, code int, message string, data any) error {
	return c.writeLine(rpcResponse{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Error:   &rpcError{Code: code, Message: message, Data: data},
	})
}

// sendNotification writes a notification (no id, no reply expected) —
// used for session/update, the streaming channel the agent pushes through.
func (c *conn) sendNotification(method string, params any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.writeLine(rpcRequest{JSONRPC: jsonrpcVersion, Method: method, Params: b})
}
