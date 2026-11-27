# AGENTS.md — AI Agent Guide for Harness

> Instructions for AI coding agents working on this codebase.

## Project Identity

- **What:** Minimal AI agent harness — a CLI tool that connects LLMs to tools via a ReAct loop
- **Language:** Go 1.24+
- **Module:** `github.com/gurcuff91/harness`
- **Binary:** Single binary, ~9MB — entry point in `cmd/harness/main.go` (module root free for an SDK facade)
- **Version:** single source of truth in package `version` (`version.Version`), injected via ldflags from the `Makefile` (`VERSION=`); falls back to `"dev"` for a plain `go build`.
- **Dependencies (direct):** `golang.org/x/term` (raw mode), `golang.org/x/sys` (macOS process start time for channel claims; was already indirect via x/term), `github.com/rivo/uniseg` (grapheme/width), `github.com/go-chi/chi/v5` (HTTP router), `modernc.org/sqlite` (pure-Go SQLite — `agent/memory`'s store AND `agent/store.FileStore`'s per-session `SearchMessages` FTS5 index), `golang.design/x/clipboard` (clipboard image paste in the TUI), `github.com/google/uuid` (IDs), `github.com/robfig/cron/v3` (schedule parsing), `github.com/alecthomas/kong` (CLI grammar/parsing — `internal/cli/kong.go`), `github.com/gorilla/websocket` (the Slack input channel's RTM connection). Keep the set minimal — no new deps without approval.

## Golden Rules

1. **No new dependencies** without explicit owner approval. Solve problems with stdlib first.
2. **Always streaming.** There is no non-streaming path. Every provider implements `CompleteStream()`. Never add `Complete()`.
3. **`provider/model` format everywhere.** Settings, env vars, CLI display, Resolve — all use `provider/model` (e.g., `anthropic/claude-sonnet-4-20250514`).
4. **Backend/frontend separation.** `agent/` and `internal/providers/` never import `server`, `acp` or `channels/`. The agent emits events over an HTTP/SSE API (`server`); the clients (`internal/cli`, `internal/tui`, `acp`) consume it.
5. **Persistent state is explicit.** No model caching (provider model lists are fetched per process). On-disk state is limited to `~/.harness/{credentials.json, settings.json}` and `~/.harness/agent/{sessions/, memory.db}` — plus one approved CACHE, `~/.harness/cache/model-registry.json`: the last good OpenRouter catalog, a fallback only (never the primary source), deletable at any time (see `internal/providers/llm/registry_fetch.go`). Every piece of process-global state (schedules, Telegram pairing, the Slack login, input-channel claims, sessions and secrets) goes through the `configstore` ports via a typed `internal/config` manager method — never a new ad-hoc JSON file. Secrets go to `CredentialsStore`, everything else to `SettingsStore`, one namespace per domain; composite keys use `entryKey(parts...)` (truncated SHA-256, never parsed back — identity lives in the value).
6. **SDK boundary.** Public packages (`agent`, `agent/{tools,store,resources,memory}`, `configstore`, `mcp`, `types`) form the SDK. Keep implementation detail (`providers`, `config`, `version`) under `internal/`, and never expose an `internal/…` type in a public signature.

## Architecture

The **agent is the SDK**. Public packages form the embeddable surface — this now
includes `server`, `acp` and `channels/{slack,telegram,colleague}`, so an SDK consumer can
programmatically run any of those frontends on top of an embedded agent, not
just the core ReAct loop. Everything under `internal/` is implementation detail
the Go compiler forbids third parties from importing. A thin `harness.go`
facade at the root re-exports the essentials.

```
harness.go                      ← 🔓 SDK facade (package harness): NewAgent/AgentWith* (agent construction), RunServer/RunAcp + their XxxWith* (frontend runners — thin aliases over server.Run/acp.Run), Client/NewClient — nothing else (no type aliases beyond these; use agent.Agent, agent.Session, types.Event, etc. directly)
cmd/harness/main.go             ← executable entry point (package main) — just calls cli.Main(os.Args)

🔓 PUBLIC (the SDK surface)
├── agent/                      ← core ReAct loop — the SDK
│   ├── agent.go                ← Chat() loop, tool execution, MCP + memory wiring, Close()
│   ├── session.go              ← session lifecycle, history, tool pairing
│   ├── prompts.go              ← system prompt assembly
│   ├── store/                  ← session persistence (JSONL per cwd) — custom stores here. Optional `WindowReader` capability (window.go): a suffix of the log (`?from=` / `?compactions=k` of GET /messages) read without unmarshalling the skipped part — FileStore/InMemoryStore implement it, a custom store falls back to load-all-and-cut; `SessionStore` itself stays unchanged. SessionStore also owns full-text SearchMessages(sessionID, query, limit) ([]SearchResult, error) — FileStore backs it with a per-session SQLite FTS5 index (file.go), InMemoryStore returns ErrSearchNotSupported; a custom port is free to implement it however it wants (or not at all)
│   ├── resources/              ← skill/resource discovery — custom loaders here
│   ├── memory/                 ← persistent memory port (Store: Write/Get/Search/Delete/Close, cwd + global scoping contract) + default SQLiteStore (OpenSQLite, SQLite + FTS5). Per-agent like store/resources: AgentOptions.Memory / harness.AgentWithMemory(store); nil = no memory; the agent owns and closes it
│   └── tools/                  ← built-in tools — custom tools here (package tools)
│       ├── registry.go / bash.go / file.go / edit.go / fetch.go
│       ├── skill.go / memory.go / session.go / websearch.go / truncate.go / names.go
├── configstore/                ← persistence ports for process-global config: SettingsStore (non-sensitive) + CredentialsStore (secrets) — two deliberately distinct types with the same dumb namespace/key/JSON-value API (Get/Set/Delete/List/SwapValue/Close). Same split as agent/store: the store is a dumb CRUD, internal/config's managers own every typed method and domain rule. Implementations own their atomicity (SwapValue = atomic read-modify-write per key, also across processes). FileStore (default, ~/.harness/{settings,credentials}.json, layout {"<ns>":{"<key>":<json>}}) and InMemoryStore; register custom ones process-globally via harness.SetSettingsStore/SetCredentialsStore before building any Agent. A new kind of setting extends a manager, never these interfaces. **`configstore/storetest.RunContract(t, newStore)` is the bar ANY implementation must pass** (FileStore, InMemoryStore, and a store served by an RPC client): its `Cases` are the behavior (copies, ErrInvalidValue, atomic SwapValue, nil-next delete…), driven through a small `TB` so a test can prove the suite catches a broken store; a new case goes THERE, never in one implementation's tests. **Credential reads tell failure from absence:** `CredentialsManager.Credential/APIKey/OAuth` return `(value, found, err)` — not found is `(zero,false,nil)`, a failing store is `err`; provider code must never read an error as "not connected" (`resolveAPIKeyErr`; an already-resolved provider keeps its last known state, a never-resolved one returns the read error). Other readers still return zero values on failure.
│   ├── store.go / file.go / memory.go
├── mcp/                        ← Model Context Protocol client (stdlib). Manager.Start(ctx, servers) connects exactly the servers it is given — it never reads config itself; ServersFromSettings() is the explicit settings source. Per-agent: AgentOptions.MCPServers / harness.AgentWithMCPs(servers); empty = no MCP; Agent.Close terminates them; MCPStatuses() exposes per-server status. Two DISTINCT per-server timeouts (types.MCPServer): `timeout` = ONE tool call (default 5 min, enforced in wrapTool for BOTH transports; the HTTP client's own timeout is the call timeout + a margin, a backstop that must never fire first) and `connection_timeout` = connect only (initialize + tools/list, default 30 s). Never let the connect timeout become a per-call limit again — that was a real bug (every remote tool slower than 5 s was cut off), and it is also why `timeout` was RENAMED: until 0.101.3 `timeout` meant connecting and `call_timeout` meant the call. `timeout` now means the CALL (the one an MCP author most wants to configure) and `connection_timeout` means connecting. There is NO alias for the old `call_timeout` key: it is ignored, not read as the new `timeout`.
│   ├── jsonrpc.go / stdio.go / http.go / client.go / manager.go
├── client/                     ← the ONE typed HTTP/SSE SDK over server's API — every frontend uses *client.Client directly (no per-frontend wrappers)
│   ├── client.go / types.go / event.go / error.go / stream.go
├── server/                     ← HTTP/SSE backend — the API all clients talk to. Run(ctx, *agent.Agent, ...Option) is the blocking convenience wrapper (listen → serve → wait for ctx → graceful shutdown); Server/NewServer/Serve/Close stay exported for fine-grained control. WithLogger(logx.Logger) sets what receives request/lifecycle log lines — default logx.NewNilLogger() (silent). Memory CRUD: GET /api/memories (list/search) + GET/PUT/DELETE /api/memories/{slug}?cwd=<path|<global>> over admin.Memory (Agent.Admin()) (503 when the agent has none; slug is URL-unescaped).
│   ├── server.go / sse.go / proxy.go / middleware.go / server_docs.go / run.go
│   │                           ← provider_proxy.go: TRANSPARENT proxy `ANY /api/providers/{name}/proxy/<path>` to a provider's NATIVE API (Anthropic Messages / OpenAI Chat Completions / Codex Responses / Ollama) — body and response (SSE flushed live) pass through verbatim; harness supplies credentials (OAuth refresh) + identity headers and drops the caller's. Driven by `providers.Provider.Upstream()` (internal/providers/upstream.go: base URL, resolved headers, allowlist, optional rewrite) — every provider implements it, built from the same header builders as CompleteStream. Only two kinds of rewrite exist: claude-oauth (identity block, tool-name mapping both ways, billing header) and `injectReasoningSplit` (adds `"reasoning_split":true` to a chat completion — minimax always, a custom provider only when declared with reasoning_split — unless the caller set it; a textual splice, never a re-encode). A provider whose session path sends a request flag a proxy client can't be expected to know MUST get the same flag through its `Upstream().Rewrite`. Allowlists (`allowList`) take `"METHOD /path"`, `/*` (any suffix) and `/:id` (exactly one segment — what an SDK's `models.retrieve` calls); an encoded slash is accepted by `CleanProxyPath` only as a model id (`…/models/<id>`). Keep the proxy compatible with the OFFICIAL SDKs (Anthropic + OpenAI, Python/Node/Go): any route an SDK calls on the surface the proxy serves must be allowed. Client side: `client.ProviderBaseURL` / `ProviderDo` — no typed completions.
│   │                           ← The model registry: `GET /api/models[?provider=]`, `GET /api/models/{provider}/{id}`, `GET /api/providers` — all built on `admin.Providers.ListModels(provider)` (per-provider models + the FetchModels error; `admin.Providers.Models` is it flattened). `types.ModelMeta.Sources` is the provenance of every datum (provider > catalog > registry > inferred > default; pricing can also be `unknown`, which is what makes a 0 price readable) — set by `llm.EnrichMeta`/`ApplyRegistryPricing` and by providers that report fields natively (anthropic, ollama-cloud). A provider whose own fetch reports a field MUST mark it `provider`; never leave a defaulted field unmarked. `Provider.Dialect()` feeds `dialect`/`proxy_path`.
├── channels/
│   └── slack/                  ← agent.Channel for Slack (input channels, see "Input channels" below) — EVERYTHING Slack in harness since transports/slack was removed (v0.108.0): its own copy of the Slack client, the tag format (`<slack user= channel=>…</slack>`, `<slack-attach name= path=|skipped= [image="true"]/>` — an image is BOTH a prompt image and a saved file —, `<slack-upload path=/>`), the file handling and the four Slack* tools (no SlackAsk). New(workspace, xoxc, xoxd) takes credentials explicitly; it owns the login too (LoadCredentials/SaveCredentials/DeleteCredentials/DeriveXoxC/VerifyAndSave over the same stored keys) and `harness slack login` uses it. Nothing may import the removed transports/slack again — pinned by channels/slack/independence_test.go
├── acp/                        ← Agent Client Protocol agent (agentclientprotocol.com) — JSON-RPC/stdio bridge for Zed and other ACP clients; one session per ACP sessionId, pure protocol translation (never touches agent/ or agent/tools/). Run(ctx, srv, ...Option) on a server it's given — WithStdin/WithStdout, defaulting to os.Stdin/os.Stdout. Deliberately no WithLogger: it never logs anything itself. (It lived in `transports/acp` until v0.119.0; `transports/` is gone — a test keeps it gone.)
├── logx/                       ← logging, ONE package: the Logger CONTRACT (interface) server.Run and the channels accept via WithLogger, NewNilLogger (the silent default every one of them falls back to) and NewHarnessLogger (harness's line format `LEVEL [component] event k=v` through the standard log package — what the CLI passes; public since v0.119.0, there is no internal/logx). Implement Logger to route the logs anywhere else.
└── types/                      ← Event, Message, ModelMeta — shared types

🔒 INTERNAL (compiler-enforced, not importable by third parties)
└── internal/
    ├── providers/              ← LLM provider layer (Resolve, streaming)
    │   ├── provider.go / anthropic.go / claude_oauth.go / codex_oauth.go / openai.go
    │   ├── ollama*.go / opencode_go.go / minimax.go / registry.go / status.go
    │   └── llm/                ← core LLM types, metadata cascade, model registry
    ├── oauthflow/              ← native OAuth PKCE login flows — a SERVER-SIDE implementation detail: the only caller is server/oauth.go's handler behind POST /api/oauth/{provider}. OauthFlow interface (Start returns authURL+verifierCode, stateless; Exchange takes code+verifierCode back) + shared PKCE/token-POST + For(provider) dispatch in oauthflow.go; one file per provider (claude.go, codex.go), each implementing OauthFlow. Add a provider = one new file + one For() case, no call-site changes. Neither the CLI nor the TUI import this package — both drive OAuth through client.Client's StartOAuth/ExchangeOAuth instead (see server/oauth.go and internal/browseropen below).
    ├── browseropen/            ← the one function (Open) that opens a URL in the user's default browser — used by the CLI and TUI after StartOAuth returns an auth_url. The server NEVER opens a browser itself; that's exclusively a client-side concern.
    ├── config/                 ← typed settings + credentials managers over the configstore ports (validation, typed accessors, UpdateCredential → store.SwapValue); manager.go holds the process-global store registry + GetSettingsManager/GetCredentialsManager
    │   ├── settings.go / credentials.go / manager.go / codec.go
    │   ├── settings_{schedules,channels,slack}.go / credentials_transports.go ← domain namespaces (schedules, channels, slack*; the Telegram bot token is a credential); testhooks.go: SwapStoresForTest
    ├── filelock/               ← the cross-process lock used only by configstore.FileStore: the KERNEL's (flock on Unix, LockFileEx on Windows) on `<file>.flock`, a file never deleted — released by the OS when the holder dies (SIGKILL included), waiters block instead of polling (no starvation by a burst writer), wait up to 2 min (an OAuth refresh holds it up to ~30 s). It ALSO takes the legacy O_EXCL `<file>.lock` so pre-0.114.0 binaries sharing the home stay excluded; a `.lock` with the `flock:` token is a provable orphan and removed at once, an old binary's is reclaimed after 45 s as they do. Platforms without flock in x/sys (Solaris, AIX…) fall back to the legacy lock alone. Drop the legacy layer once no old binary can share a home (schedules, channel claims and the Telegram state have no files of their own — they live in the stores)
    ├── version/                ← build version (ldflags target)
    ├── tui/                    ← pure-Go terminal UI (zero external TUI libs) — the one interactive frontend that stays INTERNAL: unlike acp, it's a terminal frontend tied to this binary, not something an SDK consumer embeds programmatically
    └── cli/                    ← CLI app: Kong grammar (kong.go) + kong_run*.go Run() methods, agent.go builders
```

`server`, `acp` and `channels/*` sit at the module root (not
under `internal/`) even though they still freely import `internal/config`,
`internal/providers`, `internal/version` — moving a package out of
`internal/` doesn't change what it's allowed to import (Go's `internal/`
rule only blocks OTHER modules, never siblings in the same module), and
`agent/` already does exactly this (imports `internal/config` and
`internal/providers` directly). Exposing `server` doesn't add a new kind of
exposure: it's an HTTP interface over the same global config/provider
machinery `agent.New`/`harness.NewAgent` already depend on. Logging is the
one thing worth calling out: `server` and the channels never pick a logger
themselves — they take a `logx.Logger` through `WithLogger` and default to
`logx.NewNilLogger()`. The CLI is what passes `logx.NewHarnessLogger()`;
everyone else (an SDK consumer, the in-process server of the TUI or acp) stays
silent unless it passes one.

> **internal/ rule:** its parent is the module root, so *all* harness code can
> import `internal/…`, but external modules cannot. This lets `agent`, `server`,
> `acp` and `channels/*` use providers/config/version freely while keeping them out
> of the SDK's own exported signatures.
> **Corollary:** no public package (agent, tools, mcp, memory, types, server,
> acp, channels/*, …) may expose an `internal/…` type in an exported signature.

## Key Interfaces

### Provider (`internal/providers/provider.go`)

Every LLM provider implements this. No exceptions, no optional methods:

```go
type Provider interface {
    CompleteStream(ctx context.Context, req *Request, cb StreamCallback) (*Response, error)
    FormatUserMessage(text string) json.RawMessage
    FormatUserMessageWithImages(text string, images []ImageData) json.RawMessage
    FormatToolResults(results []ToolResult) []json.RawMessage
    Model() string
}
```

### Agent Events (`types/event.go`)

The agent emits events — the frontend subscribes. This is the ONLY coupling.
`agent/event.go` only holds aliases (`Event`, `Handler`, `EventType`) to `types.*`;
the canonical `EventType` list lives in `types/event.go`:

```
EventTurnStart              → User submitted input, agent working
EventTurnEnd                → Agent done, ready for next input
EventLoopStart              → ReAct iteration starting
EventLoopEnd                → ReAct iteration finished
EventStreamTextDelta        → Response text fragment (stream)
EventStreamTextEnd          → Response text finished
EventStreamThinkingDelta    → Thinking text fragment (stream)
EventStreamThinkingEnd      → Thinking block finished
EventToolStart              → Tool announced (name + ID known, args not yet — spinner)
EventToolArgsDelta          → Tool arguments arriving in fragments
EventToolCall               → Args complete, tool about to execute
EventToolResult             → Tool finished (output + duration + IsError)
EventTokens                 → Token usage update
EventError                  → Something broke
EventMaxIterationsReached   → Iteration budget exhausted (LLM summarized progress)
EventFollowUpStart          → Queued follow-up prompt about to process
EventReceivedPrompt         → Immediate (non-queued) prompt received
EventCompactStart           → Session compaction started
EventCompactEnd             → Session compaction finished (Summary set)
EventStop                   → Turn stopped by user (not an error)
```

> `ToolID` correlates `ToolStart → ToolArgsDelta → ToolCall → ToolResult`.

### Tool (`tools/registry.go`)

```go
type Tool struct {
    Def     llm.ToolDef                              // JSON schema
    Execute func(input json.RawMessage) (string, error)
}
```

## Data Flow

```
User Input
    ↓
session.Prompt(ctx, text, opts…)   → queues into followUps (returns PromptStatus)
    ↓  drainFollowUps() processes the queue serially → promptSync()
    ↓  emit(EventTurnStart)
    ↓
┌── ReAct Loop (default max 50 iterations, 1 reserved) ─┐
│   emit(EventLoopStart)                                 │
│       ↓                                                │
│   runStream() → provider.CompleteStream(req, callback)  │
│       ↓ callback fires events:                         │
│       ├── EventStreamThinkingDelta / ThinkingEnd        │
│       ├── EventToolStart → EventToolArgsDelta           │
│       └── EventStreamTextDelta / TextEnd                │
│       ↓                                                │
│   if no tool calls → return response (break)            │
│       ↓                                                │
│   execute ALL pending tool calls in PARALLEL            │
│   (goroutine each + sync.WaitGroup, ApplyTruncation):    │
│       emit(EventToolCall) → run → emit(EventToolResult) │
│       ↓ wait for all before next iteration              │
│   append tool results to history                        │
│   if ContextUsage >= 0.95 → auto-compact mid-turn        │
│   emit(EventLoopEnd)                                    │
│   continue loop                                         │
└────────────────────────────────────────────────────────┘
    ↓  budget exhausted → emit(EventMaxIterationsReached)
    ↓                     then requestProgressUpdate()
    ↓  ctx cancelled → emit(EventStop) (never EventError)
    ↓  emit(EventTurnEnd)   [LoopEnd/TurnEnd always balanced]
    ↓
Frontend renders everything via event handler
```

## Development Workflow

### Build & Test

```bash
make build                # build binary (injects version via ldflags)
go build -o harness ./cmd/harness # plain build (version = "dev")
go vet ./...              # lint
make install              # build + install to ~/go/bin
./harness                 # run
```

### Adding a New Provider

For a genuinely new API dialect or auth flow, follow the steps below (a real
`internal/providers/<name>.go` file, built into the binary). For a user's own
OpenAI Chat Completions-compatible endpoint (a proxy, gateway, or self-hosted
server) that just needs a name/URL/headers, no code change is needed at all —
two paths, both landing on the same `internal/providers.CustomOpenAI`:
These providers have **no credential concept of their own** — no
`CredentialType`, `harness connect` is rejected outright (same fixed-error
pattern as auto-detected local `Ollama`), and they're always reported active.
Any auth a gateway needs (bearer token, `X-Api-Key`, custom scheme, whatever)
travels as static request headers configured up front — never a stored key:
- **Declarative**: `harness provider add <name> --url <url> --header
  X-Api-Key:<key> ...` writes a `types.CustomProvider` entry (with its
  `Headers` map) to `settings.json`'s `"provider"` collection (singular,
  same style as `"mcp"`), and `internal/providers/registry.go`'s
  `initRegistry()` constructs a `CustomOpenAI` for each enabled entry at
  start; `providers.ReloadCustom()` (`admin.ReloadCustomProviders`, `POST
  /api/settings/provider/reload`) re-applies the collection live — it only
  swaps settings-declared providers (unchanged entries keep their object and
  model cache), never built-in or programmatic ones; on a name clash the
  programmatic provider wins and the skipped entries are reported
  (`ErrNameConflict`). Headers are sent on every request, including model
  discovery.
- **Programmatic (SDK)**: `admin.RegisterOpenAI` (`harness.NewOpenAIProvider`)`(name, url, ...opts)`
  (aliased in `harness.go`) registers one directly in Go code — global to
  the process (same as the declarative path; `providers.All` has no
  per-`Agent` isolation, unlike `mcp.Manager`). Auth goes through
  `admin.ProviderWithHeaders(map[string]string{...})`; an optional
  `admin.ProviderWithFetchModels(func() ([]types.ModelMeta, error))` hook
  replaces model discovery entirely — capture whatever auth it needs in its
  own closure. It may be called at any time, even while agents run, and
  paired with `admin.UnregisterProvider(name)` (aliased in `harness.go`)
  to drop and re-register a provider live (new key/tenant); running sessions
  re-resolve their provider by name at each turn. The registry is
  copy-on-write behind a lock: always read it through `providers.All()` /
  `providers.Find(name)` — never a package variable — and only programmatic
  providers can be unregistered (built-in and settings.json ones return
  `ErrProviderNotRemovable`).

Both paths also accept an opt-in `ReasoningSplit`/`--reasoning-split`/
`admin.ProviderWithReasoningSplit()` flag (default `false`, additive —
every existing custom provider is unaffected). The SDK option takes no
argument — calling it always means "on" (there's no use case for
explicitly passing `false`; just omit the option). It sends
`"reasoning_split": true` on every chat-completions request, the same wire flag the
built-in `minimax` provider (`internal/providers/minimax.go`) always
sends. Needed for a custom provider fronting a MiniMax-compatible backend:
confirmed live (a real gateway proxying to MiniMax) that WITHOUT this
flag, thinking is emitted inline inside `content` as literal
`<think>...</think>` wrapping the final answer, instead of the separate
`reasoning_content` field
`parseOpenAIStream` already routes to `EventStreamThinkingDelta`. Leave it
off for any other backend.

See `internal/providers/custom_openai.go`,
`docs/plans/2026-09-21-custom-providers-design.md` (including its
Addendum section for the programmatic path) and
`internal/providers/custom_anthropic.go`. Two types are supported, either way:
`type: "openai"` (Chat Completions) and `type: "anthropic"` (Messages, the same
shape — `harness provider add --type anthropic`, `admin.RegisterAnthropic`).
An anthropic custom provider pointed at another harness instance's
`/api/providers/claude-oauth/proxy` shares that instance's subscription: it sends
PLAIN Messages requests and never applies the Claude Code identity / tool
mapping itself — the proxy does, once. `type: "openai-responses"` (to share
codex-oauth the same way) is NOT supported yet: see the TODO in
`internal/providers/registry.go`'s `buildCustomProviders`.

1. Create `providers/<name>.go`
2. Implement the `providers.Provider` interface
3. Add the constructor to `internal/providers/registry.go`'s `initRegistry()`, appending to the `All` slice
4. Register the provider key + status in `internal/providers/status.go`
5. Add credential handling (`config/credentials.go` is the store; api-key providers use `resolveAPIKey`)
6. For an API-key provider, `RunConnect` (`internal/cli/commands.go`) already handles it generically via the credential type — no new code needed there. For OAuth, add an `OauthFlow` implementation in `internal/oauthflow/<provider>.go` (one file implementing `Start() (authURL, verifierCode string, err error)` / `Exchange(code, verifierCode string) (*types.Credentials, error)` — see `claude.go`/`codex.go`) plus a `case` in `oauthflow.For`. This flow is driven ENTIRELY by `server/oauth.go`'s `POST /api/oauth/{provider}` handler — the CLI and TUI never call `oauthflow` directly, they call `client.Client`'s `StartOAuth`/`ExchangeOAuth`, which hit that endpoint. The server holds no state between the two calls; the PKCE verifier travels through the caller.

### Pending: `openai-responses` custom providers

Custom providers cover `openai` and `anthropic`. Sharing **codex-oauth** between
harness instances needs a third type, `"openai-responses"` (a `CustomResponses`
provider reusing the Codex request translation and stream parser, plus a case in
`buildCustomProviders`, `customProviderTypes`, the CLI `--type` enum, the OpenAPI
enum and a `RegisterResponses`). Deferred on purpose — see the
`TODO(openai-responses)` markers in `internal/providers/registry.go` and
`internal/config/settings.go`. The proxy side already serves that dialect.

### Synthetic (`internal/providers/synthetic.go`) — an OpenAI-dialect provider measured, not assumed

`synthetic/hf:zai-org/GLM-4.7-Flash` (key: `harness connect synthetic` or `SYNTHETIC_API_KEY`). Everything below was MEASURED against the live API (Synthetic's own documentation leaves most of it out), and each point is a test:

- **The model catalog is PUBLIC** (`GET /openai/v1/models` answers 200 with no key and with a bad one), so listing models proves nothing about a key: `validateKey` sends a one-token chat request on the cheapest model. **Only a 401/403 means the key is wrong**; a **402** (no credits and no plan) and a **429** (concurrency limit) come from an account that EXISTS, so connecting succeeds.
- **Only `hf:` models that produce text are offered.** `syn:` and `infinite:` are aliases of models already listed under their `hf:` id (and `infinite:` needs a plan and streaming); an embeddings or image model cannot hold a conversation. The filter is on `output_modalities` containing `text`, NOT on a word in the id. The `syn:` aliases' targets change over time, which is one more reason not to list them. `provider/model` is split on the FIRST `/`, so `synthetic/hf:org/name` is safe (a test resolves it).
- **Thinking: `reasoning_effort` is the ONLY control.** `think:false` and `thinking:{type:disabled}` do nothing on Synthetic. The shared body builder's DeepSeek rule (every level but `max` becomes `high`, DeepSeek's OWN API) would be wrong for `hf:deepseek-ai/...` (Synthetic's accepts `low`/`xhigh` as themselves), so the provider opts into `OpenAIRequest.VerbatimReasoningEffort`: the level goes out as it is, `off` as `none`, nothing else added. `none` switches reasoning off fully on GLM-4.7-Flash, DeepSeek and Qwen only; Kimi, gpt-oss and GLM-5.3 accept it and keep reasoning a little. A level a model does not list is accepted too, so nothing is clamped.
- **Errors are `{"error":"<plain string>"}`**, not OpenAI's object. `OpenAIRequest.ProviderName` and `ErrorText` (opt-in, default unchanged: `openai API error N` with the raw body) make the user read `synthetic API error 402: Insufficient credits ...`. **A 429 is a normal error: harness retries only 502/503/504, and nothing here changes that.** The limit is ~7-8 SIMULTANEOUS requests per account with no `Retry-After`, so an agent with many parallel subagents can hit it.
- **Prices are per TOKEN strings** (`"$0.0000006"`); `ModelMeta` is USD per MILLION. A malformed or missing price is `SourceUnknown`, never a 0 that reads as free; a price Synthetic reports as `"0"` IS an answer (free).
- **Subscription = `GET /v2/quotas` has a `subscription` OBJECT** (documented shape `{"subscription":{"limit":135,"requests":0,"renewsAt":"..."}}`); a credits-only account answers `{}`. ANY other outcome (network error, non-200, not JSON, a null/non-object `subscription`) is "not known to be a subscription": pay-per-use, and it must never be the reason a working key fails to connect. Checked once per `FetchModels`. Verified live on a subscription account (2026-11-21): `{"subscription":{"limit":500,"requests":0,"renewsAt":"…"},"search":{"hourly":{…}},"planWeeklyLimit":{"percentRemaining":…,"nextRegenAt":"…"}}` — the same body feeds the quota endpoint.
- **The cache (shared parser, `splitPromptTokens`).** OpenAI-dialect `prompt_tokens` is the WHOLE prompt and `prompt_tokens_details.cached_tokens` (read) and `created_cache_tokens` (written, reported by some Synthetic models: DeepSeek and Kimi, not the GLM ones) are SUBSETS of it (measured: the same prompt has the same `prompt_tokens` on the call that wrote the cache and on the one that read it); the session's accounting is Anthropic's (fresh + `CacheRead` + `CacheWrite`, cost = fresh*in + cacheRead*cachePrice + cacheWrite*cacheWritePrice, context = the three). The parser therefore reports `InputTokens = prompt - cached - created`, `CacheRead = cached`, `CacheWrite = created`, the read taken first and the write getting what is left, every part clamped so none is negative and the three never exceed the prompt (`clampInt`). Providers that report no cache are unchanged. **On Synthetic a cache write costs nothing** (`input_cache_writes` is 0 on all seven models), so the write only fixes the footer's `W` and the context breakdown. **Codex already passes `input_tokens` and `cached_tokens` through as they arrive (`codex_oauth_test.go` pins `123` and `100` as-is) and very probably double-counts: not touched, not verified live.**
- `usage` arrives on EVERY chunk and is cumulative, ending in a `choices: []` chunk: the parser overwrites it on each event, so only the last counts.

### Provider quotas (`GET /api/providers/{name}/quota`, `internal/providers/quota.go`, `server/quota.go`)

Design: `docs/plans/2026-11-21-provider-quotas-design.md`. One format (`types.Quota`: `windows[]` of `{name, label, scope?, used_percent 0–100, resets_at UTC}`, optional `balance` in USD, `plan`, `fetched_at`, `stale`) for six providers that implement the OPTIONAL `providers.QuotaReporter` (`Provider` is unchanged; a provider without it is 404). Rules that are easy to break:

- **Every source was measured live** (opencode-go's from its own client's source: the stored key is expired); each parser is a pure `parseXxxQuota(body, now)` tested against the REAL body. A provider that reports what REMAINS is converted (`usedFromRemaining`); every percentage goes through `clampPercent` (0–100, two decimals: Synthetic's `99.99999476` remaining would otherwise read `0.0000052` used); epochs (codex seconds, minimax ms) and ISO with offsets all become UTC.
- **Credentials come from `Upstream()`** (the same headers and client CompleteStream uses — codex's `ChatGPT-Account-ID` included), so the quota call cannot drift from the session's. No credentials = `ErrNoUpstream` → 409.
- **MiniMax answers 200 for errors**: `base_resp.status_code` must be 0. **opencode-go's 403 means "no Go subscription"** (`ErrNoQuota` → 404), as in opencode's own client. **ollama-cloud's plan** comes from a second, best-effort `POST /api/me` (a failure there leaves `plan` empty, never fails the quota); Ollama allows ~10 calls a minute. **claude-oauth answers 429 when polled**: that is why the cache exists.
- **Cache (server, process-global, per provider)**: 60 s TTL; `?refresh=true` never calls more than once per 5 s; a failure with an earlier reading serves it `stale: true`; a `Retry-After` (or 60 s after a bare 429) blocks every call, refresh included, and is advertised. `ErrNoUpstream`/`ErrNoQuota` DROP the earlier reading, and connect/disconnect call `forgetQuota`: a reading of a previous account is never served. Concurrent requests make ONE provider call (the entry's mutex).
- **`/info` — TUI and ACP — shows the quota of the SESSION's provider only** (owner's choice: never the other providers). The layout is shared in `internal/infoview` (window labels, bar, percent, detail, reset times, `NoQuota`, channel grouping), each frontend styles it: the TUI (`internal/tui/info_quota.go`) colors the bar; ACP (`acp/commands.go`, via `internal/infoview.InfoPlain`) is a Markdown code fence where ANSI does not exist, so a window ≥ 70 % gets `⚠ high` and > 90 % `⚠ almost out`. ACP's `/info` matches the TUI's rows except `terminal` (an editor starts ACP; it runs in none) — id, quota, system, mcps, schedules, channels. ACP must never import `internal/tui` (a test pins it): shared pieces go to `internal/infoview`. TUI detail: a 10-cell bar per window (normal < 70 %, warn 70–90 %, error > 90 %; any use shows at least one cell), the percentage, `resets in 4h 05m`, the scope in the detail column, `· plan`, `· stale (3m ago)`, and `balance $x of $y`. A 404 (no quota) renders NOTHING; any other failure is ONE dimmed `unavailable (...)` line: /info never breaks because of the quota. Labels fit the 10-column layout (`shortWindowLabel`): claude's `session`/`weekly`, a per-model weekly window shows the model, else the label's first word, else the last part of the stable name.

### Reopening a session whose provider is gone

`Agent.ResumeSession` and `Agent.ForkSession` resolve the saved model through
`resolveSessionModel` (`agent/model_fallback.go`): the session's own model first,
then — only if that is *unavailable* (`providers.IsUnavailable`) — the active
model, then the first available model. The switch is persisted and reported
(`Session.ModelFallback()` → `model_fallback` in the API); only "nothing at all
is usable" refuses (`ErrNoModelAvailable`, HTTP 409). Never add a path that
reopens a session with a bare `providers.Resolve(meta.Model)`: it would strand the
history again. A restored context count must come from the window it was measured
against (`Stats.ContextWindow`), not the current model's.

### Input channels (`agent.Channel`, `channels/slack`)

A session can listen to an external conversation while it is open: `agent.SessionWithChannels(types.InputChannel{Type, Ref})` on `NewSession`/`ResumeSession` (API: `channels` on create/resume; TUI: `--slack-channel`, repeatable). Design: `docs/plans/2026-11-18-input-channels-design.md`. Rules that are easy to break:

- **Explicit, never persisted.** Not in the session meta; a resume without channels listens to nothing. An explicit channel is honored or the open fails (`ErrChannelUnavailable`/`ErrChannelInvalid` → 400, `ErrChannelTaken` → 409), and a failed open leaves NOTHING behind (no claim, no listener, no session in the store). Asking an already-open session for a conversation it does not listen to is an error, never silently ignored (no live rebind yet).
- **1:1 across processes.** `prepareChannels` claims `(type, id)` in the settings store (`internal/config` `ClaimChannel`, namespace `channels`, atomic `SwapValue`) BEFORE listening; the owner is `(session, pid)` plus `started` (the owner process's start marker); a claim is taken over only when its owner is dead: no process with that pid, or one whose start marker differs (a reused pid — containers restart at the same low pid). Every doubt (marker unreadable, claim without marker) keeps the claim: `ownerAlive` in agent/proc.go, OS lookups in proc_{linux,darwin,windows,unix,other_unix}.go (Windows: access denied = alive, exit code != STILL_ACTIVE = dead). The CI workflow test.yml runs these on Linux/macOS/Windows. Release only if the claim is still ours.
- **Origin `<type>:<id>`** (`ChannelOrigin`, e.g. `slack:C0123`). `sessionChannels.tap` runs in `Session.emit` BEFORE the handler: the prompt echo (`received_prompt`/`follow_up_start`) picks the owner of the turn (or none — every other origin), every event until `turn_end` goes to that channel's `HandleEvent`, which must not block (the Slack one queues to a per-conversation publisher). Events outside a turn (a manual compaction) reach no channel. `Session.Subscribe` is single-handler (the server owns it) — that is why the tap is internal.
- **The channel brings its tools and directive** (not a profile): `Channel.Tools()`/`Directive(ids)`, added per session and kept by `reconfigureSession`. A tool name the session already has (a built-in — `tools.BuiltinNames()` —, an agent tool, a session tool such as a profile's Slack* tools) fails the open instead of shadowing.
- **Names.** `Channel.Name(ctx, id)` gives a readable name (Slack: `#channel`, `@person` for a DM, the id when unknown), resolved once at bind time; `Session.Channels()` returns `[]ChannelInfo{Type, ID, Name}` and the API's session detail `channels: [{type, id, name}]`.
- **Slack specifics:** one RTM socket per `Channel`, opened by the first `Listen`, closed with the last stop, reconnecting with backoff. Every process with the same account receives every event and keeps only the conversations it claimed. Only `<@me>` mentions in a channel, every message in a DM; own/bot messages and every subtype but `thread_broadcast` ignored. Published like the removed Slack transport did: text at `text_end`/before a tool/`turn_end`/max iterations, typing indicator, uploads, errors, compaction notices; into the thread a message came from (see Threads below). **`SlackPost` refuses the conversation the running turn answers** (`Channel.inTurn`, set from the prompt echo in `HandleEvent`, cleared at `turn_end`; only the session's own ids; compared by channel AND thread): its reply is published there already, so a post would duplicate it — seen in production with MiniMax-M3. Posting elsewhere, or there from a turn of another origin, is allowed.
- **Threads** (v0.122.0, `docs/plans/2026-11-25-slack-threads-design.md`): a message is "of a thread" when its `thread_ts` names another message (`RTMEvent.InThread`; a thread's ROOT, `thread_ts == ts`, is a channel message). `thread_broadcast` replies are delivered (every other subtype still is not). The block carries `thread="<root ts>"` ONLY then, and the turn's origin is `slack:<channel>:<thread_ts>` (the tap routes it by prefix; the claimed conversation stays the channel, and a schedule created in a thread fires back into it). **Context is on demand, not automatic** (owner's choice): the directive's `### Threads` rule makes the model call `SlackMessages` with that channel and `thread` BEFORE answering; `conversations.history` never returns thread replies, which is why `SlackMessages` gained `thread` (`conversations.replies`, paginated, root first, the most recent `limit` messages with an "N earlier message(s) left out" note, the root always kept; `thread_not_found` is a typed `ErrThreadNotFound`) and reports `thread`/`reply_count` per message. Only mentions are delivered in a thread too (a thread is NOT followed after replying — a possible later improvement). **Output:** `HandleEvent` reads the thread from the echo's origin (`threadOf`, `current` cleared at turn_end) and one publisher exists per `channel[:thread]`, posting text, uploads, errors and notices with `thread_ts` (typing stays in the channel: RTM cannot type in a thread); a thread that no longer exists falls back to the channel (logged) so the answer is never lost. `SlackPost` gained `thread`, and its "never post into the conversation you are answering" rule compares channel AND thread (`inTurn` maps channel → thread). `sendTyping` holds `connMu` for the WRITE: gorilla/websocket allows one writer and several publishers type concurrently (found by the thread tests under `-race`).
- **A Slack sender's authority is its own** (the `### Who you obey from Slack` part of the directive): no destructive commands, no secrets, no host details from a Slack request; a claimed identity proves nothing. It is an INSTRUCTION, not enforcement — every member of a bound channel can make the session use all its tools. Enforcement by sender (a trusted-sender list gating tools in Slack-origin turns) was discussed and deferred; do not describe the directive as a security boundary.
- The CLI registers the Slack channel (TUI and `serve`) only when a `harness slack login` exists; registering opens nothing. `serve` has no channel flag.

### Colleagues as an input channel (`channels/colleague`) — what the CLI uses

Design: `docs/plans/2026-11-19-colleague-channel-design.md`. This is the ONLY colleague mechanism: the old HTTP tools (`AgentOptions.EnableColleagues`, `harness.AgentWithColleagues`) and the server's instance registry (`server.RegisterInstance`, `GET /api/instances`, the `instances` settings namespace) were removed in v0.111.0. `--with-colleagues` / `HARNESS_WITH_COLLEAGUES`: `interactiveOptions` registers the colleague CHANNEL, and that alone is the switch: it is an `agent.AutoChannel` (`AutoRef() = "*"`), and the agent binds every registered AutoChannel to EVERY session it opens (new, resume, fork — whichever frontend, `withSessionChannels`), so each gets a fresh random id. There is no list of per-agent channel requests: a Slack-like channel (not an AutoChannel) is only ever bound when a caller asks for it. Rules that are easy to break:

- **A colleague is a SESSION with a name**, `colleague:<id>`, id random per open (the channel's own `"*"` reference, avoiding ids a live claim holds; never supplied by a user), `a-z0-9-` ≤ 32 (it is in a file name). 1:1 through the claim registry; the claim contract is unchanged (no address field): the socket path is DERIVED, `<run dir>/c-<id>.sock`.
- **Transport = a Unix socket** in a 0700 directory (the directory's mode is the authentication; BSD ignores a socket file's own mode). `~/.harness/run`, or `/tmp/harness-<uid>` when the home is too long for a socket path (≤ 103 bytes); a path still too long fails the open. One connection per message, one JSON line each way (`{kind: ask|reply, from, text, images}` → `{ok, error}`), 50 MB cap. Windows runs it too (CI).
- **Asynchronous and loop-free BY CONSTRUCTION**: an `ask` is delivered as `<colleague from="x">` with origin `colleague:x`; when that turn ends the inbox sends the turn's FINAL text (text before a tool call is reset — it was commentary) or its error back as a `reply`. A reply arrives as `<colleague from="x" reply>` with origin `colleague:x#reply`, and `askerOf` returns nobody for it: its turn sends nothing back. A reply to a session that left is logged and dropped (chat-room semantics).
- **`Inbound.Origin`** lets a channel put the SENDER in the origin while the turn is still routed to the conversation that delivered it (`sessionChannels.byPrompt`). **`Channel.Tools(ids)`** gets the bound ids (the colleague tools speak as the session's own id).
- `ColleagueList` reads the live claims (`agent.LiveChannelClaims`) and returns `{id, session, cwd, you?}`. `session` is the colleague's CURRENT session name, asked to the colleague ITSELF: a `ping` over its socket (`{kind:"ping"}` → `{ok:true, name}`), all in parallel, each bounded by `pingTimeout` (300 ms); a conversation this process listens to (its own included) is read directly. One that does not answer (gone, slow, or older than 0.115.0, which answers "unknown message kind") is listed WITHOUT `session` — never an error. The name is NOT stored in the claim (0.112.x did, as `session_name`; removed in 0.115.0): the claim is identity only, and any store works (an in-memory or custom store of another process included).
- **How a channel learns its session: the `ctx` of `Listen`.** `sessionChannels.start` passes `context.WithValue(…, sessionCtxKey{}, sess)` (unexported key); `agent.SessionName(ctx)` is the ONLY public reader and returns the name at call time (`Session.name` is an `atomic.Value`: renamed and read from different goroutines). This was the minimal contract change: `agent.Channel`'s methods are unchanged, implementations ignore the ctx if they want, and it exposes the name only, never the `*Session`. The ctx is never cancelled: `stop` ends listening. A channel tool may only clash with a built-in the session REALLY has (`builtinNamesForSession`).
- **A colleague's question is never published to another channel**: the tap routes a turn only to the channel of its origin, so a colleague's turn reaches no Slack or Telegram conversation (the old Telegram transport needed an explicit "quiet mode" for this; channels get it for free).
- **Signals**: the TUI and every command's context also cancel on SIGHUP (a closed terminal, `hc kill`): without it the process died without closing its sessions and left their sockets and claims behind (seen live).
- The section names no implementation ("this machine's group of agents" — a test greps for "harness").

### Telegram as an input channel (`channels/telegram`)

Design: `docs/plans/2026-11-22-telegram-channel-design.md`. Since v0.116.0 the bot is an `agent.Channel`; `transports/telegram` (one session per paired chat, `pair`/`unpair`, `telegram_allowlist`/`telegram_sessions`) was REMOVED, and an independence test forbids importing it or recreating the directory. Rules that are easy to break:

- **The conversation is the BOT, the ref is the trusted users.** `Resolve("gus,ana")` validates the usernames (`ParseAllowed`: lower case, no `@`, 5–32 of `a-z0-9_`, deduplicated, sorted; empty = invalid) and returns the bot's user id (`getMe`): the claim is `telegram:<bot_id>`, so ONE session in ONE process listens to a bot (Telegram allows one getUpdates consumer per token anyway). The allowed list is stored per bot at Resolve and never retargeted while bound.
- **Only trusted senders reach the session**, by `from.username` (case-insensitive), in any chat (private or group); bots are never trusted; everyone else never reaches the session and gets ONE notice per (chat, user) per listening run (`notAllowed`: how to be added with `--telegram-allow @their_user`, or to create a username first; never names who IS allowed). Each prompt is `<telegram chat="…" user="@…">…</telegram>` with origin `telegram:<bot>:<chat>`.
- **Routing by prefix.** The bound conversation is `telegram:<bot>` but a turn's origin carries the chat: `sessionChannels.routeLocked` matches exactly, then `byPrompt`, then a bound origin that is a PREFIX followed by `:`. `HandleEvent` reads the chat from the echo's origin (`chatOf`) and keeps it as `current` until `turn_end`; events outside a turn reach no chat.
- **Read-only commands only.** `/info` and `/context` are answered in the chat through `agent.SessionViewOf(ctx)` (`Info`, `Context`, `Quota` — NO writer: a chat must never reconfigure the session; a test pins the interface's method count), rendered by `internal/infoview` (shared with ACP). `/start` is dropped; `/x@otherbot` and any other `/x` are messages.
- **Files.** Photos: prompt image AND saved file; documents of any type up to 20 MB (the Bot API's getFile limit); programs are skipped by name and by content. Albums (`media_group_id`) are ONE prompt after `albumWait` (the timer restarts per message). Out: `<telegram-upload path=…/>`; photos as photos (content-checked), GIF/MP4 as animations, else documents. MarkdownV2, 4096-char chunks, plain-text fallback on 400.
- **CLI.** The channel is REGISTERED when a token exists: `--telegram-token` (TUI and serve, `telegramTokenFlag`) > `TELEGRAM_BOT_TOKEN` > the saved one (`harness telegram token`). The TUI binds it with `--telegram-allow` (repeatable; Kong's default separator ALSO splits commas — safe, a username has none; `TELEGRAM_ALLOW` comma-separated). Errors before anything opens: users without a token anywhere; `--telegram-token` without users. Clients of serve bind it with `channels:[{type:"telegram", ref:"<users>"}]`. `harness telegram` is only `token [tok] | --status | --delete`.
- **Schedules go back to their conversation (any channel).** `Session.turnOrigin` (set from the prompt echo, cleared at turn_end); the Schedule tool stores it as `Schedule.Origin` only when the session reaches it by a DURABLE route (`listensTo` → `durableRouteLocked`: the conversation itself, a sub-conversation by prefix, or an origin a channel OWNS through the optional `agent.SenderOrigins` interface — NOT `byPrompt`, which only knows what was delivered in this run and is lost on restart; `routeLocked` = `byPrompt` + the durable routes, for live turns). Colleagues implement `OwnsOrigin` (a QUESTION origin `colleague:<x>`, never a `#reply`), so a schedule created while answering a colleague's question fires back as that colleague's question: its final answer goes to the colleague as a reply, and that reply's turn sends nothing back (no loop). The `## Colleagues` directive says so; `schedule.FireFunc` gets the whole `Schedule`; `fireScheduledPrompt` ALWAYS wraps the prompt in `<scheduled slug="…">` and uses `PromptWithOrigin(s.Origin)` when `listensTo` it, else no origin at all (the wrapper says it is a schedule).
- **The quota cache lives in `internal/providers`** (`CachedQuota`, `ForgetQuota`, `SetQuotaRulesForTest`) so a channel's `SessionView.Quota` and the HTTP endpoint share it.

### Settings and credentials served by the RPC client (`kind: "client"`, v0.133.0)

Design: `docs/plans/2026-11-26-rpc-client-config-stores-design.md`. `initialize` accepts `settings` and `credentials` as `{kind: file|ephemeral|client}` (omitted = `file`, the global `~/.harness` files); `client` registers `rpc/clientstore.go`'s adapter (a THIRD implementation of `configstore.SettingsStore`/`CredentialsStore` next to FileStore and InMemoryStore) that calls the client with the reverse methods `settings/{get,set,delete,list,swap}` and `credentials/…`. Rules that are easy to break:

- **`swap` is compare-and-swap on an opaque `version` the CLIENT chooses** (`expected_version: null` = must not exist, `value: null` = delete, mismatch → `swapped:false`); the adapter's `SwapValue` is a get → run `fn` IN THE BINARY → swap loop (10 attempts, then `ErrStoreConflict` → `conflict`) — the callback never crosses the pipe, which is what keeps the single-use OAuth refresh token safe. A per-key mutex inside the adapter serializes the binary's own goroutines; the client's `swap` arbitrates between processes. A `client` store is only as atomic as the client's `swap`.
- **Order matters:** stores are process-global (`SetSettingsStore` fails with `ErrAlreadyInitialized` once used), so `initialize` validates the whole config, registers the stores, runs a readiness probe (`get core/probe`, an entry that never exists: without it a client that never answers would let the agent start on empty settings because the managers read leniently), and only then builds the agent (which reads settings). The client must therefore answer `settings/get` WHILE `initialize` is pending. A failure after a store was registered ENDS THE PROCESS (`Error.After = s.stop`); one before stays retryable.
- **Every error from the client is logged on stderr and returned**, with a 30 s deadline per request (`the client did not answer settings/get within 30s: …`). `CredentialsManager.Credential/APIKey/OAuth` return `(value, found, err)` so a transport failure is NEVER read as "this provider has no credential" (`getJSONErr`; providers keep their last known state on a read error). Other readers still read errors as "absent".
- **Names:** the `store` kind is `ephemeral` (was `memory`, which clashed with the `memory` field) and `memory` accepts only `sqlite` (omit it for none). **Any implementation of the config stores must pass `configstore/storetest.RunContract`** (file, in-memory and the RPC adapter all do).

### Resource loaders are stateless and `nil` discovers NOTHING (v0.132.0)

`resources.ResourceLoader` is ONE method, `Load(cwd string) (*Resources, error)`; it holds no state and is bound to no directory, so every session and sub-agent calls it with its own cwd and ONE instance (shared, never copied — `Copy()` and `ReadSkill` are gone) serves them all; implementations must be safe for concurrent use. `Resources.Skills` are `resources.Skill` WITH their `Content` (Load already read them all; one call returns everything, which is also what a client-side loader over RPC needs). `agent.loadResources` is the single place the agent loads (`nil` loader → empty Resources) and `skillReader` serves the Skill tool and `Session.ReadSkill` from the skills a session was opened with. **`AgentOptions.ResourceLoader == nil` means "discover nothing"** (it used to mean "read the project's files", through a `nil` with a hidden meaning that built a `FileResourceLoader(cwd)` per session): the CLI frontends pass `resources.File()` explicitly (`internal/cli/agent.go`), and an SDK/RPC consumer opts in. Do not reintroduce a per-session instance or a `nil` that does something.

### The RPC frontend (`rpc/`, `harness rpc`) — the protocol for other languages

Design: `docs/plans/2026-11-26-rpc-frontend-design.md`. JSON-RPC 2.0 over stdio, built DIRECTLY on `agent` and `admin` (no HTTP server in between, nothing another process can reach). Rules that are easy to break:

- **One table of operations.** `rpc/table.go` concatenates `coreOps()`, `sessionOps()`, `sessionMoreOps()` and `adminOps()`; an operation is `Define[P, R](method, doc, fn)` over the REAL Go types. Dispatch, strict param validation (`DisallowUnknownFields`) and the JSON Schema all come from it by reflection. **A new public method of `agent.Agent`, `agent.Session` or `admin` fails `TestEveryAgentAndSessionMethodIsDecided` / `admin_parity_test.go` until it has an operation or an explicit exclusion with a reason** — that is what keeps the TS SDK at parity with the Go one. After changing the table (or the HTTP API) run **`make docs`**: it regenerates `rpc/schema.json`, `reference/rpc-protocol.md` and `reference/http-api.md` and builds the site (`make docs-generate` does only the generating). A test fails when `schema.json` is stale, and the Pages workflow refuses to publish when a generated page differs from the committed one.
- **Names:** `initialize`, then `<domain>/<method>`: `agent/*`, `session/*`, `admin/<service>/*`, plus the reverse calls `tool/call` (a REQUEST to the client), `tool/cancel` and the `event` notification. **snake_case everywhere, Go types serialize as they are — no key conversion.** ACP is camelCase ONLY because its spec mandates it; do not "unify" them (277 own snake_case tags, the files on disk and the providers' wire formats are snake_case).
- **`initialize` builds the agent** from the serializable `Config`; `store` (`file`|`memory`), `memory` (`sqlite`|`none`, DEFAULT NONE = `agent.New`'s default, not the CLI's) and `resources` (`file` only: OMIT it and nothing is discovered, like `agent.New`) are chosen by `kind`, never passed as objects. The binary always closes them. A custom Store/Memory/ResourceLoader implemented in the client (`kind:"custom"`, reverse calls `store/*`, `memory/*`, `resources/*` like `tool/call`) is FUTURE work, deliberately not in v1.
- **Client tools are a callback bridge** (`rpc/bridge.go`): the registered `Execute` sends `tool/call` and waits; the turn's ctx cancels it (`session/stop` → `tool/cancel`). The tool learns its session through `agent.SessionID(ctx)`.
- **Ordering is a correctness property.** `jsonrpc.Serve` runs NOTIFICATIONS in order on the reading goroutine (a goroutine each swapped neighbouring `event`s — found by a flaky test) and REQUESTS each in their own goroutine. `Receiver.Received` marks `initialize` as begun in arrival order so requests pipelined behind it wait (`awaitReady`) instead of failing "not initialized". Do not make notification handlers slow.
- **Errors:** `rpcError` maps Go errors with `admin.Kind` (the ONE error taxonomy, shared with the HTTP `adminStatus`) to the codes -32000 not_found, -32001 invalid/unprocessable, -32002 session_active/conflict, -32003 not_connected, -32004 upstream, -32005 not_configured, -32006 protocol_version, with `data.kind`.
- `session/exec_command` exists ONLY for commands without a method of their own (`skill:<name>`). Reconnection policy (relaunch + `session/resume`) belongs to the SDK, not the protocol. stdout carries ONLY protocol messages; logs go to stderr.

### `agent` operates live sessions, `admin` does everything else

Design: `docs/plans/2026-11-26-admin-layer-design.md`. Public package `admin` (services `Sessions`, `Providers`, `Settings`, `MCP`, `Memory`, `Schedules`) holds every operation that does not need a live session; `agent` keeps only what operates one (`NewSession`/`ResumeSession`/`ForkSession`/`ImportSession`, profiles, `RegisterTool`, `MCPTools`/`MCPStatuses`, `ActiveSessions`, `Admin()`). **A session's output ceiling (`max_tokens`) is INTERNAL**: it is worked out every time a session opens or switches model (`sessionMaxTokens`: `AgentOptions.MaxTokens` > the model's catalog value capped at 64k > 32k) and bounds each request and the compaction trigger, but it is reported nowhere — not in the API, not in `/info`, not by `Session` — and is not stored; do not add it back (`MaxTokensFor` and `Session.MaxTokens()` were removed in v0.130.0). Rules that are easy to break:

- **`admin` never imports `agent`** (a test greps for it). It talks to running agents through SHARED STATE (a session re-resolves its provider by name at every turn; there is no Notifier) and, for deleting or renaming a stored session, through `WithActiveSessions(a.ActiveSessions)` → `ErrSessionActive`. A session open in ANOTHER process is NOT protected (as before; do not build cross-process claims for it).
- **Business logic does not live in a handler.** A server handler is decode → `admin`/`agent` → encode; `adminStatus` (server/admin_handlers.go) maps admin's sentinels with `errors.Is` (`ErrUnprocessable` BEFORE `ErrInvalid`, since a bad thinking level or MCP server matches both). The CLI calls `admin.New()` directly for connect/disconnect/providers/sessions/delete/memo/schedules/settings/mcp/provider — it no longer starts an in-memory server for them (`startInternalServer` is only `-p`'s).
- **`admin.New(opts...)` options are plain `With*`** (one kind of option) and mirror the agent's: `WithSessionStore`, `WithMemory`, `WithActiveSessions`; defaults are what `agent.New` uses. A store passed in is the caller's: `Admin.Close` closes only what admin opened. `Agent.Admin()` builds one bound to the agent's own store/memory.
- **Live-or-stored reads stay in the caller** (the server reads the live `*agent.Session` when open, `admin.Sessions` otherwise; export passes the live meta with `ExportAs`).
- **Fan-out and single-open live in the agent:** `Session.Watch(buffer, onDrop)` → `*Watcher` (deltas dropped without waiting when a watcher's buffer is full; control events wait up to 500 ms then are dropped and `onDrop` is called; `Close` is idempotent and a session's close closes its watchers); `ResumeSession` of the same id is serialized per id, so concurrent opens yield ONE `*Session`. The server's `SessionProxy` is built on `Watch`; do not reimplement the drop rules there.
- Custom-provider registration (`NewOpenAIProvider`… `ProviderWith*`, `ErrProvider*`) is `admin.RegisterOpenAI`/`RegisterAnthropic`/`UnregisterProvider`/`ReloadCustomProviders`; `harness.go` keeps the old names as aliases.

### The agent configures, the session obeys

Design: `docs/plans/2026-11-26-agent-configures-session-obeys-design.md`. A session option exists only for what is particular to ONE session: `SessionWithChannels` (which conversations it listens to) and `SessionWithEnabledProfiles` (which registered profiles are enabled for it) — in `agent` and in `client`. There is NO per-session tool or disallowed-tools option (`SessionWithTools` was removed in v0.126.0): `AgentOptions.Tools`/`DisallowedTools` configure every session, and a subset of sessions that needs shared text and tools is what a profile is. **`SessionWithDirectives` (restored in v0.128.0, `agent` and `client`, `directives` on create/resume/fork) is the third session option**: text particular to ONE session (a worktree, a task) where registering a profile per text would be the wrong tool. It is NEVER persisted (not in the meta, not in the export; a fork and an import do not inherit it; an already-open session ignores it on resume) and is kept on the live `Session` (`ownDirectives`) so a profile (un)registration does not drop it. Order: agent → profiles → own → channels. **Before removing or renaming ANY public SDK API, ask the owner whether an external embedder uses it** (the v0.126.0 removal was justified by a grep of this repo, which proves nothing about external consumers such as jade-kaiban). Do not add `SessionWithDisallowedTools`: a restriction that is not persisted is lost when the scheduler or a colleague reopens the session. `reconfigureSession` and `setSessionProfiles` are PRIVATE (nobody outside the agent forces a reconfiguration). **Base prompt precedence: `AgentOptions.SystemPrompt` > `SYSTEM.md` > the built-in default** (`New` no longer fills the default in, so a caller's prompt — a sub-agent's included — is never overridden by a project's SYSTEM.md). Prompt order: base → skills → memory → tool sections → working directory → AGENTS.md → agent directives → enabled profiles' directives → channels' directives.

### Profiles (`agent/profiles`) — owned by the AGENT, not the server

Design: `docs/plans/2026-11-26-agent-profiles-design.md`. A profile is `{Directives []string, Tools func() []tools.Tool}` registered by name on the agent (`AgentOptions.Profiles` / `harness.AgentWithProfiles` / `Agent.RegisterProfile`); `server.Profile` is only an alias and `server.RegisterProfile` delegates. Rules that are easy to break:

- **`agent.SessionWithEnabledProfiles(names...)` is PATCH-like and REPLACES**: not passed = unchanged (new session: none; fork: the parent's; import: the export's), names = exactly those, passed with none = remove all. A blank name is an error (`cfg.err`). The API mirrors it with a pointer (`*[]string`: absent/null vs `[]`), the client with `*[]string` in `sessionOptions`. Resume used to be ADDITIVE — do not bring that back (two frontends cannot then remove each other's).
- **Unknown name = failure, stored name no longer registered = skipped.** An explicit name that is not registered fails the open (`ErrUnknownProfile` → 400) and leaves nothing behind (fork/import delete the copy). A name already stored that is not registered now is skipped (its directive would describe tools that do not exist), KEPT in the meta, and reported: `Session.MissingProfiles()`, `missing_profiles`, a TUI warning. Registering it later applies it.
- **One path:** `profilesFor(stored, cfg)` decides names + directives/tools for New, Resume, Fork/Import (`openStoredCopy`) and `reconfigureSession`. The profiles' directives/tools come BEFORE the caller's own. `RegisterProfile`/`UnregisterProfile` call `reconfigureBound` (open sessions apply it at their next turn boundary). Resuming an already-open session with profiles rebinds it (persist + reconfigure).

### Session export / import

Design: `docs/plans/2026-11-23-session-export-import-design.md`. Rules that are easy to break:

- **The file** is JSONL: line 1 an `agent.ExportHeader` (`format: "harness-session-export"`, `version: 1`, `exported_at`, `harness`, `meta`), then EVERY message of the log as stored (pre-compaction too). Name: `agent.ExportFileName(meta)` = `<lowercase ascii, runs of non a-z0-9 → "-", ≤ 50, never ending in "-", "session" if empty>-<id8>.export.jsonl` (re-exporting a session replaces its own file).
- **Import always creates a NEW session** (`Agent.ImportSession(r, cwd, opts...)`), never touches an existing one: a new id, created now, the given cwd (else the meta's); everything else (name, model with ResumeSession's fallback, thinking, iterations, profiles, compaction offset/count/reset count, stats) comes from the export. Validation (wrapping `ErrInvalidExport`, naming the line): the header (format, version ≤ 1, meta), every line a message with a role, `CompactOffset ≤ count`. A failure deletes the partial session: NOTHING is left. A read error is never taken for the end of the file (`readFullLine`): a body cut at the size limit must be a 413, not "line N is not a message".
- **Streaming both ways** (a real session: 643 MB, 136 891 messages; export 1.4 s with 16 MB of heap, import 5 s). Two OPTIONAL store capabilities with a generic fallback (like `WindowReader`): `store.MessageStreamer` (FileStore hands raw lines of a size-bounded snapshot: never blocks a running turn, never re-encodes) and `store.MessageImporter` (FileStore: one buffered pass to a temp file, fsync, rename; the directory is looked up ONCE — `AppendMessage` per message re-scans every session directory). The line limit of every log reader is `store.maxLogLine` (64 MB; it was 10 MB, repeated in each reader, and a message with a big image made its session unreadable).
- **One way per frontend.** The API moves CONTENT only: `GET /api/sessions/{id}/export` (streamed, `application/x-ndjson`, `Content-Disposition` with the proposed name) and `POST /api/sessions/import[?cwd=&channel=type:ref]` with the JSONL body (≤ `ServerOptions.ImportMaxBodyBytes`, default 1 GiB → 413; 400 invalid; 201 the new session). `client`: `ExportSession(id, w)` and `ImportSession(r, cwd, opts...)`. The server never reads or writes a path from an API request. Files are the job of two SESSION COMMANDS (what the TUI's `/export` and `/import <path>` send): `export` (no params) writes `~/.harness/exports/<file>` (dir 0700, file 0600, temp + rename; no path parameter, so nobody can make the server write elsewhere) and answers the path and count; `import {path}` only accepts a `*.export.jsonl` that is an export, opens it in the CALLING session's project, and answers the new session's id in `message` — the TUI then switches to it like `/resume` (`resumeInPlace`).
- **`openStoredCopy`** is the shared tail of fork and import (model with fallback, tools, prompt, channels, register): every failure deletes the copy (the fork used to leave it behind when no model was available).

### The TUI's session state is shared across goroutines

The session the footer and status lines show — `sessionID`, `sessionName`, `model`, `thinking`, `isSubscription`, `maxIterations`, `channelInfos`, `currTurn`, `queueCount`, `mcpConnected`, `scheduleJobs` — is written by session switches (`/resume`, `/fork`, `/new`, `/import`), command results, the input goroutine and the SSE consumer, while `updateInfo` reads it on another goroutine. Writes go through `viewMu` (`withView`, `switchView`, `setSessionName`); `updateInfo`/`statusBadges` read a COPY (`view()`, `queued()`); `stats` is under `t.mu` (`statsSnapshot`). Found by the export/import TUI test under `-race` (two commands in a row); `TestSessionStateIsSafeAcrossGoroutines` pins it (its mutants only die under `-race`).

### SessionInfo's environment and message times

- **SessionInfo** reports `os`, `arch` (runtime), `channels` (`[{type, id, name}]`, always an array) and `terminal` — the latter ONLY when the agent says it runs in one (`AgentOptions.Terminal` / `harness.AgentWithTerminal`): the TUI sets it from `tui.TerminalName()` (best effort from the environment; Herdr FIRST because it also sets `TERM_PROGRAM=ghostty` → `Herdr (<id>)`, then TERM_PROGRAM, WT_SESSION, KITTY_WINDOW_ID, ALACRITTY_*, KONSOLE_VERSION, VTE_VERSION, JetBrains, else `Generic terminal`). serve, SDK, Telegram, ACP and `-p` leave it empty: they would only report their launcher's terminal. No hostname and no user name, on purpose (a Slack turn can call the tool).
- **Every logged message has `created_at`** (`types.Message.CreatedAt`, UTC, ms), stamped in ONE place: `store.Session.AppendMessage`/`AddCompactionSummary` (`stamp`), unless the caller set it. `Message.MarshalJSON` has its own wire struct — a new field must be added THERE too (a test caught it missing). Older messages have none (`omitzero`). It never reaches a provider (providers build from `parts`; `internal/providers/created_at_wire_test.go` checks every dialect). A fork copies the log, times included.

### `harness serve` listens on localhost unless told otherwise

`harness serve :8080` means **127.0.0.1:8080** (`serveAddr` in `internal/cli/kong_run_serve.go`): a missing host is THIS machine, not every interface as Go reads it, because the server has no authentication (whoever reaches it gets the sessions, the providers through the proxy, and the tools — Bash included). Exposing it takes an explicit host: `0.0.0.0:8080`, `[::]:8080` or a real address. A bare port (`8080`) is an error. This is the CLI's rule only: `server.WithAddr` (SDK) passes the address to `net.Listen` as given, and `server.Run`'s own default is `127.0.0.1:0`.

**The secret** (`server/secret.go`): `server.WithSecret` / `ServerOptions.Secret` make `secretMiddleware` require it on every request except the OPEN paths (exactly `GET /api/server` — also how a client checks the server is up — and `/api/docs`, `/api/docs/openapi.json`; a test pins that a sibling like `/api/server/x` is NOT open), in `X-Harness-Sec` or `Authorization: Bearer` (capital B, like the standard scheme), compared with `subtle.ConstantTimeCompare`; the 401 is `{"error":{"message":"missing or invalid secret"}}` plus `WWW-Authenticate`, never saying which part was wrong. It is registered AFTER `corsMiddleware`, which answers every OPTIONS itself (a browser preflight carries no credentials) and announces the two headers. `server.GenerateServerSec()` = `hs_` + sha256 of 8 random bytes (panics if the machine has no secure random source: a secret from anything else would look secure and not be). `client.WithSecret` wraps the client's transport (`secretTransport`), so EVERY request carries the header — JSON calls, SSE, provider proxy, export/import — and a new call cannot forget it; `StreamEvents` returns an `*Error` with the status now. CLI: `harness serve --secret` / `HARNESS_SECRET` (flag wins), `harness gen-secret` (a ROOT command: `serve` has a positional `<addr>`, which Kong cannot mix with a subcommand), no secret on localhost = a WARN at start, reachable beyond localhost (`isLoopbackAddr` false) without one = REFUSED unless `--insecure`. The TUI, ACP and the channels start their own server on localhost with no secret, on purpose. Without TLS the secret travels in the clear: it is for keeping casual callers out.

### Adding a New Tool

1. Create `agent/tools/<name>.go`
2. Define the `Tool` struct with JSON schema and Execute function
3. Add the name constant in `agent/tools/names.go`
4. Register it in `agent/agent.go` `buildSessionTools()` — always-on built-ins (Bash/Read/Write/Edit/Fetch, and `SessionInfo` — a small, purely informational session snapshot that needs no gating beyond `DisallowedTools`) just check `isToolAllowed`; an *optional* built-in that needs its own on/off switch (e.g. `WebSearch`) adds a new `AgentOptions.EnableX bool` field instead, gating registration with `if a.opts.EnableX && a.isToolAllowed(...)`. **Every new `AgentOptions.EnableX` field must also get a matching `harness.AgentWithX()` option in the root `harness.go` facade** (see `AgentWithWebSearch`/`AgentWithScheduler` for the one-line pattern) — `EnableWebSearch` shipped without one for a full release cycle before an SDK user reported being unable to reach it from outside the CLI (`internal/cli/agent.go`'s `newInteractiveAgent` could set the struct field directly, but an external embedder using the public facade had no way to). Add a `TestAgentWithBoolOptionsSetTheirFlag` case (`harness_test.go`, root package) alongside it. (`SessionInfo` used to share a gate — `EnableSessionInfo` — with `SessionSearch`, a full-text search tool over the session's own history; `SessionSearch` was removed entirely for being largely unused in practice and redundant with persistent memory, at which point `SessionInfo` lost its flag too and became always-on — see `docs/plans/2026-09-22-sessionsearch-removal.md`.)

   **`*Session`'s read-side contract, after a deliberate audit-and-consolidate pass**: every public getter (`Model()`, `Thinking()`, `Stats()`, `MaxIterations()`, `Meta()`, `ContextBreakdown()`, plus the always-immutable `ID()`/`CWD()`/`CreatedAt()`/`Name()`) is LOCK-FREE — safe to call from literally anywhere, including from inside a tool executor (the tool executor runs inside a turn while `promptSync` holds `s.mu` for the whole turn, so anything that DID take `s.mu` there would deadlock instantly with no timeout) or an HTTP handler that might be racing a running turn (`server.go`'s `GET /api/sessions/{id}/info`, `GET /api/sessions/{id}`, and `GET /api/sessions/{id}/context` are built on exactly this — found and fixed live as a real blocking bug before this consolidation, in two rounds: `Meta()`/`Model()`/`Thinking()`/`Stats()`/`MaxIterations()` first, then a follow-up report caught `ContextBreakdown()` still reading `s.provider.Name()`/`s.lastInputTokens`/`s.contextWindow` under `s.mu` with no lock-free twin at all — a request against a busy session used to queue behind `s.mu` for as long as the turn took in both cases, confirmed live by comparing the blocked request's duration against the turn's own). There is NO separate, differently-named public method that takes `s.mu` for a "more thorough" read — that distinction used to exist (`Meta()`/`Stats()` blocking vs. `CurrentModel()`/`CurrentThinking()`/`CurrentStats()`/`CurrentMaxIterations()`/`CurrentMeta()` lock-free) and was a genuine source of confusion for SDK consumers (which one do I call, and why are there two ways to get "the current stats"?). The blocking, drain-and-persist internals (folding `Subagent`/`Fetch` delegated cost — see `addDelegatedCost`'s doc comment — into `s.stats` and persisting it) are now the UNEXPORTED `syncMeta()`/`syncStats()`, used only by `agent.go` itself right before discarding an ephemeral sub-agent session; an external caller has no way to trigger the drain these exist for in the first place (`pendingDelegatedCost` is only ever fed by harness's own built-in tools), so there was never a reason to expose them. `providerNameVal`/`lastInputTokensVal`/`contextWindowVal` (backing `ContextBreakdown()`) mirror `modelStr`/`maxIterationsVal`'s exact pattern — see any of them for how to add a new lock-free field.

   `Model()`/`Thinking()`/`Stats()`/`MaxIterations()` mirror each other's exact `atomic.Value`/`atomic.Int64` pattern — see any one of them for how to add a new lock-free field: write the snapshot under `s.mu` at every point the guarded field mutates (including `Reset()` — a real bug where `Reset()` cleared the store's persisted stats but forgot the in-memory copy was found and fixed here, see `TestResetClearsInMemoryStats`), read it back with no lock at all (see `agent/session_info_test.go`'s `TestSessionInfoGettersDoNotDeadlockUnderPromptSyncLock`/`TestModelDoesNotDeadlockUnderPromptSyncLock`/`TestMetaDoesNotBlockUnderPromptSyncLock` for the deadlock/blocking regression tests this pattern guards against). `Meta()` layers on top of these — it's not simply `syncMeta()` with the lock removed, it's `s.store.Meta()` (a DIFFERENT, short-lived lock — `store.Session`'s own `mu`, never held for an entire turn) with Model/Thinking/Stats/MaxIterations overwritten by their own lock-free accessors, so a caller mid-turn sees CURRENT values, not whatever was last persisted before this turn started (one accepted trade-off: pending delegated cost isn't drained by `Meta()`, only by `syncMeta()`/`syncStats()` — see `Stats()`'s own doc comment for why that staleness window is nothing new).

   A tool that needs actual PERSISTENCE logic (not just a live read) belongs on the `SessionStore` interface itself (`agent/store/store.go`), not inside the tool — see `SearchMessages` for the pattern: the interface method, a thin `*Session`/`*store.Session` wrapper, and the tool reduced to an adapter that only parses/validates input and delegates to an injected func. `agent/tools` importing `agent/store` for shared result/error types (`store.SearchResult`, `store.ErrSearchNotSupported`) is fine — no import cycle (`agent/store` has zero dependency on `agent/tools`) — same as `client/types.go` already does for `store.SessionMeta`.
5. Add tool icon + primary param in `internal/tui/toolfmt.go` (`primaryParam`) and `internal/tui/output.go` (`toolStyle`).

### The root command takes a first message

`tuiCmd` (the root default, `default:"withargs"`) has an optional trailing positional, `Initial []string` (`arg:"" optional:""`): `harness "task"` / `harness fix the bug` opens the interactive TUI with that message submitted once the first session is ready (`TUI.SetInitialPrompt` → `sendInitialPrompt`). Rules that are easy to break:

- **A subcommand name wins over a prompt** (`harness sessions` is the command). Kong allows the positional because `tuiCmd` has no `cmd:""` children of its own (the "can't mix `arg` and `cmd`" rule is per struct), and the siblings are still resolved first. **Kong's own `--` does NOT escape a command name here** — verified: `harness -- serve` still runs `serve`, because at the root `--` only makes the root node's OWN positionals positional, and the prompt belongs to the TUI node one level down. `parseArgs` in `internal/cli/app.go` splits at the first `--`: if what precedes it selects the TUI, what follows is appended to the prompt verbatim; otherwise the whole line goes to Kong unchanged. `newParser` is the one place the parser is configured (tests parse exactly what `Main` parses).
- **`-p` and a non-blank positional are an error** (`use either -p (one-shot) or a positional prompt (interactive)`); a blank positional is no prompt. `--resume <id> "x"` resumes and sends `x` as the next turn.
- **Sent once, after the stream is subscribed.** `sendInitialPrompt` consumes the field (nothing that reopens a stream or switches session — `/resume`, `/new`, `/fork` — can replay it) and `awaitStream` waits for `startSSE`'s stream to be open first: the echo of a prompt is an event on that stream, so a prompt sent first loses it. The wait is bounded (`streamReadyTimeout`) so a stuck connection cannot hold the message forever. The server registers an SSE client BEFORE it flushes the response headers (`handleEvents`) — it used to do it after, leaving a window (2 of 4000 connections) where an event emitted right after `StreamEvents` returned reached no one.
- **It goes through `submitPrompt`, not `handleSubmit`**: it is text for the agent, so `harness "/quit"` must not exit and `harness "!ls"` must not run a shell command. It still queues behind a running turn like any typed message.
- **No usable session ⇒ nothing is sent and nothing is lost** — but only when no explicit flag is violated: no model/provider at all, or the server refusing to create a session when no `--model` was passed, leave the text in the editor (`keepInitialPrompt`). A failed `--resume`, or an explicit `--model`/`--thinking` that cannot be honored, is NOT this case: it is an error (next bullet).
- **An explicit `--model`, `--thinking` or `--resume` is a requirement, not a hint — it is honored exactly or the process exits 1 BEFORE the TUI opens; it is never replaced and never ignored.** The principle: an explicit flag wins over settings and defaults; fallbacks apply only to what the person did NOT pass (the settings model, then the first active one — and the existing `Model '…' not available` warning for the settings case). `autoConnect` returns an `error` and `Run` returns it before `t.tui.Start()`, so raw mode is never entered; `Main` prints `error: <msg>` to stderr and exits 1 — no banner, no TUI, no model call, **nothing written to the terminal** (no OSC 7501: idle is reported only once a usable session exists; no OSC 0 title; no `clear` for a record that never existed). Details that are easy to break: (1) an explicit `--model` is used AS GIVEN, decided in ONE place after the settings/first-active choice, and is deliberately not checked against the models list — the server's error from creating the session is the text `-p` already prints (`provider not found: "zzz"`, `model "x" not found in provider "y"`); it also applies when the list is empty. (2) `--resume` failing (not found, unreadable, refused) is an error with the server's message and NO fresh session, with or without a prompt. (3) `--resume ID --model/--thinking X`: the `ExecCommand` result is CHECKED; on failure the resumed session is CLOSED (so nothing is half-applied) and the process fails; `t.model`/`t.thinking` are stored only after the server confirmed (autoConnect works on locals), so the footer cannot show a model that was never applied. (4) `--thinking` is applied to a FRESH session too (it used to be silently ignored there: a new session starts on the settings level). (5) The banner is added only AFTER the outcome of creating the session: adding a block schedules a render, and a render writes to the terminal before the TUI has started. (6) A session whose SAVED model is gone is still moved by the automatic fallback (`noteModelFallback`) — that concerns the session's own model, not a flag; an explicit `--model` then applies on top. (7) Inside the running TUI `/model`, `/resume`, `/new`, `/fork` keep their warning style (the session exists; failing the process makes no sense). (8) `-p … --resume` is rejected (`--resume is not supported with -p`, exit 1) in `parseArgs`, before any agent exists: `-p` uses an in-memory store, there is nothing to resume, and silently dropping the flag was the bug.
- The positional is documented in the ROOT's `--help` only (`helpWithHiddenDefaultFlags` shows the default command's positional beside the flags it already borrows, and restores it after); it must not leak into `serve --help` and the others.
- **A mistyped command is an error, not a prompt — Claude Code's rule, MEASURED on its binary (v2.1.202), not guessed.** The shell strips quotes, so the process cannot tell `harness sesions` from `harness "sesions"`; accepting every lone word would make a typo cost a model turn. `parseArgs` checks the FIRST word of the prompt (`suggestCommand`): within ONE edit of a visible command name (`editDistance`: insert, delete, substitute, or swap two NEIGHBOURING letters — restricted Damerau–Levenshtein; runes, not bytes), ignoring case, it fails with `unknown command "sesions"` + `did you mean: harness sessions` + both ways out (`harness -- "sesions"`, `harness -p "sesions"`), exit 1, no TUI, no model call. **The threshold is ONE, not two** (the first draft said two; Claude Code showed otherwise — `doct`, `updt`, `agnt` are messages there — and two edits would swallow real one-word messages like `harness test`). Only the first word decides, even with more words after it; flags before it change nothing; words after `--` are never checked; a word spelled exactly like a command is never a typo of another (`help me fix this` is a message). The 55 probes against Claude Code are pinned as DATA in `internal/cli/command_suggestion_test.go`: if you change the rule, that test says whether you still match it. The candidates are the VISIBLE top-level commands plus `help`/`version` (never the hidden default `tui`).
- **`help`, `help <command>` and `version` are real** (`rewriteHelpVersion` maps them to `--help`, `<command> --help`, `--version`; `-v`/`--version` print `version.Version`). `help` followed by words that are not a command is a message.

### The agent the CLI builds is light; extras are opt-in (`internal/cli/extras.go`)

Every command that does agent work builds its agent from `newInteractiveAgent(extras, directives...)` (TUI, `serve`, `acp`) or `newOneShotAgent()` (`-p`); the config commands use `newConfigAgent`/`newAgent`. The base is the SDK's built-in tools plus **WebSearch, always on, with no switch**. Two extras are **opt-in**, because each costs every request whether used or not (measured: colleagues +2 403 characters of system prompt and +1 165 bytes of tool definitions, the scheduler +343 and +1 585): **`--with-colleagues`** (ColleagueList/ColleagueAsk and the `## Colleagues` section) and **`--with-scheduler`** (the cron engine and the Schedule* tools).

- **One declaration.** `withFlags` is embedded (`embed:""`) by the TUI and `serve` (and `telegramTokenFlag`, the `--telegram-token`, by both too), so the names and help text cannot drift. **Never add `EnableScheduler: true` (or the colleague channel) as a literal in a constructor**: the decision is `extras`, built once by `extrasOf`.
- **Flag OR environment variable**, either is enough: `HARNESS_WITH_COLLEAGUES` / `HARNESS_WITH_SCHEDULER` (`1`/`true`/`yes` on; `0`/`false`/`no`/empty/unset off; anything else is an ERROR naming the variable). There is no `--without-*`: a flag only turns something on and a false variable never turns a flag off. **Do not use Kong's `env:"..."` tag for these**: measured on Kong 1.16, an EMPTY variable fails the whole command (`bool value must be true, 1, yes, false, 0 or no but got ""`), and even a correct `--flag` fails with it set; `export X=` and an unset `${X}` are everyday, so `envSwitch` reads them itself.
- **ACP takes no flags** (an editor starts it), so the variables are the only way there. **`-p` never has the extras** (an ephemeral process has nothing to offer another agent, and no engine to keep running) but has WebSearch like everyone.
- **`--scheduler` was renamed `--with-scheduler` with NO alias** (owner's decision): `harness --scheduler` is now `unknown flag`, exit 80, and a test fails if an alias comes back.
- **Each `Run` builds its agent through its own `newAgent`** (`serveCmd`, `acpCmd`, and `tuiCmd.newTUIAgent`), so a test can build exactly what Run builds without a terminal or a port; a test greps that no `Run` calls `newInteractiveAgent` directly. The TUI's `Run` calls `extrasOf` exactly once and hands the same answer to the agent and to the footer's scheduler badge.
- The SDK mechanism: `AgentWithScheduler()`, `AgentWithWebSearch()`, and for colleagues just `AgentWithChannels(colleague.New())` (registering it makes every session a colleague; what `interactiveOptions` does); the CLI only decides what it passes.

### Adding a New Command

The CLI grammar is declared with [`alecthomas/kong`](https://github.com/alecthomas/kong) struct tags — no manual `flag.FlagSet`, no hand-maintained help text, no dispatch switch.

1. Add the command's struct field (`cmd:""`) to its parent in `internal/cli/kong.go` — this is the single source of truth for flags, args, help text, and tree position. Named types only (never anonymous), so a `Run()` can be attached separately. Watch the gotchas documented at the top of that file: only one `default:"..."` command per tree level, Kong forbids mixing `arg:""` with `cmd:""` siblings in the same struct, and — the easiest one to miss — **a struct with both its own `Run()` and `cmd:""` children is wrong**: Kong calls `Run()` on every node in the selected chain (child *and* parent), so the parent's action would re-fire after every subcommand and its flags would leak into each subcommand's `--help`. If a command needs both an action of its own and real subcommands (e.g. `harness telegram` vs. `telegram pair`), give the action its own hidden `default:"withargs"` child instead (see `telegramRunCmd`/`settingsShowCmd`) — never a `Run()` directly on the parent struct. That hidden child's flags still show up correctly in the PARENT's own `--help` (e.g. `harness telegram --help` lists the bot's own flags) thanks to `helpWithHiddenDefaultFlags` in `app.go`, which borrows them onto the parent node being displayed without leaking them into the parent's OTHER subcommands — nothing to do here, just don't be surprised the flags aren't declared directly on the parent struct.
2. Add the `Run() error` method in a `kong_run*.go` file (grouped by area: `kong_run.go` for management/mcp/memo/settings/schedules, `kong_run_telegram.go`, `kong_run_slack.go`, `kong_run_tui.go`, `kong_run_serve.go`, `kong_run_acp.go`). Keep it a thin adapter over real business logic in `commands.go`/`settings.go`/`memory.go`/`schedule.go` — don't inline logic in the `Run()` method itself.
3. Add an HTTP route in `server/server.go` if it needs backend data.
4. Run `go build ./... && go test ./internal/cli/...` — Kong validates the whole grammar (enums, arg/cmd conflicts, duplicate defaults) at parser-construction time, so a bad tag fails loudly instead of silently.

### Adding a New Frontend

A frontend (the TUI, `acp`, anything an SDK consumer writes) opens sessions against the SAME in-process HTTP/SSE server every other frontend uses — it never talks to `agent/` directly. There is no "transport" concept any more: a chat platform is an input channel (see "Input channels" and `channels/{slack,telegram,colleague}`), not a frontend. Follow `acp` (protocol bridge over stdio) as the template. Frontends except the TUI (which stays under `internal/tui/`: a terminal frontend tied to this binary) are PUBLIC packages, since an SDK consumer can run them programmatically on an already-built `*agent.Agent`:

1. Create `<name>/` at the module root, like `acp/`. The frontend is a pure translation layer over a server it is GIVEN: `Run(ctx, srv *server.Server, opts...)`, talking to it only through `client.New(srv.Addr())` — it never builds a server and never closes the server or the agent (their owner does; one server is typically shared by several frontends and the caller's own clients). Its own directive and tools go in a named `profiles.Profile` it registers on start (`srv.RegisterProfile(ProfileName, …)`, which delegates to the agent) and unregisters on stop, binding its sessions with `client.SessionWithEnabledProfiles(ProfileName)` on every create AND resume — the binding is persisted by name in the session's meta. Remember `SessionWithEnabledProfiles` REPLACES the binding on resume: a frontend that wants to add its profile without removing others must pass the full list. NEVER `Agent.RegisterTool` or `AgentOptions.Directives` for frontend-specific behavior (they leak into every session). Tag its prompts with `client.PromptWithOrigin(<name>)`. On stop, close the sessions it drives unless `WithKeepSessionsOnStop`. Its `Option`/`With*` constructors (plain `With*`: a frontend package has ONE kind of option; the `Agent…`/`Session…`/`Prompt…`/`Provider…With*` prefixes are for packages that mix several, like `agent`, and for the `harness` facade) configure the FRONTEND itself only (e.g. `WithStdin`, `WithModel`, plus a `WithLogger(logx.Logger)` defaulting to `logx.NewNilLogger()` if it logs anything of its own — skip it otherwise, like `acp`) — never the agent's own construction-time config (thinking level, scheduler, tools, memory, …), which the caller decides before Run is ever invoked via `agent.AgentOptions`/`harness.AgentWith*`. In particular, don't add a scheduler option here: enabling the cron engine is always an `AgentOptions.EnableScheduler` decision made when the agent is built.
2. Translate the frontend's native protocol into `client.Client` calls (`CreateSession`, `SendPrompt`/`SendPromptWithImages`, `StreamEvents`, `ExecCommand`, …) and translate `client.Event`s back into its native wire format. This translation is the entire job of the package — no business logic belongs here.
3. Add the CLI command per "Adding a New Command" above (`kong.go` + `kong_run_<name>.go`): agent → server → frontend — build the agent, then `startFrontendServer(a, "<name>")` (`internal/cli/server.go`, a `server.Start` the CLI owns and `defer srv.Close()`s, which also closes the agent), then `<name>.Run(ctx, srv, …)`. Build the agent with `newInteractiveAgent(...)` (or `newConfigAgent()`/`newAgent()` if it is one-shot). If the frontend has a `WithLogger`, its `kong_run_<name>.go` must pass `<name>.WithLogger(logx.NewHarnessLogger())` explicitly — the real CLI is the ONLY caller that ever constructs `NewHarnessLogger()`; every other caller gets `logx.NewNilLogger()`.
4. If the frontend has its own on-disk state, it goes through a typed `internal/config` manager over the `configstore` ports, never a new ad-hoc JSON file (see Golden Rule 5).
5. Add a `RunXxx` + `XxxWith*` alias set to `harness.go`'s facade (see `RunAcp`/`AcpWith*` for the pattern) — each is a thin `var RunXxx = xxx.Run` / `var XxxWithY = xxx.WithY` alias, never a reimplementation.

## Thinking System

Universal levels mapped per-provider:

| Level | Anthropic | OpenAI (o-series) | DeepSeek | Ollama |
|-------|-----------|-------------------|----------|--------|
| `off` | thinking off | — | — | `think: false` |
| `low` | `effort: low` | `low` | `low` | `think: true` |
| `medium` | `effort: medium` | `medium` | `medium` | `think: true` |
| `high` | `effort: high` | `high` | `high` | `think: true` |
| `xhigh` | `effort: high` | `high` | `high` | `think: true` |

- Accepted levels are `off | low | medium | high | xhigh`, validated in
  `config.SetThinkingLevel` (single source of truth). Per-invocation override:
  the `--thinking` CLI/TUI flag (there is no `HARNESS_THINKING` env var).
- Anthropic 4.6+: uses `effort` param (adaptive). Older: `budget_tokens` (legacy).
- DeepSeek: `reasoning_content` must be replayed in assistant messages when tool calls follow.
- Mapping lives in `internal/providers/llm/openai.go` `translateThinkingLevel()`. Levels: `off|low|medium|high|xhigh`.

## Model Capabilities Resolution

4-tier fallback for models without capability APIs:

```
1. Provider API        (Anthropic, Ollama /api/show)  — authoritative, never overwritten
2. OpenRouter catalog  (remote; cache-busted, timeout + retries, disk last-good-copy fallback, background refresh — registry_fetch.go)
3. Hardcoded registry  (internal/providers/llm/models.go, a handful of aggregator-ID models OpenRouter doesn't carry under a matching key)
4. Name inference       ("vision" in name → vision=true, etc.)
5. Generic defaults     (128k context, 32k max tokens)
```

`EnrichMeta()` in `internal/providers/llm/models.go` runs tiers 2-5, additively (a later tier only fills fields an earlier one left empty). Used by `minimax`, `openai`, `opencode-go`, `ollama`, and `codex-oauth` — providers with fully authoritative capability APIs (Anthropic, Ollama's local `/api/show`) call `ApplyRegistryPricing` instead, to fill only missing prices without touching already-authoritative fields.

## Rendering Rules

- Spinner shows ONLY during silent gaps (model thinking, no output streaming)
- One spinner label per agent turn (chosen at `EventTurnStart`, reused throughout)
- Spinner stops when any content appears (thinking, text, tool calls)
- Spinner restarts when content stops and model is still working
- Text streaming: word-wrap to terminal width, left border (`│`)
- Thinking: gray border. Response: cyan border.
- **The TUI touches component state from several goroutines: the input goroutine (every key, `HandleInput` → `OnChange` → `onEditorChange`), the render timer (`TUI.scheduleRender.func1` → `Render`), the SSE event stream (`consumeEvents` → history, spinner, footer), a clipboard-paste helper goroutine (`pasteClipboardImage`) and the spinner's ticker.** A panic or a `fatal error: concurrent map writes` on one of them cannot be recovered and kills the process. Every shared component is therefore safe for concurrent use under the same three rules: (1) its state is guarded by a mutex (or is atomic: `paletteController.open`, `TUI.bashMode`); (2) `Render`/`layout` take ONE snapshot under the lock — a copy, never an alias — and draw outside it (a lock held while drawing would stall typing; tests pin it for `SelectList` and `Editor`); never read a length and index in two steps; (3) **no callback runs with a lock held** — `OnChange`/`OnSubmit`/`OnEscape`, `OnSelect`/`OnCancel`/`OnSelectionChange` re-enter the component (the palette's `OnChange` calls `Clear`/`SetValue`/`Value`). `Editor` collects what an edit wants to tell the outside world in `act` while locked and `deliver`s it after unlocking. `paletteController.openRoot` publishes the list's items BEFORE storing `open`, so a frame never draws an open palette with a stale list. `TUI.sseCancel` (set by startSSE, taken by stopSSE/quit on different goroutines) is behind `sseMu`: use `setSSECancel`/`takeSSECancel`, never the field. Process-global state counts too: `ansi.VisibleWidth`'s cache is behind `widthMu` and `spinnerLabel` uses the top-level `math/rand` functions (a shared `*rand.Rand` is not safe). **There is NO global render lock — do not add one.** `internal/tui/tui_race_test.go` types through the real input callback (`inputTerminal` keeps the `onInput` that `Terminal.Start` receives) while the render timer draws and an SSE stream feeds the history; run it under `-race` whenever a component gains state. A new component that is rendered by the timer and edited by anything else needs the same treatment and a case there.
- **Wrapping must stay linear.** `ansi.splitTokens` builds a token in a `strings.Builder`: appending to a string made one long unbroken token (a pasted blob) quadratic — 1 000 000 characters took 80 s inside the render, which holds the renderer's lock and freezes the keyboard (`TestWrappingALongTokenCostsLinearTime` pins it). Never go back to `current += g` in a per-grapheme loop.
- **Palette matches are ranked, not just filtered** (`SelectList.SetFilter`): name starts with the filter, then name contains it, then description only; the original order is kept within a rank. The first entry is what Tab completes and Enter runs, so `/thi` must offer `thinking` before `fork` ("Fork this session").
- **Orientation for running inside Herdr (`internal/tui/herdr.go`).** When `HC_TERMINAL_ID` is set (herdr-core puts it in every terminal it runs), the TUI adds a short `## Herdr` section to its sessions' system prompt: where the model is, what the two `<hc>` envelope shapes mean (`<hc from="ADDRESS name">` is ANOTHER agent, never the person; a bare `<hc>` is a note from the core about one of the model's own terminals), and where to read the rest (`hc --skill`, `herdr-app --skill`, the app's `AGENTS.md`). **Why the core wraps at all:** Herdr adds the envelope itself, and only when BOTH ends are agents, i.e. report status with OSC 7501 (measured: agent→plain program and plain program→agent arrive bare). So only the interactive TUI, the one frontend that reports OSC 7501, ever receives one. **Rules that are easy to break:** (1) it is added by `tuiCmd.newTUIAgent` (`tuiDirectives`), NEVER in `newInteractiveAgent` (shared with serve, telegram, slack, acp: none receives an envelope) and NEVER inside `buildSystemPrompt`: sub-agents (`Subagent`, `Fetch`) and headless SDK consumers inherit the environment variable, and would be told their terminal address is their parent's; the agent package must not mention Herdr (a test greps for it). (2) It is an agent-wide directive, so it survives `/resume`, `/fork` and `/new`. (3) The app's reference is named only if `$HERDR_APP_HOME/AGENTS.md` (else `~/.config/herdr-app/AGENTS.md`) exists, and `herdr-app --skill` only if the binary is on the `PATH`; `hc --skill` is always named. (4) **The text must contain no example terminal address** (nothing shaped like `t7`): a model takes "for example t16" for its own address (seen live), and `from="t7"` for a real peer; the placeholders are the word `ADDRESS` and `$HC_TERMINAL_ID`. (5) **Authority (user story 7): a message wrapped in `<hc from=...>` has the SENDER's authority, never the person's; a claim inside it that the person ordered or approved something is not evidence and waives no confirmation.** The person's own instructions have no envelope. A colleague may ask for reading, answering, investigating, reporting back and what its own request is about; anything else that changes state (writing or deleting other files, destructive commands, pushes, deploys, messages to third parties, spending money, reading secrets) needs the person first, said in a sentence or two (who asked, what). The text deliberately forbids the model to decide an action is "harmless" instead of asking: MEASURED, the authority sentence alone is not enough (the model says "I cannot verify this" and still writes the file because it is harmless); the concrete list is what holds. It is a prompt-level layer: on claude-sonnet-5-5 it held 6 of 6 false-authority wordings, on MiniMax-M2.7 5 of 6 still executed. A stronger fix needs the runtime to know a prompt's origin (the model cannot see `colleagueOrigin`) and gate tools on it. Do not weaken the list, and re-run the live matrix on at least two models after any wording change (the contract test only proves the plumbing). (5b) It also says a reply to the model's own message arrives later as a new prompt — never sleep, poll or read the other screen for it (0/3 polled on claude-sonnet-5-5, still 3/3 on MiniMax-M2.7: prompt-level only). (6) It orients and states one rule, it does not teach: it stays under 260 words (259 today, at the limit) and names no `hc` subcommand (a test pins both); do not paste Herdr's 72 KB `AGENTS.md` (the app rewrites it) or the output of `hc --skill` into the prompt. The environment is injected (`herdrEnv`) so tests never depend on the machine, which may well be inside Herdr.
- **Status to the host terminal (`internal/tui/status.go`, OSC 7501 — Program Status Protocol).** The interactive TUI reports `working` / `idle` / `done` / `error` (and `clear` on exit) as `ESC ] 7501 ; state=<s>:app=harness ESC \`, so a terminal that implements it (Herdr) can update its sidebar/tabs and notify when a turn ends in another tab; one that doesn't ignores it. **Every report is exactly ONE `Terminal.Write` on the SAME terminal the renderer draws on** (`attachTerminal` builds both — the only place a `statusReporter` is constructed): the renderer writes each frame as one write inside SyncBegin/End and an OSC moves no cursor, so they cannot interleave. Never a second write path, never `fmt.Print`. **TUI only — never from ACP, serve, telegram, slack or `-p`** (ACP's stdout is its JSON-RPC channel): a test pins that none of them can import `internal/tui`. A turn's final state comes from an outcome flag, not from `turn_end` itself: `stop` → `idle`, `error` → `error`, anything else → `done`; `turn_start`/`loop_start` reset it (so a failed auto-compaction that lets the turn continue still ends `done`), `turn_end` reads it and never resets it; `turn_end` with `queueCount > 0` reports nothing (still working). Identical consecutive states are one write (`loop_start` fires every iteration). A stream that ends or is replaced while `working` reports `idle` (`stopSSE` does it synchronously, BEFORE the new stream can report — and a cancelled consumer never reports on its own, or it could overwrite the new session's state); a finished turn's `done`/`error` survives a switch. `clear` is terminal (nothing is reported after it) and is sent in `Run` after the renderer stops. A session that is ready reports `idle` BEFORE any turn (`startSSE` — every path that establishes a session ends there; nothing is reported when no session could be created), so a fresh instance is already an agent in the terminal's list and `hc send --if idle` can target it. **The session's name is published as the terminal title (OSC 0, `ESC ] 0 ; <name> BEL`) through the same reporter** — the same rules: one `Write` per sequence on the renderer's own terminal, dedupe, nil-safe, silent after `clear`. Every change of `sessionName` goes through `TUI.setSessionName` (a test fails if anything assigns the field directly, so the title cannot fall behind the footer). The name is **sanitized** (`ansi.SanitizeTitle`: control characters C0/DEL/C1 dropped — ESC and BEL would end the OSC early or inject escapes —, whitespace collapsed, ≤ 80 runes, never cut mid-character): it can come from the user's own prompt. The title is the bare name, no prefix: Herdr's agents list already shows the program beside it, and its mod drops a title that equals the program name, looks like a path or contains `@`. On exit an empty OSC 0 gives the title back just before the `clear` (only if one was published); the XTWINOPS title stack would restore the exact previous title but is not universally implemented, so the simple option was chosen. `title=`/`msg=` of OSC 7501 are deliberately not sent (Herdr does not display them). Neither sequence is ever emitted from ACP/server/`-p`. No setting, env var or disk state.
- **Nothing the TUI draws reaches the terminal without crossing the barrier (`ansi.SafeLine`, applied in `render/diff.go` by both `sanitizeLines` and the cached `lineSanitizer`).** Most of what is drawn is not ours — the user's prompt, the model's reply and reasoning, a tool's args and output, a provider's error, a session name — and an ESC in any of it would be INTERPRETED by the host terminal (OSC 52 writes the clipboard, OSC 0 retitles, CSI 6n makes the terminal answer on stdin, `?1049h` switches screens, DCS/APC carry payloads, conceal hides text). The width helpers (`ExtractAnsiCode`, `VisibleWidth`) only MEASURE: they recognize just the few sequences the TUI emits and clean nothing. The barrier is an **allow-list**, not a block-list: **a TAB is expanded to `tabWidth` (3) spaces** — the width the helpers measure it at; left as a TAB the terminal moves it to the next 8-column stop, the line is wider on screen than measured, the terminal wraps it itself and every row below drifts (the editor's border once smeared into a Bash tool's body); it keeps SGR made only of the attributes/colors the TUI uses (`allowedSGR`: no conceal 8, no blink 5/6, nothing unknown), `CSI K`, and OSC 8 hyperlinks closed by ST whose URL passes `SafeURL` (http/https/mailto, ≤ 2 KiB, printable ASCII); anything else has its ESC replaced by the visible `␛` (BEL by `␇`) and the text kept, so nothing is hidden; C0/C1 controls and invalid UTF-8 are dropped. It runs BEFORE measuring and clipping (neutralizing makes text visible and wider) and inside the line cache, so an unchanged line costs nothing; a clean line is returned as-is with zero allocations. **Do not clean at the entry points** (editor, `addRaw`, markdown, thinking, tool header, footer): that covers today's routes and leaves tomorrow's open — the renderer is the one place every line passes. `stripANSI` (one-line tool-result summaries) is `ansi.StripEscapes`, which knows the whole shape of an escape sequence; never reintroduce a recognizer that only knows what the TUI itself emits. The harness's OWN sequences (title, status) are written through `statusReporter`, not through lines, so they never meet the barrier.
- **Three prompt icons, no more** (`promptIcon`): `❯` the person at this terminal (no origin / `user`), `◷` a fired schedule — told by its `<scheduled slug="…">` WRAPPER (`isScheduledPrompt`), not an origin: since 0.117.0 there is no `scheduled` origin (`OriginScheduled`/`PromptWithOriginScheduled` were removed; a schedule fires with no origin, or with the channel conversation it goes back to), and `scheduled` is still read for sessions saved before; display only, anyone can type `<scheduled` —, `⇌` EVERYTHING else — it came from outside (`colleague`, `herdr`, `slack:<id>`, `telegram`, any future origin). Do not add a fourth. **Herdr is an origin:** `submitPrompt` sends a text starting with `<hc` (`<hc from=…>` from another agent, or a bare `<hc>` note) with origin `herdr` (`originOf`), so it is stored and a resumed history shows `⇌` too. **Tool icons stay distinct from prompt icons**: colleague tools are `⚇`, and an input channel's tools are `⌁` — recognized by name prefix (`channelToolPrefixes` in internal/tui/output.go; add one per channel type), which is a FACT, not a guess: `prepareChannels` fails the open unless every tool of a channel starts with its type, first letter upper-cased (`channelToolPrefix`: `slack` → `Slack…`). A per-tool `source` reported by the API (more robust) was analyzed and deferred: ~8 files + SDK signature change, worth it only together with tool gating by origin. An origin is display data and grants nothing: anyone can type `<hc`.
- **The channels the session listens to get a status line of their own, ABOVE the session line**, text only, NO icon: `channels [slack: #producto-y, @gus] [telegram: @bot]`, from the create/resume response (`client.Session.Channels`); the count form (`channels [slack: 2]`) when the names do not fit (`TruncatedText.SetTexts`: chosen at render time, by width). It exists ONLY while there are channels: `components.NewCollapsibleText` renders no line at all when empty (a plain TruncatedText renders an empty one), so the bar stays two lines otherwise.
- Footer (`internal/tui/output.go` `updateInfo()`): `↑input ↓output Rcache_read Wcache_write $cost(sub) ctx%/ctx_max model(thinking)` — the `R`/`W` cache segment and the `(thinking)`/`(sub)` suffixes only appear when non-zero/non-default.
- Tool calls: icon + name + args. Results: ✓/✗ + one-line summary + duration.

## Patterns to Follow

- **Error handling:** Return errors up, don't panic. Log to stderr only for fatal.
- **Streaming callback:** `StreamCallback = func(StreamEvent)` — events fire inline during HTTP read.
- **Tool execution after stream:** the stream accumulates `pendingCalls`; once it closes, all tool calls run **concurrently** (one goroutine each, joined by `sync.WaitGroup`) and the loop waits for every result before the next iteration.
- **Goroutines are the exception, not the rule.** The ReAct loop itself is sequential. Deliberate uses: parallel tool execution (`agent/session.go`), TUI spinner (`internal/tui/components/spinner.go`), SSE readers (`internal/providers/llm/sse.go`, `client/stream.go`), `bash` timeout wait, **reaping a detached child** (`reapInBackground` in `agent/tools/bash.go`, `startAndReap` in `internal/browseropen`), MCP HTTP transport, and each channel's listener and publishers (Slack RTM, Telegram long polling, colleague socket). Don't add new ones without a reason.
- **An iteration with reasoning but no answer and no tool call is an error, not a success** (`ErrReasoningOnly`, `agent/llm_retry.go`; checked in `promptSync` BEFORE the message is persisted — never retried by harness, never stored). Its message text is a contract orchestrators match on: do not reword it. Keep the check in the ReAct loop, not in `runStream`, which the progress summary also uses.
- **A provider "refusal" is an error, not an answer** (`llm.ErrRefused`/`llm.RefusalError`, `internal/providers/llm/anthropic.go`): Anthropic's stream can complete cleanly with no content and `stop_reason: "refusal"` (safety classifiers, category `cyber`, judged on the WHOLE conversation). `ParseAnthropicStream` returns it as a typed error — after `message_stop`, with usage still reported — so it is never retried (deterministic for the history; `isTransientNetworkError` must keep saying no), never stored as an empty assistant message, and its text ends with a hint to switch provider (the same history was answered by MiniMax and Codex). Anthropic dialect only; don't generalise to other providers without evidence.
- **Tests must not leak process-global state** (provider registry, fixed ports, settings stores): anything registered gets a `t.Cleanup` (`admin.UnregisterProvider`, closing a listener on a fixed port such as codex's 1455) so `go test -count=2 ./...` stays green — a leaked provider can even change which model a later test resolves.
- **Every child process you `Start()` must be `Wait()`ed** (inline, or in a goroutine for a detached one). `Process.Release()` and a bare `Start()` leave a zombie until the harness dies — a long-lived server runs out of PIDs.
- **`bufio.Writer` for output.** All terminal output goes through the buffered writer with explicit `flush()`. Never use `fmt.Println` directly.

## Anti-Patterns to Avoid

- ❌ Adding `Complete()` (non-streaming) to Provider interface
- ❌ Importing `server`, `acp` or `channels/` from `agent/` or `internal/providers/`
- ❌ Exposing an `internal/…` type in a public (SDK) package signature
- ❌ File-based model cache as a primary source (the only file cache is the registry's last-good-copy FALLBACK, `~/.harness/cache/model-registry.json`)
- ❌ Multiple spinner goroutines running simultaneously
- ❌ Direct `fmt.Print` to stdout (use `printf()` + `flush()`)
- ❌ Blocking the SSE read loop
- ❌ Adding dependencies without approval

## File Size Guide

Keep files focused. Current largest files for reference:

| File | Lines | Role |
|------|-------|------|
| `agent/session.go` | ~1860 | Session lifecycle, ReAct loop, history, tool pairing |
| `server/server.go` | ~1430 | HTTP/SSE routes + handlers |
| `agent/agent.go` | ~1370 | Agent factory, MCP/memory/scheduler/SessionInfo wiring, prompt assembly |
| `internal/tui/components/markdown.go` | ~1280 | Faithful streaming markdown renderer (complex by nature) |
| `internal/providers/codex_oauth.go` | ~990 | Codex OAuth + Responses-dialect streaming |
| `agent/store/file.go` | ~680 | Filesystem SessionStore — meta/log I/O plus per-session FTS5 SearchMessages |
| `internal/providers/claude_oauth.go` | ~790 | OAuth token management + streaming |
| `internal/providers/llm/anthropic.go` | ~540 | Anthropic request/response types |
| `internal/cli/app.go` | ~105 | CLI router + dispatch |

If a file grows past ~500 lines, consider splitting — but only along real boundaries.
