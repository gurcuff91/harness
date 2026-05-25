package llm

import "encoding/json"

// Request represents an LLM completion request.
type Request struct {
	SystemPrompt string            `json:"system_prompt"`
	Messages     []json.RawMessage `json:"messages"`
	Tools        []ToolDef         `json:"tools,omitempty"`
	MaxTokens    int               `json:"max_tokens"`
}

// Response represents an LLM completion response.
type Response struct {
	// Text is the final text content (empty if tool calls pending)
	Text string `json:"text"`
	// Thinking is the model's reasoning content (if extended thinking enabled)
	Thinking string `json:"thinking,omitempty"`
	// AssistantMessage is the raw message to append to history
	AssistantMessage json.RawMessage `json:"assistant_message"`
	// ToolCalls requested by the model
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Usage tracks token consumption
	Usage Usage `json:"usage"`
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult represents the output of a tool execution.
type ToolResult struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	IsErr  bool   `json:"is_error,omitempty"`
}

// ToolDef defines a tool's schema for the LLM.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Usage tracks token counts.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read_input_tokens,omitempty"`
	CacheWrite   int `json:"cache_creation_input_tokens,omitempty"`
}

// ============================================================
// Streaming types
// ============================================================

// StreamEventType identifies what kind of streaming event this is.
type StreamEventType int

const (
	StreamTextDelta     StreamEventType = iota // Partial text content
	StreamThinkingDelta                        // Partial thinking/reasoning content
	StreamToolStart                            // Tool use block started (name + id known)
	StreamToolDelta                            // Partial tool input JSON
	StreamToolEnd                              // Tool use block complete
	StreamUsage                                // Token usage update
	StreamDone                                 // Stream finished, Response is ready
	StreamError                                // Stream error
)

// StreamEvent carries a single granular event from the SSE stream.
type StreamEvent struct {
	Type  StreamEventType
	Delta string // text or thinking delta fragment

	// Tool events
	ToolID   string          // tool_use block ID
	ToolName string          // tool name
	ToolArgs json.RawMessage // complete args (only on StreamToolEnd)

	// Usage (StreamUsage)
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
}

// StreamCallback receives streaming events as they arrive from the LLM.
type StreamCallback func(StreamEvent)
