package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/providers"
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
	id   string
	cwd  string
	name string

	// Dependencies
	store        store.SessionStore
	provider     providers.Provider
	modelID      string
	thinkingLvl  string
	tools        *tools.Registry
	systemPrompt string

	// Stats — accumulated over the session lifetime
	stats           types.SessionStats
	lastInputTokens int // last turn input tokens — used to compute ContextUsage
	contextWindow   int // from model meta, updated on SwitchModel
	pricing         modelPricing

	handler Handler

	// Skills
	skills    []resources.SkillInfo
	readSkill func(string) (string, error)

	mu        sync.Mutex
	maxTurns  int
	maxTokens int

	// Follow-up prompts — separate mutex to avoid deadlock with mu
	followMu  sync.Mutex
	followUps []followUp
	busy      bool
	followCtx context.Context
	currentCancel context.CancelFunc // cancel the currently executing turn
}

type followUp struct {
	text   string
	images []types.ImageData
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
	provider providers.Provider, modelID, thinkingLvl string,
	toolReg *tools.Registry, systemPrompt string,
	maxTurns, maxTokens int,
	skills []resources.SkillInfo, readSkill func(string) (string, error)) *Session {

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
		skills:       skills,
		readSkill:    readSkill,
	}
	s.loadModelMeta(modelID)
	return s
}

// loadModelMeta updates contextWindow and pricing from the provider (authoritative)
// falling back to the registry chain via provider.ModelMeta().
func (s *Session) loadModelMeta(modelID string) {
	meta := s.provider.ModelMeta(modelID)
	if meta == nil {
		s.contextWindow = 128000
		return
	}
	s.contextWindow = meta.ContextWindow
	// Update maxTokens to match the new model's capability
	if meta.MaxTokens > 0 {
		s.maxTokens = meta.MaxTokens
	}
	s.pricing = modelPricing{
		InputPrice:  meta.InputPrice,
		OutputPrice: meta.OutputPrice,
		CacheRead:   meta.CacheRead,
		CacheWrite:  meta.CacheWrite,
	}
}

// ── Public methods ──────────────────────────────────────────────────────

// Prompt runs one full turn: user message → ReAct loop → final response.
// Prompt sends a message to the session. If no turn is active, it starts
// processing immediately. If a turn is running, the message is queued and
// processed automatically when the current turn finishes.
func (s *Session) Prompt(ctx context.Context, text string, images ...types.ImageData) types.PromptStatus {
	s.followMu.Lock()
	s.followUps = append(s.followUps, followUp{text: text, images: images})
	if !s.busy {
		s.busy = true
		s.followCtx = ctx // parent context for all turns
		s.followMu.Unlock()
		go s.drainFollowUps()
		return types.PromptStarted
	}
	s.followMu.Unlock()
	return types.PromptQueued
}

// Stop cancels the currently executing turn. Queued prompts continue normally.
func (s *Session) Stop() {
	s.followMu.Lock()
	if s.currentCancel != nil {
		s.currentCancel()
		s.currentCancel = nil
	}
	s.followMu.Unlock()
}

// FollowUpCount returns the number of messages waiting in the queue.
func (s *Session) FollowUpCount() int {
	s.followMu.Lock()
	defer s.followMu.Unlock()
	return len(s.followUps)
}

// IsBusy returns whether the session is currently processing a turn.
func (s *Session) IsBusy() bool {
	s.followMu.Lock()
	defer s.followMu.Unlock()
	return s.busy
}

// Skills returns the discovered skills for this session.
func (s *Session) Skills() []resources.SkillInfo { return s.skills }

// ReadSkill returns the content of a skill by name.
func (s *Session) ReadSkill(name string) (string, error) {
	if s.readSkill == nil {
		return "", fmt.Errorf("no skill reader")
	}
	return s.readSkill(name)
}

// ModelMeta returns the current model's metadata.
func (s *Session) ModelMeta() *types.ModelMeta {
	return s.provider.ModelMeta(s.modelID)
}

// PeekQueue calls fn with the next queued message without removing it.
func (s *Session) PeekQueue(fn func(string)) {
	s.followMu.Lock()
	defer s.followMu.Unlock()
	if len(s.followUps) > 0 {
		fn(s.followUps[0].text)
	}
}

func (s *Session) drainFollowUps() {
	first := true
	for {
		s.followMu.Lock()
		if len(s.followUps) == 0 {
			s.busy = false
			s.currentCancel = nil
			s.followMu.Unlock()
			return
		}
		fu := s.followUps[0]
		s.followUps = s.followUps[1:]
		// Create a fresh cancellable context for each turn
		parentCtx := s.followCtx
		ctx, cancel := context.WithCancel(parentCtx)
		s.currentCancel = cancel
		s.followMu.Unlock()

		if !first {
			s.emit(types.Event{Type: types.EventFollowUpStart, Output: fu.text})
		}
		first = false

		_, err := s.promptSync(ctx, fu.text, fu.images)
		cancel() // always release resources
		if err != nil && ctx.Err() == nil {
			s.emit(types.Event{Type: types.EventError, Message: err.Error()})
		}
	}
}

func (s *Session) promptSync(ctx context.Context, text string, images []types.ImageData) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var userMsg types.Message
	if len(images) > 0 {
		userMsg = types.NewUserImageMessage(text, images)
	} else {
		userMsg = types.NewUserTextMessage(text)
	}
	if err := s.store.AddMessage(userMsg); err != nil {
		return "", fmt.Errorf("store user: %w", err)
	}

	// Auto-name session from first prompt (like Claude Code)

	s.emit(types.Event{Type: types.EventTurnStart})

	// Reserve one turn for the summary call if max turns is reached mid-task.
	for i := range s.maxTurns - 1 {
		if ctx.Err() != nil {
			s.emit(types.Event{Type: types.EventStop})
			s.emit(types.Event{Type: types.EventTurnEnd})
			return "", nil
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
		if ctx.Err() != nil {
			// Cancelled by user Stop() — emit stop event and exit cleanly
			s.emit(types.Event{Type: types.EventStop})
			s.emit(types.Event{Type: types.EventTurnEnd})
			return "", nil
		}
		if err != nil {
			s.emit(types.Event{Type: types.EventError, Message: err.Error()})
			s.emit(types.Event{Type: types.EventLoopEnd})
			s.emit(types.Event{Type: types.EventTurnEnd})
			return "", err
		}

		if err := s.store.AddMessage(resp.Message); err != nil {
			return "", fmt.Errorf("store assistant: %w", err)
		}

		if len(resp.ToolCalls) == 0 {
			s.emit(types.Event{Type: types.EventLoopEnd, Loop: i})
			s.emit(types.Event{Type: types.EventTurnEnd})
			return resp.Text, nil
		}

		if len(toolResults) > 0 {
			if err := s.store.AddMessage(types.NewToolResultMessage(toolResults)); err != nil {
				return "", fmt.Errorf("store tool results: %w", err)
			}
		}

		// Auto-compact at 98% context usage before next iteration
		// Compact emits EventError itself if it fails — no duplication here
		if s.stats.ContextUsage >= 0.98 {
			s.Compact(ctx) //nolint:errcheck — error already emitted as EventError
		}
	}

	// Max turns reached while still executing tools.
	// Ask the LLM to summarize progress and let the user decide what to do next.
	s.emit(types.Event{Type: types.EventLoopEnd})
	summary, _ := s.requestProgressUpdate(ctx)
	s.emit(types.Event{Type: types.EventMaxTurnsReached, MaxTurns: s.maxTurns})
	s.emit(types.Event{Type: types.EventTurnEnd})
	return summary, nil
}

// Subscribe registers an event handler for this session.
func (s *Session) Subscribe(h Handler) {
	s.handler = h
}

// SwitchModel resolves, validates, and switches to a new "provider/model".
// If the new model has a smaller context window than the current usage,
// Compact() is called automatically before switching.
func (s *Session) SwitchModel(ctx context.Context, fullModel string) error {
	provider, modelID, err := providers.Resolve(fullModel)
	if err != nil {
		return err
	}

	// If the new model has a smaller context window than current usage,
	// compact is mandatory — switch fails if compact fails.
	if meta := provider.ModelMeta(modelID); meta != nil && meta.ContextWindow > 0 {
		if s.lastInputTokens > meta.ContextWindow {
			if compactErr := s.Compact(ctx); compactErr != nil {
				// Compact already emitted EventError — just return
				return fmt.Errorf("cannot switch to %s: history (%d tokens) exceeds context window (%d): %w",
					fullModel, s.lastInputTokens, meta.ContextWindow, compactErr)
			}
		}
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
	if level == "" {
		level = "off"
	}
	s.mu.Lock()
	s.thinkingLvl = level
	meta := s.store.Meta()
	meta.Thinking = level
	s.store.UpdateMeta(meta)
	s.mu.Unlock()
	return nil
}

const compactSystemPrompt = `Your task is to produce a concise but complete summary of the conversation so far.
This summary will REPLACE the full conversation history — it must contain everything
needed to continue the work without losing context.

Include:
1. What was asked / the goal
2. What has been done (decisions made, files changed, commands run, key findings)
3. Current state — what is working, what is pending
4. Any critical context (errors encountered, constraints, important details)

Be specific and factual. Use bullet points. Do NOT ask questions or add commentary.
Respond with ONLY the summary text.`

// Compact summarizes the conversation via LLM and stores a checkpoint.
//
// Events emitted:
//   - EventCompactStart always
//   - EventCompactEnd{Output: summary} on success
//   - EventError{Output: msg} on failure (no EventCompactEnd)
//
// The store is never modified if summary generation fails.
func (s *Session) Compact(ctx context.Context) error {
	s.emit(types.Event{Type: types.EventCompactStart})

	// Generate compaction summary — store is untouched until this succeeds
	summary, err := s.generateCompactionSummary(ctx)
	if err != nil {
		s.emit(types.Event{Type: types.EventError, Message: fmt.Sprintf("compact failed: %v", err)})
		return fmt.Errorf("compact: %w", err)
	}

	// Commit checkpoint — append-only, no data lost
	if err := s.store.AddCompactionSummary(summary); err != nil {
		s.emit(types.Event{Type: types.EventError, Message: fmt.Sprintf("compact checkpoint failed: %v", err)})
		return fmt.Errorf("compact: checkpoint: %w", err)
	}

	// Reset context usage stats
	s.stats.InputTokens = 0
	s.stats.ContextUsage = 0
	meta := s.store.Meta()
	meta.Stats = s.stats
	s.store.UpdateMeta(meta)

	s.emit(types.Event{Type: types.EventCompactEnd, Summary: summary})
	return nil
}

// generateCompactionSummary makes a focused LLM call to summarize the full conversation
// for use as a compaction checkpoint. Uses no tools and a dedicated system prompt.
// The result is stored internally — NOT streamed to the transport.
func (s *Session) generateCompactionSummary(ctx context.Context) (string, error) {
	req := &types.Request{
		SystemPrompt: compactSystemPrompt,
		Model:        s.modelID,
		Messages:     s.store.Messages(),
		Tools:        nil, // no tools — pure text
		MaxTokens:    4096,
	}

	var summaryText string
	_, err := s.provider.CompleteStream(ctx, req, func(se types.StreamEvent) {
		if se.Type == types.StreamTextDelta {
			summaryText += se.Delta
		}
	})
	if err != nil {
		return "", err
	}
	if summaryText == "" {
		return "", fmt.Errorf("empty summary")
	}
	return summaryText, nil
}

// ID returns the session's unique identifier.
func (s *Session) ID() string { return s.id }

// Name returns the session's display name.
func (s *Session) Name() string { return s.name }

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
	return s.stats
}

// AllMessages returns the complete conversation history including pre-compaction messages.
// Use this for display purposes. The LLM only sees store.Messages() (from last compact offset).
func (s *Session) AllMessages() []types.Message {
	return s.store.AllMessages()
}

// Meta returns a snapshot of session metadata.
// Meta returns the full session metadata from the store.
// Includes: id, cwd, name, model, thinking, stats, timestamps.
func (s *Session) Meta() store.SessionMeta {
	m := s.store.Meta()
	// Always inject current context window so it's available before the first turn
	if s.contextWindow > 0 && m.Stats.ContextWindow == 0 {
		m.Stats.ContextWindow = s.contextWindow
	}
	return m
}

// Close flushes and closes the store.
func (s *Session) Close() error {
	return s.store.Close()
}

// ── Internals ───────────────────────────────────────────────────────────

// pendingToolCall holds a tool call collected during streaming, ready for parallel execution.
type pendingToolCall struct {
	toolID   string
	toolName string
	toolArgs json.RawMessage
}

// runStream is one ReAct iteration: stream LLM → collect tool calls → execute all in parallel.
func (s *Session) runStream(ctx context.Context, req *types.Request) (*types.Response, []types.ToolResult, error) {
	var (
		hadThinking  bool
		hadText      bool
		pendingCalls []pendingToolCall
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
			s.emit(types.Event{Type: types.EventToolStart, ToolID: se.ToolID, ToolName: se.ToolName})

		case types.StreamToolDelta:
			s.emit(types.Event{Type: types.EventToolArgsDelta, ToolID: se.ToolID, ToolName: se.ToolName, Delta: se.Delta})

		case types.StreamToolEnd:
			// Emit tool_call event (args finalized) then queue for parallel execution
			if len(se.ToolArgs) > 0 {
				s.emit(types.Event{Type: types.EventToolCall, ToolID: se.ToolID, ToolName: se.ToolName, ToolArgs: string(se.ToolArgs)})
				pendingCalls = append(pendingCalls, pendingToolCall{
					toolID:   se.ToolID,
					toolName: se.ToolName,
					toolArgs: se.ToolArgs,
				})
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
			s.emit(types.Event{Type: types.EventError, Message: se.Delta})
		}
	})
	if err != nil {
		return resp, nil, err
	}

	// Execute all pending tool calls in parallel, emit results as they complete.
	if len(pendingCalls) == 0 {
		return resp, nil, nil
	}

	var (
		wg           sync.WaitGroup
		resultsMu    sync.Mutex
		streamResults []types.ToolResult
	)

	for _, call := range pendingCalls {
		call := call // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			output, execErr := s.tools.Run(ctx, call.toolName, call.toolArgs)
			dur := time.Since(start)
			// If ctx was cancelled (Stop), skip emitting — EventStop handles it
			if ctx.Err() != nil {
				return
			}
			const maxOut = 15000
			if len(output) > maxOut {
				output = output[:maxOut] + "\n...(truncated)"
			}
			isErr := execErr != nil
			s.emit(types.Event{Type: types.EventToolResult, ToolID: call.toolID, ToolName: call.toolName, Output: output, Duration: dur, IsError: isErr})
			resultsMu.Lock()
			streamResults = append(streamResults, types.ToolResult{ID: call.toolID, Output: output, IsErr: isErr})
			resultsMu.Unlock()
		}()
	}
	wg.Wait() // wait for ALL tools before next ReAct iteration

	return resp, streamResults, nil
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

	// Context usage = (fresh input + cache reads) / context window
	// Cache reads count because they were sent to the model as context
	s.lastInputTokens = se.InputTokens + se.CacheRead
	s.stats.ContextWindow = s.contextWindow // persist current model's context window
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
			Input:           s.lastInputTokens, // fresh + cache = total context sent
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

// requestProgressUpdate makes a final LLM call when max turns is reached.
// Asks the model to summarize progress and check with the user on next steps.
// The response IS streamed to the transport via EventStreamTextDelta.
func (s *Session) requestProgressUpdate(ctx context.Context) (string, error) {
	const summaryPrompt = "You've reached the maximum number of tool calls allowed for this turn. " +
		"Please summarize: (1) what you have completed so far, (2) what still needs to be done, " +
		"and (3) ask the user if they want you to continue or if they'd like to change direction."

	// Inject summary request into history
	if err := s.store.AddMessage(types.NewUserTextMessage(summaryPrompt)); err != nil {
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

	if err := s.store.AddMessage(resp.Message); err != nil {
		return "", err
	}

	return resp.Text, nil
}

func (s *Session) emit(e types.Event) {
	if s.handler != nil {
		s.handler(e)
	}
}
