---
title: MCP servers
section: Using Harness
order: 150
summary: Connect local (stdio) and remote (HTTP) Model Context Protocol servers with harness mcp, including timeouts and enabling or disabling.
---

Harness is an MCP client. Tools exposed by an MCP server are added to every session, named `mcp__<server>__<tool>`.

## Add a server

The transport is inferred: `--command` makes a local (stdio) server, `--url` a remote (HTTP) one. Exactly one is required.

```bash
# local: command and arguments in one string
$ harness mcp add filesystem --command "npx -y @modelcontextprotocol/server-filesystem /tmp"
$ harness mcp add db --command "uvx mcp-server-sqlite" --env DB_PATH=/data/app.db

# remote
$ harness mcp add docs --url https://mcp.example.com/mcp --bearer <token>
$ harness mcp add internal --url https://mcp.example.com/mcp --header X-Team:platform
```

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--command` | local | Executable and arguments, split on whitespace. |
| `--env KEY=VAL` | local | Environment variable for the process (repeatable). |
| `--url` | remote | Server URL. |
| `--header KEY:VAL` | remote | HTTP header (repeatable). |
| `--bearer TOKEN` | remote | Shorthand for `--header "Authorization: Bearer TOKEN"`; ignored if you also pass an `Authorization` header. |
| `--timeout MS` | both | Time allowed for **one tool call** (default 300000, 5 minutes). |
| `--connection-timeout MS` | both | Time allowed to connect: initialize plus listing tools (default 30000, 30 seconds). |
| `--disabled` | both | Save the server without enabling it. |

Both timeouts are in milliseconds and cannot be negative. `timeout` limits each tool call, so raise it for tools that legitimately run long; `connection_timeout` only limits connecting.

## Manage servers

```bash
$ harness mcp list              # transport, target and live state (connected + tool count, failed, disabled)
$ harness mcp disable docs      # keep the config, stop using it
$ harness mcp enable docs
$ harness mcp rm docs           # also: remove
```

Servers are saved in `~/.harness/settings.json` in the `mcp` collection. They are connected when an agent starts, so changes apply to the next `harness` process (or server restart). A server that fails to connect is reported in `harness mcp list` and the other servers still work. Connected servers are counted in the TUI footer (`[2 mcps]`) and in `/info`.

## Configuration shape

The same fields are used by the HTTP API (`PUT /api/settings/mcp/{name}`, `GET /api/mcp/status`) and the SDK:

```json
{
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
  "env": { "KEY": "value" },
  "cwd": "/optional/working/dir",
  "timeout": 300000,
  "connection_timeout": 30000,
  "disabled": false
}
```

A remote server uses `url` and `headers` instead of `command`, `args`, `env` and `cwd`. A config with both or neither of `command` and `url` is rejected (HTTP 422).

## SDK

An embedded agent connects exactly the servers it is given: `harness.AgentWithMCPs(servers)`, or `mcp.ServersFromSettings()` to use the saved ones. See [Options](../sdk/options.md).
