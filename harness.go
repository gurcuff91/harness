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
// the types. The contracts you implement are re-exported here too, so the common
// case needs no sub-package imports: [SessionStore] + [SessionMeta] for custom
// persistence, [ResourceLoader] for custom skill loading, and [Tool] for custom
// tools. Deeper building blocks still live in their own public packages:
//   - agent/tools     — tool registry and helpers
//   - agent/store     — session storage internals
//   - agent/resources — resource loader internals
//   - agent/memory    — the persistent memory store internals
//   - types           — Event, Message, ModelMeta and other shared types
//
// Everything under internal/ (providers, config, transports, build version) is
// implementation detail and not part of the SDK's compatibility surface.
package harness

import (
	"github.com/gurcuff91/harness/agent"
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

// ── Implementable contracts ───────────────────────────────────────────────
// Re-exported so the common case (a custom store, loader, or tool) needs only
// the harness import — no sub-package imports to name the type you implement.

// SessionStore is the persistence port for sessions: implement it to back
// sessions with files, SQLite, Postgres, S3, etc. See [store.SessionStore].
type SessionStore = store.SessionStore

// SessionMeta is the per-session metadata your [SessionStore] persists. See
// [store.SessionMeta].
type SessionMeta = store.SessionMeta

// ResourceLoader discovers skills and project context: implement it to load
// skills/resources from a custom source. See [resources.ResourceLoader].
type ResourceLoader = resources.ResourceLoader

// Tool is a custom tool the agent can call. Build one and register it with
// [WithTools]. See [tools.Tool].
type Tool = tools.Tool

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
func WithTools(ts ...Tool) Option {
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

// WithStore sets a custom session store (default: file-backed, falling back to
// in-memory). Implement store.SessionStore — a small primitive persistence port.
func WithStore(s SessionStore) Option {
	return func(o *Options) { o.Store = s }
}

// WithResourceLoader sets a custom skill/resource loader (default: filesystem
// per session). Pass resources.NilLoader{} to disable discovery.
func WithResourceLoader(l ResourceLoader) Option {
	return func(o *Options) { o.ResourceLoader = l }
}

// WithMemory enables project-scoped persistent memory. The agent opens and owns
// the store (~/.harness/agent/memory.db) and registers the Memo* tools. Off by
// default.
func WithMemory() Option {
	return func(o *Options) { o.EnableMemory = true }
}

// WithScheduler enables cron-scheduled prompts: the agent runs the engine that
// fires due schedules (in addition to the Schedule* management tools, which are
// always available). Each schedule records the id of the session that created
// it; when due, the engine routes the prompt back to that session if it's
// active (otherwise the prompt is dropped). Only one agent per process should
// enable this, so prompts don't fire twice. Off by default.
func WithScheduler() Option {
	return func(o *Options) { o.EnableScheduler = true }
}
