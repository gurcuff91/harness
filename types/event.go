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
	EventMaxIterationsReached // agent reached max ReAct iterations limit (LLM summarized progress)
	EventFollowUpStart        // follow-up prompt about to process (Output = text, Origin = source)
	EventReceivedPrompt       // an immediate (non-queued) prompt was received (Output = text, Origin = source)

	// ── Compaction ─────────────────────────────────────────────────────────
	EventCompactStart // session compaction started
	EventCompactEnd   // session compaction finished
	EventStop         // turn was stopped by user
)

// TokenUsage carries token counts and derived metrics for an EventTokens event.
//
// Every field has exactly ONE semantic, stated below, and at least one real
// consumer. Two semantics coexist here on purpose, for two genuinely
// different questions:
//
//   - LIVE CONTEXT ("how full is the model's window right now?") — Input,
//     ContextUsage, ContextWindow. These shrink after a compaction and are
//     the basis for the auto-compact trigger.
//   - SESSION HISTORY ("what has this session consumed in total?") —
//     TotalInput, TotalOutput, CacheRead, CacheWrite, CostUSD. These only
//     ever grow, mirror SessionStats on disk 1:1, and are what a footer or
//     billing view should show.
//
// The history fields deliberately carry the SAME values SessionStats
// persists, so a client that loads stats on resume and then consumes this
// event never sees a value change meaning underneath it (a real bug before:
// the footer showed the session total on resume, then dropped to the current
// turn's numbers on the first event).
type TokenUsage struct {
	// ── Live context (per-turn; shrinks on compaction) ──────────────────
	// Input is the total context sent to the model on the last request:
	// fresh + cache-read + cache-write tokens. ContextUsage is that same
	// number over ContextWindow — one truth, two presentations, because ACP's
	// usage_update takes raw tokens (used/size) while the TUI footer takes
	// the ratio.
	Input         int     // tokens sent last request (= current context size)
	ContextUsage  float64 // Input / ContextWindow (0.0–1.0)
	ContextWindow int     // model context window size (tokens)

	// ── Session history (accumulated; mirrors SessionStats on disk) ─────
	// CacheRead/CacheWrite are accumulated (not per-turn) so they match
	// TotalInput/TotalOutput's semantic — they're named without the Total
	// prefix only because that's the wire name clients already consume.
	TotalInput  int     // = SessionStats.InputTokens
	TotalOutput int     // = SessionStats.OutputTokens
	CacheRead   int     // = SessionStats.CacheRead (accumulated)
	CacheWrite  int     // = SessionStats.CacheWrite (accumulated)
	CostUSD     float64 // = SessionStats.CostUSD (accumulated)
}

// Event carries information about what's happening in the agent loop.
type Event struct {
	Type          EventType
	Loop          int
	MaxIterations int    // configured max ReAct iterations (populated on EventMaxIterationsReached)
	ToolID        string // unique tool call ID (from LLM) — correlates Start/ArgsDelta/Call/Result
	ToolName      string
	ToolArgs      string
	Output        string         // generic output (tool results, turn text)
	Message       string         // error messages (EventError)
	Details       map[string]any // structured error details, if any (EventError)
	Summary       string         // compaction summary (EventCompactEnd)
	Origin        string         // prompt source for EventReceivedPrompt/EventFollowUpStart ("user", "scheduled", …)
	Delta         string
	Tokens        TokenUsage
	Duration      time.Duration
	IsError       bool
}

// Handler receives events from the agent loop for rendering.
type Handler func(Event)

// ── Prompt status ────────────────────────────────────────────────────────

// PromptStatus indicates whether a prompt started immediately or was queued.
type PromptStatus int

const (
	PromptStarted PromptStatus = iota // prompt was dequeued and is now processing
	PromptQueued                      // prompt was added to the queue (session was busy)
)
