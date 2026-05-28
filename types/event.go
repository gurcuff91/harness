package types

import "time"

// ── Agent event types ────────────────────────────────────────────────────

// EventType represents the type of agent event.
type EventType int

const (
	EventThinking    EventType = iota // LLM is processing (spinner)
	EventThinkingEnd                  // LLM finished thinking
	EventText                         // LLM text response (non-streamed)
	EventToolCall                     // Tool call initiated
	EventToolResult                   // Tool execution completed
	EventLoopStart                    // ReAct loop iteration start
	EventLoopEnd                      // ReAct loop completed
	EventError                        // Error occurred
	EventTokens                       // Token usage update
	EventTurnStart                    // Agent turn started
	EventTurnEnd                      // Agent turn finished

	// Streaming events
	EventStreamTextDelta     // Streamed text fragment
	EventStreamThinkingDelta // Streamed thinking fragment
	EventStreamThinkingEnd   // Thinking stream finished
	EventStreamTextEnd       // Text stream finished
	EventStreamToolBuilding  // Tool input being generated (show spinner)

	// Compaction events
	EventCompactStart // Session compaction started
	EventCompactEnd   // Session compaction finished
)

// Event carries information about what's happening in the agent loop.
type Event struct {
	Type     EventType
	Loop     int
	ToolName string
	ToolArgs string
	Output   string
	Delta    string
	Tokens struct {
		// Per-turn (last StreamUsage)
		Input      int
		Output     int
		CacheRead  int
		CacheWrite int
		// Accumulated (entire session)
		TotalInput      int
		TotalOutput     int
		TotalCacheRead  int
		TotalCacheWrite int
		// Derived
		CostUSD       float64 // accumulated cost for the session
		ContextUsage    float64 // last input / model context window (0.0–1.0)
		ContextWindow int     // model context window size (tokens)
	}
	Duration time.Duration
	IsError  bool
}

// Handler receives events from the agent loop for rendering.
type Handler func(Event)
