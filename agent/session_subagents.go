package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/names"
	"github.com/gurcuff91/harness/types"
)

// maxLiveSubagents is how many sub-agents one parent session may have running
// at once: each is a full session talking to a provider, and nothing else
// holds a runaway model back now that the Subagent tool does not block.
const maxLiveSubagents = 10

// subagentShutdownWait bounds how long Close waits for the cancelled
// sub-agents to run their cleanup (their stop events). They unwind as soon as
// their context is cancelled; this only protects Close from a stuck provider.
const subagentShutdownWait = 10 * time.Second

// subagentOrigin is the origin of the prompt that carries a sub-agent's answer.
const subagentOrigin = "subagent"

// subagentSet is a session's registry of live sub-agents. Its own small mutex
// is never s.mu, so it is safe from inside a turn and from any goroutine.
type subagentSet struct {
	mu      sync.Mutex
	names   []string // live names, in launch order
	closing bool     // set by shutdown: nothing new may start
	wg      sync.WaitGroup
}

// Subagents returns the live sub-agent names of this session in launch order.
// Never nil; safe to call from anywhere (it never takes s.mu).
func (s *Session) Subagents() []string {
	s.subs.mu.Lock()
	defer s.subs.mu.Unlock()
	return append([]string{}, s.subs.names...)
}

// pickSubagentNameLocked draws a name not in use by a live sub-agent: it
// redraws while the draw is taken and, when the pool cannot give a free one
// (every name is live — or, in a test, the pool was shrunk), appends -2, -3…
// to the last draw. Caller holds s.subs.mu.
func (s *Session) pickSubagentNameLocked() string {
	used := make(map[string]bool, len(s.subs.names))
	for _, n := range s.subs.names {
		used[n] = true
	}
	base := ""
	for i := 0; i < 64; i++ {
		base = names.Random()
		if !used[base] {
			return base
		}
	}
	for n := 2; ; n++ {
		if cand := fmt.Sprintf("%s-%d", base, n); !used[cand] {
			return cand
		}
	}
}

// launchSubagent starts a sub-agent in the background and returns its name at
// once. The sub-agent lives as long as this SESSION (s.lifeCtx), never as long
// as the turn that launched it: Stop() leaves it alone, Close() cancels it. Its
// answer arrives later as a prompt to this session (see runSubagent).
func (s *Session) launchSubagent(prompt string, requestedMaxIterations int, readonly bool) (string, error) {
	if s.agent == nil || s.lifeCtx == nil {
		return "", fmt.Errorf("sub-agent: this session cannot launch sub-agents")
	}
	s.subs.mu.Lock()
	if s.subs.closing || s.lifeCtx.Err() != nil {
		s.subs.mu.Unlock()
		return "", fmt.Errorf("sub-agent: the session is closing")
	}
	if len(s.subs.names) >= maxLiveSubagents {
		s.subs.mu.Unlock()
		return "", fmt.Errorf("too many sub-agents running (%d): wait for one to finish", maxLiveSubagents)
	}
	name := s.pickSubagentNameLocked()
	s.subs.names = append(s.subs.names, name)
	s.subs.wg.Add(1)
	s.subs.mu.Unlock()

	// The model is the parent's CURRENT one: a /model switch since the session
	// was opened must be honored (see buildSessionTools' sessRef comment).
	model := s.Model()
	ctx, cancel := context.WithCancel(s.lifeCtx)
	s.emit(types.Event{Type: types.EventSubagentStart, Subagent: name})
	go s.runSubagent(ctx, cancel, name, prompt, requestedMaxIterations, readonly, model)
	return name, nil
}

// runSubagent builds the ephemeral sub-agent, runs it and reports how it ended.
// Everything after the run happens in ONE deferred cleanup, always, in this
// order: cost folded and the sub-agent closed; the result worked out; the name
// removed from the live list; the stop event emitted; and only THEN the answer
// delivered — so when the model receives `<subagent … reply>` every frontend
// has already dropped the name.
func (s *Session) runSubagent(ctx context.Context, cancel context.CancelFunc, name, prompt string, requestedMaxIterations int, readonly bool, model string) {
	var (
		text string
		err  error
		sub  *Session
	)
	defer s.subs.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sub-agent panicked: %v", r)
		}
		// (a) cost into the parent, then discard the ephemeral session.
		if sub != nil {
			s.addDelegatedCost(sub.syncStats())
			sub.Close()
		}
		cancel()
		// (b) the result. A closed parent wins: whatever the sub-agent was
		// doing, it was cancelled from above and nobody is left to answer.
		result := types.SubagentOK
		switch {
		case s.lifeCtx.Err() != nil:
			result = types.SubagentCancelled
		case err != nil:
			result = types.SubagentError
		}
		// (c) out of the live list, (d) stop announced, (e) answer delivered.
		s.removeSubagent(name)
		s.emit(types.Event{Type: types.EventSubagentStop, Subagent: name, SubagentResult: result})
		if result == types.SubagentCancelled {
			return
		}
		reply := subagentReply(name, text, err)
		s.Prompt(context.Background(), reply, PromptWithOrigin(subagentOrigin+":"+name), PromptWithDisplayText(subagentDisplay(name, text, err)))
	}()

	if hook := s.agent.subagentStartHookForTest; hook != nil {
		hook()
	}
	maxIter := subagentMaxIterations
	if requestedMaxIterations > 0 {
		maxIter = requestedMaxIterations
	}
	subAgent, newErr := s.agent.newSubagent(subagentSystemPrompt, maxIter, readonly)
	if newErr != nil {
		err = fmt.Errorf("sub-agent: %w", newErr)
		return
	}
	sub, err = subAgent.NewSession(s.cwd, model)
	if err != nil {
		err = fmt.Errorf("sub-agent: %w", err)
		sub = nil
		return
	}

	var (
		mu      sync.Mutex
		textBuf strings.Builder
		ranOut  bool
	)
	done := make(chan error, 2)
	send := func(e error) {
		select {
		case done <- e:
		default:
		}
	}
	sub.Subscribe(func(e types.Event) {
		switch e.Type {
		case types.EventStreamTextDelta:
			mu.Lock()
			textBuf.WriteString(e.Delta)
			mu.Unlock()
		case types.EventMaxIterationsReached:
			mu.Lock()
			ranOut = true
			mu.Unlock()
		case types.EventTurnEnd:
			send(nil)
		case types.EventError:
			send(fmt.Errorf("%s", e.Message))
		}
	})
	sub.Prompt(ctx, prompt)
	select {
	case err = <-done:
		// A TurnEnd that raced a cancelled ctx is a cancellation in disguise.
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	case <-ctx.Done():
		err = ctx.Err()
	}
	mu.Lock()
	text = strings.TrimSpace(textBuf.String())
	if err == nil && ranOut {
		err = fmt.Errorf("the sub-agent ran out of its %d iterations before finishing; this is its progress summary", maxIter)
	}
	mu.Unlock()
}

func (s *Session) removeSubagent(name string) {
	s.subs.mu.Lock()
	defer s.subs.mu.Unlock()
	for i, n := range s.subs.names {
		if n == name {
			s.subs.names = append(s.subs.names[:i:i], s.subs.names[i+1:]...)
			return
		}
	}
}

// escapeSubagentText keeps a `</subagent>` inside a sub-agent's text from
// closing the reply tag early: `</subagent` becomes `<\/subagent`. Minimal on
// purpose — the model reads it as the same text, and nothing else changes.
func escapeSubagentText(text string) string {
	return strings.ReplaceAll(text, "</subagent", `<\/subagent`)
}

// subagentReply is the prompt that carries a sub-agent's answer:
// `<subagent name="N" reply>TEXT</subagent>`, or, when it failed or ran out of
// iterations, `<subagent name="N" reply error>REASON\n\nPARTIAL TEXT</subagent>`.
func subagentReply(name, text string, err error) string {
	if err == nil {
		return fmt.Sprintf("<subagent name=%q reply>%s</subagent>", name, escapeSubagentText(text))
	}
	body := err.Error()
	if text != "" {
		body += "\n\n" + text
	}
	return fmt.Sprintf("<subagent name=%q reply error>%s</subagent>", name, escapeSubagentText(body))
}

// cancelSubagents refuses new prompts and sub-agents and cancels every live
// sub-agent (they run on s.lifeCtx). Idempotent.
func (s *Session) cancelSubagents() {
	s.followMu.Lock()
	s.closed = true
	s.followMu.Unlock()
	s.subs.mu.Lock()
	s.subs.closing = true
	s.subs.mu.Unlock()
	if s.lifeCancel != nil {
		s.lifeCancel()
	}
}

// shutdownSubagents cancels every live sub-agent and waits (bounded) for each
// to run its cleanup, so every stop event is emitted while the handler,
// channels and watchers still exist, and Subagents() is empty afterwards.
func (s *Session) shutdownSubagents() {
	s.cancelSubagents()
	waited := make(chan struct{})
	go func() { s.subs.wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(subagentShutdownWait):
	}
}

// newSubagent builds the ephemeral agent a sub-agent runs in, from its parent
// (a). The rule is that a sub-agent NEVER has a tool its parent does not: the
// owner limits an agent with DisallowedTools and by not enabling WebSearch, and
// a model must not be able to step over that by launching a sub-agent. So it
// INHERITS the parent's tool configuration and only ADDS restrictions:
//
//   - DisallowedTools is the UNION of the parent's and the sub-agent's own
//     (subagentDisallowedTools: no Subagent — no recursion —, no memory writes,
//     no schedule management, and no Write/Edit when readonly);
//   - WebSearch is on exactly when the parent's is, so a sub-agent can research
//     (the main use of a background sub-agent) but not when its parent cannot.
//
// It reuses the parent's MCP tools WITHOUT spawning MCP processes, shares the
// parent's memory (read-only: the memory writes are disallowed) and its stateless
// resource loader. systemPrompt and maxIterations are the caller's.
func (a *Agent) newSubagent(systemPrompt string, maxIterations int, readonly bool) (*Agent, error) {
	denied := append(append([]string(nil), a.disallowedTools...), subagentDisallowedTools(readonly)...)
	return New(AgentOptions{
		ThinkingLevel:   a.thinkingLevel,
		SystemPrompt:    systemPrompt,
		MaxIterations:   maxIterations,
		MaxTokens:       a.maxTokens,
		Store:           store.NewInMemoryStore(),
		ResourceLoader:  a.resourceLoader, // stateless: shared, never copied
		DisallowedTools: denied,
		EnableWebSearch: a.opts.EnableWebSearch,
		Tools:           a.MCPTools(),
		sharedMemory:    a.memStore, // not closed by the sub-agent
	}), nil
}

// subagentDisplay is what a frontend shows for a sub-agent's reply INSTEAD of the
// reply itself (PromptWithDisplayText, persisted in the message's Meta.Display):
// the report can be long and the parent summarizes it for the person, so the
// screen gets one line, `subagent:<name> reply (N lines)` — or `reply error` when
// it failed — and the full text stays in the log and goes to the model. No excerpt:
// it would only be the start of the text, and show less than it looks like.
func subagentDisplay(name, text string, err error) string {
	body := text
	kind := "reply"
	if err != nil {
		kind = "reply error"
		body = err.Error()
		if text != "" {
			body += "\n\n" + text
		}
	}
	return fmt.Sprintf("%s:%s %s (%s)", subagentOrigin, name, kind, lineCount(body))
}

// lineCount is "N lines" (or "1 line"): the number of lines of s, not counting
// trailing newlines; an empty text is "0 lines".
func lineCount(s string) string {
	s = strings.TrimRight(s, "\r\n")
	n := 0
	if s != "" {
		n = strings.Count(s, "\n") + 1
	}
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}
