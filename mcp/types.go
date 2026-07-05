package mcp

import "encoding/json"

// protocolVersion is the MCP spec version this client negotiates. 2024-11-05 is
// the original stable version and remains supported by all servers; stdio + the
// tools primitive have not changed across revisions.
const protocolVersion = "2024-11-05"

// clientName / clientVersion identify harness in the initialize handshake.
const (
	clientName    = "harness"
	clientVersion = "0.1.0"
)

// implementation identifies a participant (client or server) in initialize.
type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeParams is sent in the initialize request.
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      implementation `json:"clientInfo"`
}

// initializeResult is the server's reply to initialize.
type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      implementation `json:"serverInfo"`
}

// Tool is an MCP tool advertised by a server via tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// listToolsResult is the tools/list response.
type listToolsResult struct {
	Tools []Tool `json:"tools"`
}

// callToolParams is sent in a tools/call request.
type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// contentBlock is one element of a tool result's content array. Phase 1 handles
// text blocks; other kinds (image, resource) are ignored for now.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallResult is the outcome of a tools/call. Content holds the returned blocks
// and IsError signals a tool-level failure (distinct from a transport error).
type CallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// Text flattens all text content blocks into a single string.
func (r CallResult) Text() string {
	out := ""
	for i, c := range r.Content {
		if c.Type != "text" {
			continue
		}
		if i > 0 && out != "" {
			out += "\n"
		}
		out += c.Text
	}
	return out
}
