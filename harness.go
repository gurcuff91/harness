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
// events, commands) → frontends (pure translation layers). Build ONE agent,
// wrap it in ONE server with [StartServer], and hand that same server to any
// number of frontends — [RunAcp] — and your own
// clients. Every session lives in exactly one place, and every frontend can
// watch and drive the same session at once. Frontends never close the
// server or the agent; you do:
//
//	srv, err := harness.StartServer(a, "127.0.0.1:0", server.ServerOptions{})
//	if err != nil { log.Fatal(err) }
//	defer srv.Close() // also closes the agent
//	err = harness.RunAcp(ctx, srv)
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
//   - server           — the HTTP/SSE backend every frontend (including this
//     facade's RunServer) runs on top of
//   - acp              — the runner RunAcp wraps, and its Option type
//   - channels/slack, channels/telegram, channels/colleague — the input
//     channels (see [AgentWithChannels]; a session listens to one with
//     agent.SessionWithChannels; registering the colleague channel makes
//     every session a colleague)
//   - client           — typed HTTP/SSE client for a running harness server
//   - types            — Event, Message, ModelMeta and other shared types
//
// Everything under internal/ (providers, config, the TUI, build version) is
// implementation detail and not part of the SDK's compatibility surface.
package harness

import (
	"github.com/gurcuff91/harness/acp"
	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/profiles"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/rpc"
	"github.com/gurcuff91/harness/server"
	"github.com/gurcuff91/harness/types"
)

// Logger is the structured logging contract RunServer and the channels
// accept via their WithLogger option — implement it to route harness's
// backend logs anywhere (RunAcp has no WithLogger of its own: it never logs
// anything itself). See [logx.Logger].
type Logger = logx.Logger

// NewNilLogger returns a Logger that discards everything — the default
// every runner falls back to when no WithLogger is passed. See
// [logx.NewNilLogger].
var NewNilLogger = logx.NewNilLogger

// Client is a typed HTTP/SSE client for a running harness server (`harness
// serve`, or the in-process server any frontend starts). See [client.Client].
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
// caller — typically a frontend — can teach the agent capabilities specific
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

// AgentWithResourceLoader sets the loader that discovers SYSTEM.md, AGENTS.md
// and skills for each session's working directory. The default is to discover
// NOTHING; pass resources.File() to read them from disk (what the harness CLI
// does), or implement [agent/resources.ResourceLoader] — one method,
// Load(cwd), no state, shared by every session — to load them from anywhere.
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

// AgentWithChannels registers external-conversation implementations (e.g.
// channels/slack.New(...)) that sessions can be bound to with
// agent.SessionWithChannels. Registering opens nothing: a channel listens only
// for the sessions bound to it, and a conversation is listened to by at most
// one session at a time, across processes.
func AgentWithChannels(chs ...agent.Channel) AgentOption {
	return func(o *agent.AgentOptions) { o.Channels = append(o.Channels, chs...) }
}

// AgentWithProfiles registers named bundles of per-session directives and
// tools (see agent/profiles). A session is bound to profiles by name with
// agent.SessionWithEnabledProfiles; more can be registered or removed at any time
// with Agent.RegisterProfile / UnregisterProfile.
func AgentWithProfiles(ps map[string]profiles.Profile) AgentOption {
	return func(o *agent.AgentOptions) {
		if o.Profiles == nil {
			o.Profiles = map[string]profiles.Profile{}
		}
		for n, p := range ps {
			o.Profiles[n] = p
		}
	}
}

// AgentWithWebSearch enables the built-in WebSearch tool. The tool owns its
// own HTTP client and uses the active minimax provider's API key at call
// time — if the provider isn't connected, the tool returns a single
// actionable error instead of failing to register, so it's safe to enable
// unconditionally without a pre-flight provider check. Off by default.
func AgentWithWebSearch() AgentOption {
	return func(o *agent.AgentOptions) { o.EnableWebSearch = true }
}

// AgentWithTerminal names the terminal the agent's sessions run in, reported
// by the SessionInfo tool (e.g. "Ghostty"). Leave it unset for an agent with
// no terminal of its own (a server, a bot).
func AgentWithTerminal(name string) AgentOption {
	return func(o *agent.AgentOptions) { o.Terminal = name }
}

// ── Admin ────────────────────────────────────────────────────────────────

// NewAdmin builds the admin layer (stored sessions, providers, settings, MCP
// config, memory, schedules). See [admin.New].
var NewAdmin = admin.New

// AdminOption configures NewAdmin.
type AdminOption = admin.Option

// AdminWithSessionStore sets where stored sessions live. See [admin.WithSessionStore].
var AdminWithSessionStore = admin.WithSessionStore

// AdminWithMemory sets the memory store. See [admin.WithMemory].
var AdminWithMemory = admin.WithMemory

// AdminWithActiveSessions tells admin which sessions are open in this process.
// See [admin.WithActiveSessions].
var AdminWithActiveSessions = admin.WithActiveSessions

// ── Custom providers ─────────────────────────────────────────────────────
//
// Registers a custom OpenAI Chat Completions-compatible provider globally
// for this process — usable by any Agent built afterward via "name/model".
// May be called at any time, and paired with UnregisterCustomProvider.
// See admin.RegisterOpenAI's doc comment for the full contract
// (why apiKey is memory-only, how this differs from the declarative
// `harness provider add` / settings.json path, registration-ordering
// requirements).

// NewOpenAIProvider registers a custom OpenAI-compatible provider. See
// [admin.RegisterOpenAI].
var NewOpenAIProvider = admin.RegisterOpenAI

// NewAnthropicProvider registers a custom Anthropic Messages-compatible
// provider — Anthropic itself, a gateway, or another harness instance's
// provider proxy. See [admin.RegisterAnthropic].
var NewAnthropicProvider = admin.RegisterAnthropic

// UnregisterCustomProvider removes a provider registered with
// NewOpenAIProvider or NewAnthropicProvider, so the name can be registered again in the same process.
// See [admin.UnregisterProvider].
var UnregisterCustomProvider = admin.UnregisterProvider

// ReloadCustomProviders applies settings.json's "provider" collection live.
// See [admin.ReloadCustomProviders].
var ReloadCustomProviders = admin.ReloadCustomProviders

// ErrProviderNameConflict: a settings.json entry was skipped because a
// programmatic provider uses its name. See [admin.ErrProviderNameConflict].
var ErrProviderNameConflict = admin.ErrProviderNameConflict

// ErrProviderNotFound: no provider is registered under that name. See
// [admin.ErrProviderNotFound].
var ErrProviderNotFound = admin.ErrProviderNotFound

// ErrProviderNotRemovable: built-in or settings.json-declared provider. See
// [admin.ErrProviderNotRemovable].
var ErrProviderNotRemovable = admin.ErrProviderNotRemovable

// CustomProviderOption configures a NewOpenAIProvider call. See
// [admin.CustomProviderOption].
type CustomProviderOption = admin.CustomProviderOption

// ProviderWithDisplay sets the human-friendly display name. See
// [admin.ProviderWithDisplay].
var ProviderWithDisplay = admin.ProviderWithDisplay

// ProviderWithHeaders sets extra HTTP headers sent on every request. See
// [admin.ProviderWithHeaders].
var ProviderWithHeaders = admin.ProviderWithHeaders

// ProviderWithFetchModels overrides model discovery entirely. See
// [admin.ProviderWithFetchModels].
var ProviderWithFetchModels = admin.ProviderWithFetchModels

// ProviderWithReasoningSplit sends "reasoning_split": true on every
// request — needed for a custom provider fronting a MiniMax-compatible
// backend. See [admin.ProviderWithReasoningSplit].
var ProviderWithReasoningSplit = admin.ProviderWithReasoningSplit

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
// the FRONTEND itself (listen address, stdin/stdout, session
// overrides), never the agent (see each package's Options doc comment).
// All four block until ctx is cancelled (or the frontend's own natural end,
// e.g. ACP's stdin closing) and return nil for that expected shutdown path.

// RunServer starts the HTTP/SSE server on top of an already-built agent and
// blocks until ctx is cancelled, performing a graceful shutdown before
// returning. See [server.Run].
var RunServer = server.Run

// StartServer binds and serves an agent in the background, returning the
// running server — the one to share with every frontend. See [server.Start].
var StartServer = server.Start

// ServerOption configures a [RunServer] call. See [server.Option].
type ServerOption = server.Option

// ServerWithAddr sets the listen address (default: "127.0.0.1:0" — loopback
// only, OS-assigned port). See [server.WithAddr].
var ServerWithAddr = server.WithAddr

// ServerWithLogger sets the Logger that receives request/lifecycle log
// lines. Default: [NewNilLogger] (silent). See [server.WithLogger].
var ServerWithLogger = server.WithLogger

// RunAcp runs the Agent Client Protocol bridge (Zed and other ACP clients) on
// a running (possibly shared) server, over stdin/stdout, and blocks until ctx
// is cancelled or stdin closes. See [acp.Run]. Deliberately has no
// AcpWithLogger: it never logs anything itself.
var RunAcp = acp.Run

// RunRpc runs the Harness JSON-RPC protocol over stdin/stdout, the way SDKs in
// other languages drive an agent. Unlike every other runner it takes no agent:
// the client builds it with the protocol's `initialize` request. Blocks until
// ctx is cancelled or stdin closes. See [rpc.Run] and the protocol's schema.
var RunRpc = rpc.Run

// RpcOption configures a [RunRpc] call. See [rpc.Option].
type RpcOption = rpc.Option

// RpcWithStdin sets the stream protocol requests are read from (default:
// os.Stdin). See [rpc.WithStdin].
var RpcWithStdin = rpc.WithStdin

// RpcWithStdout sets the stream protocol messages are written to (default:
// os.Stdout). See [rpc.WithStdout].
var RpcWithStdout = rpc.WithStdout

// RpcWithLogger sets the Logger that receives diagnostics (default: silent).
// See [rpc.WithLogger].
var RpcWithLogger = rpc.WithLogger

// AcpOption configures a [RunAcp] call. See [acp.Option].
type AcpOption = acp.Option

// AcpWithStdin sets the stream ACP JSON-RPC requests are read from (default:
// os.Stdin). See [acp.WithStdin].
var AcpWithStdin = acp.WithStdin

// AcpWithStdout sets the stream ACP JSON-RPC responses/notifications are
// written to (default: os.Stdout). See [acp.WithStdout].
var AcpWithStdout = acp.WithStdout
