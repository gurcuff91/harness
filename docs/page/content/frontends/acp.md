---
title: ACP (editors)
section: Frontends & channels
order: 220
summary: Run harness as an Agent Client Protocol agent over stdio so Zed and other ACP editors can drive sessions.
---

`harness acp` speaks the [Agent Client Protocol](https://agentclientprotocol.com) over stdin and stdout, so an editor that supports ACP can use Harness as its coding agent. The editor starts the process; you do not run it by hand.

```bash
$ harness acp
```

The command takes no flags. It reads JSON-RPC messages from stdin until the editor closes the connection or sends SIGTERM, then closes the sessions it opened and exits cleanly. Provider credentials are not part of the protocol: connect providers beforehand with `harness connect` (see [Connect a provider](../start/connect-a-provider.md)).

## Configuring Zed

Register Harness as a custom agent server in Zed's `settings.json`:

```json
{
  "agent_servers": {
    "Harness": {
      "command": "harness",
      "args": ["acp"]
    }
  }
}
```

> **Note** The exact settings keys belong to Zed and can change between its versions; check Zed's documentation for external agents if the entry above is not picked up. What Harness provides is the command: `harness acp`, speaking ACP over stdio.

Use the absolute path to the binary as `command` if `harness` is not on the PATH Zed sees. Open the agent panel and pick Harness as the agent for a new thread.

## Turning on the extras

Because the editor starts the command, flags are not available. The colleague channel and the scheduler are off by default and are enabled through the environment of the process:

```json
{
  "agent_servers": {
    "Harness": {
      "command": "harness",
      "args": ["acp"],
      "env": { "HARNESS_WITH_COLLEAGUES": "1", "HARNESS_WITH_SCHEDULER": "1" }
    }
  }
}
```

`1`, `true` and `yes` enable an extra; `0`, `false`, `no`, empty or unset leave it off. Any other value is an error that names the variable. See [Colleagues](colleagues.md) and [Scheduled prompts](../using/scheduling.md).

## What is supported

The agent advertises these capabilities in the handshake:

- Loading sessions (`session/load`) with a full replay of the history, followed by a usage update, plus `session/resume`, `session/close`, `session/delete` and `session/list`.
- Images and embedded context (text resources) in prompts.
- Two session configuration options, `model` and `thinking` (`off`, `low`, `medium`, `high`, `xhigh`, `max`), shown as selectors by the editor.
- Edit and Write tool calls are reported with a diff of the file before and after.
- Cancelling a running prompt (`session/cancel`).

New sessions use the active model from settings and the working directory the editor passes.

### Slash commands

Type these in the editor's prompt:

| Command | Effect |
| --- | --- |
| `/compact` | Compact the session's history. |
| `/skill:<name> [prompt]` | Run a [skill](../using/skills.md). |
| `/info` | Session id, model, tokens, cost, the provider's quota, MCP servers, schedules and channels. |
| `/context` | Context window breakdown: system, tools, conversation, free space. |

`/info` and `/context` are answered directly and never reach the model. `/info` shows the quota of the session's own provider only, as plain text; a window at 70% or more is marked `high` and above 90% `almost out`.

## Limits

- The editor does not delegate file access or terminals: tools run in the Harness process with its own permissions.
- No authentication methods are advertised.
- The `mcpServers` an editor sends with a session request are ignored. MCP servers come from Harness's own settings; see [MCP servers](../using/mcp.md).
- The model and thinking level can be changed through the options. Renaming and resetting a session are not exposed, since the editor owns how a thread is displayed.
