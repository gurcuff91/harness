package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// ── Session ─────────────────────────────────────────────────────────────

// Session is one conversation. It owns:
//   - store: the *store.Session handle — working set in memory, durable via the port
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
	agent        *Agent // owning agent — used to unregister on Close (may be nil in tests)
	store        *store.Session
	provider     providers.Provider
	modelID      string
	thinkingLvl  string
	tools        *tools.Registry
	systemPrompt string
	// hasMemory mirrors Agent.memStore != nil — the same condition that gates
	// the "## Memory" block in buildSystemPrompt. Used to add a brief,
	// equally-conditional nudge to the compaction checkpoint (see
	// generateCompactionSummary): right after compaction the model's nearest
	// context is a dense summary, not the system prompt, so the memory
	// reminder is easy to lose track of exactly when "lack context about
	// earlier work" (the system prompt's own trigger for using MemoSearch) is
	// most likely to be true.
	hasMemory bool

	// Stats — accumulated over the session lifetime
	stats           types.SessionStats
	lastInputTokens int // last turn input tokens — used to compute ContextUsage
	contextWindow   int // from model meta, updated on SwitchModel
	pricing         modelPricing

	// Context breakdown lens — set once at construction, never mutated.
	// Byte lengths; ContextBreakdown() divides by the active provider's
	// chars-per-token at query time (Anthropic=6, OpenAI=4).
	sysPromptLen int // full system prompt byte length
	toolsLen     int // total JSON byte length of all tool schemas

	handler Handler

	// Skills
	skills    []resources.SkillInfo
	readSkill func(string) (content string, dir string, err error)

	mu            sync.Mutex
	maxIterations int
	maxTokens     int

	// modelStr is the session's active "provider/model" string, stored in an
	// atomic.Value so it can be read lock-free from CurrentModel(). The value
	// is written under s.mu (in newSession and SwitchModel — the same lock
	// that guards the individual provider/modelID fields) but reads from
	// CurrentModel() don't need s.mu at all, which is the whole point:
	// CurrentModel() is called by the Subagent tool's executor, which runs
	// INSIDE a turn (promptSync holds s.mu for the entire turn, including
	// tool execution), so taking s.mu there would deadlock. atomic.Value gives
	// us a safe, consistent snapshot without any lock contention.
	modelStr atomic.Value // string

	// Follow-up prompts — separate mutex to avoid deadlock with mu
	followMu      sync.Mutex
	followCond    *sync.Cond // signals when the queue drains (busy → false); lazily created
	followUps     []followUp
	busy          bool
	followCtx     context.Context
	currentCancel context.CancelFunc // cancel the currently executing turn
}

type followUp struct {
	text   string
	images []types.ImageData
	origin string // where the prompt came from ("user", "scheduled", …); default "user"
	// done, when non-nil, receives the turn's final text (or error) once this
	// specific prompt finishes — used by PromptSync. nil for fire-and-forget.
	done chan promptResult
}

// ── Prompt options ────────────────────────────────────────────────────────────

// Origin constants tag where a prompt came from, so transports can render it
// distinctly (e.g. a scheduled prompt with a clock icon).
const (
	OriginUser      = "user"
	OriginScheduled = "scheduled"
)

// PromptOption configures a Prompt / PromptAndWait call.
type PromptOption func(*promptConfig)

type promptConfig struct {
	images []types.ImageData
	origin string
}

// PromptWithImages attaches images to the prompt (vision requests).
func PromptWithImages(images ...types.ImageData) PromptOption {
	return func(c *promptConfig) { c.images = append(c.images, images...) }
}

// PromptWithOriginUser tags the prompt as user-originated (the default).
func PromptWithOriginUser() PromptOption {
	return func(c *promptConfig) { c.origin = OriginUser }
}

// PromptWithOriginScheduled tags the prompt as fired by the scheduler, so
// transports can render it with a scheduled indicator.
func PromptWithOriginScheduled() PromptOption {
	return func(c *promptConfig) { c.origin = OriginScheduled }
}

func buildPromptConfig(opts []PromptOption) promptConfig {
	c := promptConfig{origin: OriginUser}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

type promptResult struct {
	text string
	err  error
}

// modelPricing holds per-million-token rates for cost calculation.
type modelPricing struct {
	InputPrice  float64
	OutputPrice float64
	CacheRead   float64
	CacheWrite  float64
}

// ── Constructor (called by Agent.NewSession) ───────────────────────────

func newSession(storeInst *store.Session,
	provider providers.Provider, modelID, thinkingLvl string,
	toolReg *tools.Registry, tl toolLens, systemPrompt string, pl promptLens,
	maxIterations, maxTokens int,
	skills []resources.SkillInfo, readSkill func(string) (content string, dir string, err error),
	hasMemory bool) *Session {

	meta := storeInst.Meta()
	s := &Session{
		id:            meta.ID,
		cwd:           meta.CWD,
		name:          meta.Name,
		store:         storeInst,
		provider:      provider,
		modelID:       modelID,
		thinkingLvl:   thinkingLvl,
		tools:         toolReg,
		systemPrompt:  systemPrompt,
		maxIterations: maxIterations,
		maxTokens:     maxTokens,
		stats:         meta.Stats, // restore accumulated stats
		skills:        skills,
		readSkill:     readSkill,
		hasMemory:     hasMemory,
		// Context breakdown lens — write-once, from builder functions.
		sysPromptLen: pl.total,
		toolsLen:     tl.totalBytes,
	}
	s.followCond = sync.NewCond(&s.followMu)
	s.loadModelMeta(modelID)
	s.modelStr.Store(provider.Name() + "/" + modelID)

	// Restore lastInputTokens from persisted stats so ContextBreakdown() shows
	// meaningful "actual" + "free space" values immediately on resume, without
	// requiring a new turn. lastInputTokens is not persisted directly, but
	// ContextUsage × ContextWindow gives back the same value that was stored.
	if meta.Stats.ContextUsage > 0 && s.contextWindow > 0 {
		s.lastInputTokens = int(meta.Stats.ContextUsage * float64(s.contextWindow))
	}

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

// Prompt sends a message to the session. If no turn is active, it starts
// processing immediately; if a turn is running, the message is queued and
// processed when the current turn finishes. Options attach images
// (PromptWithImages) or tag the origin (PromptWithOriginUser/
// PromptWithOriginScheduled; default user).
func (s *Session) Prompt(ctx context.Context, text string, opts ...PromptOption) types.PromptStatus {
	c := buildPromptConfig(opts)
	s.followMu.Lock()
	s.followUps = append(s.followUps, followUp{text: text, images: c.images, origin: c.origin})
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

// ErrBusy is returned by Compact when a turn is in flight.
var ErrBusy = errors.New("session is busy; try again when the current turn finishes")

// reportedErr wraps an error that has ALREADY been emitted as an EventError at
// its point of failure (inside the ReAct for-loop — stream/provider errors,
// store errors saving the assistant message or tool results — see promptSync).
// It exists to prevent a real double-emit bug: those three sites both emit
// locally AND return the error so the turn unwinds; without this wrapper,
// drainFollowUps (which emits EventError for any error a turn returns, so
// requestProgressUpdate's failure — which does NOT emit locally — is reported
// at all) had no way to tell "already told the user" apart from "never told
// the user", and reported the SAME error a second time.
//
// Unwrap() means errors.Is/errors.As (e.g. errorEvent's own ProviderAPIError
// unwrap below) still see straight through the wrapper — wrapping only adds a
// "was this reported already?" bit, it never changes what the error actually
// is.
//
// Deliberately NOT solved by moving the loop's emit calls out to
// drainFollowUps (a single emission point, no wrapper needed): that would
// invert the wire order from error→loop_end to loop_end→error. No client
// today has a loop_end handler to notice, but that's exactly the kind of
// implicit contract change ("here's what actually failed" arriving AFTER
// "this iteration is closed") a future SSE consumer reading the raw event
// order could reasonably depend on. This wrapper keeps the existing order
// (emit right next to its own return, auditable in place) while still
// guaranteeing exactly one EventError per failure.
type reportedErr struct{ err error }

func (r *reportedErr) Error() string { return r.err.Error() }
func (r *reportedErr) Unwrap() error { return r.err }

// reported marks err as already-emitted — call at every site inside the
// for-loop that both s.emit(errorEvent(err)) and returns the error.
func reported(err error) error { return &reportedErr{err} }

// errorEvent builds an EventError from an error, lifting a provider ProviderAPIError's
// structured details into the event so transports can render them richly.
func errorEvent(err error) types.Event {
	e := types.Event{Type: types.EventError, Message: err.Error()}
	var apiErr *types.ProviderAPIError
	if errors.As(err, &apiErr) {
		e.Message = apiErr.Message
		e.Details = apiErr.Details
	}
	return e
}

// Compact summarizes the conversation and stores a checkpoint, reclaiming
// context. It refuses to run while a turn is active (returns ErrBusy) —
// compacting mid-turn mutates the message history the turn is still using,
// corrupting the conversation. (Automatic compaction runs internally between
// ReAct iterations, where it's safe.)
//
// Events emitted: EventCompactStart, then EventCompactEnd on success or
// EventError on failure. The store is untouched if summary generation fails.
func (s *Session) Compact(ctx context.Context) error {
	if s.IsBusy() {
		return ErrBusy
	}
	return s.compact(ctx)
}

// Reset wipes the session's message history and accumulated stats, returning it
// to a freshly-created state. Identity fields (ID, CWD, Name, Model, Thinking,
// CreatedAt) are preserved — the session is the same entity, just empty.
// Returns ErrBusy if a turn is in flight.
func (s *Session) Reset() error {
	if s.IsBusy() {
		return ErrBusy
	}
	return s.store.Reset()
}

// Wait blocks until the session's queue is fully drained (no turn in flight and
// nothing queued). It uses condition-variable signaling — no polling. Useful for
// SDK/batch callers that fire several prompts and then wait for all of them:
//
//	s.Prompt(ctx, "task 1")
//	s.Prompt(ctx, "task 2")
//	s.Wait() // returns when both have finished
//
// Events still stream to Subscribe handlers throughout. Wait on an idle session
// returns immediately.
func (s *Session) Wait() {
	s.followMu.Lock()
	for s.busy {
		s.followCond.Wait()
	}
	s.followMu.Unlock()
}

// PromptAndWait enqueues a prompt and blocks until THAT prompt's turn finishes,
// returning its final assistant text (or an error). It's the synchronous
// convenience for SDK callers who want a single request/response; the async
// Prompt + Subscribe model remains the primary API for streaming/UIs. Other
// queued prompts are unaffected. Respects ctx for the turn's execution.
func (s *Session) PromptAndWait(ctx context.Context, text string, opts ...PromptOption) (string, error) {
	c := buildPromptConfig(opts)
	done := make(chan promptResult, 1)
	s.followMu.Lock()
	s.followUps = append(s.followUps, followUp{text: text, images: c.images, origin: c.origin, done: done})
	if !s.busy {
		s.busy = true
		s.followCtx = ctx
		s.followMu.Unlock()
		go s.drainFollowUps()
	} else {
		s.followMu.Unlock()
	}
	res := <-done
	return res.text, res.err
}

// Skills returns the discovered skills for this session.
func (s *Session) Skills() []resources.SkillInfo { return s.skills }

// ReadSkill returns the content of a skill by name plus the absolute directory
// it lives in (for resolving relative paths the skill references).
func (s *Session) ReadSkill(name string) (content string, dir string, err error) {
	if s.readSkill == nil {
		return "", "", fmt.Errorf("no skill reader")
	}
	return s.readSkill(name)
}

// ModelMeta returns the current model's metadata.
func (s *Session) ModelMeta() *types.ModelMeta {
	return s.provider.ModelMeta(s.modelID)
}

// CurrentModel returns the session's active model in "provider/model" form,
// reflecting any SwitchModel call that has happened since the session was
// created — unlike a plain string captured once at construction time (the
// bug this exists to fix: the Subagent tool's executor closure in
// agent.go's buildSessionTools used to close over the model string as it
// was when the session was FIRST created, so a later /model change was
// invisible to it — every subsequent sub-agent kept using the original
// model, including one that had since become rate-limited).
//
// Lock-free (reads an atomic.Value snapshot written under s.mu by
// newSession/SwitchModel). This is NOT just an optimization: CurrentModel()
// is called by the Subagent tool's executor, which runs INSIDE a turn — and
// promptSync holds s.mu for the entire turn, including tool execution. A
// s.mu.Lock() here would deadlock: the tool goroutine would wait for s.mu
// while promptSync's wg.Wait() waits for the tool goroutine — a circular
// wait confirmed by a real stack trace from a hung process. atomic.Value
// breaks the cycle: the reader needs no lock at all, so it can't block on
// one that the turn already holds.
func (s *Session) CurrentModel() string {
	if v := s.modelStr.Load(); v != nil {
		return v.(string)
	}
	return "" // unreachable: modelStr is set in newSession before the session escapes
}

func (s *Session) drainFollowUps() {
	first := true
	for {
		s.followMu.Lock()
		if len(s.followUps) == 0 {
			s.busy = false
			s.currentCancel = nil
			s.followCond.Broadcast() // wake any Wait()/PromptAndWait callers
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

		// Echo the prompt to clients. The immediate (first) prompt gets a
		// ReceivedPrompt event; queued ones get FollowUpStart. Both carry the text
		// and origin so transports can render them (e.g. scheduled → clock icon)
		// even though the client didn't originate the prompt.
		if first {
			s.emit(types.Event{Type: types.EventReceivedPrompt, Output: fu.text, Origin: fu.origin})
		} else {
			s.emit(types.Event{Type: types.EventFollowUpStart, Output: fu.text, Origin: fu.origin})
		}
		first = false

		result, err := s.promptSync(ctx, fu.text, fu.images)

		// Capture whether the turn was cancelled BEFORE releasing ctx below.
		// This ordering is the whole point: cancel() is unconditional (it's
		// this turn's resource cleanup), so reading ctx.Err() AFTER it always
		// reports context.Canceled — our own cancellation, indistinguishable
		// from a user Stop(). The check below used to do exactly that, which
		// made the s.emit(errorEvent(err)) branch DEAD CODE that never ran
		// once: every turn, error or not, looked "cancelled" to it.
		//
		// That silently broke the contract two callers explicitly rely on —
		// requestProgressUpdate's failure path and its empty-summary guard
		// both return their error up here precisely so this is the one place
		// that reports it (they deliberately don't emit themselves, to avoid
		// a duplicate). The result in the field: hitting the max-iteration
		// cap and then failing to produce the summary showed the user NOTHING
		// — no summary, no error, just a turn that ended. (Errors raised
		// INSIDE the ReAct for-loop were unaffected: they emit their own
		// EventError before returning, which is why only this path went
		// silent.)
		wasCancelled := ctx.Err() != nil
		cancel() // always release resources
		// A user Stop() already produced EventStop — staying quiet there is
		// intentional (Stop means stop, nothing more to report). Any OTHER
		// failure must be visible — UNLESS the for-loop already emitted it
		// itself and wrapped it with reported() (see reportedErr's doc
		// comment): this is the single point that reports a turn's error for
		// callers that don't emit locally (requestProgressUpdate's failure
		// path), so it must not blindly re-emit one that already went out.
		var already *reportedErr
		if err != nil && !wasCancelled && !errors.As(err, &already) {
			s.emit(errorEvent(err))
		}
		// If the loop already wrapped err in reported() to steer the emit
		// decision above, unwrap it back to the real underlying error before
		// handing the result to a PromptAndWait caller — that wrapper is an
		// internal bookkeeping detail of THIS function; it has no reason to
		// leak past it. Without this, PromptAndWait callers (including the
		// public SDK — internal/cli.go's client.Ask, server.go's
		// handlePrompt) would receive *reportedErr instead of the real error,
		// which still behaves correctly under errors.Is/errors.As (Unwrap()
		// sees through it) but would silently break any caller doing a plain
		// type assertion (err.(*types.ProviderAPIError)) instead of the
		// idiomatic errors.As — an avoidable footgun for zero benefit, since
		// nothing outside this function needs to know "was this reported".
		if already != nil {
			err = already.Unwrap()
		}
		// Deliver the outcome to a PromptAndWait caller, if any.
		if fu.done != nil {
			fu.done <- promptResult{text: result, err: err}
		}
	}
}

func (s *Session) promptSync(ctx context.Context, text string, images []types.ImageData) (retText string, retErr error) {
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

	// Guarantee turn_end is always emitted — even on unexpected early returns
	// (store errors, panics recovered by the runtime). Without this, transports
	// (TUI spinner, Telegram typing indicator, Slack typing) never stop.
	turnEnded := false
	emitTurnEnd := func() {
		if !turnEnded {
			turnEnded = true
			s.emit(types.Event{Type: types.EventTurnEnd})
		}
	}
	defer emitTurnEnd()

	// Reserve one iteration for the summary call if max iterations is reached mid-task.
	for i := range s.maxIterations - 1 {
		if ctx.Err() != nil {
			s.emit(types.Event{Type: types.EventStop})
			return "", nil // turn_end via defer
		}

		// Strip images from all turns except the current one before sending to
		// the provider. Providers like Anthropic reject requests with multiple
		// large images (>2000px) across the full history. Images in the current
		// turn are always preserved so the model can see what was just shared.
		history := stripOldTurnImages(s.store.Messages())

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
			// Cancelled by user Stop() — close the loop, emit stop.
			// turn_end is handled by defer emitTurnEnd().
			s.emit(types.Event{Type: types.EventStop})
			s.emit(types.Event{Type: types.EventLoopEnd, Loop: i})
			return "", nil
		}
		if err != nil {
			// Provider/stream error — emit error + loop_end.
			// turn_end is handled by defer emitTurnEnd(). reported() marks
			// this err as already told to the user, so drainFollowUps
			// doesn't emit it again (see reportedErr's doc comment).
			s.emit(errorEvent(err))
			s.emit(types.Event{Type: types.EventLoopEnd, Loop: i})
			return "", reported(err)
		}

		if err := s.store.AddMessage(resp.Message); err != nil {
			// Store error — emit error + loop_end so clients close cleanly.
			// turn_end is handled by defer emitTurnEnd().
			storeErr := fmt.Errorf("store assistant: %w", err)
			s.emit(errorEvent(storeErr))
			s.emit(types.Event{Type: types.EventLoopEnd, Loop: i})
			return "", reported(storeErr)
		}

		if len(resp.ToolCalls) == 0 {
			// No tool calls — turn is complete. turn_end via defer.
			s.emit(types.Event{Type: types.EventLoopEnd, Loop: i})
			return resp.Text, nil
		}

		if len(toolResults) > 0 {
			if err := s.store.AddMessage(types.NewToolResultMessage(toolResults)); err != nil {
				// Store error mid-turn — emit error + loop_end. turn_end via defer.
				storeErr := fmt.Errorf("store tool results: %w", err)
				s.emit(errorEvent(storeErr))
				s.emit(types.Event{Type: types.EventLoopEnd, Loop: i})
				return "", reported(storeErr)
			}
		}

		// This iteration ran tools and will loop again — close it so LoopStart and
		// LoopEnd stay balanced across iterations.
		s.emit(types.Event{Type: types.EventLoopEnd, Loop: i})

		// Auto-compact at 98% context usage before next iteration. Uses the
		// unguarded compact: we're mid-turn (busy), which the public Compact would
		// reject, but here it's the safe between-iterations point.
		if s.stats.ContextUsage >= 0.98 {
			s.compact(ctx) //nolint:errcheck — error already emitted as EventError
		}
	}

	// Max iterations reached while still executing tools. This is the ONE
	// iteration reserved by "for i := range s.maxIterations - 1" above — it
	// gets its own LoopStart/LoopEnd, exactly like every other iteration in
	// the loop, instead of running requestProgressUpdate's LLM call as a gap
	// between the last real loop_end and turn_end with no loop_start of its
	// own. That asymmetry (a real bug, not just a TUI cosmetic issue) meant
	// clients that re-arm per-turn state on loop_start specifically — the
	// TUI's spinner (re-armed on loop_start after a mid-turn auto-compact
	// turns it off in compact_end, see internal/tui/events.go) and its
	// "(turn/max_iterations)" footer counter (incremented only on
	// loop_start) — never fired for this reserved iteration. If the
	// PREVIOUS iteration's auto-compact (ContextUsage >= 0.98, right above)
	// happened to be the very last one before hitting the cap, the spinner
	// was left off with nothing left in the event stream to turn it back on
	// — making the turn look frozen right at the "reached the N-iteration
	// limit" warning even though the summary was still streaming in right
	// behind it.
	//
	// EventMaxIterationsReached fires right after LoopStart (before the
	// summary request) so the TUI's "⚠ reached the N-iteration limit —
	// summarizing progress" arrives as a forewarning, not an afterword.
	//
	// Entry guard, mirroring the one at the top of every for-loop iteration
	// above: if the user already pressed Stop, this reserved iteration must
	// behave like any other — emit EventStop and leave, rather than firing a
	// "summarizing progress" warning it can't honour (the LLM call below
	// would fail on the cancelled ctx immediately). A user Stop stops
	// everything, summary included.
	if ctx.Err() != nil {
		s.emit(types.Event{Type: types.EventStop})
		return "", nil // turn_end via defer
	}

	s.emit(types.Event{Type: types.EventLoopStart, Loop: s.maxIterations - 1})
	s.emit(types.Event{Type: types.EventMaxIterationsReached, MaxIterations: s.maxIterations})
	summary, err := s.requestProgressUpdate(ctx)
	s.emit(types.Event{Type: types.EventLoopEnd, Loop: s.maxIterations - 1})

	// Cancelled MID-summary (Stop pressed while the summary was streaming):
	// same symmetry as the for-loop's post-runStream check — report it as a
	// stop, not as an error, and swallow the ctx error itself.
	if err != nil && ctx.Err() != nil {
		s.emit(types.Event{Type: types.EventStop})
		return "", nil
	}
	// Any other failure propagates up to drainFollowUps, which emits the
	// EventError (it no longer swallows it — see the wasCancelled note
	// there). turn_end is handled by defer emitTurnEnd() on the way out.
	return summary, err
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
			if compactErr := s.compact(ctx); compactErr != nil {
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
	s.modelStr.Store(fullModel) // update lock-free snapshot for CurrentModel()
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

// Compact summarizes the conversation via LLM and stores a checkpoint.
//
// Events emitted:
//   - EventCompactStart always
//   - EventCompactEnd{Output: summary} on success
//   - EventError{Output: msg} on failure (no EventCompactEnd)
//
// compact does the actual summarize-and-checkpoint work, unsynchronized. The
// auto-compaction path calls it directly between ReAct iterations (inside the
// turn), where it's safe; external callers go through the public Compact, which
// guards against running mid-turn.
func (s *Session) compact(ctx context.Context) error {
	s.emit(types.Event{Type: types.EventCompactStart})

	// Generate compaction summary — store is untouched until this succeeds
	summary, err := s.generateCompactionSummary(ctx)
	if err != nil {
		s.emit(types.Event{Type: types.EventError, Message: fmt.Sprintf("compact failed: %v", err)})
		return fmt.Errorf("compact: %w", err)
	}

	// Commit checkpoint — append-only, no data lost. The persisted checkpoint
	// gets the memory nudge appended (when memory is enabled for this session);
	// the event below keeps the LLM's summary as-is so the UI shows a clean
	// summary, not the internal reminder.
	checkpoint := buildCompactionCheckpoint(summary, s.hasMemory)
	if err := s.store.AddCompactionSummary(checkpoint); err != nil {
		s.emit(types.Event{Type: types.EventError, Message: fmt.Sprintf("compact checkpoint failed: %v", err)})
		return fmt.Errorf("compact: checkpoint: %w", err)
	}

	// Compaction shrinks the ACTIVE context, not the session's lifetime usage.
	// Reset only the context-usage gauge (and the last-turn input it's derived
	// from); the accumulated input/output token totals are historical — they
	// already happened and drive cost/stats, so they must be preserved.
	s.lastInputTokens = 0
	s.stats.ContextUsage = 0
	meta := s.store.Meta()
	meta.Stats = s.stats
	s.store.UpdateMeta(meta)

	s.emit(types.Event{Type: types.EventCompactEnd, Summary: summary})

	// Publish the reset context gauge immediately, rather than leaving clients
	// showing the pre-compaction figure until the NEXT turn happens to emit
	// one. EventTokens is reused for this on purpose (instead of adding stats
	// fields to compact_end): it's already the one event that carries usage,
	// every client already handles it, and the values below are exactly what
	// was just persisted — live context zeroed, session history untouched.
	s.emit(types.Event{
		Type: types.EventTokens,
		Tokens: types.TokenUsage{
			Input:         0, // context was just reclaimed
			ContextUsage:  0,
			ContextWindow: s.contextWindow,
			TotalInput:    s.stats.InputTokens,
			TotalOutput:   s.stats.OutputTokens,
			CacheRead:     s.stats.CacheRead,
			CacheWrite:    s.stats.CacheWrite,
			CostUSD:       s.stats.CostUSD,
		},
	})
	return nil
}

// generateCompactionSummary makes a focused LLM call to summarize the full conversation
// for use as a compaction checkpoint. Uses no tools and a dedicated system prompt.
// The result is stored internally — NOT streamed to the transport.
// Retries up to 3 times with exponential backoff to handle transient token
// refresh failures or network errors during compact.
func (s *Session) generateCompactionSummary(ctx context.Context) (string, error) {
	// Append a user message asking for the summary. Besides making the request
	// explicit, it guarantees the conversation ends with a user turn — required by
	// providers that reject assistant-message prefill (e.g. Claude subscription),
	// since the working set may otherwise end on an assistant message.
	//
	// Strip inline images from the history before sending to the compaction
	// model: the LLM only needs text to produce a summary, and providers like
	// Anthropic reject requests with many large images (>2000px) when they appear
	// together in a single request (error: "image dimensions exceed max allowed
	// size for many-image requests"). Replacing each image part with a placeholder
	// text preserves the conversational structure without triggering the limit.
	messages := append(stripImages(s.store.Messages()), types.NewUserTextMessage(compactRequestPrompt))
	req := &types.Request{
		SystemPrompt: compactSystemPrompt,
		Model:        s.modelID,
		Messages:     messages,
		Tools:        nil, // no tools — pure text
		MaxTokens:    4096,
	}

	const maxAttempts = 3
	backoff := 2 * time.Second
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		var summaryText string
		_, err := s.provider.CompleteStream(ctx, req, func(se types.StreamEvent) {
			if se.Type == types.StreamTextDelta {
				summaryText += se.Delta
			}
		})
		if err == nil {
			return summaryText, nil
		}
		lastErr = err
	}

	// All attempts exhausted — return the last error.
	return "", lastErr
}

// ID returns the session's unique identifier.
func (s *Session) ID() string { return s.id }

// Name returns the session's display name.
func (s *Session) Name() string { return s.name }

// MaxIterations returns the max ReAct iterations allowed per turn for this
// session (AgentOptions.MaxIterations, default 50). Exposed read-only so
// clients (e.g. the TUI footer) can show progress like "(3/25)" without
// duplicating the limit.
func (s *Session) MaxIterations() int { return s.maxIterations }

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

// ContextBreakdown is a token-count estimate of how the model's context window
// is used by this session, broken into three components (S/T/C):
//   - System: the full system prompt (base + skills + agents.md + directives)
//   - Tools: all tool schemas sent to the model (built-in + MCP)
//   - Conversation: the working-set messages (post-compaction checkpoint)
//
// Estimates use the provider's wire format and the correct chars-per-token
// divisor for the active tokenizer family (Anthropic=6, OpenAI=4).
// Only LastRealTotal and ContextWindow come from the actual provider response.
type ContextBreakdown struct {
	System       int `json:"system"`       // estimated tokens for the full system prompt
	Tools        int `json:"tools"`        // estimated tokens for all tool schemas
	Conversation int `json:"conversation"` // estimated tokens for the working-set messages

	EstimatedTotal int `json:"estimated_total"` // System + Tools + Conversation
	LastRealTotal  int `json:"last_real_total"` // actual input tokens from last model turn (0 if none yet)
	ContextWindow  int `json:"context_window"`  // model context window (tokens)
	FreeSpace      int `json:"free_space"`      // ContextWindow − LastRealTotal (0 if none yet)
}

// ContextBreakdown returns how the model's context window is used.
//
// S (System) and T (Tools) are estimated from stored byte lengths using the
// provider's chars-per-token divisor (Anthropic=6, OpenAI=4) — computed once
// at session creation from the actual text/JSON, never re-measured.
//
// C (Conversation) is derived as (actual - S - T) from the provider-reported
// token count, not estimated from local messages. This makes it exact by
// definition and naturally accounts for everything the model received that we
// cannot enumerate locally: cached thinking blocks, pre-compaction history,
// protocol overhead.
//
// LastRealTotal and ContextWindow come directly from the provider response.
func (s *Session) ContextBreakdown() ContextBreakdown {
	// Derive tokenizer family from the current provider — always reflects the
	// active model even after SwitchModel, no extra stored field needed.
	s.mu.Lock()
	provName := s.provider.Name()
	lastReal := s.lastInputTokens
	ctxWin := s.contextWindow
	s.mu.Unlock()

	family := llm.FamilyForProvider(provName)
	cpt := family.CharsPerToken()

	// S — system prompt: stored as total bytes, divide by family divisor.
	system := s.sysPromptLen / cpt

	// T — tools: all tool schemas (built-in + MCP) stored as total JSON bytes.
	tools := s.toolsLen / cpt

	// C — conversation: derived from the actual provider-reported total rather
	// than estimated locally. This is intentional: the model's actual token
	// count includes cached context (thinking blocks, pre-compaction history)
	// that we cannot enumerate from the local working set. Deriving C as
	// (actual - S - T) makes it exact by definition and absorbs all cache.
	// Falls back to 0 if no turn has happened yet (lastReal == 0).
	conv := 0
	if lastReal > 0 {
		conv = lastReal - system - tools
		if conv < 0 {
			conv = 0
		}
	}

	estimated := system + tools + conv

	free := 0
	if ctxWin > 0 && lastReal > 0 {
		free = ctxWin - lastReal
	}

	return ContextBreakdown{
		System:         system,
		Tools:          tools,
		Conversation:   conv,
		EstimatedTotal: estimated,
		LastRealTotal:  lastReal,
		ContextWindow:  ctxWin,
		FreeSpace:      free,
	}
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

// Close flushes and closes the store, and removes the session from its agent's
// active set (so the scheduler no longer routes prompts to it).
func (s *Session) Close() error {
	if s.agent != nil {
		s.agent.unregisterSession(s.id)
	}
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
			// Do NOT emit EventError here — runStream returns this same error
			// to promptSync, which emits EventError via errorEvent(err). Emitting
			// here too produces a duplicate error message in the TUI.
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
		wg            sync.WaitGroup
		resultsMu     sync.Mutex
		streamResults []types.ToolResult
	)

	for _, call := range pendingCalls {
		call := call // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			output, images, execErr := s.tools.Run(ctx, call.toolName, call.toolArgs)
			dur := time.Since(start)
			// If ctx was cancelled (Stop), skip emitting — EventStop handles it
			if ctx.Err() != nil {
				return
			}
			isErr := execErr != nil
			// A failing tool may return an empty output with the message carried in
			// execErr (e.g. MCP tools do `return "", err`). Providers reject a
			// tool_result that is is_error=true with empty content (Anthropic 400),
			// so surface the error text as the output.
			if isErr && output == "" {
				output = execErr.Error()
			}
			// Safety-net truncation for tools we don't control (e.g. MCP servers).
			// Built-in tools already truncate with their own head/tail strategy and
			// are no-ops here (already within limits). Prevents a giant tool result
			// from blowing the model's context; the full output is saved to a temp
			// file and the model is told where to find it.
			output = tools.ApplyTruncation(call.toolName, output, true)
			s.emit(types.Event{Type: types.EventToolResult, ToolID: call.toolID, ToolName: call.toolName, Output: output, Duration: dur, IsError: isErr})
			resultsMu.Lock()
			streamResults = append(streamResults, types.ToolResult{ID: call.toolID, Output: output, Images: images, IsErr: isErr})
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

	// Context usage = (fresh input + cache reads + cache writes) / context window.
	//
	// All three count because all three were sent to the model as context on
	// this request — they're just billed differently. Anthropic's own field
	// name says it outright: cache_creation_input_tokens (our CacheWrite) is
	// INPUT that was additionally written to the cache.
	//
	// CacheWrite was missing here, and it was not a cosmetic bug: on a turn
	// that (re)writes the cache — the norm right after a compaction, a model
	// switch, or any system-prompt change — Anthropic reports nearly the whole
	// context as cache_creation and only a handful of fresh input tokens. A
	// real field report showed 2 fresh + 0 read + 827.6k written on a 1M
	// window: this formula produced 0.0002% (footer read "0.0%") while the
	// true occupancy was 82.8%, matching the 82.7% persisted from the turn
	// before — the context had not shrunk at all, the accounting had lost it.
	//
	// Because ContextUsage >= 0.98 is what triggers the mid-turn auto-compact
	// (see promptSync), under-counting here meant the guard could stay silent
	// while the real context ran to the window limit — losing the turn to a
	// provider overflow error instead of compacting in time.
	s.lastInputTokens = se.InputTokens + se.CacheRead + se.CacheWrite
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
			// Live context: raw tokens for ACP's usage_update{used,size}, the
			// same number as a ratio for the TUI footer's "%/window".
			Input:         s.lastInputTokens, // fresh + cache read + cache write
			ContextUsage:  s.stats.ContextUsage,
			ContextWindow: s.contextWindow,
			// Session history: the SAME values SessionStats persists, so a
			// client that loaded stats on resume keeps seeing that semantic
			// (per-turn values here used to silently replace the session
			// totals a resuming footer had just loaded).
			TotalInput:  s.stats.InputTokens,
			TotalOutput: s.stats.OutputTokens,
			CacheRead:   s.stats.CacheRead,
			CacheWrite:  s.stats.CacheWrite,
			CostUSD:     s.stats.CostUSD,
		},
	})
}

// requestProgressUpdate makes a final LLM call when max iterations is reached.
// Asks the model to summarize progress and check with the user on next steps.
// The response IS streamed to the transport via EventStreamTextDelta.
func (s *Session) requestProgressUpdate(ctx context.Context) (string, error) {
	// Inject summary request into history. Mark it as system-generated so
	// transports that replay history (e.g. the TUI on resume) can render it
	// as a notice instead of as a user message the human never typed.
	msg := types.NewUserTextMessage(maxIterationsPrompt)
	msg.Meta = &types.MessageMeta{IsSystemGenerated: true}
	if err := s.store.AddMessage(msg); err != nil {
		return "", err
	}

	// LLM call with no tools — pure text response.
	//
	// Strip inline images from the history first, for exactly the reason
	// generateCompactionSummary already does (see its own comment): a summary
	// only needs TEXT, and Anthropic rejects a request whose history carries
	// several large images together with
	//   "At least one of the image dimensions exceed max allowed size for
	//    many-image requests: 2000 pixels" (HTTP 400).
	// stripImages replaces every image with a "[image: <mime>]" placeholder,
	// preserving the conversational structure (and the fact that an image was
	// there) without shipping any base64 payload. The on-disk store is never
	// modified — this only shapes the wire payload.
	//
	// This path is MORE exposed to the limit than any other, which is why it
	// was the one failing in the field: the ReAct loop above uses
	// stripOldTurnImages, which deliberately PRESERVES the current turn's
	// images so the model can see what was just shared. After ~120 iterations
	// the "current turn" can have accumulated many of them, and this final
	// call used to send every single one at full size.
	req := &types.Request{
		SystemPrompt: s.systemPrompt,
		Model:        s.modelID,
		Messages:     stripImages(s.store.Messages()),
		Tools:        nil, // no tools — force text response
		MaxTokens:    s.maxTokens,
	}

	// Retry the summary call, same 3-attempts-with-backoff shape
	// generateCompactionSummary already uses (see its loop) — and for the same
	// reason: this is a single, unrepeatable LLM call at the very end of a
	// long turn, so a transient failure (OAuth token refresh landing mid-call,
	// a 429, a dropped connection) permanently costs the user the ONLY report
	// of ~120 iterations of work. It used to have no retry at all, which is
	// exactly how a summary went missing in the field while the
	// "reached the N-iteration limit" warning had already been shown.
	//
	// A retry is safe here even though this call STREAMS to the transport
	// (unlike generateCompactionSummary, which accumulates silently): a
	// failed attempt emits at most a partial text run, and the transports
	// append streamed deltas into the live block, so a retry continues in the
	// same block rather than corrupting state. Losing the summary entirely is
	// strictly worse than a rare duplicated fragment.
	const maxSummaryAttempts = 3
	backoff := 2 * time.Second
	var (
		resp    *types.Response
		lastErr error
	)
	for attempt := 0; attempt < maxSummaryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		var err error
		resp, _, err = s.runStream(ctx, req)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		// A cancelled ctx (user Stop) is final — retrying can't succeed and
		// would just burn the backoff waiting. Surface it immediately.
		if ctx.Err() != nil {
			return "", err
		}
	}
	if lastErr != nil {
		return "", lastErr
	}

	if err := s.store.AddMessage(resp.Message); err != nil {
		return "", err
	}

	// Guard against providers that return an empty response when asked for a
	// progress summary (e.g. some OpenAI-compatible endpoints ignore Tools:nil
	// or fail to emit content deltas). Returning an error lets drainFollowUps
	// emit EventError so the user sees something instead of a silent stop.
	if resp.Text == "" {
		return "", fmt.Errorf("model returned an empty summary; conversation capped at %d iterations", s.maxIterations)
	}

	return resp.Text, nil
}

func (s *Session) emit(e types.Event) {
	if s.handler != nil {
		s.handler(e)
	}
}

// stripOldTurnImages strips inline images from all turns except the current one.
//
// "Current turn" = all messages after the last assistant message that has no
// tool_calls (i.e. the terminal response of the previous turn). Everything
// before that boundary already had its images processed by the model in a
// previous request — sending them again wastes tokens and, for providers like
// Anthropic, triggers a 400 when multiple large images exceed 2000px.
//
// Images in the current turn are always preserved so the model can analyse
// whatever was just shared (pasted screenshot, Read tool result, etc.).
//
// The on-disk store is never modified — this only affects the wire payload.
func stripOldTurnImages(msgs []types.Message) []types.Message {
	// Find the boundary: scan backward for the last assistant message without
	// tool_calls. Everything after that index is the current turn.
	boundary := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != types.RoleAssistant {
			continue
		}
		hasToolCall := false
		for _, p := range m.Parts {
			if p.ToolCall != nil {
				hasToolCall = true
				break
			}
		}
		if !hasToolCall {
			boundary = i + 1 // current turn starts after this message
			break
		}
	}

	if boundary == 0 {
		// No previous terminal assistant message found — everything is the
		// current turn (first turn ever, or all messages are tool calls).
		// Nothing to strip.
		return msgs
	}

	result := make([]types.Message, len(msgs))
	copy(result, msgs) // keep current-turn messages (boundary..end) intact
	for i := 0; i < boundary; i++ {
		result[i] = stripImagesFromMessage(msgs[i])
	}
	return result
}

// stripImages returns a copy of msgs with all inline image parts replaced by a
// short placeholder text. Used by generateCompactionSummary to avoid provider
// errors when many large images are present in a single request (e.g. Anthropic
// rejects requests with multiple images exceeding 2000px in any dimension).
// ToolResult images are replaced with "[image omitted]" in the output string.
func stripImages(msgs []types.Message) []types.Message {
	result := make([]types.Message, len(msgs))
	for i, m := range msgs {
		result[i] = stripImagesFromMessage(m)
	}
	return result
}

// stripImagesFromMessage returns a copy of msg with all inline images replaced
// by a short placeholder text. The on-disk representation is never touched.
func stripImagesFromMessage(m types.Message) types.Message {
	stripped := types.Message{Role: m.Role, Meta: m.Meta}
	for _, p := range m.Parts {
		switch {
		case p.Image != nil:
			// Top-level image part (user message with pasted image).
			mime := p.Image.MimeType
			if mime == "" {
				mime = "image"
			}
			stripped.Parts = append(stripped.Parts, types.ContentPart{
				Text: "[image: " + mime + "]",
			})
		case p.ToolResult != nil && len(p.ToolResult.Images) > 0:
			// Tool result carrying inline images (e.g. Read tool on an image file).
			// Keep the text output, replace images with a per-image placeholder.
			tr := *p.ToolResult
			placeholder := ""
			for _, img := range tr.Images {
				mime := img.MimeType
				if mime == "" {
					mime = "image"
				}
				placeholder += "[image: " + mime + "] "
			}
			tr.Images = nil
			if tr.Output != "" {
				tr.Output = tr.Output + " " + placeholder
			} else {
				tr.Output = placeholder
			}
			stripped.Parts = append(stripped.Parts, types.ContentPart{ToolResult: &tr})
		default:
			stripped.Parts = append(stripped.Parts, p)
		}
	}
	return stripped
}
