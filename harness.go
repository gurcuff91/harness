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
// An already-built agent can also be handed to a RUNNER — a blocking call
// that serves it over a transport until ctx is cancelled: [RunServer] for
// the HTTP/SSE API, [RunTelegram] / [RunSlack] for a chat bot, [RunAcp] for
// the Agent Client Protocol (Zed and other ACP clients). Each is a thin
// alias over its package's own Run — see [server], [transports/telegram],
// [transports/slack], [transports/acp] for the real implementations and
// their Option types.
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	err := harness.RunServer(ctx, a, harness.ServerWithAddr(":8080"))
//
// Deeper building blocks live in their own public packages:
//   - agent            — Agent, Session, and the contracts you implement
//     (agent.ResourceLoader) or extend (agent.PromptOption)
//   - agent/store      — SessionStore + SessionMeta, the persistence port
//   - agent/resources  — ResourceLoader internals (the default filesystem one)
//   - agent/tools      — Tool, the registry, and built-in tools
//   - agent/memory     — the persistent memory store internals
//   - server           — the HTTP/SSE backend every transport (including this
//     facade's RunServer) runs on top of
//   - transports/{telegram,slack,acp} — the runners RunTelegram/RunSlack/
//     RunAcp wrap, and their Option types
//   - client           — typed HTTP/SSE client for a running harness server
//   - types            — Event, Message, ModelMeta and other shared types
//
// Everything under internal/ (providers, config, the TUI, build version) is
// implementation detail and not part of the SDK's compatibility surface.
package harness

import (
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
	"github.com/gurcuff91/harness/transports/acp"
	"github.com/gurcuff91/harness/transports/slack"
	"github.com/gurcuff91/harness/transports/telegram"
)

// Logger is the structured logging contract RunServer/RunTelegram/RunSlack
// accept via their WithLogger option — implement it to route harness's
// backend logs anywhere (RunAcp has no WithLogger of its own: it never logs
// anything itself). See [logx.Logger].
type Logger = logx.Logger

// NilLogger discards everything — the default every runner falls back to
// when no WithLogger is passed. See [logx.NilLogger].
type NilLogger = logx.NilLogger

// Client is a typed HTTP/SSE client for a running harness server (`harness
// serve`, or the in-process server any transport starts). See [client.Client].
type Client = client.Client

// NewClient connects to a harness server at addr (e.g. "127.0.0.1:8080") and
// returns a typed client. See [client.New].
var NewClient = client.New

// ── Agent construction ──────────────────────────────────────────────────

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

// ── Runners ──────────────────────────────────────────────────────────────
//
// Each RunX is a direct alias for its package's own Run(ctx, *agent.Agent,
// ...Option) error — none of them reimplement anything, they only save the
// caller a sub-package import for the common case. The agent is always
// already fully configured (thinking level, scheduler, tools, memory, …) by
// the time it's passed in — a runner's own Option type only ever configures
// the TRANSPORT itself (listen address, bot credentials, session
// overrides), never the agent (see each package's Options doc comment).
// All four block until ctx is cancelled (or the transport's own natural end,
// e.g. ACP's stdin closing) and return nil for that expected shutdown path.

// RunServer starts the HTTP/SSE server on top of an already-built agent and
// blocks until ctx is cancelled, performing a graceful shutdown before
// returning. See [server.Run].
var RunServer = server.Run

// ServerOption configures a [RunServer] call. See [server.Option].
type ServerOption = server.Option

// ServerWithAddr sets the listen address (default: "127.0.0.1:0" — loopback
// only, OS-assigned port). See [server.WithAddr].
var ServerWithAddr = server.WithAddr

// ServerWithLogger sets the Logger that receives request/lifecycle log
// lines. Default: [NilLogger] (silent). See [server.WithLogger].
var ServerWithLogger = server.WithLogger

// RunTelegram starts the Telegram bot transport on top of an already-built
// agent and blocks until ctx is cancelled. See [telegram.Run].
var RunTelegram = telegram.Run

// TelegramOption configures a [RunTelegram] call. See [telegram.Option].
type TelegramOption = telegram.Option

// TelegramWithToken sets the bot token (required). See [telegram.WithToken].
var TelegramWithToken = telegram.WithToken

// TelegramWithSessionModel overrides the model for sessions this transport
// creates. See [telegram.WithSessionModel].
var TelegramWithSessionModel = telegram.WithSessionModel

// TelegramWithSessionThinking overrides the thinking level for sessions
// this transport creates. See [telegram.WithSessionThinking].
var TelegramWithSessionThinking = telegram.WithSessionThinking

// TelegramWithAllowUnpair enables auto-pairing: any chat is accepted on
// first contact instead of requiring `harness telegram pair <chat_id>`
// first. See [telegram.WithAllowUnpair].
var TelegramWithAllowUnpair = telegram.WithAllowUnpair

// TelegramWithLogger sets the Logger this transport uses for its own log
// lines. Default: [NilLogger] (silent). See [telegram.WithLogger].
var TelegramWithLogger = telegram.WithLogger

// RunSlack starts the Slack bot transport on top of an already-built agent
// and blocks until ctx is cancelled. See [slack.Run].
var RunSlack = slack.Run

// SlackOption configures a [RunSlack] call. See [slack.Option].
type SlackOption = slack.Option

// SlackWithWorkspace sets the Slack workspace URL (required, unless already
// saved via `harness slack login`). See [slack.WithWorkspace].
var SlackWithWorkspace = slack.WithWorkspace

// SlackWithXoxC sets the xoxc- browser session API token (required, unless
// already saved). See [slack.WithXoxC].
var SlackWithXoxC = slack.WithXoxC

// SlackWithXoxD sets the xoxd- browser session cookie (required, unless
// already saved). See [slack.WithXoxD].
var SlackWithXoxD = slack.WithXoxD

// SlackWithSessionModel overrides the model for sessions this transport
// creates. See [slack.WithSessionModel].
var SlackWithSessionModel = slack.WithSessionModel

// SlackWithSessionThinking overrides the thinking level for sessions this
// transport creates. See [slack.WithSessionThinking].
var SlackWithSessionThinking = slack.WithSessionThinking

// SlackWithLogger sets the Logger this transport uses for its own log
// lines. Default: [NilLogger] (silent). See [slack.WithLogger].
var SlackWithLogger = slack.WithLogger

// RunAcp starts the Agent Client Protocol transport (for Zed and other ACP
// clients) on top of an already-built agent and blocks until ctx is
// cancelled or stdin closes. See [acp.Run]. Deliberately has no
// AcpWithLogger: this transport never logs anything itself (its whole job
// is pure JSON-RPC protocol translation over stdin/stdout), so there's
// nothing for a caller to configure — its in-process server always runs
// silently.
var RunAcp = acp.Run

// AcpOption configures a [RunAcp] call. See [acp.Option].
type AcpOption = acp.Option

// AcpWithStdin sets the stream ACP JSON-RPC requests are read from (default:
// os.Stdin). See [acp.WithStdin].
var AcpWithStdin = acp.WithStdin

// AcpWithStdout sets the stream ACP JSON-RPC responses/notifications are
// written to (default: os.Stdout). See [acp.WithStdout].
var AcpWithStdout = acp.WithStdout
