# Harness

A minimal AI agent harness built in pure Go. Single binary, multi-provider, streaming-first.

```
Agent = LLM + Harness
If you're not the model, you're the harness.
```

## Quick Start

```bash
go install github.com/gurcuff91/harness@latest
harness
```

On first run, connect a provider:
```
harness connect claude-oauth    # Browser OAuth (Claude subscription)
harness connect anthropic       # API key
harness connect openai          # API key
harness connect opencode-go     # API key
harness connect ollama-cloud    # API key
harness connect minimax         # API key
# Ollama local is auto-detected
```
Providers can also be connected from inside the TUI command palette.

## Features

- **Streaming-first** — all providers stream token-by-token
- **Multi-provider** — Claude OAuth, Anthropic, OpenAI, OpenCode Go, Ollama Cloud, Ollama local, MiniMax
- **Thinking support** — extended thinking with configurable levels (off/low/medium/high/xhigh)
- **Tool execution** — Bash, Read, Write, Edit, Fetch, Skill, Subagent
- **MCP** — external tools via Model Context Protocol (local stdio + remote HTTP servers)
- **Persistent memory** — project-scoped + global memories (SQLite + FTS5), recalled across sessions
- **Vision** — image support via file paths or clipboard (Cmd+V)
- **Pure-Go TUI** — from-scratch terminal UI, zero external TUI libraries
- **Auto-detection** — Ollama local auto-connects, models fetched from APIs
- **Zero config** — works with just `harness`, configure via `harness connect`
- **Single binary** — `go build` produces one executable, ~9MB

## Architecture

The **agent is the SDK**. Public packages form the embeddable surface; everything
under `internal/` is implementation detail the Go compiler forbids third parties
from importing. A thin `harness.go` facade at the root re-exports the essentials,
and the binary lives in `cmd/harness/`.

```
harness/
├── harness.go                # 🔓 SDK facade: New, Agent, Session, Options, Event
├── cmd/harness/main.go       # Executable entry point (package main), CLI dispatch
│
├── agent/                    # 🔓 Core ReAct loop — the SDK
│   ├── agent.go              # Chat loop, tool execution, MCP + memory wiring, Close()
│   ├── session.go            # Session lifecycle, history, tool pairing
│   ├── prompts.go            # System prompt assembly
│   ├── store/                # Session persistence — implement custom stores here
│   ├── resources/            # Skill/resource discovery — custom loaders here
│   ├── memory/               # Persistent memory (SQLite + FTS5, cwd + global)
│   └── tools/                # Built-in tools — implement custom tools here
│       ├── bash.go / file.go / edit.go / fetch.go / skill.go
│       └── memory.go / truncate.go / names.go
├── mcp/                      # 🔓 Model Context Protocol client (stdlib)
│   └── jsonrpc.go / stdio.go / http.go / client.go / manager.go
├── types/                    # 🔓 Shared types (Event, Message, ModelMeta)
│
└── internal/                 # 🔒 Implementation detail (not importable by third parties)
    ├── providers/            # LLM provider layer
    │   ├── anthropic.go / claude_oauth.go / openai.go / ollama*.go
    │   ├── opencode_go.go / minimax.go / registry.go / status.go
    │   ├── authflow/         # Shared OAuth flow (keychain → file → login)
    │   └── llm/              # Core LLM types + metadata cascade + model registry
    ├── config/               # Typed settings + credentials managers
    ├── version/              # Build version (ldflags target)
    └── transport/            # Client transports (used by the binary)
        ├── cli/              # CLI command handlers
        ├── http/             # HTTP/SSE server (Serve(listener), handler())
        └── tui/              # Pure-Go terminal UI (zero external TUI libs)
```

### Embedding the SDK

```go
import "github.com/gurcuff91/harness"

a := harness.New(harness.Options{ThinkingLevel: "medium", EnableMCPs: true})
defer a.Close()

sess, _ := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514")
sess.Subscribe(func(e harness.Event) { /* render */ })
sess.Prompt(ctx, "Hello!")
```

## Providers

| Provider | Auth | Models Source | Capabilities Source |
|----------|------|---------------|---------------------|
| `claude-oauth` | OAuth via `claude auth login` | Anthropic `/v1/models` API | API (context, vision, thinking) |
| `anthropic` | `ANTHROPIC_API_KEY` | Anthropic `/v1/models` API | API |
| `openai` | `OPENAI_API_KEY` | Static list | llm-registry (GitHub) |
| `opencode-go` | `OPENCODE_GO_API_KEY` | `/v1/models` API | llm-registry + hardcoded |
| `ollama-cloud` | `OLLAMA_API_KEY` | `/v1/models` + `/api/show` | `/api/show` (context, vision, thinking) |
| `ollama` | None (auto-detect) | `/api/tags` + `/api/show` | `/api/show` |

## Commands

Run `harness` for the interactive TUI, or use subcommands directly:

```
harness                       — Interactive TUI
harness -p <prompt>           — Single-turn CLI (--model, --thinking, --output)
harness http <addr>           — HTTP/SSE server
harness --resume <id>         — Resume a session in the TUI

harness providers             — List providers and status
harness connect <name>        — Connect a provider (api_key optional)
harness disconnect <name>     — Disconnect a provider
harness sessions [--all]      — List sessions (this cwd, or all)
harness delete <id>           — Delete a session

harness settings              — Show core settings
harness settings set <k> <v>  — Set model | thinking
harness mcp [list]            — List MCP servers
harness mcp add <name> ...    — Add MCP server (--local | --remote)
harness mcp rm <name>         — Remove MCP server

harness memo [<query>]        — List (no query) or search memories
harness memo <query> --all    — Search across ALL projects
harness memo --global         — Only global (cross-project) memories
```

Inside the TUI, a command palette exposes session actions (model, thinking,
connect, resume, compact, skills, quit).

## Env Vars

```
ANTHROPIC_API_KEY       — Anthropic API key
OPENAI_API_KEY          — OpenAI API key
OPENCODE_GO_API_KEY     — OpenCode Go API key
OLLAMA_API_KEY          — Ollama Cloud API key
MINIMAX_API_KEY         — MiniMax API key
OLLAMA_URL              — Ollama server URL (default: localhost:11434)
HARNESS_MODEL           — Override default model (provider/model)
```

Thinking level is set via `--thinking` or `harness settings set thinking <level>`
(`settings.json` is the single source of truth).

## Data

All data stored in `~/.harness/`:

```
~/.harness/
├── credentials.json        — API keys + OAuth tokens (0600)
├── settings.json           — Active model, thinking level, providers, MCP servers
└── agent/
    ├── sessions/<cwd>/     — Session history (JSONL, partitioned by project)
    └── memory.db           — Persistent memory (SQLite + FTS5)
```

## Tools

| Tool | Description |
|------|-------------|
| `Bash` | Execute shell commands |
| `Read` | Read files (supports offset/limit) |
| `Write` | Create/overwrite files |
| `Edit` | Find/replace in files |
| `Fetch` | HTTP requests (text + binary) |
| `Skill` | Invoke a discovered skill |
| `Subagent` | Spawn a scoped sub-agent |
| `MemoWrite` / `MemoSearch` / `MemoDelete` | Persistent project + global memory |

External tools can be added via **MCP** servers (`harness mcp add`), namespaced
as `mcp__<server>__<tool>`.

## License

MIT
