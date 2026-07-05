package mcp

import "context"

// Transport is the pluggable channel over which a Client exchanges JSON-RPC
// messages with an MCP server. Phase 1 ships StdioTransport (local subprocess);
// a future StreamableHTTPTransport can implement this same interface without any
// changes to Client or Manager.
//
// Send issues a request and blocks for its matching response. Notify sends a
// one-way notification (no response). Close terminates the connection and, for
// stdio, the child process.
type Transport interface {
	Send(ctx context.Context, req request) (response, error)
	Notify(ctx context.Context, note request) error
	Close() error
}
