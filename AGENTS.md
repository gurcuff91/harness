# AGENTS.md — AI Agent Guide for Harness

> Instructions for AI coding agents working on this codebase.

## Project Identity

- **What:** Minimal AI agent harness — a CLI tool that connects LLMs to tools via a ReAct loop
- **Language:** Go 1.24+
- **Module:** `github.com/gurcuff91/harness`
- **Binary:** Single binary, ~9MB — entry point in `cmd/harness/main.go` (module root free for an SDK facade)
- **Version:** single source of truth in package `version` (`version.Version`), injected via ldflags from the `Makefile` (`VERSION=`); falls back to `"dev"` for a plain `go build`.
- **Dependencies (direct):** `golang.org/x/term` (raw mode), `github.com/rivo/uniseg` (grapheme/width), `github.com/go-chi/chi/v5` (HTTP router), `modernc.org/sqlite` (pure-Go SQLite for the memory store), `golang.design/x/clipboard` (clipboard image paste in the TUI). Keep the set minimal — no new deps without approval.

## Golden Rules

1. **No new dependencies** without explicit owner approval. Solve problems with stdlib first.
2. **Always streaming.** There is no non-streaming path. Every provider implements `CompleteStream()`. Never add `Complete()`.
3. **`provider/model` format everywhere.** Settings, env vars, CLI display, Resolve — all use `provider/model` (e.g., `anthropic/claude-sonnet-4-20250514`).
4. **Backend/frontend separation.** `agent/` and `internal/providers/` never import `internal/server` or `internal/transport/`. The agent emits events over an HTTP/SSE API (`internal/server`); the clients (`internal/cli`, `internal/transport/tui`) consume it.
5. **Persistent state is explicit.** No model caching. On-disk state is limited to `~/.harness/{credentials.json, settings.json}` and `~/.harness/agent/{sessions/, memory.db}`.
6. **SDK boundary.** Public packages (`agent`, `agent/{tools,store,resources,memory}`, `mcp`, `types`) form the SDK. Keep implementation detail (`providers`, `config`, `transport`, `version`) under `internal/`, and never expose an `internal/…` type in a public signature.

## Architecture

The **agent is the SDK**. Public packages form the embeddable surface; everything
under `internal/` is implementation detail the Go compiler forbids third parties
from importing. A thin `harness.go` facade at the root re-exports the essentials.

```
harness.go                      ← 🔓 SDK facade (package harness): New, Agent, Session, Options, Event
cmd/harness/main.go             ← executable entry point (package main), CLI dispatch

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
└── types/                      ← Event, Message, ModelMeta — shared types

🔒 INTERNAL (compiler-enforced, not importable by third parties)
└── internal/
    ├── providers/              ← LLM provider layer (Resolve, streaming)
    │   ├── provider.go / anthropic.go / claude_oauth.go / openai.go
    │   ├── ollama*.go / opencode_go.go / minimax.go / registry.go / status.go
    │   ├── authflow/           ← shared OAuth flow (keychain → file → login)
    │   └── llm/                ← core LLM types, metadata cascade, model registry
    ├── config/                 ← typed settings + credentials managers
    │   ├── settings.go / credentials.go / manager.go
    ├── version/                ← build version (ldflags target)
    ├── server/                 ← HTTP/SSE backend (Serve(listener), handler()) — the API all clients talk to
    │   ├── server.go / sse.go / proxy.go
    ├── cli/                    ← CLI command handlers (a client of server)
    └── transport/              ← interactive session frontends (each opens a session over server)
        ├── tui/                ← pure-Go terminal UI (zero external TUI libs)
        └── telegram/           ← Telegram bot (stdlib Bot API; one session per chat)
```

> **internal/ rule:** its parent is the module root, so *all* harness code can
> import `internal/…`, but external modules cannot. This lets the agent use
> providers/config/transport freely while keeping them out of the SDK contract.
> **Corollary:** no public package (agent, tools, mcp, memory, types, …) may
> expose an `internal/…` type in an exported signature.

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

### Agent Events (`agent/event.go`)

The agent emits events — the transport subscribes. This is the ONLY coupling:

```
EventTurnStart              → User submitted input, agent working
EventLoopStart              → ReAct iteration starting
EventStreamThinkingDelta    → Thinking text fragment (stream)
EventStreamThinkingEnd      → Thinking block finished
EventStreamTextDelta        → Response text fragment (stream)
EventStreamTextEnd          → Response text finished
EventStreamToolBuilding     → Model generating tool args (spinner)
EventToolCall               → Tool about to execute
EventToolResult             → Tool finished (output + duration)
EventTokens                 → Token usage update
EventLoopEnd                → ReAct iteration finished
EventTurnEnd                → Agent done, ready for next input
EventError                  → Something broke
```

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
agent.Chat(userID, text, images)
    ↓  emit(EventTurnStart)
    ↓
┌── ReAct Loop (max 25 iterations) ──────────────────┐
│   emit(EventLoopStart)                              │
│       ↓                                             │
│   provider.CompleteStream(req, callback)             │
│       ↓ callback fires events:                      │
│       ├── EventStreamThinkingDelta (thinking text)   │
│       ├── EventStreamThinkingEnd                     │
│       ├── EventStreamToolBuilding (tool args coming) │
│       ├── EventStreamTextDelta (response text)       │
│       └── EventStreamTextEnd                         │
│       ↓                                             │
│   if no tool calls → return response (break)         │
│       ↓                                             │
│   for each tool call:                                │
│       emit(EventToolCall)                            │
│       tools.Run(name, args) → output                 │
│       emit(EventToolResult)                          │
│       ↓                                             │
│   append tool results to history                     │
│   emit(EventLoopEnd)                                │
│   continue loop                                      │
└─────────────────────────────────────────────────────┘
    ↓  emit(EventTurnEnd)
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
6. Add a connect handler in `internal/cli/cli.go` and, if OAuth, wire `internal/providers/authflow`

### Adding a New Tool

1. Create `agent/tools/<name>.go`
2. Define the `Tool` struct with JSON schema and Execute function
3. Add the name constant in `agent/tools/names.go`
4. Register it in `agent/agent.go` `buildSessionTools()`
5. Add tool icon + primary param in `transport/tui/toolfmt.go`

### Adding a New Command

1. Add a subcommand `case` in `cmd/harness/main.go` and a `cli.Run*` handler in `internal/cli/`
2. Add an HTTP route in `internal/server/server.go` if it needs backend data
3. Update the `--help` text in `cmd/harness/main.go`

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
- **Tool execution during stream:** Tools execute in `StreamToolEnd` callback, not batched after stream ends.
- **No goroutines in core.** Only the TUI spinner (`internal/transport/tui/components/spinner.go`) uses a goroutine. Agent loop is synchronous.
- **`bufio.Writer` for output.** All terminal output goes through the buffered writer with explicit `flush()`. Never use `fmt.Println` directly.

## Anti-Patterns to Avoid

- ❌ Adding `Complete()` (non-streaming) to Provider interface
- ❌ Importing `internal/server` or `internal/transport/` from `agent/` or `internal/providers/`
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
| `transport/tui/components/markdown.go` | ~1100 | Faithful streaming markdown renderer (complex by nature) |
| `internal/server/server.go` | ~960 | HTTP/SSE routes + handlers |
| `agent/session.go` | ~680 | Session lifecycle, history, tool pairing |
| `internal/providers/claude_oauth.go` | ~610 | OAuth token management + streaming |
| `cmd/harness/main.go` | ~555 | Entry point + CLI dispatch |
| `internal/providers/llm/anthropic.go` | ~500 | Anthropic request/response types |

If a file grows past ~500 lines, consider splitting — but only along real boundaries.
