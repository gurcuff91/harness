# AGENTS.md — AI Agent Guide for Harness

> Instructions for AI coding agents working on this codebase.

## Project Identity

- **What:** Minimal AI agent harness — a CLI tool that connects LLMs to tools via a ReAct loop
- **Language:** Go 1.24+
- **Module:** `github.com/gurcuff91/harness`
- **Binary:** Single binary, ~9MB — entry point in `cmd/harness/main.go` (module root free for an SDK facade)
- **Version:** single source of truth in package `version` (`version.Version`), injected via ldflags from the `Makefile` (`VERSION=`); falls back to `"dev"` for a plain `go build`.
- **Dependencies (direct):** `golang.org/x/term` (raw mode), `github.com/rivo/uniseg` (grapheme/width), `github.com/go-chi/chi/v5` (HTTP router), `modernc.org/sqlite` (pure-Go SQLite for the memory store), `golang.design/x/clipboard` (clipboard image paste in the TUI), `github.com/google/uuid` (IDs), `github.com/robfig/cron/v3` (schedule parsing), `github.com/alecthomas/kong` (CLI grammar/parsing — `internal/cli/kong.go`). Keep the set minimal — no new deps without approval.

## Golden Rules

1. **No new dependencies** without explicit owner approval. Solve problems with stdlib first.
2. **Always streaming.** There is no non-streaming path. Every provider implements `CompleteStream()`. Never add `Complete()`.
3. **`provider/model` format everywhere.** Settings, env vars, CLI display, Resolve — all use `provider/model` (e.g., `anthropic/claude-sonnet-4-20250514`).
4. **Backend/frontend separation.** `agent/` and `internal/providers/` never import `server` or `transports/`. The agent emits events over an HTTP/SSE API (`server`); the clients (`internal/cli`, `internal/tui`, `transports/{telegram,slack,acp}`) consume it.
5. **Persistent state is explicit.** No model caching. On-disk state is limited to `~/.harness/{credentials.json, settings.json}` and `~/.harness/agent/{sessions/, memory.db}`.
6. **SDK boundary.** Public packages (`agent`, `agent/{tools,store,resources,memory}`, `mcp`, `types`) form the SDK. Keep implementation detail (`providers`, `config`, `transport`, `version`) under `internal/`, and never expose an `internal/…` type in a public signature.

## Architecture

The **agent is the SDK**. Public packages form the embeddable surface — this now
includes `server` and `transports/{telegram,slack,acp}`, so an SDK consumer can
programmatically run any of those transports on top of an embedded agent, not
just the core ReAct loop. Everything under `internal/` is implementation detail
the Go compiler forbids third parties from importing. A thin `harness.go`
facade at the root re-exports the essentials.

```
harness.go                      ← 🔓 SDK facade (package harness): NewAgent/AgentWith* (agent construction), RunServer/RunTelegram/RunSlack/RunAcp + their XxxWith* (transport runners — thin aliases over server.Run/transports/*.Run), Client/NewClient — nothing else (no type aliases beyond these; use agent.Agent, agent.Session, types.Event, etc. directly)
cmd/harness/main.go             ← executable entry point (package main) — just calls cli.Main(os.Args)

🔓 PUBLIC (the SDK surface)
├── agent/                      ← core ReAct loop — the SDK
│   ├── agent.go                ← Chat() loop, tool execution, MCP + memory wiring, Close()
│   ├── session.go              ← session lifecycle, history, tool pairing
│   ├── prompts.go              ← system prompt assembly
│   ├── store/                  ← session persistence (JSONL per cwd) — custom stores here
│   ├── resources/              ← skill/resource discovery — custom loaders here
│   ├── memory/                 ← persistent memory (SQLite + FTS5, cwd + global)
│   └── tools/                  ← built-in tools — custom tools here (package tools)
│       ├── registry.go / bash.go / file.go / edit.go / fetch.go
│       ├── skill.go / memory.go / truncate.go / names.go
├── mcp/                        ← Model Context Protocol client (stdlib) — MCPStatuses() exposes it
│   ├── jsonrpc.go / stdio.go / http.go / client.go / manager.go
├── client/                     ← the ONE typed HTTP/SSE SDK over server's API — every transport uses *client.Client directly (no per-transport wrappers)
│   ├── client.go / types.go / event.go / error.go / stream.go
├── server/                     ← HTTP/SSE backend — the API all clients talk to. Run(ctx, *agent.Agent, ...Option) is the blocking convenience wrapper (listen → serve → wait for ctx → graceful shutdown); Server/NewServer/Serve/Close stay exported for fine-grained control. WithLogger(logx.Logger) sets what receives request/lifecycle log lines — default logx.NewNilLogger() (silent).
│   ├── server.go / sse.go / proxy.go / instances.go / middleware.go / server_docs.go / run.go
├── transports/                 ← interactive session frontends (each opens a session over server via client.Client); PUBLIC because an SDK consumer can run any of these programmatically on an already-built *agent.Agent
│   ├── telegram/               ← Telegram bot (stdlib Bot API; one session per chat). Run(ctx, a, ...Option) — WithToken (required), WithSessionModel, WithSessionThinking, WithAllowUnpair, WithLogger. No Scheduler option: that's an agent.AgentOptions.EnableScheduler concern, decided before Run is ever called.
│   ├── slack/                  ← Slack user bot (one session per channel/DM). Run(ctx, a, ...Option) — WithWorkspace/WithXoxC/WithXoxD (required unless saved via `harness slack login`), WithSessionModel, WithSessionThinking, WithLogger. Same no-Scheduler rule as telegram.
│   └── acp/                    ← Agent Client Protocol agent (agentclientprotocol.com) — JSON-RPC/stdio bridge for Zed and other ACP clients; one session per ACP sessionId, pure protocol translation (never touches agent/ or agent/tools/). Run(ctx, a, ...Option) — WithStdin/WithStdout, defaulting to os.Stdin/os.Stdout. Deliberately no WithLogger: this transport never logs anything itself.
├── logx/                       ← the Logger CONTRACT (interface) server.Run/transports/{telegram,slack}.Run accept via WithLogger, plus NewNilLogger (the silent default every one of them falls back to). Implement Logger to route harness's backend logs anywhere. See internal/logx.NewHarnessLogger() for the real line-oriented implementation harness's own CLI uses — the only concrete Logger lives there, kept internal on purpose (a formatting/output choice, not part of the contract).
└── types/                      ← Event, Message, ModelMeta — shared types

🔒 INTERNAL (compiler-enforced, not importable by third parties)
└── internal/
    ├── providers/              ← LLM provider layer (Resolve, streaming)
    │   ├── provider.go / anthropic.go / claude_oauth.go / openai.go
    │   ├── ollama*.go / opencode_go.go / minimax.go / registry.go / status.go
    │   └── llm/                ← core LLM types, metadata cascade, model registry
    ├── oauthflow/              ← native OAuth PKCE login flows. OauthFlow interface (Start→browser, Exchange→creds) + shared PKCE/browser/token-POST + For(provider) dispatch in oauthflow.go; one file per provider (claude.go), each implementing OauthFlow. Add a provider = one new file + one For() case, no call-site changes.
    ├── config/                 ← typed settings + credentials managers
    │   ├── settings.go / credentials.go / manager.go
    ├── version/                ← build version (ldflags target)
    ├── logx/                   ← NewHarnessLogger() — harness's own logx.Logger implementation (the historical `LEVEL [component] event k=v` line format). Only internal/cli constructs it, passing it explicitly to every server.Run/transports/{telegram,slack}.Run call the real binary makes; every other caller (an SDK consumer, or a transport's own in-process server) gets logx.NewNilLogger() instead.
    ├── tui/                    ← pure-Go terminal UI (zero external TUI libs) — the one interactive frontend that stays INTERNAL: unlike the other transports, it's a terminal frontend tied to this binary, not something an SDK consumer embeds programmatically
    └── cli/                    ← CLI app: Kong grammar (kong.go) + kong_run*.go Run() methods, agent.go builders
```

`server` and `transports/{telegram,slack,acp}` sit at the module root (not
under `internal/`) even though they still freely import `internal/config`,
`internal/providers`, `internal/version` — moving a package out of
`internal/` doesn't change what it's allowed to import (Go's `internal/`
rule only blocks OTHER modules, never siblings in the same module), and
`agent/` already does exactly this (imports `internal/config` and
`internal/providers` directly). Exposing `server` doesn't add a new kind of
exposure: it's an HTTP interface over the same global config/provider
machinery `agent.New`/`harness.NewAgent` already depend on. Logging is the
one exception worth calling out: `server`/`transports/{telegram,slack}` only
ever depend on the PUBLIC `logx.Logger` contract (never `internal/logx`
directly) — the concrete `NewHarnessLogger()` implementation is constructed
exactly once, in `internal/cli`, and threaded in via `WithLogger`; everyone
else (including each transport's own in-process `server.Server`) gets
`logx.NewNilLogger()`.

> **internal/ rule:** its parent is the module root, so *all* harness code can
> import `internal/…`, but external modules cannot. This lets `agent`, `server`,
> and `transports/*` use providers/config/version freely while keeping them out
> of the SDK's own exported signatures.
> **Corollary:** no public package (agent, tools, mcp, memory, types, server,
> transports/*, …) may expose an `internal/…` type in an exported signature.

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

The agent emits events — the transport subscribes. This is the ONLY coupling.
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
│   if ContextUsage >= 0.98 → auto-compact mid-turn        │
│   emit(EventLoopEnd)                                    │
│   continue loop                                         │
└────────────────────────────────────────────────────────┘
    ↓  budget exhausted → emit(EventMaxIterationsReached)
    ↓                     then requestProgressUpdate()
    ↓  ctx cancelled → emit(EventStop) (never EventError)
    ↓  emit(EventTurnEnd)   [LoopEnd/TurnEnd always balanced]
    ↓
Transport renders everything via event handler
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

1. Create `providers/<name>.go`
2. Implement the `providers.Provider` interface
3. Add constructor to `internal/providers/registry.go` in the `Resolve()` switch
4. Register the provider key + status in `internal/providers/status.go`
5. Add credential handling (`config/credentials.go` is the store; api-key providers use `resolveAPIKey`)
6. Add a connect handler in `internal/cli/cli.go` and, if OAuth, add an `OauthFlow` implementation in `internal/oauthflow/<provider>.go` (one file implementing the `OauthFlow` interface — see `claude.go`)

### Adding a New Tool

1. Create `agent/tools/<name>.go`
2. Define the `Tool` struct with JSON schema and Execute function
3. Add the name constant in `agent/tools/names.go`
4. Register it in `agent/agent.go` `buildSessionTools()`
5. Add tool icon + primary param in `transport/tui/toolfmt.go`

### Adding a New Command

The CLI grammar is declared with [`alecthomas/kong`](https://github.com/alecthomas/kong) struct tags — no manual `flag.FlagSet`, no hand-maintained help text, no dispatch switch.

1. Add the command's struct field (`cmd:""`) to its parent in `internal/cli/kong.go` — this is the single source of truth for flags, args, help text, and tree position. Named types only (never anonymous), so a `Run()` can be attached separately. Watch the gotchas documented at the top of that file: only one `default:"..."` command per tree level, Kong forbids mixing `arg:""` with `cmd:""` siblings in the same struct, and — the easiest one to miss — **a struct with both its own `Run()` and `cmd:""` children is wrong**: Kong calls `Run()` on every node in the selected chain (child *and* parent), so the parent's action would re-fire after every subcommand and its flags would leak into each subcommand's `--help`. If a command needs both an action of its own and real subcommands (e.g. `harness telegram` vs. `telegram pair`), give the action its own hidden `default:"withargs"` child instead (see `telegramRunCmd`/`slackRunCmd`/`settingsShowCmd`) — never a `Run()` directly on the parent struct. That hidden child's flags still show up correctly in the PARENT's own `--help` (e.g. `harness slack --help` lists `--workspace`/`--xoxc`/etc.) thanks to `helpWithHiddenDefaultFlags` in `app.go`, which borrows them onto the parent node being displayed without leaking them into the parent's OTHER subcommands — nothing to do here, just don't be surprised the flags aren't declared directly on the parent struct.
2. Add the `Run() error` method in a `kong_run*.go` file (grouped by area: `kong_run.go` for management/mcp/memo/settings/schedules, `kong_run_telegram.go`, `kong_run_slack.go`, `kong_run_tui.go`, `kong_run_serve.go`, `kong_run_acp.go`). Keep it a thin adapter over real business logic in `commands.go`/`settings.go`/`memory.go`/`schedule.go` — don't inline logic in the `Run()` method itself.
3. Add an HTTP route in `server/server.go` if it needs backend data.
4. Run `go build ./... && go test ./internal/cli/...` — Kong validates the whole grammar (enums, arg/cmd conflicts, duplicate defaults) at parser-construction time, so a bad tag fails loudly instead of silently.

### Adding a New Transport

A transport is a frontend that opens sessions against the SAME in-process HTTP/SSE server every other transport uses — it never talks to `agent/` directly. Follow `transports/telegram` (chat bot) or `transports/acp` (protocol bridge over stdio) as templates. Transports (except the TUI, which stays under `internal/tui/` — see the architecture diagram above for why) are PUBLIC packages, since an SDK consumer can run any of them programmatically on an already-built `*agent.Agent`:

1. Create `transports/<name>/`. Its `Run(ctx, a *agent.Agent, opts ...Option) error` — a functional-options runner, matching `server.Run`/`transports/acp.Run`/`transports/telegram.Run`/`transports/slack.Run` — starts an in-process server on a loopback port (`server.NewServer(a, server.ServerOptions{Logger: logx.NewNilLogger(), Transport: "<name>"})` — always a fresh `NewNilLogger()` here, never whatever this transport's own `WithLogger` set, so logs aren't duplicated between the two layers) and builds a `*client.Client` pointed at it — this is the ONLY way the transport touches the agent. Its `Option`/`With*` constructors configure the TRANSPORT itself only (listen address, bot credentials, session model/thinking overrides — named `WithSession*` to make that scope explicit, e.g. `WithSessionModel` — plus, if the transport logs anything of its own, a `WithLogger(logx.Logger)` defaulting to `logx.NewNilLogger()`, see `transports/telegram`/`transports/slack` for the pattern; skip it entirely if the transport never logs anything itself, like `transports/acp`) — never the agent's own construction-time config (thinking level, scheduler, tools, memory, …), which the caller decides before Run is ever invoked via `agent.AgentOptions`/`harness.AgentWith*`. In particular, don't add a `WithScheduler`-style option here: enabling the cron engine is always an `AgentOptions.EnableScheduler` decision made when the agent is built, not something a transport configures.
2. Translate the transport's native protocol into `client.Client` calls (`CreateSession`, `SendPrompt`/`SendPromptWithImages`, `StreamEvents`, `ExecCommand`, …) and translate `client.Event`s back into the transport's native wire format. This translation is the entire job of the package — no business logic belongs here.
3. Add the CLI command per "Adding a New Command" above (`kong.go` + `kong_run_<name>.go`), building the agent with `newInteractiveAgent(...)` (or `newConfigAgent()`/`newAgent()` if the transport is one-shot, not interactive) in `internal/cli/agent.go`. If the transport has a `WithLogger`, its `kong_run_<name>.go` must pass `<name>.WithLogger(internal/logx.NewHarnessLogger())` explicitly — the real CLI is the ONLY caller that ever constructs `NewHarnessLogger()`; every other caller (an SDK consumer, or this transport's own in-process server) gets `logx.NewNilLogger()`.
4. If the transport has its own on-disk state (auth tokens, pairing lists, …), keep it under `~/.harness/<name>.json` or a directory of its own — never reuse another transport's store.
5. Add a `RunXxx` + `XxxWith*` alias set to `harness.go`'s facade (see `RunTelegram`/`TelegramWith*` for the pattern) — each is a thin `var RunXxx = xxx.Run` / `var XxxWithY = xxx.WithY` alias, never a reimplementation.

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
1. Provider API        (Anthropic, Ollama /api/show)  — authoritative
2. llm-registry        (GitHub JSON, fetched once/session)
3. Hardcoded registry  (model_registry.go, ~15 models)
4. Name inference       ("vision" in name → vision=true, etc.)
```

`enrichMeta()` in `model_registry.go` runs tiers 2-4. Only used for OpenAI and OpenCode Go providers.

## Rendering Rules

- Spinner shows ONLY during silent gaps (model thinking, no output streaming)
- One spinner label per agent turn (chosen at `EventTurnStart`, reused throughout)
- Spinner stops when any content appears (thinking, text, tool calls)
- Spinner restarts when content stops and model is still working
- Text streaming: word-wrap to terminal width, left border (`│`)
- Thinking: gray border. Response: cyan border.
- Footer: `╰ duration ↑input ↓output R:cache_read W:cache_write $cost ctx%/ctx_max model`
- Tool calls: icon + name + args. Results: ✓/✗ + one-line summary + duration.

## Patterns to Follow

- **Error handling:** Return errors up, don't panic. Log to stderr only for fatal.
- **Streaming callback:** `StreamCallback = func(StreamEvent)` — events fire inline during HTTP read.
- **Tool execution after stream:** the stream accumulates `pendingCalls`; once it closes, all tool calls run **concurrently** (one goroutine each, joined by `sync.WaitGroup`) and the loop waits for every result before the next iteration.
- **Goroutines are the exception, not the rule.** The ReAct loop itself is sequential. Deliberate uses: parallel tool execution (`agent/session.go`), TUI spinner (`internal/tui/components/spinner.go`), SSE readers (`internal/providers/llm/sse.go`, `client/stream.go`), `bash` timeout wait, MCP HTTP transport, and the Telegram pump. Don't add new ones without a reason.
- **`bufio.Writer` for output.** All terminal output goes through the buffered writer with explicit `flush()`. Never use `fmt.Println` directly.

## Anti-Patterns to Avoid

- ❌ Adding `Complete()` (non-streaming) to Provider interface
- ❌ Importing `server` or `transports/` from `agent/` or `internal/providers/`
- ❌ Exposing an `internal/…` type in a public (SDK) package signature
- ❌ File-based model cache
- ❌ Multiple spinner goroutines running simultaneously
- ❌ Direct `fmt.Print` to stdout (use `printf()` + `flush()`)
- ❌ Blocking the SSE read loop
- ❌ Adding dependencies without approval

## File Size Guide

Keep files focused. Current largest files for reference:

| File | Lines | Role |
|------|-------|------|
| `server/server.go` | ~1420 | HTTP/SSE routes + handlers |
| `agent/session.go` | ~1170 | Session lifecycle, ReAct loop, history, tool pairing |
| `internal/tui/components/markdown.go` | ~1130 | Faithful streaming markdown renderer (complex by nature) |
| `agent/agent.go` | ~920 | Agent factory, MCP/memory/scheduler wiring, prompt assembly |
| `internal/providers/claude_oauth.go` | ~610 | OAuth token management + streaming |
| `internal/providers/llm/anthropic.go` | ~505 | Anthropic request/response types |
| `internal/cli/app.go` | ~85 | CLI router + dispatch |

If a file grows past ~500 lines, consider splitting — but only along real boundaries.
