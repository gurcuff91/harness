package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/providers"
	pllm "github.com/gurcuff91/harness/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// ── Session ─────────────────────────────────────────────────────────────

// Session is one conversation. It owns:
//   - store: source of truth for all messages (no in-memory history)
//   - provider + modelID: the LLM for this session (mutable via SwitchModel)
//   - tools: cloned registry with read_skill injected
//   - systemPrompt: built once at creation, immutable
//   - stats: accumulated token usage + cost (always calculated)
//
// All Prompt() calls are serialized via mu.
type Session struct {
	id           string
	cwd          string
	name         string

	// Dependencies
	store        store.SessionStore
	provider     pllm.Provider
	modelID      string
	thinkingLvl  string
	tools        *tools.Registry
	systemPrompt string

	// Stats — accumulated over the session lifetime
	stats          types.SessionStats
	lastInputTokens int    // last turn input tokens — used to compute ContextUsage
	contextWindow   int    // from model meta, updated on SwitchModel
	pricing         modelPricing

	handler Handler

	mu        sync.Mutex
	maxTurns  int
	maxTokens int
}

// modelPricing holds per-million-token rates for cost calculation.
type modelPricing struct {
	InputPrice  float64
	OutputPrice float64
	CacheRead   float64
	CacheWrite  float64
}

// ── Constructor (called by Agent.NewSession) ───────────────────────────

func newSession(storeInst store.SessionStore,
	provider pllm.Provider, modelID, thinkingLvl string,
	toolReg *tools.Registry, systemPrompt string,
	maxTurns, maxTokens int) *Session {

	meta := storeInst.Meta()
	s := &Session{
		id:           meta.ID,
		cwd:          meta.CWD,
		name:         meta.Name,
		store:        storeInst,
		provider:     provider,
		modelID:      modelID,
		thinkingLvl:  thinkingLvl,
		tools:        toolReg,
		systemPrompt: systemPrompt,
		maxTurns:     maxTurns,
		maxTokens:    maxTokens,
		stats:        meta.Stats, // restore accumulated stats
	}
	s.loadModelMeta(modelID)
	return s
}

// loadModelMeta updates contextWindow and pricing from the provider (authoritative)
// falling back to the registry chain via provider.ModelMeta().
func (s *Session) loadModelMeta(modelID string) {
	meta := s.provider.ModelMeta(modelID)
	if meta == nil {
		s.contextWindow = 128000 // safe default
		return
	}
	s.contextWindow = meta.ContextWindow
	s.pricing = modelPricing{
		InputPrice:  meta.InputPrice,
		OutputPrice: meta.OutputPrice,
		CacheRead:   meta.CacheRead,
		CacheWrite:  meta.CacheWrite,
	}
}

// ── Public methods ──────────────────────────────────────────────────────

// Prompt runs one full turn: user message → ReAct loop → final response.
func (s *Session) Prompt(ctx context.Context, text string, images []types.ImageData) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	userMsg := s.formatUser(text, images)
	if err := s.store.AddMessage(userMsg); err != nil {
		return "", fmt.Errorf("store user: %w", err)
	}

	s.emit(types.Event{Type: types.EventTurnStart})

	// Reserve one turn for the summary call if max turns is reached mid-task.
	for i := range s.maxTurns - 1 {
		if ctx.Err() != nil {
			return "Cancelled.", nil
		}

		history := s.store.Messages()

		req := &types.Request{
			SystemPrompt:  s.systemPrompt,
			Model:         s.modelID,
			Messages:      history,
			Tools:         s.tools.Definitions(),
			MaxTokens:     s.maxTokens,
			ThinkingLevel: s.thinkingLvl,
		}

		s.emit(types.Event{Type: types.EventLoopStart, Loop: i})

		resp, toolResults, err := s.runStream(ctx, req)
		if err != nil {
			s.emit(types.Event{Type: types.EventError, Output: err.Error()})
			s.emit(types.Event{Type: types.EventLoopEnd})
			s.emit(types.Event{Type: types.EventTurnEnd})
			return "", err
		}

		if err := s.store.AddMessage(resp.AssistantMessage); err != nil {
			return "", fmt.Errorf("store assistant: %w", err)
		}

		if len(resp.ToolCalls) == 0 {
			s.emit(types.Event{Type: types.EventLoopEnd, Loop: i})
			s.emit(types.Event{Type: types.EventTurnEnd})
			return resp.Text, nil
		}

		for _, tr := range toolResults {
			msg := s.formatToolResult(tr)
			if err := s.store.AddMessage(msg); err != nil {
				return "", fmt.Errorf("store tool result: %w", err)
			}
		}
	}

	// Max turns reached while still executing tools.
	// Ask the LLM to summarize progress and let the user decide what to do next.
	s.emit(types.Event{Type: types.EventLoopEnd})
	summary, _ := s.requestSummary(ctx)
	s.emit(types.Event{Type: types.EventMaxTurnsReached})
	s.emit(types.Event{Type: types.EventTurnEnd})
	return summary, nil
}

// Subscribe registers an event handler for this session.
func (s *Session) Subscribe(h Handler) {
	s.handler = h
}

// SwitchModel resolves, validates, and switches to a new "provider/model".
func (s *Session) SwitchModel(fullModel string) error {
	provider, modelID, err := providers.Resolve(fullModel)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.provider = provider
	s.modelID = modelID
	s.loadModelMeta(modelID)
	meta := s.store.Meta()
	meta.Model = fullModel
	s.store.UpdateMeta(meta)
	s.mu.Unlock()
	return nil
}

// SwitchThinking changes the thinking level for this session.
func (s *Session) SwitchThinking(level string) error {
	s.mu.Lock()
	s.thinkingLvl = level
	meta := s.store.Meta()
	meta.Thinking = level
	s.store.UpdateMeta(meta)
	s.mu.Unlock()
	return nil
}

// Compact truncates old messages keeping the last N.
func (s *Session) Compact(ctx context.Context) error {
	s.emit(types.Event{Type: types.EventCompactStart})
	if err := s.store.Truncate(30); err != nil {
		return err
	}
	s.emit(types.Event{Type: types.EventCompactEnd})
	return nil
}

// Rename sets a friendly display name.
func (s *Session) Rename(name string) error {
	s.name = name
	meta := s.store.Meta()
	meta.Name = name
	return s.store.UpdateMeta(meta)
}

// Stats returns a snapshot of the accumulated session stats.
func (s *Session) Stats() types.SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.stats
	stats.ContextWindow = s.contextWindow
	return stats
}

// Meta returns a snapshot of session metadata.
// Meta returns the full session metadata from the store.
// Includes: id, cwd, name, model, thinking, stats, timestamps.
func (s *Session) Meta() store.SessionMeta {
	return s.store.Meta()
}

// Close flushes and closes the store.
func (s *Session) Close() error {
	return s.store.Close()
}

// ── Internals ───────────────────────────────────────────────────────────

// runStream is one ReAct iteration: stream LLM → execute tools during stream.
func (s *Session) runStream(ctx context.Context, req *types.Request) (*types.Response, []types.ToolResult, error) {
	var (
		hadThinking   bool
		hadText       bool
		streamResults []types.ToolResult
	)

	resp, err := s.provider.CompleteStream(ctx, req, func(se types.StreamEvent) {
		switch se.Type {
		case types.StreamThinkingDelta:
			s.emit(types.Event{Type: types.EventStreamThinkingDelta, Delta: se.Delta})
			hadThinking = true

		case types.StreamTextDelta:
			if hadThinking && !hadText {
				s.emit(types.Event{Type: types.EventStreamThinkingEnd})
				hadThinking = false
			}
			s.emit(types.Event{Type: types.EventStreamTextDelta, Delta: se.Delta})
			hadText = true

		case types.StreamToolStart:
			if hadThinking {
				s.emit(types.Event{Type: types.EventStreamThinkingEnd})
				hadThinking = false
			}
			if hadText {
				s.emit(types.Event{Type: types.EventStreamTextEnd})
				hadText = false
			}
			s.emit(types.Event{Type: types.EventToolStart, ToolName: se.ToolName})

		case types.StreamToolDelta:
			s.emit(types.Event{Type: types.EventToolArgsDelta, ToolName: se.ToolName, Delta: se.Delta})

		case types.StreamToolEnd:
			if len(se.ToolArgs) > 0 {
				s.emit(types.Event{Type: types.EventToolCall, ToolName: se.ToolName, ToolArgs: string(se.ToolArgs)})
				start := time.Now()
				output, execErr := s.tools.Run(se.ToolName, se.ToolArgs)
				dur := time.Since(start)
				const maxOut = 15000
				if len(output) > maxOut {
					output = output[:maxOut] + "\n...(truncated)"
				}
				isErr := execErr != nil
				s.emit(types.Event{Type: types.EventToolResult, ToolName: se.ToolName, Output: output, Duration: dur, IsError: isErr})
				streamResults = append(streamResults, types.ToolResult{ID: se.ToolID, Output: output, IsErr: isErr})
			}

		case types.StreamUsage:
			if hadThinking {
				s.emit(types.Event{Type: types.EventStreamThinkingEnd})
				hadThinking = false
			}
			s.updateStats(se)

		case types.StreamDone:
			if hadText {
				s.emit(types.Event{Type: types.EventStreamTextEnd})
				hadText = false
			}

		case types.StreamError:
			s.emit(types.Event{Type: types.EventError, Output: se.Delta})
		}
	})

	return resp, streamResults, err
}

// updateStats accumulates token counts, calculates cost and context%, then emits EventTokens.
// Called on StreamUsage. Must be called while mu is held (we're inside Prompt's lock).
func (s *Session) updateStats(se types.StreamEvent) {
	// Accumulate
	s.stats.InputTokens += se.InputTokens
	s.stats.OutputTokens += se.OutputTokens
	s.stats.CacheRead += se.CacheRead
	s.stats.CacheWrite += se.CacheWrite

	// Cost for this turn (per million tokens)
	turnCost := (float64(se.InputTokens)*s.pricing.InputPrice +
		float64(se.OutputTokens)*s.pricing.OutputPrice +
		float64(se.CacheRead)*s.pricing.CacheRead +
		float64(se.CacheWrite)*s.pricing.CacheWrite) / 1_000_000
	s.stats.CostUSD += turnCost

	// Context % — last input tokens / model context window
	s.lastInputTokens = se.InputTokens
	if s.contextWindow > 0 {
		s.stats.ContextUsage = float64(s.lastInputTokens) / float64(s.contextWindow)
	}

	// Persist stats to store
	meta := s.store.Meta()
	meta.Stats = s.stats
	meta.LastActiveAt = time.Now()
	s.store.UpdateMeta(meta)

	// Emit enriched EventTokens to handler
	s.emit(types.Event{
		Type: types.EventTokens,
		Tokens: types.TokenUsage{
			Input:           se.InputTokens,
			Output:          se.OutputTokens,
			CacheRead:       se.CacheRead,
			CacheWrite:      se.CacheWrite,
			TotalOutput:     s.stats.OutputTokens,
			TotalCacheRead:  s.stats.CacheRead,
			TotalCacheWrite: s.stats.CacheWrite,
			CostUSD:         s.stats.CostUSD,
			ContextUsage:    s.stats.ContextUsage,
			ContextWindow:   s.contextWindow,
		},
	})
}

// requestSummary makes a final LLM call without tools, asking the model to
// summarize what it has done and what still needs to be done.
// The response is streamed normally so the transport receives it via EventStreamTextDelta.
func (s *Session) requestSummary(ctx context.Context) (string, error) {
	const summaryPrompt = "You've reached the maximum number of tool calls allowed for this turn. " +
		"Please summarize: (1) what you have completed so far, (2) what still needs to be done, " +
		"and (3) ask the user if they want you to continue or if they'd like to change direction."

	// Inject summary request into history
	summaryMsg := s.provider.FormatUserMessage(summaryPrompt)
	if err := s.store.AddMessage(summaryMsg); err != nil {
		return "", err
	}

	// LLM call with no tools — pure text response
	req := &types.Request{
		SystemPrompt: s.systemPrompt,
		Model:        s.modelID,
		Messages:     s.store.Messages(),
		Tools:        nil, // no tools — force text response
		MaxTokens:    s.maxTokens,
	}

	resp, _, err := s.runStream(ctx, req)
	if err != nil {
		return "", err
	}

	if err := s.store.AddMessage(resp.AssistantMessage); err != nil {
		return "", err
	}

	return resp.Text, nil
}

func (s *Session) emit(e types.Event) {
	if s.handler != nil {
		s.handler(e)
	}
}

func (s *Session) formatUser(text string, images []types.ImageData) []byte {
	if len(images) > 0 {
		return s.provider.FormatUserMessageWithImages(text, images)
	}
	return s.provider.FormatUserMessage(text)
}

func (s *Session) formatToolResult(tr types.ToolResult) []byte {
	msgs := s.provider.FormatToolResults([]types.ToolResult{tr})
	if len(msgs) > 0 {
		return msgs[0]
	}
	return nil
}


