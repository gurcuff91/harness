// Package harness is the public SDK entry point for embedding the harness agent.
//
// The agent is the SDK: create one with [New], open a [Session], subscribe to
// its events, and drive it with prompts.
//
//	a := harness.New(harness.Options{
//	    ThinkingLevel: "medium",
//	    EnableMCPs:    true,
//	})
//	defer a.Close()
//
//	sess, err := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	sess.Subscribe(func(e harness.Event) { /* render */ })
//	sess.Prompt(ctx, "Hello!")
//
// These are convenience aliases over the [agent] package — the canonical home of
// the types. Deeper building blocks live in their own public packages:
//   - agent/tools     — implement custom tools
//   - agent/store     — implement custom session storage
//   - agent/resources — implement custom skill/resource loaders
//   - agent/memory    — the persistent memory store
//   - types           — Event, Message, ModelMeta and other shared types
//
// Everything under internal/ (providers, config, transports, build version) is
// implementation detail and not part of the SDK's compatibility surface.
package harness

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
)

// Agent is a configured harness agent. See [agent.Agent].
type Agent = agent.Agent

// Options configures a new [Agent]. See [agent.AgentOptions]. Most callers use
// the [Option] functions with [New] instead of building this directly; it stays
// exported for callers who already have a fully-formed config (see [WithOptions]).
type Options = agent.AgentOptions

// Session is a single conversation with an [Agent]. See [agent.Session].
type Session = agent.Session

// PromptOption configures a [Session.Prompt] call (images, origin). See the
// agent package's WithImages / WithOriginUser / WithOriginScheduled.
type PromptOption = agent.PromptOption

// WithImages attaches images to a prompt. See [agent.WithImages].
var WithImages = agent.WithImages

// Event is an event emitted by a session. See [agent.Event].
type Event = agent.Event

// Handler receives session events. See [agent.Handler].
type Handler = agent.Handler

// Option configures an [Agent] at construction time. Options are applied in
// order by [New]; later options win. Zero options yields a sensible default
// agent.
type Option func(*Options)

// New creates an Agent, applying the given options over the defaults. With no
// options it returns a default agent:
//
//	a := harness.New(
//		harness.WithThinking("medium"),
//		harness.WithMCPs(),
//	)
//	defer a.Close()
func New(opts ...Option) *Agent {
	var o Options
	for _, opt := range opts {
		opt(&o)
	}
	return agent.New(o)
}

// WithOptions applies a pre-built [Options] struct. Useful when a config was
// assembled elsewhere; individual With* options applied after it still win.
func WithOptions(o Options) Option {
	return func(dst *Options) { *dst = o }
}

// WithThinking sets the reasoning effort: "off", "low", "medium", "high", or
// "xhigh".
func WithThinking(level string) Option {
	return func(o *Options) { o.ThinkingLevel = level }
}

// WithSystemPrompt sets the base system prompt applied to all sessions.
func WithSystemPrompt(prompt string) Option {
	return func(o *Options) { o.SystemPrompt = prompt }
}

// WithMaxTurns caps the ReAct iterations per turn (default 25).
func WithMaxTurns(n int) Option {
	return func(o *Options) { o.MaxTurns = n }
}

// WithMaxTokens caps output tokens per turn (default: the model's max).
func WithMaxTokens(n int) Option {
	return func(o *Options) { o.MaxTokens = n }
}

// WithTools registers additional tools alongside the built-ins (Bash, Read,
// Write, Edit, Fetch). Repeated calls accumulate.
func WithTools(ts ...tools.Tool) Option {
	return func(o *Options) { o.Tools = append(o.Tools, ts...) }
}

// WithDisallowedTools excludes tools by name (built-in or MCP), e.g. for a
// read-only sandbox: WithDisallowedTools("Bash", "Write", "Edit").
func WithDisallowedTools(names ...string) Option {
	return func(o *Options) { o.DisallowedTools = append(o.DisallowedTools, names...) }
}

// WithMCPs enables spawning and connecting the configured MCP servers (root
// agent only). Its presence turns MCP on.
func WithMCPs() Option {
	return func(o *Options) { o.EnableMCPs = true }
}

// WithStore sets a custom session store (default: in-memory).
func WithStore(s store.SessionStoreManager) Option {
	return func(o *Options) { o.Store = s }
}

// WithResourceLoader sets a custom skill/resource loader (default: filesystem
// per session). Pass resources.NilLoader{} to disable discovery.
func WithResourceLoader(l resources.ResourceLoader) Option {
	return func(o *Options) { o.ResourceLoader = l }
}

// WithMemory enables project-scoped persistent memory backed by the given store
// (open one with memory.Open). nil leaves memory disabled.
func WithMemory(m *memory.Store) Option {
	return func(o *Options) { o.Memory = m }
}
