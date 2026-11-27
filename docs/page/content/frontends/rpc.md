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
| `store` | Where sessions live: `{"kind":"file","path":"…"}` (default, `~/.harness/agent/sessions`) or `{"kind":"ephemeral"}` (in memory, never on disk). |
| `memory` | `{"kind":"sqlite","path":"…"}`. **Default: no memory**, so it is on only if you ask; omit the field to leave it off. |
| `settings` | Where the settings live: `{"kind":"file"}` (default, `~/.harness/settings.json`; a `path` picks another file), `{"kind":"ephemeral"}` or `{"kind":"client"}` ([served by the client](#stores-served-by-the-client)). |
| `credentials` | The same kinds for the secrets (default `~/.harness/credentials.json`, mode 0600). Chosen separately from `settings`: secrets can live in a secret manager and settings in files. |
| `resources` | `{"kind":"file"}` makes each session read its directory's `SYSTEM.md`, `AGENTS.md` and skills. **Default: nothing is read**, so a directory chosen by an end user cannot influence the agent unless you ask. |

A `kind` a field does not accept is an error naming the field and the kinds it accepts; `path` is only accepted by `file` (and `sqlite` for `memory`). The `store` kind `memory` was renamed `ephemeral`, and `memory: {"kind":"none"}` no longer exists: omit the field. Providing your own session store, memory or resource loader from the client is not supported yet.

## Stores served by the client

With `"settings": {"kind":"client"}` (or `credentials`) the data lives in the client, not in a file of the binary. Each time Harness needs to read or write an entry it sends the client a request; the client keeps the data wherever it likes (a database, a secret manager, memory). The binary keeps no copy.

> **Note** Before it builds the agent, the binary does one `get` of the entry `core` / `probe`, which never exists: it is how it finds out that the client really serves the store. Answer it like any other missing entry (`found: false`). Without a reply the `initialize` request fails instead of the agent starting on empty settings.

There are ten requests the client must answer, five per domain: `settings/get`, `settings/set`, `settings/delete`, `settings/list`, `settings/swap`, and the same five under `credentials/`.

| Method | Params | Result |
|---|---|---|
| `…/get` | `namespace`, `key` | `found`, `value`, `version` |
| `…/set` | `namespace`, `key`, `value` | `{}` (creates or replaces) |
| `…/delete` | `namespace`, `key` | `{}` (a missing entry is not an error) |
| `…/list` | `namespace` | `entries`: an object of `key` to `{value, version}` (empty when there are none) |
| `…/swap` | `namespace`, `key`, `expected_version`, `value` | `swapped` |

`value` is a JSON document, passed verbatim: the client never needs to interpret it. `version` is an opaque string **the client chooses** (a counter, an etag, a hash, a row version). The binary only hands it back in `swap`.

`swap` is a conditional write and must be atomic in the client:

- `expected_version: null` means the entry must not exist (a create).
- `value: null` deletes the entry.
- If the entry's current version is not `expected_version`, answer `{"swapped": false}` and change nothing. The binary then reads again and retries, up to 10 times; after that the operation fails with a `conflict` error.

Why it exists: an OAuth refresh token can be redeemed only once. When two requests try to renew the same token at the same moment, only one `swap` can win; the other re-reads the renewed token instead of redeeming the old one again. A `client` store is only as atomic as the client makes `swap`: Harness adds no lock across processes and no cache.

**Answer from the first message.** The binary reads settings while it builds the agent, so it calls `settings/get` (and `credentials/get`) **during `initialize`**, before it has replied. The client must already be serving them. Requests are answered concurrently, so a client that waits for the `initialize` reply before it answers deadlocks itself.

Every request has a 30 second deadline. If the client does not answer in time, the call fails with:

> the client did not answer settings/get within 30s: a "client" store must be served from the first message

During `initialize` this fails the whole request. A failure at that point also ends the process: the stores are global to the process and cannot be replaced, so start it again. Errors the client returns are passed on with their message and logged on stderr; a failed read is never taken for "not found". Registering a store fails with a `conflict` error if the process already used its settings or credentials before `initialize`.

An exchange, with the binary's requests first:

```json
{"jsonrpc":"2.0","id":"1","method":"settings/get","params":{"namespace":"core","key":"active_model"}}
{"jsonrpc":"2.0","id":"1","result":{"found":false}}
{"jsonrpc":"2.0","id":"2","method":"credentials/swap","params":{"namespace":"providers","key":"claude-oauth","expected_version":"v7","value":{"type":"oauth"}}}
{"jsonrpc":"2.0","id":"2","result":{"swapped":false}}
{"jsonrpc":"2.0","id":"3","method":"credentials/get","params":{"namespace":"providers","key":"claude-oauth"}}
{"jsonrpc":"2.0","id":"3","result":{"found":true,"value":{"type":"oauth"},"version":"v8"}}
```

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
