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
→ /connect claude-oauth    # Browser OAuth (Claude subscription)
→ /connect anthropic       # API key
→ /connect openai          # API key
→ /connect opencode-go     # API key
→ /connect ollama-cloud    # API key
# Ollama local is auto-detected
```

## Features

- **Streaming-first** — all providers stream token-by-token
- **Multi-provider** — Claude OAuth, Anthropic, OpenAI, OpenCode Go, Ollama Cloud, Ollama local
- **Thinking support** — extended thinking with configurable levels (disable/low/medium/high/xhigh)
- **Tool execution** — bash, read_file, write_file, edit, fetch
- **Vision** — image support via file paths or clipboard (Cmd+V)
- **Auto-detection** — Ollama local auto-connects, models fetched from APIs
- **Model switching** — `/model <provider/model>` to switch at runtime
- **Zero config** — works with just `./harness`, configure via `/connect` commands
- **Single binary** — `go build` produces one executable, ~9MB

## Architecture

```
harness/
├── agent/                    # Core ReAct loop + events
│   ├── agent.go              # Chat loop, tool execution
│   └── event.go              # Event types for transport layer
├── config/                   # Minimal config loader
├── llm/                      # Core types + utilities
│   ├── provider.go           # Provider interface (streaming-only)
│   ├── types.go              # Request, Response, StreamEvent
│   ├── sse.go                # SSE stream parser
│   ├── image.go              # Image loader
│   ├── providers/            # Provider implementations
│   │   ├── anthropic.go      # Anthropic API key
│   │   ├── claude_oauth.go   # Claude OAuth (subscription)
│   │   ├── openai.go         # OpenAI + base for compatible APIs
│   │   ├── ollama.go         # Ollama local (auto-detect)
│   │   ├── ollama_cloud.go   # Ollama Cloud
│   │   ├── opencode_go.go    # OpenCode Go
│   │   ├── catalog.go        # In-memory model cache
│   │   ├── credentials.go    # API keys + OAuth tokens
│   │   ├── settings.go       # User settings persistence
│   │   ├── status.go         # Provider status for transports
│   │   └── model_registry.go # Model capabilities (registry + hardcoded + inference)
│   └── registry/
│       └── resolve.go        # Provider factory
├── tools/                    # Built-in tools
│   ├── bash.go               # Shell execution
│   ├── file.go               # read_file, write_file
│   ├── edit.go               # Find/replace editing
│   └── fetch.go              # HTTP client
└── transport/cli/            # Terminal UI
    ├── cli.go                # REPL, commands, banner
    ├── render.go             # Streaming renderer + footer
    ├── colors.go             # ANSI colors
    ├── rawinput.go           # Raw terminal input (Ctrl+V)
    └── clipboard.go          # Clipboard image paste
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

```
/connect              — List providers and status
/connect <provider>   — Connect a provider
/model                — List available models
/model <prov/model>   — Switch active model
/thinking             — Show current thinking level
/thinking <level>     — Set thinking (disable/low/medium/high/xhigh)
/clear                — Reset conversation history
/help                 — Full command reference
/exit                 — Quit
```

## Env Vars

```
ANTHROPIC_API_KEY       — Anthropic API key
OPENAI_API_KEY          — OpenAI API key
OPENCODE_GO_API_KEY     — OpenCode Go API key
OLLAMA_API_KEY          — Ollama Cloud API key
OLLAMA_URL              — Ollama server URL (default: localhost:11434)
HARNESS_MODEL           — Override default model (provider/model)
HARNESS_THINKING        — Thinking level (disable/low/medium/high/xhigh)
```

## Data

All data stored in `~/.harness/`:

```
~/.harness/
├── credentials.json   — API keys + OAuth tokens
└── settings.json      — Active model + thinking level
```

## Tools

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands |
| `read_file` | Read files (supports offset/limit) |
| `write_file` | Create/overwrite files |
| `edit` | Find/replace in files |
| `fetch` | HTTP requests (GET/POST/PUT/DELETE) |

## License

MIT
