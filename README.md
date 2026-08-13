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

- **Streaming-first** — all providers stream token-by-token; there is no non-streaming path
- **Multi-provider** — Claude OAuth, Anthropic, OpenAI, OpenCode Go, Ollama Cloud, Ollama local, MiniMax
- **Thinking support** — extended thinking with configurable levels (off/low/medium/high/xhigh), mapped per-provider
- **Tool execution** — Bash, Read, Write, Edit, Fetch, Skill, Subagent — plus tool calls run in parallel within a turn
- **MCP** — external tools via Model Context Protocol (local stdio + remote HTTP servers)
- **Persistent memory** — project-scoped + global memories (SQLite + FTS5), recalled across sessions
- **Scheduled prompts** — cron-scheduled prompts that fire back into the session that created them
- **Colleagues** — discover other running harness instances on the machine and delegate prompts to them
- **Multiple frontends** — interactive TUI, one-shot CLI, HTTP/SSE server, Telegram bot, Slack bot, ACP agent (Zed)
- **Vision** — image support via file paths or clipboard image paste (Ctrl+V in the TUI)
- **Pure-Go TUI** — from-scratch terminal UI, zero external TUI libraries
- **Auto-detection** — Ollama local auto-connects, models fetched from provider APIs
- **Zero config** — works with just `harness`; configure via `harness connect`
- **Embeddable SDK** — the agent, server, and every transport are importable Go packages
- **Single binary** — `go build` produces one executable, no runtime dependencies

## Architecture

The **agent is the SDK**. Public packages form the embeddable surface — this
includes the core agent, the HTTP/SSE `server`, and the `transports/*`, so an
SDK consumer can programmatically run any frontend on top of an embedded agent.
Everything under `internal/` is implementation detail the Go compiler forbids
third parties from importing. A thin `harness.go` facade at the root re-exports
the essentials, and the binary lives in `cmd/harness/`.

```
harness/
├── harness.go                # 🔓 SDK facade: NewAgent + AgentWith* (construction),
│                             #    RunServer/RunTelegram/RunSlack/RunAcp + their
│                             #    XxxWith* (transport runners), Client/NewClient
├── cmd/harness/main.go       # Executable entry point (package main) — calls cli.Main
│
├── agent/                    # 🔓 Core ReAct loop — the SDK
│   ├── agent.go              # Chat loop, tool execution, MCP + memory + scheduler wiring
│   ├── session.go            # Session lifecycle, ReAct loop, history, tool pairing
│   ├── prompts.go            # System prompt assembly
│   ├── store/                # Session persistence (JSONL per cwd) — custom stores here
│   ├── resources/            # Skill/resource discovery — custom loaders here
│   ├── memory/               # Persistent memory (SQLite + FTS5, cwd + global)
│   └── tools/                # Built-in tools — custom tools here
│       ├── bash.go / file.go / edit.go / fetch.go / skill.go
│       └── memory.go / truncate.go / names.go
├── mcp/                       # 🔓 Model Context Protocol client (stdlib)
│   └── jsonrpc.go / stdio.go / http.go / client.go / manager.go
├── client/                   # 🔓 Typed HTTP/SSE client over the server API
│   └── client.go / types.go / event.go / error.go / stream.go
├── server/                   # 🔓 HTTP/SSE backend — the API all clients talk to
│   └── server.go / sse.go / proxy.go / instances.go / run.go
├── transports/               # 🔓 Interactive session frontends (each runs on server)
│   ├── telegram/             # Telegram bot (stdlib Bot API; one session per chat)
│   ├── slack/                # Slack user bot (one session per channel/DM)
│   └── acp/                  # Agent Client Protocol agent (Zed and other ACP clients)
├── logx/                     # 🔓 Logger contract (interface) + NewNilLogger
├── types/                    # 🔓 Shared types (Event, Message, ModelMeta)
│
└── internal/                 # 🔒 Implementation detail (not importable by third parties)
    ├── providers/            # LLM provider layer (Resolve, streaming)
    │   ├── anthropic.go / claude_oauth.go / openai.go / ollama*.go
    │   ├── opencode_go.go / minimax.go / registry.go / status.go
    │   └── llm/              # Core LLM types + metadata cascade + model registry
    ├── oauthflow/            # Native OAuth PKCE login flows (one file per provider)
    ├── config/               # Typed settings + credentials managers
    ├── logx/                 # NewHarnessLogger() — the concrete line-format logger
    ├── version/              # Build version (ldflags target)
    ├── tui/                  # Pure-Go terminal UI (stays internal — binary frontend)
    └── cli/                  # CLI app: Kong grammar + Run() methods, agent builders
```

### Embedding the SDK

Providers are configured once via the CLI (`harness connect anthropic <key>`);
the SDK then reads that configuration and drives the agent. API-key providers
also work from env vars (`ANTHROPIC_API_KEY`, …) with no CLI step.

```go
import (
	"context"
	"fmt"

	"github.com/gurcuff91/harness"
	"github.com/gurcuff91/harness/types"
)

a := harness.NewAgent(
	harness.AgentWithThinking("medium"),
	harness.AgentWithMCPs(),
	harness.AgentWithMemory(),
)
defer a.Close()

// Discover what's available (configured beforehand via `harness connect`).
for _, m := range a.Models() {
	fmt.Println(m.Model) // e.g. "anthropic/claude-sonnet-4-20250514"
}

sess, _ := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514")
defer sess.Close()

// Async + streaming: drive via events.
sess.Subscribe(func(e types.Event) {
	if e.Type == types.EventStreamTextDelta {
		fmt.Print(e.Delta)
	}
})
sess.Prompt(context.Background(), "Hello!")

// …or synchronous request/response (SDK convenience):
answer, _ := sess.PromptAndWait(context.Background(), "Explain goroutines, briefly.")
fmt.Println(answer)
```

**Provider administration lives in the CLI, not the SDK** — `harness connect`,
`harness disconnect`, `harness providers`. The SDK exposes read-only
`Agent.Providers()` and `Agent.Models()`. This keeps interactive flows (OAuth,
secrets) out of embedded code.

The agent is configured with functional options: `AgentWithThinking`,
`AgentWithMCPs`, `AgentWithMemory`, `AgentWithScheduler`, `AgentWithColleagues`,
`AgentWithMaxIterations`, `AgentWithMaxTokens`, `AgentWithSystemPrompt`,
`AgentWithDirectives`, `AgentWithTools`, `AgentWithDisallowedTools`,
`AgentWithStore`, `AgentWithResourceLoader` (and `AgentWithOptions` to apply a
pre-built config). `NewAgent()` with no options returns a sensible default agent.

### Running a transport on an embedded agent

An already-built agent can be handed to a **runner** — a blocking call that
serves it over a transport until the context is cancelled:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// HTTP/SSE server:
harness.RunServer(ctx, a, harness.ServerWithAddr(":8080"))

// …or a Telegram bot:
harness.RunTelegram(ctx, a, harness.TelegramWithToken(token))

// …or a Slack bot, or an ACP agent (RunSlack / RunAcp).
```

Each `RunX` is a thin alias over its package's own `Run`. A runner's options
configure only the **transport** (listen address, bot credentials, per-session
model/thinking overrides) — the agent itself is already fully configured by the
time it's passed in.

## Providers

| Provider | Auth | Models Source | Capabilities Source |
|----------|------|---------------|---------------------|
| `claude-oauth` | Browser OAuth (PKCE) | Anthropic `/v1/models` API | API (context, vision, thinking) |
| `anthropic` | `ANTHROPIC_API_KEY` | Anthropic `/v1/models` API | API |
| `openai` | `OPENAI_API_KEY` | Static list | llm-registry (GitHub) |
| `opencode-go` | `OPENCODE_GO_API_KEY` | `/v1/models` API | llm-registry + hardcoded |
| `minimax` | `MINIMAX_API_KEY` | Static list | llm-registry + hardcoded |
| `ollama-cloud` | `OLLAMA_CLOUD_API_KEY` | `/v1/models` + `/api/show` | `/api/show` (context, vision, thinking) |
| `ollama` | None (auto-detect) | `/api/tags` + `/api/show` | `/api/show` |

## Commands

Run `harness` for the interactive TUI, or use subcommands directly:

```
harness                       — Interactive TUI (default)
harness -p <prompt>           — Single-turn CLI (--model, --thinking, --output)
harness --resume <id>         — Resume a session in the TUI
harness --scheduler           — Run the TUI with the cron scheduler engine

harness serve <addr>          — HTTP/SSE server (headless transport)
harness telegram              — Run as a Telegram bot (one session per chat)
harness telegram pair <id>    — Allow a chat to use the bot
harness telegram unpair <id>  — Revoke a chat
harness telegram list         — List paired chats
harness telegram token [tok]  — Save the bot token (or check it with --status)
harness slack                 — Run as a Slack user bot (one session per channel/DM)
harness slack login           — Authenticate interactively (saves to ~/.harness/slack.json)
harness slack admin add <id>  — Add an admin (can run /reset /stop /compact /thinking /model)
harness slack admin list      — List current admins
harness slack admin remove    — Remove an admin
harness acp                   — Run as an Agent Client Protocol agent over stdio (for Zed)

harness providers             — List providers and status
harness connect <name> [key]  — Connect a provider (key optional; omit for OAuth)
harness disconnect <name>     — Disconnect a provider
harness sessions [--all]      — List sessions (this cwd, or all)
harness delete <id>           — Delete a session

harness settings              — Show core settings
harness settings set <k> <v>  — Set model | thinking
harness mcp [list]            — List MCP servers
harness mcp add <name> ...    — Add an MCP server (--command → local, --url → remote)
harness mcp rm <name>         — Remove an MCP server
harness mcp enable <name>     — Enable a server
harness mcp disable <name>    — Disable a server (keeps its config)

harness memo [<query>]        — List (no query) or search memories
harness memo <query> --all    — Search across ALL projects
harness memo --global         — Only global (cross-project) memories
harness schedules             — List cron-scheduled prompts (read-only)
```

Inside the TUI, a command palette exposes session actions (model, thinking,
connect, resume, compact, skills, quit).

## Env Vars

```
ANTHROPIC_API_KEY       — Anthropic API key
OPENAI_API_KEY          — OpenAI API key
OPENCODE_GO_API_KEY     — OpenCode Go API key
OLLAMA_CLOUD_API_KEY    — Ollama Cloud API key
MINIMAX_API_KEY         — MiniMax API key
OLLAMA_URL              — Ollama server URL (default: localhost:11434)
TELEGRAM_BOT_TOKEN      — Telegram bot token (for `harness telegram`)
SLACK_WORKSPACE         — Slack workspace URL (for `harness slack`)
SLACK_XOXC              — Slack xoxc- session token
SLACK_XOXD              — Slack xoxd- session cookie
```

Model and thinking level are set via `harness settings set model|thinking <value>`
(or the `--model` / `--thinking` flags per invocation). `settings.json` is the
single source of truth.

## Data

All data stored in `~/.harness/`:

```
~/.harness/
├── credentials.json        — API keys + OAuth tokens (0600)
├── settings.json           — Active model, thinking level, providers, MCP servers
├── instances.json          — Registry of running server instances (for colleagues)
├── schedules.json          — Cron-scheduled prompts
├── telegram.json           — Telegram bot token + paired chats
├── slack.json              — Slack credentials + admin list
└── agent/
    ├── sessions/<cwd>/     — Session history (JSONL, partitioned by project)
    ├── skills/             — Discovered skills
    ├── memory.db           — Persistent memory (SQLite + FTS5, WAL mode)
    └── SYSTEM.md           — Optional user-level system prompt addendum
```

## Tools

| Tool | Description |
|------|-------------|
| `Bash` | Execute shell commands (with timeout + background support) |
| `Read` | Read files/images (supports offset/limit) |
| `Write` | Create/overwrite files |
| `Edit` | Find/replace in files (single or batch edits) |
| `Fetch` | HTTP requests (text + binary downloads) |
| `Skill` | Load a discovered skill |
| `Subagent` | Spawn a scoped autonomous sub-agent (parallelizable) |
| `MemoWrite` / `MemoSearch` / `MemoDelete` | Persistent project + global memory |
| `Schedule` / `ScheduleList` / `ScheduleDelete` | Cron-scheduled prompts |
| `ColleagueList` / `ColleagueAsk` | Discover and delegate to other running instances |

The memory tools require `AgentWithMemory` (or the CLI's memory-enabled path);
`Schedule*` management tools are always available, while the engine that *fires*
schedules requires `--scheduler` / `AgentWithScheduler`; `Colleague*` requires
`AgentWithColleagues`. External tools can be added via **MCP** servers
(`harness mcp add`), namespaced as `mcp__<server>__<tool>`.

## License

MIT
