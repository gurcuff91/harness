package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gurcuff91/harness/llm"
	"github.com/gurcuff91/harness/tools"
)

// Options configures agent behavior.
type Options struct {
	SystemPrompt string
	MaxLoops     int // max tool-use iterations per turn
	MaxTokens    int // max output tokens per LLM call
}

// Agent is the core harness: it manages the ReAct loop between LLM and tools.
type Agent struct {
	provider llm.Provider
	tools    *tools.Registry
	opts     Options

	// Event handler for UX rendering (optional)
	onEvent Handler

	// Per-user conversation history and locks
	mu      sync.Mutex
	history map[string][]json.RawMessage
	locks   map[string]*sync.Mutex
}

// New creates a new Agent.
func New(provider llm.Provider, registry *tools.Registry, opts Options) *Agent {
	if opts.MaxLoops <= 0 {
		opts.MaxLoops = 25
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 8192
	}
	return &Agent{
		provider: provider,
		tools:    registry,
		opts:     opts,
		history:  make(map[string][]json.RawMessage),
		locks:    make(map[string]*sync.Mutex),
	}
}

// OnEvent registers a handler to receive agent loop events for UX.
func (a *Agent) OnEvent(h Handler) {
	a.onEvent = h
}

// emit sends an event to the registered handler (if any).
func (a *Agent) emit(e Event) {
	if a.onEvent != nil {
		a.onEvent(e)
	}
}

// userLock returns a per-user mutex to prevent concurrent history corruption.
func (a *Agent) userLock(userID string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.locks[userID]; !ok {
		a.locks[userID] = &sync.Mutex{}
	}
	return a.locks[userID]
}

// Chat processes a user message through the ReAct loop and returns the final response.
// This is the heartbeat of the harness.
func (a *Agent) Chat(ctx context.Context, userID, text string, images []llm.ImageData) (string, error) {
	lock := a.userLock(userID)
	lock.Lock()
	defer lock.Unlock()

	// Append user message to history
	if len(images) > 0 {
		a.history[userID] = append(a.history[userID], a.provider.FormatUserMessageWithImages(text, images))
	} else {
		a.history[userID] = append(a.history[userID], a.provider.FormatUserMessage(text))
	}

	a.emit(Event{Type: EventTurnStart})

	// All providers support streaming — call directly
	// ReAct loop: Think → Act → Observe → Repeat
	for i := range a.opts.MaxLoops {
		if ctx.Err() != nil {
			return "Cancelled.", nil
		}

		req := &llm.Request{
			SystemPrompt: a.opts.SystemPrompt,
			Messages:     a.history[userID],
			Tools:        a.tools.Definitions(),
			MaxTokens:    a.opts.MaxTokens,
		}

		var resp *llm.Response
		var err error

		// ── Always streaming ──
		a.emit(Event{Type: EventLoopStart, Loop: i})

		hadThinking := false
		hadText := false
		var streamResults []llm.ToolResult // collected during stream
		var streamToolOrder []string        // preserve order

		resp, err = a.provider.CompleteStream(ctx, req, func(se llm.StreamEvent) {
				switch se.Type {
				case llm.StreamThinkingDelta:
					a.emit(Event{Type: EventStreamThinkingDelta, Delta: se.Delta})
					hadThinking = true

				case llm.StreamTextDelta:
					// If we were thinking, close that first
					if hadThinking && !hadText {
						a.emit(Event{Type: EventStreamThinkingEnd})
						hadThinking = false
					}
					a.emit(Event{Type: EventStreamTextDelta, Delta: se.Delta})
					hadText = true

				case llm.StreamToolStart:
					// Close any open streams before tool output
					if hadThinking {
						a.emit(Event{Type: EventStreamThinkingEnd})
						hadThinking = false
					}
					if hadText {
						a.emit(Event{Type: EventStreamTextEnd})
						hadText = false
					}
					// Show spinner while model generates tool args
					a.emit(Event{Type: EventStreamToolBuilding, ToolName: se.ToolName})

				case llm.StreamToolDelta:
					// Tool input accumulating — nothing to render yet

				case llm.StreamToolEnd:
					// Args complete — execute immediately during stream
					if len(se.ToolArgs) > 0 {
						a.emit(Event{Type: EventToolCall, Loop: i, ToolName: se.ToolName, ToolArgs: string(se.ToolArgs)})
						start := time.Now()
						output, execErr := a.tools.Run(se.ToolName, se.ToolArgs)
						dur := time.Since(start)
						if execErr != nil {
							output = fmt.Sprintf("TOOL ERROR: %v", execErr)
						}
						const maxOut = 15000
						if len(output) > maxOut { output = output[:maxOut] + "\n...(truncated)" }
						a.emit(Event{Type: EventToolResult, Loop: i, ToolName: se.ToolName, Output: output, Duration: dur, IsError: execErr != nil})
						streamResults = append(streamResults, llm.ToolResult{ID: se.ToolID, Output: output, IsErr: execErr != nil})
						streamToolOrder = append(streamToolOrder, se.ToolID)
					}

				case llm.StreamUsage:
					// Close thinking before tokens (text stays open)
					if hadThinking {
						a.emit(Event{Type: EventStreamThinkingEnd})
						hadThinking = false
					}
					// Store usage — emitted after text closes
					a.emit(Event{
						Type: EventTokens,
						Tokens: struct {
							Input      int
							Output     int
							CacheRead  int
							CacheWrite int
						}{se.InputTokens, se.OutputTokens, se.CacheRead, se.CacheWrite},
					})

				case llm.StreamDone:
					// Close text stream (footer with tokens is rendered by finishTextStream)
					if hadText {
						a.emit(Event{Type: EventStreamTextEnd})
						hadText = false
					}

				case llm.StreamError:
					a.emit(Event{Type: EventError, Output: se.Delta})
				}
			})

		if err != nil {
			a.emit(Event{Type: EventLoopEnd})
			a.emit(Event{Type: EventTurnEnd})
			return "", err
		}

		// Append assistant message to history
		a.history[userID] = append(a.history[userID], resp.AssistantMessage)

		// No tool calls = final response
		if len(resp.ToolCalls) == 0 {
			a.emit(Event{Type: EventLoopEnd, Loop: i})
			a.emit(Event{Type: EventTurnEnd})
			return resp.Text, nil
		}

		// Use results from stream execution if available, otherwise execute now
		var results []llm.ToolResult
		if len(streamResults) == len(resp.ToolCalls) {
			// All tools were executed during stream — use those results
			results = streamResults
		} else {
			// Fallback: execute any tools not yet run
			executedIDs := make(map[string]bool)
			for _, id := range streamToolOrder { executedIDs[id] = true }
			results = streamResults
			for _, tc := range resp.ToolCalls {
				if executedIDs[tc.ID] { continue }
				a.emit(Event{Type: EventToolCall, Loop: i, ToolName: tc.Name, ToolArgs: string(tc.Input)})
				start := time.Now()
				output, execErr := a.tools.Run(tc.Name, tc.Input)
				dur := time.Since(start)
				if execErr != nil { output = fmt.Sprintf("TOOL ERROR: %v", execErr) }
				const maxToolOutput = 15000
				if len(output) > maxToolOutput { output = output[:maxToolOutput] + "\n...(truncated)" }
				a.emit(Event{Type: EventToolResult, Loop: i, ToolName: tc.Name, Output: output, Duration: dur, IsError: execErr != nil})
				results = append(results, llm.ToolResult{ID: tc.ID, Output: output, IsErr: execErr != nil})
			}
		}

		// Append tool results to history
		toolMsgs := a.provider.FormatToolResults(results)
		a.history[userID] = append(a.history[userID], toolMsgs...)

		a.maybeCompact(userID)
	}

	a.emit(Event{Type: EventLoopEnd})
	a.emit(Event{Type: EventTurnEnd})
	return "Hit max tool iterations. Try breaking the task into smaller steps.", nil
}

// ClearHistory resets conversation history for a user.
func (a *Agent) ClearHistory(userID string) {
	lock := a.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	delete(a.history, userID)
}

// Provider returns the current LLM provider.
func (a *Agent) Provider() llm.Provider {
	return a.provider
}

// SetProvider swaps the LLM provider at runtime (e.g. for model switching).
func (a *Agent) SetProvider(p llm.Provider) {
	a.provider = p
}

// maybeCompact trims old messages if history gets too large.
func (a *Agent) maybeCompact(userID string) {
	const maxMessages = 100
	h := a.history[userID]
	if len(h) <= maxMessages {
		return
	}
	a.history[userID] = h[len(h)-60:]
}
