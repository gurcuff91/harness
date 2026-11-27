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
| `memory` | `{"kind":"sqlite","path":"…"}` or `{"kind":"client"}` ([served by the client](#memory-and-resources-served-by-the-client); no `path`). **Default: no memory**, so it is on only if you ask; omit the field to leave it off. |
| `settings` | Where the settings live: `{"kind":"file"}` (default, `~/.harness/settings.json`; a `path` picks another file), `{"kind":"ephemeral"}` or `{"kind":"client"}` ([served by the client](#stores-served-by-the-client)). |
| `credentials` | The same kinds for the secrets (default `~/.harness/credentials.json`, mode 0600). Chosen separately from `settings`: secrets can live in a secret manager and settings in files. |
| `resources` | `{"kind":"file"}` makes each session read its directory's `SYSTEM.md`, `AGENTS.md` and skills; `{"kind":"client"}` has [the client serve them](#memory-and-resources-served-by-the-client). Neither takes a `path`. **Default: nothing is read**, so a directory chosen by an end user cannot influence the agent unless you ask. |

A `kind` a field does not accept is an error naming the field and the kinds it accepts; `path` is only accepted by `file` (and `sqlite` for `memory`). The `store` kind `memory` was renamed `ephemeral`, and `memory: {"kind":"none"}` no longer exists: omit the field. Providing your own session store from the client is not supported yet.

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

## Memory and resources served by the client

With `"memory": {"kind":"client"}` the memories live in the client, and with `"resources": {"kind":"client"}` the client supplies each session's `SYSTEM.md`, `AGENTS.md` and skills. Unlike the settings and credentials stores these belong to one agent: there is no order to respect and no readiness check, and the client can start answering after `initialize`.

Memory requests:

| Method | Params | Result |
|---|---|---|
| `memory/write` | `cwd`, `slug`, `content`, `global` | `created` |
| `memory/get` | `cwd`, `slug`, `global` | `found`, `memory` |
| `memory/search` | `cwd`, `query`, `include_content`, `skip`, `limit` | `total`, `returned`, `skip`, `limit`, `results` |
| `memory/delete` | `cwd`, `slug`, `global` | `deleted` |

Resources request:

| Method | Params | Result |
|---|---|---|
| `resources/load` | `cwd` | `system_md`, `agents_md`, `skills` |

The rules the client must honor are the memory store's:

- A memory is identified by `cwd` and `slug`. `memory/write` creates or replaces it and answers `created: true` only the first time.
- With `global: true`, `write`, `get` and `delete` address the global scope instead of `cwd`. A global memory is visible from every project.
- `memory/get` of a missing memory answers `found: false`, not an error. `memory/delete` answers `deleted: true` only if one existed.
- `memory/search` takes `cwd` as a filter: `""` searches all projects, `"<global>"` only the global memories, and any other value that project's memories plus the global ones.
- An empty `query` is list mode: the most recently updated first, with no `score`. A non-empty `query` is a search by whole words, case-insensitive, with `score` set.
- `limit` of 0 or less means 10 and a negative `skip` means 0. `total` counts every match across pages, `returned` the ones in this page.
- With `include_content: false` the `content` of each result is left out. Every result carries its `cwd`.
- Results are ranked by the **order** of the list, never by the value of `score`: a memory that repeats the word comes before one that mentions it once, but the numbers themselves carry no meaning. `results` is an array, an empty one when nothing matches.

```json
{"jsonrpc":"2.0","id":"1","method":"memory/search","params":{"cwd":"/work/shop","query":"index","include_content":true,"skip":0,"limit":10}}
{"jsonrpc":"2.0","id":"1","result":{"total":1,"returned":1,"skip":0,"limit":10,"results":[{"slug":"db-schema","cwd":"/work/shop","content":"orders has a unique index","score":1.2,"created_at":1764200000000,"updated_at":1764200000000}]}}
{"jsonrpc":"2.0","id":"2","method":"memory/get","params":{"cwd":"/work/shop","slug":"nope","global":false}}
{"jsonrpc":"2.0","id":"2","result":{"found":false}}
{"jsonrpc":"2.0","id":"3","method":"memory/write","params":{"cwd":"/work/shop","slug":"style","content":"use tabs","global":true}}
{"jsonrpc":"2.0","id":"3","result":{"created":true}}
```

`resources/load` is sent each time a session is opened, resumed, forked or reconfigured. Every skill travels with its `content`, so one reply carries everything:

```json
{"jsonrpc":"2.0","id":"4","method":"resources/load","params":{"cwd":"/work/shop"}}
{"jsonrpc":"2.0","id":"4","result":{"system_md":"","agents_md":"Project rules…","skills":[{"name":"deploy","description":"Ship a release","location":"/work/shop/.agents/skills/deploy/SKILL.md","content":"Run the deploy script…"},{"name":"from-db","description":"Kept in a database","content":"…"}]}}
```

`location` is optional for a skill: a client that keeps skills in a database has no path, and the Skill tool then reports no directory.

**Serve these requests concurrently.** Sessions can open at the same time, the model runs several tool calls in parallel, and sub-agents search the same memory and use the same resources. A client that answers one request at a time works, but slowly.

Every request has a 30 second deadline: if the client does not answer, the call fails with `the client did not answer memory/search within 30s` (`resources/load` likewise). An error the client returns is passed on as `memory/search: client error <code>: <message>`, and both are logged on stderr. What a failure does:

- **Memory read while a session prompt is built** (the list of memory names): the failure is tolerated. The list is left out of the prompt and the session opens. The `MemoSearch` and `MemoWrite` tools return the error to the model.
- **`resources/load` on `session/new`**: the request fails with the message, because someone creating a session expects to see the problem.
- **`resources/load` on `session/resume`, `session/fork` or a reconfiguration**: the session opens without that context.

In Go, `memory/storetest.RunContract` is the bar: it runs the whole contract above against any `memory.Store`, and the adapter of this binary passes it with a correct client behind it.

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

Each event is a notification `event` with `session_id` and the fields of one [event](../reference/rpc-protocol.md#events): `turn_start`, `text`, `thinking`, `tool_call`, `tool_result`, `tokens`, `subagent_start`, `subagent_stop`, `turn_end` and so on. The `subagents` field of the session detail (`session/new`, `session/resume`, `session/info`) lists the sub-agents running now. They are the same events, with the same fields, as the [HTTP server](server.md)'s stream, and they arrive in order. A client that reads too slowly loses streaming deltas but never a control event without a warning on stderr.

## Errors

A failed request carries a JSON-RPC error with a numeric `code` and `data.kind`, for example `not_found`, `invalid`, `session_active` or `upstream`. Test the `kind`; it does not depend on the number. The full list is in the [reference](../reference/rpc-protocol.md#errors).

## From Go

`harness.RunRpc(ctx)` runs the same protocol in your own process. Unlike the other runners it takes no agent: the client builds it with `initialize`.

> **Note** Use [Embedding an agent](../sdk/embedding.md) when your program is written in Go. This protocol exists for the other languages.
