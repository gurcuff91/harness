---
title: RPC (other languages)
section: Frontends & channels
order: 225
summary: harness rpc speaks JSON-RPC over stdio so an SDK in any language can drive agents, sessions and administration, with custom tools implemented in the client.
---

`harness rpc` speaks JSON-RPC 2.0 over stdin and stdout. It is how an SDK in another language (TypeScript, Python…) uses Harness without reimplementing it: the SDK starts the binary as a child process and drives it with the messages described in the [protocol reference](../reference/rpc-protocol.md).

```bash
$ harness rpc
```

The command takes no flags. The agent is **not** built from flags or environment variables: the client describes it in the first message.

## The session of a client

1. **`initialize`** carries `protocol_version` (the only version today is `1`) and the agent's configuration. Nothing else is accepted before it. Requests sent right behind it wait for the agent to be built.
2. **Open a session** with `session/new` (or `resume`, `fork`, `import`). From then on the binary sends that session's events as `event` notifications.
3. **Operate it**: `session/prompt`, `session/ask`, `session/stop`, `session/switch_model`…
4. **Administer** with `admin/<service>/<method>`: providers, settings, MCP, memory, schedules and stored sessions. They need no session.
5. **Close stdin** (or call `agent/shutdown`) to finish: open sessions and the agent are closed. If the client dies, even abruptly, the closed pipe ends the binary too, so no process is left behind.

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":1,"client_info":{"name":"my-app","version":"1.0.0"},"config":{"thinking_level":"medium","memory":{"kind":"sqlite"}}}}
{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/work/project","model":"anthropic/claude-sonnet-4-20250514"}}
{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"id":"<session id>","text":"List the Go files here"}}
```

Everything the binary writes to stdout is a protocol message. Diagnostics go to stderr.

## The agent configuration

`config` is the serializable form of the agent options. Every field is optional.

| Field | What it does |
|---|---|
| `thinking_level`, `system_prompt`, `directives`, `max_iterations`, `max_tokens` | Same meaning as the Go [agent options](../sdk/options.md). |
| `disallowed_tools` | Tool names the agent never offers. |
| `mcp_servers` | MCP servers, as in [MCP servers](../using/mcp.md). |
| `tools` | Custom tools the **client** implements (below). |
| `profiles` | Named bundles of `directives` and `tools`, enabled per session. See [Profiles](../sdk/profiles.md). |
| `enable_scheduler`, `enable_web_search`, `terminal` | As the Go options. |
| `channels` | Input channels to register, by name: `slack`, `telegram`, `colleagues`. |
| `store` | Where sessions live: `{"kind":"file","path":"…"}` (default, `~/.harness/agent/sessions`) or `{"kind":"memory"}`. |
| `memory` | `{"kind":"sqlite","path":"…"}` or `{"kind":"none"}`. **Default: none**, so memory is on only if you ask. |
| `resources` | `{"kind":"file"}` makes each session read its directory's `SYSTEM.md`, `AGENTS.md` and skills. **Default: nothing is read**, so a directory chosen by an end user cannot influence the agent unless you ask. |

A `kind` a field does not accept is an error naming the field. Providing your own store, memory or resource loader from the client is not supported yet.

## Custom tools

A tool is declared with a name, a description and a JSON Schema, and **implemented by the client**. When the model calls it, the binary sends the client a `tool/call` request and waits for the answer:

```json
{"jsonrpc":"2.0","id":"7","method":"tool/call","params":{"session_id":"…","tool_call_id":"…","name":"get_order","input":{"id":"A-17"}}}
{"jsonrpc":"2.0","id":"7","result":{"content":"{\"status\":\"shipped\"}"}}
```

- Reply with `is_error: true` to give the model an error instead of a result. `images` may accompany `content`.
- Calls arrive **concurrently** when the model asks for several tools at once; answer them in any order.
- If the user stops the turn, the binary sends the notification `tool/cancel` with the `tool_call_id`. A late answer is ignored.
- If the client disconnects while a call is pending, the tool fails and the turn goes on.

## Events

Each event is a notification `event` with `session_id` and the fields of one [event](../reference/rpc-protocol.md#events): `turn_start`, `text`, `thinking`, `tool_call`, `tool_result`, `tokens`, `turn_end` and so on. They are the same events, with the same fields, as the [HTTP server](server.md)'s stream, and they arrive in order. A client that reads too slowly loses streaming deltas but never a control event without a warning on stderr.

## Errors

A failed request carries a JSON-RPC error with a numeric `code` and `data.kind`, for example `not_found`, `invalid`, `session_active` or `upstream`. Test the `kind`; it does not depend on the number. The full list is in the [reference](../reference/rpc-protocol.md#errors).

## From Go

`harness.RunRpc(ctx)` runs the same protocol in your own process. Unlike the other runners it takes no agent: the client builds it with `initialize`.

> **Note** Use [Embedding an agent](../sdk/embedding.md) when your program is written in Go. This protocol exists for the other languages.
