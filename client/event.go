package client

import "encoding/json"

// Event is one decoded SSE event from a session's stream
// (GET /api/sessions/{id}/events). It is a single FLAT struct carrying every
// field any event type can have — a discriminated union keyed on Type, where a
// given Type only populates its relevant fields and the rest stay zero. This
// mirrors exactly how every consumer already treated the events (switch on the
// type string, then read the handful of fields that type carries), only typed
// instead of map[string]any lookups. The wire shape is defined by the server's
// formatEvent (server/sse.go); the json tags here match it 1:1.
type Event struct {
	Type string `json:"type"`

	// Streaming deltas — thinking / text / tool_args all use "delta".
	Delta string `json:"delta,omitempty"`

	// Tools.
	ToolName string  `json:"tool_name,omitempty"`
	ToolID   string  `json:"tool_id,omitempty"`
	ToolArgs string  `json:"tool_args,omitempty"`
	Output   string  `json:"output,omitempty"`   // tool_result
	Duration float64 `json:"duration,omitempty"` // tool_result, milliseconds
	IsError  bool    `json:"is_error,omitempty"` // tool_result

	// Prompt echo — received_prompt / follow_up_start.
	Text   string `json:"text,omitempty"`
	Origin string `json:"origin,omitempty"`

	// Tokens (EventTokens). Zero values are meaningful here (e.g. 0 cache), so
	// consumers read them unconditionally on a "tokens" event.
	Input         int     `json:"input,omitempty"`
	TotalOutput   int     `json:"total_output,omitempty"`
	CacheRead     int     `json:"cache_read,omitempty"`
	CacheWrite    int     `json:"cache_write,omitempty"`
	CostUSD       float64 `json:"cost_usd,omitempty"`
	ContextUsage  float64 `json:"context_usage,omitempty"`
	ContextWindow int     `json:"context_window,omitempty"`

	// Errors (EventError).
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`

	// Limits / compaction.
	MaxIterations int    `json:"max_iterations,omitempty"`
	Summary       string `json:"summary,omitempty"`

	// Raw is the original "data:" JSON payload for this event, verbatim. It is
	// NOT re-encoded (json:"-"), so it exactly preserves what the server sent —
	// used by the CLI's json / json-stream output modes to pass events through
	// byte-for-byte instead of round-tripping them through this struct (which
	// would drop zero-valued fields via omitempty).
	Raw json.RawMessage `json:"-"`
}
