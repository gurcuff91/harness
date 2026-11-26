---
title: CLI
section: Reference
order: 400
summary: Every harness command, subcommand, flag, environment variable and exit code.
---
`harness` with no command opens the interactive TUI. Every other command is a subcommand. Run `harness <command> --help` for the built-in help of any command; `harness help <command>` is equivalent.

Flags that belong to the root command (`--model`, `--thinking`, `--resume`, `-p`, ...) go before or after the prompt words. A first word that is exactly a command name always runs the command; to send such a word as a prompt, put it after `--`.

## Root command

```text
harness [<prompt> ...] [flags]
```

Without `-p`, opens the TUI. If prompt words are given, they are joined and submitted as the first message once the session is ready.

| Flag | Description |
|---|---|
| `-h`, `--help` | Show help. |
| `-v`, `--version` | Print the version and exit. |
| `--model=<provider/model>` | Model for the session. |
| `--thinking=<level>` | Thinking level: `off`, `low`, `medium`, `high`, `xhigh` or `max`. |
| `--resume=<id>` | Resume the session with this id. |
| `--slack-channel=<id or name>` | Listen to a Slack channel or DM (repeatable). Needs `harness slack login`. |
| `--telegram-allow=<users>` | Listen to the Telegram bot; messages from these trusted usernames become prompts (repeatable, commas also work). |
| `--telegram-token=<token>` | Telegram bot token for this process. |
| `-p`, `--prompt=<text>` | Run a single-turn prompt without the TUI. |
| `--output=<mode>` | With `-p`: `text` (default), `json` or `json-stream`. |
| `--with-colleagues` | Make every session a colleague (see [Colleagues](../frontends/colleagues.md)). |
| `--with-scheduler` | Run the cron scheduler engine and give the agent the Schedule tools (see [Scheduled prompts](../using/scheduling.md)). |

Rules:

- An explicit `--model`, `--thinking` or `--resume` is honored exactly or the process exits with code 1 before the TUI opens.
- `-p` together with a positional prompt is an error; `-p` together with `--resume` is an error.
- A mistyped command (one edit away from a command name, as the first word) is an error that suggests the command instead of being sent as a prompt.
- In `-p` mode the agent has WebSearch like every other mode but never has the colleague or scheduler extras.

```bash
$ harness
$ harness fix the failing test in agent/session.go
$ harness --model anthropic/claude-sonnet-4-20250514 --thinking high
$ harness --resume 0f3c9a1e
$ harness -- serve          # a prompt that is the word "serve"
$ harness -p "summarize README.md" --output json
```

See [The TUI](../using/tui.md) and [One-shot mode](../frontends/one-shot.md).

## serve

```text
harness serve <addr> [flags]
```

Starts the HTTP/SSE server headless. See [HTTP/SSE server](../frontends/server.md) and the [HTTP API](http-api.md).

`<addr>` is the address to listen on. A missing host means this machine only: `:8080` is `127.0.0.1:8080`. To listen on the network give a host explicitly (`0.0.0.0:8080`). A bare port such as `8080` is an error.

| Flag | Description |
|---|---|
| `--with-colleagues` | Same as the root flag. |
| `--with-scheduler` | Same as the root flag. |
| `--telegram-token=<token>` | Telegram bot token for this process. |
| `--secret=<secret>` | Require this secret on every request (`X-Harness-Sec` header or `Authorization: Bearer`). Prefer `HARNESS_SECRET`: a flag is visible in the process list. |
| `--insecure` | Allow listening beyond localhost without a secret. |

Without a secret on localhost the server starts and logs a warning. Listening beyond localhost without a secret is refused unless `--insecure` is given. Without TLS the secret travels in the clear.

```bash
$ export HARNESS_SECRET=$(harness gen-secret)
$ harness serve 127.0.0.1:8080 --with-scheduler
```

## gen-secret

```text
harness gen-secret
```

Prints a new random secret suitable for `harness serve --secret` or `HARNESS_SECRET`. It takes no arguments.

```bash
$ harness gen-secret
```

## acp

```text
harness acp
```

Runs Harness as an Agent Client Protocol agent over stdio, for Zed and other ACP clients. It takes no flags; use `HARNESS_WITH_COLLEAGUES=1` and `HARNESS_WITH_SCHEDULER=1` to turn the extras on. See [ACP (editors)](../frontends/acp.md).

## providers

```text
harness providers
```

Lists every provider with its status (`active` or `inactive`), its credential type and its model count.

## connect

```text
harness connect <name> [<api-key>]
```

Connects a provider. `<name>` is the provider name. Pass the API key as the second argument for API-key providers; omit it for OAuth providers (`claude-oauth`, `codex-oauth`), which open the browser login. A custom provider cannot be connected: its authentication travels in its configured headers. See [Connect a provider](../start/connect-a-provider.md).

```bash
$ harness connect anthropic <api-key>
$ harness connect claude-oauth
```

## disconnect

```text
harness disconnect <name>
```

Removes the stored credential of a provider.

## sessions

```text
harness sessions [--all]
```

Lists the sessions of the current directory, or of every directory with `--all`. See [Sessions](../using/sessions.md).

## delete

```text
harness delete <session-id>
```

Deletes a session and its log.

## settings set

```text
harness settings set <key> <value>
```

Sets a core setting. `<key>` is `model` (stored as `active_model`, value `provider/model`) or `thinking` (stored as `thinking_level`, one of `off`, `low`, `medium`, `high`, `xhigh`, `max`).

```bash
$ harness settings set model anthropic/claude-sonnet-4-20250514
$ harness settings set thinking medium
```

## mcp

Manage MCP servers stored in settings. See [MCP servers](../using/mcp.md) and [Settings and data](settings.md) for the fields.

| Command | Description |
|---|---|
| `harness mcp list` | List the configured servers. |
| `harness mcp add <name> [flags]` | Add a server. |
| `harness mcp rm <name>` | Remove a server (alias: `remove`). |
| `harness mcp enable <name>` | Enable a server. |
| `harness mcp disable <name>` | Disable a server without deleting its configuration. |

Flags of `mcp add`. The transport is inferred: `--command` makes a local server, `--url` a remote one; giving both, or neither, is an error.

| Flag | Description |
|---|---|
| `--command=<cmd>` | Local server: command and arguments, for example `"npx -y @mcp/fs"`. |
| `--url=<url>` | Remote server URL. |
| `--bearer=<token>` | Remote: shorthand for `--header "Authorization: Bearer <token>"`. |
| `--env=<KEY=VAL>` | Local: environment variable (repeatable). |
| `--header=<KEY:VAL>` | Remote: HTTP header (repeatable). |
| `--timeout=<ms>` | Timeout of one tool call (default 300000, 5 minutes). |
| `--connection-timeout=<ms>` | Connect timeout: initialize plus tool listing (default 30000, 30 seconds). |
| `--disabled` | Add the server disabled. |

```bash
$ harness mcp add fs --command "npx -y @modelcontextprotocol/server-filesystem ."
$ harness mcp add docs --url https://example.com/mcp --bearer <token> --timeout 600000
$ harness mcp disable docs
```

## provider

Manage custom providers (an OpenAI Chat Completions or Anthropic Messages compatible endpoint). See [Custom providers](../sdk/providers.md).

| Command | Description |
|---|---|
| `harness provider list` | List the custom providers. |
| `harness provider add <name> --url=<url> [flags]` | Add one. |
| `harness provider rm <name>` | Remove one (alias: `remove`). |

Flags of `provider add`:

| Flag | Description |
|---|---|
| `--url=<url>` | Required. Base URL. For `openai`: the chat completions base including `/v1`. For `anthropic`: the API root without `/v1`. |
| `--type=<dialect>` | `openai` (default) or `anthropic`. |
| `--models-url=<url>` | Models listing URL (default `<url>/models`, or `<url>/v1/models` for `anthropic`). |
| `--header=<KEY:VAL>` | HTTP header sent on every request (repeatable). Put any authentication here. |
| `--display=<name>` | Display name (default: the provider name). |
| `--disabled` | Add the provider disabled. |
| `--reasoning-split` | Send `"reasoning_split": true` on every request, for MiniMax-compatible backends. |

The name must not collide with a built-in provider.

```bash
$ harness provider add my-gateway --url https://gateway.example.com/v1 --header "X-Api-Key:<key>"
$ harness --model my-gateway/some-model
```

## memo

```text
harness memo [<query>] [flags]
```

Read-only view of the persistent memory. Without a query it lists memories; with one it runs a full-text search. The agent writes memories through its own tools. See [Memory](../using/memory.md).

| Flag | Description |
|---|---|
| `--all` | Include the memories of all projects (default: this project). |
| `--global` | Only global (cross-project) memories. |
| `--content` | Show a content preview of each memory. |
| `--limit=<n>` | Maximum results per page (default 10). |
| `--skip=<n>` | Pagination offset (default 0). |

## schedules

```text
harness schedules [--json]
```

Read-only list of the cron-scheduled prompts. The agent creates them through its tools when the scheduler is enabled. `--json` prints JSON.

## telegram token

```text
harness telegram token [<token>] [--status | --delete]
```

Saves a Telegram bot token (after verifying it), checks the saved one with `--status`, or removes it with `--delete`. See [Telegram](../frontends/telegram.md).

## slack login

```text
harness slack login [--status]
```

Authenticates interactively and saves the Slack login. With `--status` it verifies the saved credentials instead. See [Slack](../frontends/slack.md).

## help and version

```text
harness help [<command>]
harness version
```

`help` shows the root help, or the help of `<command>`. `version` (and `-v`, `--version`) prints the version.

## Environment variables

| Variable | Effect |
|---|---|
| `HARNESS_WITH_COLLEAGUES` | Same as `--with-colleagues`. Accepts `1`, `true`, `yes` (on) and `0`, `false`, `no`, empty or unset (off); any other value is an error. Also the only way to enable it for `harness acp`. |
| `HARNESS_WITH_SCHEDULER` | Same as `--with-scheduler`, same accepted values. |
| `HARNESS_SECRET` | Secret for `harness serve`; the `--secret` flag wins. |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (below `--telegram-token`, above the saved token). |
| `TELEGRAM_ALLOW` | Comma-separated trusted Telegram usernames, like `--telegram-allow`. |
| `ANTHROPIC_API_KEY` | API key for `anthropic`. |
| `OPENAI_API_KEY` | API key for `openai`. |
| `MINIMAX_API_KEY` | API key for `minimax`. |
| `SYNTHETIC_API_KEY` | API key for `synthetic`. |
| `OPENCODE_GO_API_KEY` | API key for `opencode-go`. |
| `OLLAMA_CLOUD_API_KEY` | API key for `ollama-cloud`. |
| `OLLAMA_URL` | Base URL of the local Ollama server (default `http://localhost:11434`). |

A flag only turns an extra on: a false variable never turns a flag off, and there is no `--without-*` flag.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | The command ran and failed (`error: <message>` on stderr), an explicit `--model`, `--thinking` or `--resume` could not be honored, or a mistyped command was detected. |
| 80 | Usage error: an unknown flag or invalid arguments (the usage text is printed). |
