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

	// Loop is the 0-based ReAct iteration index — present on loop_start and
	// loop_end (the SAME value on both: they're a matched open/close pair
	// for one iteration, not two separate counters). Deliberately no
	// omitempty: 0 is iteration 1 (the first one) and a perfectly valid,
	// common value — omitting it would make "iteration 0" indistinguishable
	// from "field absent" on any consumer that checks for zero-value.
	Loop int `json:"loop"`

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

	// Tokens (EventTokens) — see types.TokenUsage for the full contract. Two
	// semantics on purpose: live context (input/context_usage/context_window,
	// which shrink after a compaction) and session history (total_*/cache_*/
	// cost_usd, which only grow and mirror the persisted SessionStats).
	//
	// NONE of these carry omitempty, deliberately: a zero is MEANINGFUL here
	// (0 cache reads on a turn, 0.0 context usage right after a compaction),
	// and omitempty would drop it on re-encode — making "zero" and "absent"
	// indistinguishable for any consumer that re-serializes the event (the
	// CLI's json/json-stream output modes do exactly that). Same reasoning as
	// the "loop" field on loop_start/loop_end.
	Input         int     `json:"input"`
	ContextUsage  float64 `json:"context_usage"`
	ContextWindow int     `json:"context_window"`
	TotalInput    int     `json:"total_input"`
	TotalOutput   int     `json:"total_output"`
	CacheRead     int     `json:"cache_read"`
	CacheWrite    int     `json:"cache_write"`
	CostUSD       float64 `json:"cost_usd"`

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
