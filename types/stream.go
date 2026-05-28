package types

import "encoding/json"

// ── Stream types ─────────────────────────────────────────────────────────

// StreamEventType identifies what kind of streaming event this is.
type StreamEventType int

const (
	StreamTextDelta     StreamEventType = iota // Partial text content
	StreamThinkingDelta                        // Partial thinking/reasoning content
	StreamToolStart                            // Tool use block started (name + id known)
	StreamToolDelta                            // Partial tool input JSON
	StreamToolEnd                              // Tool use block complete
	StreamUsage                                // Token usage update
	StreamDone                                 // Stream finished
	StreamError                                // Stream error
)

// StreamEvent carries a single granular event from the SSE stream.
type StreamEvent struct {
	Type  StreamEventType
	Delta string

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
