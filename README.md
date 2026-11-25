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
harness connect codex-oauth     # Browser OAuth (ChatGPT/Codex subscription)
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
- **Multi-provider** — Claude OAuth, Codex OAuth (ChatGPT), Anthropic, OpenAI, OpenCode Go, Ollama Cloud, Ollama local, MiniMax
- **Thinking support** — extended thinking with configurable levels (off/low/medium/high/xhigh), mapped per-provider
- **Tool execution** — Bash, Read, Write, Edit, Fetch, Skill, Subagent — plus tool calls run in parallel within a turn
- **Web search** — live web search (MiniMax primary, Ollama Cloud fallback) via the `WebSearch` tool
- **Session awareness** — `SessionInfo` gives the model a snapshot of its own identity, config, environment, and accumulated usage
- **MCP** — external tools via Model Context Protocol (local stdio + remote HTTP servers)
- **Persistent memory** — project-scoped + global memories (SQLite + FTS5), recalled across sessions
- **Scheduled prompts** — cron-scheduled prompts that fire back into the session that created them
- **Colleagues** — every session started with `--with-colleagues` gets a name (`scorpion-hero`) and can ask the other sessions on the machine, which answer asynchronously
- **Multiple frontends** — interactive TUI, one-shot CLI, HTTP/SSE server, ACP agent (Zed), and input channels (Slack, Telegram) that let a session listen to a conversation
- **Vision** — image support via file paths or clipboard image paste (Ctrl+V in the TUI)
- **Pure-Go TUI** — from-scratch terminal UI, zero external TUI libraries
- **Auto-detection** — Ollama local auto-connects, models fetched from provider APIs
- **Zero config** — works with just `harness`; configure via `harness connect`
- **Embeddable SDK** — the agent, server, and every transport are importable Go packages
- **Single binary** — `go build` produces one executable, no runtime dependencies

## Architecture

The **agent is the SDK**. Public packages form the embeddable surface — this
includes the core agent, the HTTP/SSE `server`, `acp` and the `channels/*`, so an
SDK consumer can programmatically run any frontend on top of an embedded agent.
Everything under `internal/` is implementation detail the Go compiler forbids
third parties from importing. A thin `harness.go` facade at the root re-exports
the essentials, and the binary lives in `cmd/harness/`.

```
harness/
├── harness.go                # 🔓 SDK facade: NewAgent + AgentWith* (construction),
│                             #    RunServer/RunAcp + their
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
│       └── memory.go / session.go / websearch.go / truncate.go / names.go
├── mcp/                       # 🔓 Model Context Protocol client (stdlib)
│   └── jsonrpc.go / stdio.go / http.go / client.go / manager.go
├── client/                   # 🔓 Typed HTTP/SSE client over the server API
│   └── client.go / types.go / event.go / error.go / stream.go
├── server/                   # 🔓 HTTP/SSE backend — the API all clients talk to
│   └── server.go / sse.go / proxy.go / run.go
├── acp/                      # 🔓 Agent Client Protocol agent (Zed and other ACP clients)
├── channels/                 # 🔓 Input channels (agent.Channel): a session listens to a conversation
│   ├── slack/                # Slack conversations + the Slack login
│   ├── telegram/             # A Telegram bot (trusted @usernames) + the bot token
│   └── colleague/            # The other agents of this machine (--with-colleagues)
├── logx/                     # 🔓 Logger contract + NewNilLogger (silent) + NewHarnessLogger (the CLI's line format)
├── types/                    # 🔓 Shared types (Event, Message, ModelMeta)
│
└── internal/                 # 🔒 Implementation detail (not importable by third parties)
    ├── providers/            # LLM provider layer (Resolve, streaming)
    │   ├── anthropic.go / claude_oauth.go / codex_oauth.go / openai.go / ollama*.go
    │   ├── opencode_go.go / minimax.go / registry.go / status.go
    │   └── llm/              # Core LLM types + metadata cascade + model registry
    ├── oauthflow/            # Native OAuth PKCE login flows — server-side only,
    │                         #   driven entirely by server/oauth.go's POST /api/oauth/{provider}
    ├── browseropen/          # Opens a URL in the user's default browser (CLI/TUI only)
    ├── config/               # Typed settings + credentials managers
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
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/mcp"
	"github.com/gurcuff91/harness/types"
)

// MCP is opt-in too: the servers `harness mcp add` configured, plus your own.
servers := mcp.ServersFromSettings()
servers["docs"] = types.MCPServer{URL: "https://example.com/mcp"}

// Memory is opt-in: pass any memory.Store (here the default SQLite one).
mem, err := memory.OpenSQLite("") // "" = ~/.harness/agent/memory.db
if err != nil {
	// your call: fail, or run without memory
}

a := harness.NewAgent(
	harness.AgentWithThinking("medium"),
	harness.AgentWithMCPs(servers), // spawned now, terminated by a.Close()
	harness.AgentWithMemory(mem), // the agent owns it: a.Close() closes it
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

**Built-in provider administration lives in the CLI, not the SDK** —
`harness connect`, `harness disconnect`, `harness providers`. The SDK exposes
read-only `Agent.Providers()` and `Agent.Models()`. This keeps interactive
flows (OAuth, secrets) out of embedded code.

**Custom OpenAI-compatible providers are the one exception** — register one
programmatically, globally for the process, before building any `Agent`.
These providers have no `harness connect` concept: they're always active,
and any auth they need travels as static request headers you configure —
never a stored API key:

```go
harness.NewOpenAIProvider("my-proxy", "https://my-proxy.internal/v1",
	harness.ProviderWithDisplay("Acme Internal Proxy"),
	harness.ProviderWithHeaders(map[string]string{
		"X-Api-Key": apiKey, // or Authorization, or whatever the gateway expects
		"X-Org-Id":  "acme",
	}),
)
// now usable like any built-in: a.NewSession(cwd, "my-proxy/some-model")
```

No `settings.json` entry needed. Use
`harness.ProviderWithFetchModels(func() ([]types.ModelMeta, error))` to
replace the default `<url>/models` discovery with custom logic — capture
whatever auth the hook needs in its own closure.

If the endpoint fronts a MiniMax-compatible backend, add
`harness.ProviderWithReasoningSplit()` — without it, MiniMax emits its
thinking *inline* inside the answer as literal `<think>...</think>`
instead of a separate reasoning stream; this sends `"reasoning_split":
true` on every request so thinking routes to `EventStreamThinkingDelta`
like any other provider's. The option takes no argument (calling it always
means "on" — simply omit it to leave the flag off, which is the default
for any other backend); the real OpenAI API rejects an unrecognized
`reasoning_split` argument outright, so this must stay opt-in.

**Where configuration lives is pluggable too.** Settings and credentials are
read and written through two persistence ports in `configstore` —
`SettingsStore` (active model, thinking level, MCP servers, custom
providers) and `CredentialsStore` (API keys, OAuth tokens). Both are a
small namespace/key/JSON-value store; harness keeps all the typed logic on
top. Register your own implementations once, before building any `Agent`,
to keep everything out of the filesystem — e.g. a database for settings
and a cloud secret manager for credentials:

```go
harness.SetSettingsStore(mySettingsDB)       // implements configstore.SettingsStore
harness.SetCredentialsStore(mySecretManager) // implements configstore.CredentialsStore
```

Without them, harness uses `configstore.FileStore` over
`~/.harness/settings.json` and `~/.harness/credentials.json`.
`configstore.NewInMemoryStore()` keeps everything in memory for the life of
the process.

The agent is configured with functional options: `AgentWithThinking`,
`AgentWithMCPs`, `AgentWithMemory`, `AgentWithScheduler`, `AgentWithChannels`,
`AgentWithWebSearch`, `AgentWithTerminal`, `AgentWithMaxIterations`,
`AgentWithMaxTokens`, `AgentWithSystemPrompt`, `AgentWithDirectives`,
`AgentWithTools`, `AgentWithDisallowedTools`, `AgentWithStore`,
`AgentWithResourceLoader` (and `AgentWithOptions` to apply a pre-built
config). `NewAgent()` with no options returns a sensible default agent.
`SessionInfo` (a small, purely informational session snapshot) is always
registered like any other built-in tool — no dedicated option; pass it to
`AgentWithDisallowedTools("SessionInfo")` to turn it off.

### Running a transport on an embedded agent

An already-built agent can be handed to a **runner** — a blocking call that
serves it over a transport until the context is cancelled:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// HTTP/SSE server:
harness.RunServer(ctx, a, harness.ServerWithAddr(":8080"))

// …or an ACP agent (RunAcp).
```

Each `RunX` is a thin alias over its package's own `Run`. A runner's options
configure only the **transport** (listen address and the like) — the agent itself is already fully configured by the
time it's passed in.

## Providers

| Provider | Auth | Models Source | Capabilities Source |
|----------|------|---------------|---------------------|
| `claude-oauth` | Browser OAuth (PKCE, Claude subscription) | Anthropic `/v1/models` API | API (context, vision, thinking) |
| `codex-oauth` | Browser OAuth (PKCE, ChatGPT/Codex subscription) | Codex backend `/models` API | API (context window authoritative) |
| `anthropic` | `ANTHROPIC_API_KEY` | Anthropic `/v1/models` API | API |
| `openai` | `OPENAI_API_KEY` | Static list | OpenRouter catalog |
| `opencode-go` | `OPENCODE_GO_API_KEY` | `/v1/models` API | OpenRouter + hardcoded |
| `synthetic` | `SYNTHETIC_API_KEY` | `/openai/v1/models` API (only `hf:` text models) | API (context, vision, thinking, prices incl. cache) |
| `minimax` | `MINIMAX_API_KEY` | Static list | OpenRouter + hardcoded |
| `ollama-cloud` | `OLLAMA_CLOUD_API_KEY` | `/v1/models` + `/api/show` | `/api/show` (context, vision, thinking) |
| `ollama` | None (auto-detect) | `/api/tags` + `/api/show` | `/api/show` |

Beyond these built-ins, `harness provider add` registers a **custom
provider** — any proxy, gateway, or self-hosted endpoint speaking the OpenAI
Chat Completions dialect (`--type openai`, the default) or the Anthropic
Messages dialect (`--type anthropic`). Configured in `settings.json`'s
`"provider"` collection (same singular style as `"mcp"`), read at process
start; changes are applied live with `harness.ReloadCustomProviders()` or
`POST /api/settings/provider/reload` (`client.ReloadCustomProviders`) — no
restart needed.
These providers have no credential concept of their own — `harness connect`
is rejected for them — so any auth the gateway needs (a bearer token, an
`X-Api-Key`, whatever) goes in `--header` at add time and is sent on every
request. Once added, `<name>/<model>` works exactly like any built-in
provider — no further setup needed.

**Sharing a subscription between harness instances.** Every provider is also
served by the server's transparent proxy (`/api/providers/<name>/proxy`), and a
custom provider can point at it. Instance A has `claude-oauth` connected and
runs `harness serve :8080`; instance B (another machine, another TUI) does

```
harness provider add shared-claude --type anthropic \
  --url http://A:8080/api/providers/claude-oauth/proxy
```

and uses `shared-claude/<model>` exactly like the native provider — no
credential on B at all: A's proxy applies the OAuth token and the Claude Code
identity. The same works for any OpenAI-dialect provider with `--type openai`
and `--url http://A:8080/api/providers/<name>/proxy` (note the difference: for
`anthropic` the URL has no `/v1`, for `openai` it is the chat-completions base).
The one dialect not yet consumable this way is `codex-oauth`'s (Responses).

**Subscription usage.** `GET /api/providers/<name>/quota` (Go:
`client.ProviderQuota(name, refresh)`) reports how much of a subscription is
used, in one format for every provider: `windows` (`used_percent` 0–100 and
`resets_at`, UTC) and, for providers that meter dollars, a `balance`.
Supported: `claude-oauth`, `codex-oauth`, `minimax`, `synthetic`,
`opencode-go`, `ollama-cloud`. Readings are cached for 60 s (`?refresh=true`
asks again, at most once every 5 s); when the provider cannot be read the last
good reading comes back with `"stale": true`.

```
$ curl -s localhost:8080/api/providers/claude-oauth/quota
{"provider":"claude-oauth","fetched_at":"2026-11-21T14:15:01Z","windows":[
  {"name":"session","label":"5-hour session","used_percent":23,"resets_at":"2026-11-21T18:20:00Z"},
  {"name":"weekly","label":"Weekly (all models)","used_percent":31,"resets_at":"2026-11-26T17:00:00Z"}]}
```

SDK embedders can instead register one programmatically —
`harness.NewOpenAIProvider(name, url, ...opts)` (or
`harness.NewAnthropicProvider`) with
`harness.ProviderWithHeaders(...)` carrying the auth — without touching
`settings.json` at all; see [Embedding the SDK](#embedding-the-sdk) below.

## Commands

Run `harness` for the interactive TUI, or use subcommands directly:

```
harness                       — Interactive TUI (default)
harness "<task>"              — Interactive TUI with that first message already submitted
harness -p <prompt>           — Single-turn CLI (--model, --thinking, --output)
harness --resume <id>         — Resume a session in the TUI
harness --with-colleagues     — Make every session a colleague: a name, and ColleagueList/ColleagueAsk to talk to the others
harness --with-scheduler      — Run the TUI with the cron scheduler engine
```

**Light by default; extras on request.** The agent every command builds has the base tools (Bash, Read,
Write, Edit, Fetch, SessionInfo, Subagent, memory, Skill) and WebSearch, and nothing else. Two extras are
opt-in because each adds to every request whether it is used or not (colleagues about 2.5 KB of system
prompt and 3 KB of tool definitions): `--with-colleagues` and `--with-scheduler`, on the TUI and `serve`. Each can also be switched on by an environment variable, either one being enough:
`HARNESS_WITH_COLLEAGUES=1` and `HARNESS_WITH_SCHEDULER=1` (`1`/`true`/`yes` on, `0`/`false`/`no` or empty
off; any other value is an error that names the variable). `harness acp` is started by an editor and takes
no flags, so for it the variables are the only way. `-p` never has the extras.

**Colleagues.** With `--with-colleagues`, every session the process opens (new, resumed or forked) gets a
random name like `scorpion-hero` and listens for the other agents of the machine on a Unix socket in a
directory only you can enter (`~/.harness/run`). The model gets `ColleagueList` (who is online and the
project each works in) and `ColleagueAsk`, which is asynchronous: it returns as soon as the message is
delivered, and the answer comes back later as a new prompt (`<colleague from="raiden-god" reply>…`). The
session that is asked answers automatically when its turn ends. A session that closes takes its
conversations with it. The TUI shows the name in its channels line (`channels [colleague: scorpion-hero]`).
From the SDK: `harness.AgentWithChannels(colleague.New())` — registering the channel is enough, every
session the agent opens becomes a colleague with a name of its own.

**A first message.** `harness "fix the login bug"` (or `harness fix the login bug` — the words are
joined with a space) opens the normal interactive TUI and submits that message as if you had typed
it, so the session stays watchable, steerable and open for follow-up turns. This is what
orchestrators pass (Herdr's `tab.new --task`, `hc run … "task"`). It combines with the other flags
(`--model`, `--thinking`, `--resume <id>`: the message becomes that session's next turn). A message
that starts with `/` or `!` is sent to the agent as text, never run as a command, and it reaches
the agent byte for byte (newlines, accents, emoji). If there is no provider at all, nothing is sent
and the message is left in the editor so it is not lost. `-p` is the one-shot mode with no TUI; it
cannot be combined with a message (`use either -p (one-shot) or a positional prompt (interactive)`).

**Inside Herdr.** When the TUI runs in a [Herdr](https://herdr.dev) terminal (herdr-core sets
`HC_TERMINAL_ID`), its system prompt gets a short `## Herdr` section: where the model is, that a prompt
wrapped in `<hc from="…">…</hc>` was sent by another agent and not by you, and where to read the rest
(`hc --skill`, `herdr-app --skill`). Only the interactive TUI gets it (it is the only frontend Herdr wraps
messages for); outside Herdr there is no change.

**`--model`, `--thinking` and `--resume` are requirements, not hints.** Either the session runs
exactly as asked, or `harness` prints `error: …` and exits 1 *before* the TUI opens — it never falls
back to another model or conversation and never ignores the flag, so a task cannot run on a model or
in a session you did not choose. `harness --model zzz/x "task"` and `harness --resume <missing id>
"task"` exit 1 and run nothing; `harness --resume <id> --model X` switches the session or fails and
leaves it untouched. Fallbacks apply only to what you did not pass (your settings model, then the
first active one). `-p` rejects `--resume` (`--resume is not supported with -p`): a one-shot runs on
a throwaway session, so there is nothing to resume. Inside the running TUI, `/model`, `/resume`,
`/new` and `/fork` still warn instead of failing.

**Slack input channels.** `harness --slack-channel producto-y` (repeatable; a channel id, a
name with or without `#`, a DM id, or a user id for their DM) makes the session you open listen to
that Slack conversation with the account saved by `harness slack login`: a mention of that account
there (in a DM, any message) becomes a prompt of THIS session, which has your project's context, and
everything it writes while answering is posted back in that conversation. Your own turns in the TUI
are never posted. The session gets the Slack tools (`SlackListChannels`, `SlackListUsers`,
`SlackPost`, `SlackMessages`) and a `## Slack` section in its system prompt. One conversation is
listened to by one session at a time, across every harness process on the machine: a second one
fails naming the first. Explicit and never saved: `/new`, `/resume` and `--resume` without the flag
listen to nothing. Over the API (`harness serve`), pass `channels: [{"type":"slack","ref":"…"}]`
when creating or resuming a session. `harness slack login --status` checks the saved login.

**Telegram input channel.** `harness --telegram-allow @you` (repeatable, or comma-separated; or
`TELEGRAM_ALLOW`) makes the session you open listen to a Telegram bot: messages from those trusted
@usernames — in a private chat or a group — become prompts of THIS session, and its reply to each goes
back to the chat it came from. Anyone else never reaches the session: they get one notice saying they are
not allowed and how the owner can add them (a bot is public: anyone can find it). The bot token comes from `--telegram-token`, else `TELEGRAM_BOT_TOKEN`, else the one saved
with `harness telegram token <tok>` (verified before it is saved; `--status` checks it, `--delete`
removes it). Photos arrive as images and saved files, documents as files (`<telegram-attach>`), and
the model sends files back with `<telegram-upload path="…"/>`. `/info` and `/context` are answered in
the chat without the model; nothing in a chat can change the session's settings. One bot is listened
to by one session at a time, across processes. A schedule created from a Telegram (or Slack)
conversation fires back into it while the session listens there. Over the API (`harness serve
--telegram-token …`), pass `channels: [{"type":"telegram","ref":"you,friend"}]`: the ref is the list
of trusted usernames. From the SDK: `harness.AgentWithChannels(telegram.New(token))`, then
`agent.WithSessionChannels(types.InputChannel{Type: "telegram", Ref: "you"})`.

A command name wins over a message: `harness sessions` lists sessions. To send a message that is
exactly a command name, put it after `--`: `harness -- serve`.

A mistyped command is an error, not a message: `harness sesions` prints `unknown command "sesions"`
and `did you mean: harness sessions` (exit 1, no TUI, no model call) — the same rule Claude Code
applies (a first word within one edit of a command name). Anything further from a command is a
message (`harness OK`, `harness fix the login bug`). To send a word that looks like a command, use
`harness -- sesions` (the message) or `harness -p sesions` (a one-shot answer). `harness help`,
`harness help <command>`, `harness version` and `-v/--version` do what you would expect.

```
harness serve <addr>          — HTTP/SSE server (headless transport)
harness --telegram-allow @you — Listen to the Telegram bot (trusted users; --telegram-token to pick the bot)
harness telegram token [tok]  — Save the bot token (--status checks it, --delete removes it)
harness slack login           — Save the Slack account input channels listen with (--status checks it)
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
harness provider [list]       — List custom providers
harness provider add <name> --url <url> [--type openai|anthropic] [--models-url <url>] [--header K:V ...] [--display <name>] [--disabled] [--reasoning-split]
harness provider rm <name>    — Remove a custom provider

harness memo [<query>]        — List (no query) or search memories
harness memo <query> --all    — Search across ALL projects
harness memo --global         — Only global (cross-project) memories
harness schedules             — List cron-scheduled prompts (read-only)
```

**Export and import a session.** `/export` in the TUI writes the session's whole conversation to
`~/.harness/exports/<name>-<id>.export.jsonl` (the first line describes the session, every other
line is one of its messages); `/import <file>` opens such a file as a NEW session — an exact copy,
like a fork — and switches to it, leaving the session you were in untouched. Over the API,
`GET /api/sessions/{id}/export` downloads an export and `POST /api/sessions/import` (the file as the
body, `?cwd=` optional) creates the session from one; `client.ExportSession` / `client.ImportSession`
wrap them. Sessions of hundreds of MB stream both ways.

Memories are also fully manageable over the HTTP API (`harness serve`, docs at
`/api/docs`) and the Go SDK client: `GET /api/memories` lists/searches
(`client.SearchMemories`), and `GET`/`PUT`/`DELETE /api/memories/{slug}?cwd=…`
read, upsert and delete one memory (`client.GetMemory`/`PutMemory`/
`DeleteMemory`). `cwd` is the project path, or `<global>`
(`client.MemoryGlobalCWD`) for a global memory.

Inside the TUI, a command palette exposes session actions (model, thinking,
connect, resume, compact, skills, quit).

## Env Vars

```
ANTHROPIC_API_KEY       — Anthropic API key
OPENAI_API_KEY          — OpenAI API key
OPENCODE_GO_API_KEY     — OpenCode Go API key
SYNTHETIC_API_KEY       — Synthetic (synthetic.new) API key
OLLAMA_CLOUD_API_KEY    — Ollama Cloud API key
MINIMAX_API_KEY         — MiniMax API key
OLLAMA_URL              — Ollama server URL (default: localhost:11434)
TELEGRAM_BOT_TOKEN      — Telegram bot token (the TUI and serve; --telegram-token wins over it)
TELEGRAM_ALLOW          — Trusted Telegram @usernames, comma-separated (like --telegram-allow)
```

Model and thinking level are set via `harness settings set model|thinking <value>`
(or the `--model` / `--thinking` flags per invocation). `settings.json` is the
single source of truth.

## Data

All data stored in `~/.harness/`:

```
~/.harness/
├── credentials.json        — Secrets (0600): {"providers": {"<name>": {...}},
│                              "telegram": {"bot_token": ...}, "slack": {"session": {xoxc, xoxd}}}
├── settings.json           — Everything non-sensitive, one namespace per domain:
│                              core (active_model, thinking_level), mcp, provider,
│                              schedules (cron prompts),
│                              slack (workspace/user/team), channels (input-channel claims)
└── agent/
    ├── sessions/<cwd>/     — Session history (JSONL, partitioned by project)
    ├── skills/             — Discovered skills
    ├── memory.db           — Persistent memory (SQLite + FTS5, WAL mode)
    └── SYSTEM.md           — Optional user-level system prompt addendum — a
                               project can override it entirely with its own
                               <project>/.harness/agent/SYSTEM.md
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
| `WebSearch` | Web search (MiniMax primary, Ollama Cloud fallback) |
| `SessionInfo` | Snapshot of the current session — identity (id/cwd/name/model/thinking/created_at), environment (harness version, connected MCPs, owned schedules), and accumulated usage (tokens/cache/cost/context) |
| `MemoWrite` / `MemoSearch` / `MemoDelete` | Persistent project + global memory |
| `Schedule` / `ScheduleList` / `ScheduleDelete` | Cron-scheduled prompts |
| `ColleagueList` / `ColleagueAsk` | List the other sessions of the machine and message one (asynchronous) |

`SessionInfo` is always registered, like Bash/Read/Write/Edit/Fetch — no
dedicated option, just `AgentWithDisallowedTools("SessionInfo")` to turn it
off. The memory tools require a store via `AgentWithMemory(store)` —
`memory.OpenSQLite(path)` for the default SQLite backend, or any custom
`memory.Store` (the CLI always passes the SQLite one); `WebSearch` requires `AgentWithWebSearch` and a
connected search backend (minimax, ollama-cloud or synthetic); `Schedule*` management
tools AND the engine that fires them both require `--with-scheduler` /
`AgentWithScheduler` — they travel together, so an instance without it
neither manages nor executes schedules;
`Colleague*` requires `--with-colleagues` (the colleague channel). External tools can be added via
**MCP** servers, namespaced as `mcp__<server>__<tool>` — configured with
`harness mcp add` (the CLI always loads those), or passed programmatically
per agent via `AgentWithMCPs(servers)` (`mcp.ServersFromSettings()` returns the
configured ones, to use as-is or merge with your own). An invalid or
unreachable server is skipped and reported in `Agent.MCPStatuses()`, never
fatal. Two separate timeouts per server (ms, in `settings.json` or as
`harness mcp add --timeout/--connection-timeout`): `timeout` bounds ONE tool
call (default 300000 = 5 min, both transports) — raise it for a tool meant to run
longer — and `connection_timeout` bounds connecting (initialize + tools/list,
default 30000 = 30 s).

## License

MIT
