package agent

import "time"

// EventType represents the type of agent event.
type EventType int

const (
	EventThinking    EventType = iota // LLM is processing (spinner)
	EventThinkingEnd                  // LLM finished thinking (with content)
	EventText                         // LLM text response (non-streamed)
	EventToolCall                     // Tool call initiated
	EventToolResult                   // Tool execution completed
	EventLoopStart                    // ReAct loop iteration start
	EventLoopEnd                      // ReAct loop completed
	EventError                        // Error occurred
	EventTokens                       // Token usage update
	EventTurnStart                    // Agent turn started (user submitted input)
	EventTurnEnd                      // Agent turn finished (ready for next input)

	// Streaming events
	EventStreamTextDelta      // Streamed text fragment
	EventStreamThinkingDelta  // Streamed thinking fragment
	EventStreamThinkingEnd    // Thinking stream finished
	EventStreamTextEnd        // Text stream finished
	EventStreamToolBuilding   // Tool input being generated (show spinner)
)

// Event carries information about what's happening in the agent loop.
type Event struct {
	Type     EventType
	Loop     int
	ToolName string
	ToolArgs string
	Output   string
	Delta    string
	Tokens   struct {
		Input      int
		Output     int
		CacheRead  int
		CacheWrite int
	}
	Duration time.Duration
	IsError  bool
}

// Handler receives events from the agent loop for rendering.
type Handler func(Event)
