// Package harness is the public SDK entry point for embedding the harness agent.
//
// The agent is the SDK: create one with [NewAgent], open a session, subscribe
// to its events, and drive it with prompts. See the [agent] package for the
// full API (agent.Agent, agent.Session, agent.PromptOption, …) — this facade
// only wraps construction:
//
//	a := harness.NewAgent(
//	    harness.AgentWithThinking("medium"),
//	    harness.AgentWithMCPs(mcp.ServersFromSettings()),
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
// harness is layered: agent (core) → server (the HTTP/SSE API: sessions,
// events, commands) → transports (pure translation layers). Build ONE agent,
// wrap it in ONE server with [StartServer], and hand that same server to any
// number of transports — [RunTelegram], [RunSlack], [RunAcp] — and your own
// clients. Every session lives in exactly one place, and every frontend can
// watch and drive the same session at once. Transports never close the
// server or the agent; you do:
//
//	srv, err := harness.StartServer(a, "127.0.0.1:0", server.ServerOptions{})
//	if err != nil { log.Fatal(err) }
//	defer srv.Close() // also closes the agent
//	go harness.RunSlack(ctx, srv)              // registers the "slack" profile
//	err = harness.RunTelegram(ctx, srv, harness.TelegramWithCWD(dir))
//
// [RunServer] is the standalone blocking variant (serve an agent until ctx is
// cancelled, then close everything).
//
// Deeper building blocks live in their own public packages:
//   - agent            — Agent, Session, and the contracts you implement
//     (agent.ResourceLoader) or extend (agent.PromptOption)
//   - agent/store      — SessionStore + SessionMeta, the persistence port
//   - configstore      — SettingsStore + CredentialsStore, the persistence
//     ports for process-global configuration (see SetSettingsStore)
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
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
	"github.com/gurcuff91/harness/transports/acp"
	"github.com/gurcuff91/harness/transports/slack"
	"github.com/gurcuff91/harness/transports/telegram"
	"github.com/gurcuff91/harness/types"
)

// Logger is the structured logging contract RunServer/RunTelegram/RunSlack
// accept via their WithLogger option — implement it to route harness's
// backend logs anywhere (RunAcp has no WithLogger of its own: it never logs
// anything itself). See [logx.Logger].
type Logger = logx.Logger

// NewNilLogger returns a Logger that discards everything — the default
// every runner falls back to when no WithLogger is passed. See
// [logx.NewNilLogger].
var NewNilLogger = logx.NewNilLogger

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
//		harness.AgentWithMCPs(mcp.ServersFromSettings()),
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
// "high", "xhigh", or "max".
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

// AgentWithMCPs gives the agent these MCP servers, keyed by name: it spawns/
// connects them at construction (root agent only), registers their tools as
// mcp__<name>__<tool>, and terminates them on Agent.Close. Without it the
// agent has no MCP. Servers can come from anywhere — harness's own settings
// (what `harness mcp add` writes), code, or both merged:
//
//	servers := mcp.ServersFromSettings()
//	servers["docs"] = types.MCPServer{URL: "https://example.com/mcp"}
//	a := harness.NewAgent(harness.AgentWithMCPs(servers))
//
// An invalid or unreachable server never fails the agent: it's skipped and
// reported via Agent.MCPStatuses().
func AgentWithMCPs(servers map[string]types.MCPServer) AgentOption {
	return func(o *agent.AgentOptions) { o.MCPServers = servers }
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

// AgentWithMemory gives the agent project-scoped persistent memory backed by
// s and registers the Memo* tools. Without it the agent has no memory. The
// agent owns s: Agent.Close closes it. Implement [agent/memory.Store] for a
// custom backend, or open the default SQLite one explicitly — the caller
// decides what an open failure means:
//
//	mem, err := memory.OpenSQLite("") // "" = ~/.harness/agent/memory.db
//	if err != nil { ... }
//	a := harness.NewAgent(harness.AgentWithMemory(mem))
func AgentWithMemory(s memory.Store) AgentOption {
	return func(o *agent.AgentOptions) { o.Memory = s }
}

// AgentWithScheduler enables cron-scheduled prompts: the agent registers
// the Schedule*/ScheduleList/ScheduleDelete management tools AND runs the
// engine that fires due schedules — both travel together. Each schedule
// records the id of the session that created it; when due, the engine
// routes the prompt back to that session ONLY IF it's already active in
// THIS instance (otherwise the prompt is dropped, not resurrected from
// disk — see agent.fireScheduledPrompt). Only one agent WITHIN A GIVEN
// PROCESS should enable this, so prompts don't fire twice from the same
// process — but several SEPARATE harness processes, each with its own
// agent enabling this over the same shared settings store, is a safe,
// intended deployment shape: each engine only ever fires into the
// sessions it itself has active (see agent/schedule.Engine's doc
// comment). Off by default.
func AgentWithScheduler() AgentOption {
	return func(o *agent.AgentOptions) { o.EnableScheduler = true }
}

// AgentWithColleagues enables the ColleagueList/ColleagueAsk tools: the agent
// can discover OTHER running harness server instances on this machine (any
// process that called Serve — see the client package) via the shared
// colleague registry (kept in the settings store), and delegate a prompt to one of them by
// name over HTTP. Each colleague answers using its OWN model, MCPs, and
// project context, not the caller's — real delegation, not talking to itself.
// Meant for long-running processes (a served agent, a transport); one-shot
// callers have nothing to offer a colleague and no time to wait for one. Off
// by default.
func AgentWithColleagues() AgentOption {
	return func(o *agent.AgentOptions) { o.EnableColleagues = true }
}

// AgentWithWebSearch enables the built-in WebSearch tool. The tool owns its
// own HTTP client and uses the active minimax provider's API key at call
// time — if the provider isn't connected, the tool returns a single
// actionable error instead of failing to register, so it's safe to enable
// unconditionally without a pre-flight provider check. Off by default.
func AgentWithWebSearch() AgentOption {
	return func(o *agent.AgentOptions) { o.EnableWebSearch = true }
}

// ── Custom providers ─────────────────────────────────────────────────────
//
// Registers a custom OpenAI Chat Completions-compatible provider globally
// for this process — usable by any Agent built afterward via "name/model".
// Call once, early (typically in main(), before constructing any Agent).
// See agent.NewOpenAIProvider's doc comment for the full contract
// (why apiKey is memory-only, how this differs from the declarative
// `harness provider add` / settings.json path, registration-ordering
// requirements).

// NewOpenAIProvider registers a custom OpenAI-compatible provider. See
// [agent.NewOpenAIProvider].
var NewOpenAIProvider = agent.NewOpenAIProvider

// CustomProviderOption configures a NewOpenAIProvider call. See
// [agent.CustomProviderOption].
type CustomProviderOption = agent.CustomProviderOption

// ProviderWithDisplay sets the human-friendly display name. See
// [agent.ProviderWithDisplay].
var ProviderWithDisplay = agent.ProviderWithDisplay

// ProviderWithHeaders sets extra HTTP headers sent on every request. See
// [agent.ProviderWithHeaders].
var ProviderWithHeaders = agent.ProviderWithHeaders

// ProviderWithFetchModels overrides model discovery entirely. See
// [agent.ProviderWithFetchModels].
var ProviderWithFetchModels = agent.ProviderWithFetchModels

// ProviderWithReasoningSplit sends "reasoning_split": true on every
// request — needed for a custom provider fronting a MiniMax-compatible
// backend. See [agent.ProviderWithReasoningSplit].
var ProviderWithReasoningSplit = agent.ProviderWithReasoningSplit

// ── Configuration stores ─────────────────────────────────────────────────
//
// Where harness persists its process-global configuration. Same
// register-early contract as NewOpenAIProvider: call once in main(), before
// constructing any Agent. Without them, harness uses configstore.FileStore
// over ~/.harness/settings.json and ~/.harness/credentials.json.

// SetSettingsStore registers the store for non-sensitive settings. See
// [agent.SetSettingsStore].
var SetSettingsStore = agent.SetSettingsStore

// SetCredentialsStore registers the store for secrets (API keys, OAuth
// tokens). See [agent.SetCredentialsStore].
var SetCredentialsStore = agent.SetCredentialsStore

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

// StartServer binds and serves an agent in the background, returning the
// running server — the one to share with every transport. See [server.Start].
var StartServer = server.Start

// ServerOption configures a [RunServer] call. See [server.Option].
type ServerOption = server.Option

// ServerWithAddr sets the listen address (default: "127.0.0.1:0" — loopback
// only, OS-assigned port). See [server.WithAddr].
var ServerWithAddr = server.WithAddr

// ServerWithLogger sets the Logger that receives request/lifecycle log
// lines. Default: [NewNilLogger] (silent). See [server.WithLogger].
var ServerWithLogger = server.WithLogger

// RunTelegram runs the Telegram bot on a running (possibly shared) server and
// blocks until ctx is cancelled. See [telegram.Run].
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

// TelegramWithCWD sets the working directory the bot's chat sessions are
// created in and bound under (default: the process's cwd). See
// [telegram.WithCWD].
var TelegramWithCWD = telegram.WithCWD

// TelegramWithKeepSessionsOnStop leaves the bot's sessions open when it
// stops. See [telegram.WithKeepSessionsOnStop].
var TelegramWithKeepSessionsOnStop = telegram.WithKeepSessionsOnStop

// TelegramWithLogger sets the Logger this transport uses for its own log
// lines. Default: [NewNilLogger] (silent). See [telegram.WithLogger].
var TelegramWithLogger = telegram.WithLogger

// RunSlack runs the Slack bot on a running (possibly shared) server and blocks
// until ctx is cancelled. See [slack.Run].
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

// SlackWithCWD sets the working directory the bot's channel sessions are
// created in and bound under (default: the process's cwd). See
// [slack.WithCWD].
var SlackWithCWD = slack.WithCWD

// SlackWithKeepSessionsOnStop leaves the bot's sessions open when it stops.
// See [slack.WithKeepSessionsOnStop].
var SlackWithKeepSessionsOnStop = slack.WithKeepSessionsOnStop

// SlackWithLogger sets the Logger this transport uses for its own log
// lines. Default: [NewNilLogger] (silent). See [slack.WithLogger].
var SlackWithLogger = slack.WithLogger

// RunAcp runs the Agent Client Protocol bridge (Zed and other ACP clients) on
// a running (possibly shared) server, over stdin/stdout, and blocks until ctx
// is cancelled or stdin closes. See [acp.Run]. Deliberately has no
// AcpWithLogger: this transport never logs anything itself.
var RunAcp = acp.Run

// AcpOption configures a [RunAcp] call. See [acp.Option].
type AcpOption = acp.Option

// AcpWithStdin sets the stream ACP JSON-RPC requests are read from (default:
// os.Stdin). See [acp.WithStdin].
var AcpWithStdin = acp.WithStdin

// AcpWithStdout sets the stream ACP JSON-RPC responses/notifications are
// written to (default: os.Stdout). See [acp.WithStdout].
var AcpWithStdout = acp.WithStdout
