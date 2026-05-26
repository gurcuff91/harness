# AGENTS.md — AI Agent Guide for Harness

> Instructions for AI coding agents working on this codebase.

## Project Identity

- **What:** Minimal AI agent harness — a CLI tool that connects LLMs to tools via a ReAct loop
- **Language:** Go 1.24+
- **Module:** `github.com/gurcuff91/harness`
- **Binary:** Single binary, ~9MB
- **Dependencies:** Only `github.com/charmbracelet/x/term` (raw terminal input). Keep it that way.

## Golden Rules

1. **No new dependencies** without explicit owner approval. Solve problems with stdlib first.
2. **Always streaming.** There is no non-streaming path. Every provider implements `CompleteStream()`. Never add `Complete()`.
3. **`provider/model` format everywhere.** Settings, env vars, CLI display, Resolve — all use `provider/model` (e.g., `anthropic/claude-sonnet-4-20250514`).
4. **Backend/frontend separation.** `llm/` and `agent/` never import `transport/`. The agent emits events; the transport renders them.
5. **In-memory only.** No file caching for models or state beyond `~/.harness/credentials.json` and `~/.harness/settings.json`.

## Architecture

```
main.go                         ← entry point, wiring
├── agent/                      ← core ReAct loop + built-in tools
│   ├── agent.go                ← Chat() loop, tool execution
│   ├── event.go                ← Event types emitted to transport
│   └── tools/                  ← Built-in tools (package tools)
│       ├── registry.go         ← Tool registry (Register, Run, Definitions)
│       ├── bash.go             ← Shell execution
│       ├── file.go             ← read_file, write_file
│       ├── edit.go             ← Find/replace editing
│       └── fetch.go            ← HTTP client (text + binary via output_path)
├── llm/                        ← LLM abstraction layer
│   ├── provider.go             ← Provider interface (7 methods)
│   ├── types.go                ← Request, Response, StreamEvent, ToolCall, Usage
│   ├── sse.go                  ← SSE stream parser (shared)
│   ├── image.go                ← Base64 image loader
│   ├── providers/              ← Provider implementations + infrastructure
│   │   ├── anthropic.go        ← Anthropic API (Messages API)
│   │   ├── claude_oauth.go     ← Claude OAuth (subscription, token refresh)
│   │   ├── openai.go           ← OpenAI-compatible base (DeepSeek, Ollama, OpenCode Go)
│   │   ├── ollama.go           ← Ollama local (auto-detect via ping)
│   │   ├── ollama_cloud.go     ← Ollama Cloud (API key)
│   │   ├── opencode_go.go      ← OpenCode Go
│   │   ├── catalog.go          ← In-memory model cache, RefreshModels(), ModelMeta
│   │   ├── credentials.go      ← ~/.harness/credentials.json read/write
│   │   ├── settings.go         ← ~/.harness/settings.json read/write
│   │   ├── status.go           ← GetProviderStatuses(), GetModelGroups()
│   │   └── model_registry.go   ← enrichMeta(), ApplyRegistryPricing(), ModelSupportsThinking()
│   └── registry/
│       └── resolve.go          ← Resolve("provider/model") → Provider constructor
└── transport/cli/              ← Terminal UI (only consumer of agent events)
    ├── cli.go                  ← REPL loop, commands (/model, /connect, /thinking, etc.)
    ├── render.go               ← Streaming renderer, spinner, footer
    ├── colors.go               ← ANSI color helpers
    ├── rawinput.go             ← Raw terminal input (multiline, Ctrl+V)
    └── clipboard.go            ← Clipboard image paste (macOS/Linux/Windows)
```

## Key Interfaces

### Provider (`llm/provider.go`)

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
go build -o harness .     # build binary
go vet ./...              # lint
./harness                 # run
```

### Adding a New Provider

1. Create `llm/providers/<name>.go`
2. Implement the `llm.Provider` interface (all 5 methods)
3. Add constructor to `llm/registry/resolve.go` in the `Resolve()` switch
4. Add provider key to `llm/providers/providers.go`
5. Add to `llm/providers/catalog.go` `RefreshModels()` and `DetectAvailable()`
6. Add connect handler in `transport/cli/cli.go` `handleConnect()`
7. Add credential storage in `llm/providers/credentials.go`

### Adding a New Tool

1. Create `agent/tools/<name>.go`
2. Define the `Tool` struct with JSON schema and Execute function
3. Register in `main.go` where other tools are registered
4. Add tool icon in `transport/cli/render.go` `renderToolCall()`

### Adding a New Command

1. Add to the switch in `transport/cli/cli.go` `handleCommand()`
2. Add help text in the `/help` handler
3. Commands always start with `/`

## Thinking System

Universal levels mapped per-provider:

| Level | Anthropic | OpenAI (o-series) | DeepSeek | Ollama |
|-------|-----------|-------------------|----------|--------|
| `disable` | thinking off | — | — | `think: false` |
| `low` | `effort: low` | `low` | `low` | `think: true` |
| `medium` | `effort: medium` | `medium` | `medium` | `think: true` |
| `high` | `effort: high` | `high` | `high` | `think: true` |
| `xhigh` | `effort: high` | `high` | `high` | `think: true` |

- Anthropic 4.6+: uses `effort` param (adaptive). Older: `budget_tokens` (legacy).
- DeepSeek: `reasoning_content` must be replayed in assistant messages when tool calls follow.
- Mapping lives in `llm/providers/openai.go` `translateThinkingLevel()`.

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
- **No goroutines in core.** Only the spinner in `transport/cli/render.go` uses a goroutine. Agent loop is synchronous.
- **`bufio.Writer` for output.** All terminal output goes through the buffered writer with explicit `flush()`. Never use `fmt.Println` directly.

## Anti-Patterns to Avoid

- ❌ Adding `Complete()` (non-streaming) to Provider interface
- ❌ Importing `transport/` from `agent/` or `llm/`
- ❌ File-based model cache
- ❌ Multiple spinner goroutines running simultaneously
- ❌ Direct `fmt.Print` to stdout (use `printf()` + `flush()`)
- ❌ Blocking the SSE read loop
- ❌ Adding dependencies without approval

## File Size Guide

Keep files focused. Current largest files for reference:

| File | Lines | Role |
|------|-------|------|
| `transport/cli/render.go` | ~660 | Streaming renderer (complex by nature) |
| `llm/providers/claude_oauth.go` | ~530 | OAuth token management + streaming |
| `llm/providers/anthropic.go` | ~460 | Anthropic Messages API |
| `transport/cli/cli.go` | ~420 | REPL + all commands |
| `llm/providers/openai.go` | ~410 | OpenAI-compatible base |

If a file grows past ~500 lines, consider splitting — but only along real boundaries.
