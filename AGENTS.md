# AGENTS.md — AI Agent Guide for Harness

> Instructions for AI coding agents working on this codebase.

## Project Identity

- **What:** Minimal AI agent harness — a CLI tool that connects LLMs to tools via a ReAct loop
- **Language:** Go 1.24+
- **Module:** `github.com/gurcuff91/harness`
- **Binary:** Single binary, ~9MB — entry point in `cmd/main.go` (module root free for an SDK facade)
- **Version:** single source of truth in package `version` (`version.Version`), injected via ldflags from the `Makefile` (`VERSION=`); falls back to `"dev"` for a plain `go build`.
- **Dependencies (direct):** `golang.org/x/term` (raw mode), `github.com/rivo/uniseg` (grapheme/width), `github.com/go-chi/chi/v5` (HTTP router), `modernc.org/sqlite` (pure-Go SQLite for the memory store). Keep the set minimal — no new deps without approval.

## Golden Rules

1. **No new dependencies** without explicit owner approval. Solve problems with stdlib first.
2. **Always streaming.** There is no non-streaming path. Every provider implements `CompleteStream()`. Never add `Complete()`.
3. **`provider/model` format everywhere.** Settings, env vars, CLI display, Resolve — all use `provider/model` (e.g., `anthropic/claude-sonnet-4-20250514`).
4. **Backend/frontend separation.** `providers/` and `agent/` never import `transport/`. The agent emits events over an HTTP/SSE API; the transports (`tui`, `http`) and `cli` are pure clients.
5. **Persistent state is explicit.** No model caching. On-disk state is limited to `~/.harness/{credentials.json, settings.json}` and `~/.harness/agent/{sessions/, memory.db}`.

## Architecture

```
cmd/main.go                     ← entry point, CLI dispatch (package main)
version/version.go              ← single source of truth for the version (ldflags)
├── agent/                      ← core ReAct loop
│   ├── agent.go                ← Chat() loop, tool execution, MCP + memory wiring, Close()
│   ├── session.go              ← session lifecycle, history, tool pairing
│   ├── prompts.go              ← system prompt assembly
│   ├── store/                  ← session persistence (JSONL per cwd)
│   ├── resources/              ← skill/resource discovery
│   └── tools/                  ← built-in tools (package tools)
│       ├── registry.go         ← Tool registry (Register, Run, Definitions)
│       ├── bash.go             ← Shell execution
│       ├── file.go             ← Read, Write
│       ├── edit.go             ← Find/replace editing
│       ├── fetch.go            ← HTTP client (text + binary via output_path)
│       ├── skill.go            ← Skill invocation
│       ├── memory.go           ← MemoWrite / MemoSearch / MemoDelete
│       └── truncate.go         ← output truncation (head/tail, /tmp overflow)
├── providers/                  ← LLM provider layer
│   ├── provider.go             ← Provider interface (streaming-only)
│   ├── anthropic.go            ← Anthropic API (Messages API)
│   ├── claude_oauth.go         ← Claude OAuth (subscription, token refresh)
│   ├── openai.go               ← OpenAI-compatible base
│   ├── ollama.go / ollama_cloud.go / opencode_go.go / minimax.go
│   ├── registry.go             ← Resolve("provider/model") → Provider constructor
│   ├── status.go               ← GetProviderStatuses(), GetModelGroups()
│   ├── authflow/               ← shared OAuth flow (keychain → file → login)
│   └── llm/                    ← core types, metadata cascade, model registry
├── config/                     ← typed settings + credentials managers
│   ├── settings.go             ← active_model, thinking_level, providers, mcp
│   ├── credentials.go          ← API keys + OAuth tokens (0600)
│   └── manager.go              ← singletons
├── mcp/                        ← Model Context Protocol client (stdlib)
│   ├── jsonrpc.go / stdio.go / http.go / client.go / manager.go
├── memory/                     ← persistent memory (SQLite + FTS5)
│   ├── store.go                ← cwd-partitioned + global, prefix FTS search
│   └── adapter.go              ← scoped adapter → agent/tools.MemoryStore
├── cli/                        ← CLI command handlers (top-level module)
│   ├── cli.go                  ← providers, sessions, connect, disconnect
│   ├── settings.go / memory.go / oauth.go / server.go
├── transport/http/             ← HTTP/SSE server
│   ├── server.go               ← Serve(listener), handler(), all routes
│   └── sse.go                  ← event serialization
└── transport/tui/              ← pure-Go terminal UI (zero external TUI libs)
    ├── tui.go                  ← top-level app, banner, autoconnect
    ├── session.go              ← SSE client, history rendering
    ├── toolfmt.go              ← tool-call arg formatting
    ├── ansi/ render/ components/ term/ keys/
```

## Key Interfaces

### Provider (`providers/provider.go`)

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
go build -o harness ./cmd # plain build (version = "dev")
go vet ./...              # lint
make install              # build + install to ~/go/bin
./harness                 # run
```

### Adding a New Provider

1. Create `providers/<name>.go`
2. Implement the `providers.Provider` interface
3. Add constructor to `providers/registry.go` in the `Resolve()` switch
4. Register the provider key + status in `providers/status.go`
5. Add credential handling (`config/credentials.go` is the store; api-key providers use `resolveAPIKey`)
6. Add a connect handler in `cli/cli.go` and, if OAuth, wire `providers/authflow`

### Adding a New Tool

1. Create `agent/tools/<name>.go`
2. Define the `Tool` struct with JSON schema and Execute function
3. Add the name constant in `agent/tools/names.go`
4. Register it in `agent/agent.go` `buildSessionTools()`
5. Add tool icon + primary param in `transport/tui/toolfmt.go`

### Adding a New Command

1. Add a subcommand `case` in `cmd/main.go` and a `cli.Run*` handler in `cli/`
2. Add an HTTP route in `transport/http/server.go` if it needs backend data
3. Update the `--help` text in `cmd/main.go`

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
- Mapping lives in `providers/llm/openai.go` `translateThinkingLevel()`. Levels: `off|low|medium|high|xhigh`.

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
- **No goroutines in core.** Only the TUI spinner (`transport/tui/components/spinner.go`) uses a goroutine. Agent loop is synchronous.
- **`bufio.Writer` for output.** All terminal output goes through the buffered writer with explicit `flush()`. Never use `fmt.Println` directly.

## Anti-Patterns to Avoid

- ❌ Adding `Complete()` (non-streaming) to Provider interface
- ❌ Importing `transport/` from `agent/` or `providers/`
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
| `transport/http/server.go` | ~960 | HTTP/SSE routes + handlers |
| `agent/session.go` | ~680 | Session lifecycle, history, tool pairing |
| `providers/claude_oauth.go` | ~610 | OAuth token management + streaming |
| `cmd/main.go` | ~555 | Entry point + CLI dispatch |
| `providers/llm/anthropic.go` | ~500 | Anthropic request/response types |

If a file grows past ~500 lines, consider splitting — but only along real boundaries.
