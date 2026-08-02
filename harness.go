// Package harness is the public SDK entry point for embedding the harness agent.
//
// The agent is the SDK: create one with [NewAgent], open a session, subscribe
// to its events, and drive it with prompts. See the [agent] package for the
// full API (agent.Agent, agent.Session, agent.PromptOption, …) — this facade
// only wraps construction:
//
//	a := harness.NewAgent(
//	    harness.AgentWithThinking("medium"),
//	    harness.AgentWithMCPs(),
//	)
//	defer a.Close()
//
//	sess, err := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	sess.Subscribe(func(e types.Event) { /* render */ })
//	sess.Prompt(ctx, "Hello!")
//
// Deeper building blocks live in their own public packages:
//   - agent           — Agent, Session, and the contracts you implement
//                        (agent.ResourceLoader) or extend (agent.PromptOption)
//   - agent/store     — SessionStore + SessionMeta, the persistence port
//   - agent/resources — ResourceLoader internals (the default filesystem one)
//   - agent/tools     — Tool, the registry, and built-in tools
//   - agent/memory    — the persistent memory store internals
//   - types           — Event, Message, ModelMeta and other shared types
//
// A process that runs `harness serve` exposes the agent over HTTP/SSE; use
// [Client] to drive that server remotely instead of embedding the agent
// directly — same session/prompt/event model, just over the wire.
//
// Everything under internal/ (providers, config, transports, build version) is
// implementation detail and not part of the SDK's compatibility surface.
package harness

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
)

// Client is a typed HTTP/SSE client for a running harness server (`harness
// serve`, or the in-process server any transport starts). See [client.Client].
type Client = client.Client

// NewClient connects to a harness server at addr (e.g. "127.0.0.1:8080") and
// returns a typed client. See [client.New].
var NewClient = client.New

// AgentOption configures an [agent.Agent] at construction time. Options are
// applied in order by [NewAgent]; later options win. Zero options yields a
// sensible default agent.
type AgentOption func(*agent.AgentOptions)

// NewAgent creates an agent.Agent, applying the given options over the
// defaults. With no options it returns a default agent:
//
//	a := harness.NewAgent(
//		harness.AgentWithThinking("medium"),
//		harness.AgentWithMCPs(),
//	)
//	defer a.Close()
func NewAgent(opts ...AgentOption) *agent.Agent {
	var o agent.AgentOptions
	for _, opt := range opts {
		opt(&o)
	}
	return agent.New(o)
}

// AgentWithOptions applies a pre-built [agent.AgentOptions] struct. Useful
// when a config was assembled elsewhere; individual AgentWith* options
// applied after it still win.
func AgentWithOptions(o agent.AgentOptions) AgentOption {
	return func(dst *agent.AgentOptions) { *dst = o }
}

// AgentWithThinking sets the reasoning effort: "off", "low", "medium",
// "high", or "xhigh".
func AgentWithThinking(level string) AgentOption {
	return func(o *agent.AgentOptions) { o.ThinkingLevel = level }
}

// AgentWithSystemPrompt sets the base system prompt applied to all sessions.
func AgentWithSystemPrompt(prompt string) AgentOption {
	return func(o *agent.AgentOptions) { o.SystemPrompt = prompt }
}

// AgentWithDirectives appends extra instruction blocks to the system prompt.
// Each is added verbatim (below the base prompt, skills, memory, etc.), so a
// caller — typically a transport — can teach the agent capabilities specific
// to its environment (e.g. how to send files over Telegram). Repeated calls
// accumulate.
func AgentWithDirectives(directives ...string) AgentOption {
	return func(o *agent.AgentOptions) { o.Directives = append(o.Directives, directives...) }
}

// AgentWithMaxIterations caps the ReAct iterations per turn (default 50).
func AgentWithMaxIterations(n int) AgentOption {
	return func(o *agent.AgentOptions) { o.MaxIterations = n }
}

// AgentWithMaxTokens caps output tokens per turn (default: the model's max).
func AgentWithMaxTokens(n int) AgentOption {
	return func(o *agent.AgentOptions) { o.MaxTokens = n }
}

// AgentWithTools registers additional tools alongside the built-ins (Bash,
// Read, Write, Edit, Fetch). Repeated calls accumulate. See [agent/tools.Tool].
func AgentWithTools(ts ...tools.Tool) AgentOption {
	return func(o *agent.AgentOptions) { o.Tools = append(o.Tools, ts...) }
}

// AgentWithDisallowedTools excludes tools by name (built-in or MCP), e.g. for
// a read-only sandbox: AgentWithDisallowedTools("Bash", "Write", "Edit").
func AgentWithDisallowedTools(names ...string) AgentOption {
	return func(o *agent.AgentOptions) { o.DisallowedTools = append(o.DisallowedTools, names...) }
}

// AgentWithMCPs enables spawning and connecting the configured MCP servers
// (root agent only). Its presence turns MCP on.
func AgentWithMCPs() AgentOption {
	return func(o *agent.AgentOptions) { o.EnableMCPs = true }
}

// AgentWithStore sets a custom session store (default: file-backed, falling
// back to in-memory). Implement [agent/store.SessionStore] — a small
// primitive persistence port.
func AgentWithStore(s store.SessionStore) AgentOption {
	return func(o *agent.AgentOptions) { o.Store = s }
}

// AgentWithResourceLoader sets a custom skill/resource loader (default:
// filesystem per session). Implement [agent/resources.ResourceLoader]; pass
// resources.NilLoader{} to disable discovery.
func AgentWithResourceLoader(l resources.ResourceLoader) AgentOption {
	return func(o *agent.AgentOptions) { o.ResourceLoader = l }
}

// AgentWithMemory enables project-scoped persistent memory. The agent opens
// and owns the store (~/.harness/agent/memory.db) and registers the Memo*
// tools. Off by default.
func AgentWithMemory() AgentOption {
	return func(o *agent.AgentOptions) { o.EnableMemory = true }
}

// AgentWithScheduler enables cron-scheduled prompts: the agent runs the
// engine that fires due schedules (in addition to the Schedule* management
// tools, which are always available). Each schedule records the id of the
// session that created it; when due, the engine routes the prompt back to
// that session if it's active (otherwise the prompt is dropped). Only one
// agent per process should enable this, so prompts don't fire twice. Off by
// default.
func AgentWithScheduler() AgentOption {
	return func(o *agent.AgentOptions) { o.EnableScheduler = true }
}

// AgentWithColleagues enables the ColleagueList/ColleagueAsk tools: the agent
// can discover OTHER running harness server instances on this machine (any
// process that called Serve — see the client package) via the shared
// ~/.harness/instances.json registry, and delegate a prompt to one of them by
// name over HTTP. Each colleague answers using its OWN model, MCPs, and
// project context, not the caller's — real delegation, not talking to itself.
// Meant for long-running processes (a served agent, a transport); one-shot
// callers have nothing to offer a colleague and no time to wait for one. Off
// by default.
func AgentWithColleagues() AgentOption {
	return func(o *agent.AgentOptions) { o.EnableColleagues = true }
}
