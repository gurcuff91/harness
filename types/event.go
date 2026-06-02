package types

import "time"

// ── Agent event types ────────────────────────────────────────────────────

// EventType represents the type of agent event.
type EventType int

const (
	// ── Turn lifecycle ────────────────────────────────────────────────────
	EventTurnStart EventType = iota // user turn started
	EventTurnEnd                    // user turn finished (final response ready)

	// ── ReAct loop ─────────────────────────────────────────────────────────
	EventLoopStart // one ReAct iteration started
	EventLoopEnd   // one ReAct iteration finished

	// ── Streaming — text ───────────────────────────────────────────────────
	EventStreamTextDelta // streamed text fragment from LLM
	EventStreamTextEnd   // text stream finished (footer should render)

	// ── Streaming — thinking ───────────────────────────────────────────────
	EventStreamThinkingDelta // streamed thinking/reasoning fragment
	EventStreamThinkingEnd   // thinking stream finished

	// ── Tools ──────────────────────────────────────────────────────────────
	EventToolStart     // LLM announced a tool call (name + ID known, args not yet)
	EventToolArgsDelta // tool arguments arriving in streaming fragments
	EventToolCall      // tool arguments complete, tool executed
	EventToolResult    // tool execution completed

	// ── Tokens & cost ──────────────────────────────────────────────────────
	EventTokens // token usage update (emitted on StreamUsage)

	// ── Errors ─────────────────────────────────────────────────────────────
	EventError // error occurred in the agent loop

	// ── Limits ─────────────────────────────────────────────────────────────
	EventMaxTurnsReached // agent reached max turns limit (LLM summarized progress)

	// ── Compaction ─────────────────────────────────────────────────────────
	EventCompactStart // session compaction started
	EventCompactEnd   // session compaction finished
)

// TokenUsage carries token counts and derived metrics for an EventTokens event.
type TokenUsage struct {
	// Per-turn (from the last StreamUsage)
	Input      int // tokens sent this turn (= current context size)
	Output     int // tokens generated this turn
	CacheRead  int // cache tokens read this turn
	CacheWrite int // cache tokens written this turn
	// Accumulated output across the session (input not accumulated — see SessionStats)
	TotalOutput     int
	TotalCacheRead  int
	TotalCacheWrite int
	// Derived — calculated by the session
	CostUSD       float64 // accumulated USD cost for the session
	ContextUsage  float64 // last input / context window (0.0–1.0)
	ContextWindow int     // model context window size (tokens)
}

// Event carries information about what's happening in the agent loop.
type Event struct {
	Type     EventType
	Loop     int
	ToolID   string // unique tool call ID (from LLM) — correlates Start/ArgsDelta/Call/Result
	ToolName string
	ToolArgs string
	Output   string
	Delta    string
	Tokens   TokenUsage
	Duration time.Duration
	IsError  bool
}

// Handler receives events from the agent loop for rendering.
type Handler func(Event)
