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

Frontend/backend separation: the `agent` emits events over an HTTP/SSE API; the
transports (`tui`, `http`) and the `cli` are pure clients. The binary lives in
`cmd/`, leaving the module root free for an SDK facade.

```
harness/
├── cmd/
│   └── main.go               # Executable entry point (package main), CLI dispatch
├── agent/                    # Core ReAct loop
│   ├── agent.go              # Chat loop, tool execution, MCP + memory wiring, Close()
│   ├── session.go            # Session lifecycle, history, tool pairing
│   ├── prompts.go            # System prompt assembly
│   ├── store/                # Session persistence (JSONL per cwd)
│   ├── resources/            # Skill/resource discovery
│   └── tools/                # Built-in tools
│       ├── bash.go           # Shell execution
│       ├── file.go           # Read, Write
│       ├── edit.go           # Find/replace editing
│       ├── fetch.go          # HTTP client
│       ├── skill.go          # Skill invocation
│       ├── memory.go         # MemoWrite / MemoSearch / MemoDelete
│       └── truncate.go       # PI-style output truncation (head/tail, /tmp overflow)
├── providers/                # LLM provider layer
│   ├── provider.go           # Provider interface (streaming-only)
│   ├── anthropic.go          # Anthropic API key
│   ├── claude_oauth.go       # Claude OAuth (subscription)
│   ├── openai.go             # OpenAI + base for compatible APIs
│   ├── ollama.go             # Ollama local (auto-detect)
│   ├── ollama_cloud.go       # Ollama Cloud
│   ├── opencode_go.go        # OpenCode Go
│   ├── minimax.go            # MiniMax
│   ├── registry.go           # Provider factory (Resolve)
│   ├── status.go             # Provider status for transports
│   ├── authflow/             # Shared OAuth flow (keychain → file → login)
│   └── llm/                  # Core types + metadata cascade + model registry
├── config/                   # Typed settings + credentials managers
│   ├── settings.go           # active_model, thinking_level, providers, mcp
│   ├── credentials.go        # API keys + OAuth tokens (0600)
│   └── manager.go            # Singletons
├── mcp/                      # Model Context Protocol client (stdlib)
│   ├── jsonrpc.go            # JSON-RPC 2.0
│   ├── stdio.go              # Local servers (spawned processes)
│   ├── http.go               # Remote servers (HTTP + SSE + header auth)
│   ├── client.go             # Initialize / ListTools / CallTool
│   └── manager.go            # Eager connect, namespacing, statuses
├── memory/                   # Project-scoped persistent memory
│   ├── store.go              # SQLite + FTS5 (prefix search), cwd-partitioned + global
│   └── adapter.go            # Scoped adapter → agent/tools.MemoryStore
├── cli/                      # CLI command handlers (top-level module)
│   ├── cli.go                # providers, sessions, connect, disconnect
│   ├── settings.go           # settings [set …]
│   ├── memory.go             # memo [<query>] [--all|--global|--content]
│   ├── oauth.go              # connect OAuth flow (delegates to authflow)
│   └── server.go             # In-process HTTP server bootstrap
├── transport/
│   ├── http/                 # HTTP/SSE server (Serve(listener), handler())
│   │   ├── server.go         # Routes: sessions, providers, models, settings, mcp, memories
│   │   └── sse.go            # Event serialization
│   └── tui/                  # Pure-Go terminal UI (zero external TUI libs)
│       ├── tui.go            # Top-level app, banner, autoconnect
│       ├── session.go        # SSE client, history rendering
│       ├── toolfmt.go        # Tool-call arg formatting
│       ├── ansi/             # Color, width, wrap, truncate
│       ├── render/           # Differential rendering engine
│       ├── components/       # Widgets (markdown, history, editor, spinner, …)
│       ├── term/             # Raw terminal + stdin buffer
│       └── keys/             # Key detection
└── types/                    # Shared event + tool types
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
