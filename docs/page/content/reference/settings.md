---
title: Settings and data
section: Reference
order: 420
summary: Where Harness keeps its files under ~/.harness, what each setting means, and what is safe to delete.
---
All persistent state lives under `~/.harness`, plus optional per-project files under `<project>/.harness`. Nothing is kept anywhere else.

## Layout

```text
~/.harness/
  credentials.json            secrets, mode 0600
  settings.json               everything that is not a secret
  agent/
    sessions/<project>/       one <id>.meta.json and one <id>.jsonl per session
    memory.db                 persistent memory (SQLite, FTS5)
    SYSTEM.md                 optional global system prompt
    skills/                   optional global skills
  exports/                    session exports written by /export
  run/                      Unix sockets of colleague sessions
  cache/model-registry.json   last good copy of the model catalog
```

Lock files (`*.flock`) sit next to `credentials.json` and `settings.json` and are never deleted by Harness; they coordinate several Harness processes sharing one home.

| Path | Purpose |
|---|---|
| `credentials.json` | Provider API keys and OAuth tokens, the Telegram bot token and the Slack session. Created with mode 0600. |
| `settings.json` | Core settings, MCP servers, custom providers, schedules, channel claims and the Slack workspace identity. |
| `agent/sessions/<project>/` | Session storage, one directory per working directory. `<id>.meta.json` holds the metadata and is rewritten on save; `<id>.jsonl` holds one message per line. |
| `agent/memory.db` | The persistent memory database. See [Memory](../using/memory.md). |
| `exports/` | Files written by the `/export` session command (`<name>-<id8>.export.jsonl`, directory mode 0700, files 0600). |
| `run/` | Sockets named `c-<id>.sock`, one per live colleague session. When the home path is too long for a socket path, `/tmp/harness-<uid>` is used instead. |
| `cache/model-registry.json` | The last good copy of the OpenRouter model catalog, used only as a fallback when the catalog cannot be fetched. |

### Per-project files

| Path | Purpose |
|---|---|
| `<project>/.harness/agent/SYSTEM.md` | System prompt that replaces the built-in one for this project. |
| `~/.harness/agent/SYSTEM.md` | Same, for every project without its own. A project file wins; the two are never combined. |
| `<project>/AGENTS.md` | Project instructions, read from the working directory and walking up. |
| `<project>/.harness/agent/skills/` | Project skills (highest priority). |
| `<project>/.agents/skills/` | Project skills shared with other tools. |
| `~/.harness/agent/skills/` | Global skills. |
| `~/.agents/skills/` | Global skills shared with other tools (lowest priority). |

When two skills have the same name, the one from the higher-priority directory wins. See [Skills](../using/skills.md).

The base system prompt is, in order of precedence: the one passed by the embedding program, then `SYSTEM.md`, then the built-in default.

## settings.json

The file is a JSON object of namespaces, each holding keys:

```json
{
  "core":     { "active_model": "anthropic/claude-sonnet-4-20250514", "thinking_level": "medium" },
  "mcp":      { "fs": { "command": "npx", "args": ["-y", "@mcp/fs"] } },
  "provider": { "my-gateway": { "type": "openai", "url": "https://gateway.example.com/v1" } }
}
```

| Namespace | Content |
|---|---|
| `core` | `active_model` (`provider/model`) and `thinking_level` (`off`, `low`, `medium`, `high`, `xhigh`, `max`). Set with `harness settings set` or the `/model` and `/thinking` commands. |
| `mcp` | MCP servers, keyed by server name. |
| `provider` | Custom providers, keyed by provider name. |
| `schedules` | Cron-scheduled prompts. Each entry carries `owner` (session id), `slug`, `cron`, `prompt`, `runs`, `last_run` and, when created from a channel conversation, `origin`. Keys are opaque hashes. |
| `slack` | Key `config`: the workspace identity saved by `harness slack login` (`workspace`, `user_id`, `team`). The secret half is in `credentials.json`. |
| `channels` | Claims of input-channel conversations: which session of which process listens to which conversation (`type`, `id`, `session`, `pid`, `cwd`, `claimed_at`). A claim of a dead process is taken over automatically. |

Other keys in the file belong to Harness; do not edit them by hand while a process is running.

### credentials.json namespaces

| Namespace | Content |
|---|---|
| `providers` | One credential per provider name: an API key or an OAuth credential. |
| `telegram` | `bot_token`. |
| `slack` | `session`: the Slack session token and cookie. |

Environment variables for API keys (see [CLI](cli.md)) are read in addition to this file.

## MCP server fields

Each entry of the `mcp` namespace has exactly one of `command` (a local server that Harness spawns) or `url` (a remote server reached over HTTP). Setting both, or neither, is invalid.

| Field | Applies to | Description |
|---|---|---|
| `command` | local | Executable to run. |
| `args` | local | Arguments. |
| `env` | local | Environment variables of the process. |
| `cwd` | local | Working directory of the process. |
| `url` | remote | Server URL. |
| `headers` | remote | HTTP headers sent on every request, such as `Authorization`. |
| `timeout` | both | Milliseconds allowed for ONE tool call. Default 300000 (5 minutes). |
| `connection_timeout` | both | Milliseconds allowed to connect (initialize and list tools). Default 30000 (30 seconds). |
| `disabled` | both | `true` skips the server without deleting it. Servers are enabled by default. |

```json
{
  "mcp": {
    "fs":   { "command": "npx", "args": ["-y", "@mcp/fs"], "env": { "ROOT": "/srv" } },
    "docs": { "url": "https://example.com/mcp", "headers": { "Authorization": "Bearer <token>" }, "timeout": 600000 }
  }
}
```

`timeout` and `connection_timeout` are different limits: the first bounds a tool call, the second only the connection. See [MCP servers](../using/mcp.md).

## Custom provider fields

Each entry of the `provider` namespace; the key is the provider name used in `provider/model`.

| Field | Description |
|---|---|
| `type` | `openai` (Chat Completions) or `anthropic` (Messages). |
| `url` | `openai`: the chat completions base URL including `/v1`. `anthropic`: the API root without `/v1`. |
| `models_url` | Optional models listing URL. Defaults to `<url>/models` (`openai`) or `<url>/v1/models` (`anthropic`). |
| `headers` | Headers sent on every request, including model discovery. All authentication goes here; a custom provider has no stored key. |
| `display` | Optional display name; defaults to the key. |
| `disabled` | `true` skips the provider. |
| `reasoning_split` | `openai` only: send `"reasoning_split": true` with every request. |

Changes are applied to a running server through `POST /api/settings/provider/reload`. See [Custom providers](../sdk/providers.md) and the [HTTP API](http-api.md).

## What is safe to delete

| Path | Effect of deleting it |
|---|---|
| `cache/model-registry.json` | None beyond a slower first model listing when the catalog is unreachable. It is rebuilt. |
| `run/` contents | Only the sockets of live colleague sessions; those sessions stop being reachable until reopened. Stale sockets are harmless. |
| `exports/` | Loses the export files; sessions are untouched. |
| `agent/sessions/<project>/` or one session's files | Loses that history permanently. Use `harness delete <id>` to remove one session cleanly. |
| `agent/memory.db` | Loses all persistent memory. |
| `settings.json` | Loses the active model, MCP servers, custom providers, schedules and channel claims. |
| `credentials.json` | Disconnects every provider, Telegram and Slack. |

Back up `settings.json` and `credentials.json` before editing them by hand, and stop running Harness processes first.
