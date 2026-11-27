package rpc

import (
	"encoding/json"

	"github.com/gurcuff91/harness/types"
)

// ProtocolVersion is the protocol version this binary speaks. `initialize`
// must carry exactly this integer.
const ProtocolVersion = 1

// Empty is the params/result of an operation that takes or returns nothing
// (it serializes as `{}`).
type Empty struct{}

// ClientInfo identifies the client in `initialize`.
type ClientInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ToolDecl declares a tool whose implementation lives in the client: the
// binary calls it back with `tool/call` when the model uses it.
type ToolDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema is the tool's JSON Schema (object). Omitted = any object.
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ProfileConfig is a profile: directives and declared tools that the sessions
// that enable it get.
type ProfileConfig struct {
	Directives []string   `json:"directives,omitempty"`
	Tools      []ToolDecl `json:"tools,omitempty"`
}

// KindConfig picks one implementation of a pluggable piece by `kind`.
// Path is the location the kind needs (store: the base directory; memory: the
// database file) and is an error for a kind that has none.
type KindConfig struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
}

// ResourcesConfig turns on discovery of the project's context. Omit it and
// NOTHING is read from a session's directory (the default of agent.New too);
// "file" makes each session read its cwd's AGENTS.md, SYSTEM.md and skills.
type ResourcesConfig struct {
	Kind string `json:"kind"`
}

// Config is the serializable agent configuration sent in `initialize`. Every
// field is optional.
type Config struct {
	ThinkingLevel   string                     `json:"thinking_level,omitempty"`
	SystemPrompt    string                     `json:"system_prompt,omitempty"`
	Directives      []string                   `json:"directives,omitempty"`
	MaxIterations   int                        `json:"max_iterations,omitempty"`
	MaxTokens       int                        `json:"max_tokens,omitempty"`
	DisallowedTools []string                   `json:"disallowed_tools,omitempty"`
	MCPServers      map[string]types.MCPServer `json:"mcp_servers,omitempty"`
	Profiles        map[string]ProfileConfig   `json:"profiles,omitempty"`
	Tools           []ToolDecl                 `json:"tools,omitempty"`
	EnableScheduler bool                       `json:"enable_scheduler,omitempty"`
	EnableWebSearch bool                       `json:"enable_web_search,omitempty"`
	Terminal        string                     `json:"terminal,omitempty"`
	// Channels are the input channels to register, by name: "slack",
	// "telegram", "colleagues". One whose credentials are missing is an error.
	Channels []string `json:"channels,omitempty"`
	// Store: kind "file" (path = base directory, default
	// ~/.harness/agent/sessions) or "memory".
	Store *KindConfig `json:"store,omitempty"`
	// Memory: kind "sqlite" (path = database file, default
	// ~/.harness/agent/memory.db) or "none". Omitted = none.
	Memory *KindConfig `json:"memory,omitempty"`
	// Resources: omit it and nothing is discovered (agent.New's default); kind
	// "file" makes each session read its cwd's AGENTS.md, SYSTEM.md and skills.
	Resources *ResourcesConfig `json:"resources,omitempty"`
}

// InitializeParams are the params of `initialize`.
type InitializeParams struct {
	ProtocolVersion int        `json:"protocol_version"`
	ClientInfo      ClientInfo `json:"client_info"`
	Config          Config     `json:"config"`
}

// Capabilities says what this binary does for the connection.
type Capabilities struct {
	// Channels are the input channels registered for this agent.
	Channels []string `json:"channels"`
}

// InitializeResult is the result of `initialize`.
type InitializeResult struct {
	ProtocolVersion int          `json:"protocol_version"`
	HarnessVersion  string       `json:"harness_version"`
	Capabilities    Capabilities `json:"capabilities"`
}

// ToolCallParams are the params of the `tool/call` request the binary sends.
type ToolCallParams struct {
	SessionID  string          `json:"session_id"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input"`
}

// ToolCallResult is what the client answers to `tool/call`.
type ToolCallResult struct {
	Content string            `json:"content"`
	Images  []types.ImageData `json:"images,omitempty"`
	IsError bool              `json:"is_error,omitempty"`
}

// ToolCancelParams are the params of the `tool/cancel` notification the binary
// sends when a pending `tool/call` is abandoned (session stop, shutdown).
type ToolCancelParams struct {
	ToolCallID string `json:"tool_call_id"`
}
