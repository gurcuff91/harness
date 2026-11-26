# The RPC frontend (`harness rpc`) — a JSON-RPC protocol for other languages

Date: 2026-11-26 · Target: v0.131.0 · Owner-approved. Goal: a TS/JS SDK that is a thin client of this protocol, as close as possible to using Harness as a Go SDK.

## 1. Principles
- **Transport:** JSON-RPC 2.0 over stdio, one JSON line per message; `harness rpc` starts it. Anything that is not protocol goes to stderr.
- **Direct:** it runs on `agent` and `admin` (no HTTP server in between, no port, nothing another process can reach).
- **Symmetric:** each side may send requests to the other; the binary calls the client for `tool/call`.
- **One client per process.** Closing stdin (the client died, even SIGKILL) shuts the binary down: sessions and the agent are closed.
- **snake_case** for methods that have more than one word and for every field, like the HTTP API, the disk formats and the providers. Go types serialize as they are; there is NO key conversion. ACP stays camelCase ONLY because its specification mandates it.
- **Version:** `initialize` carries an integer `protocol_version`; an unsupported one is an error saying what is expected, and the process exits.
- **Shared layer:** the JSON-RPC code of `acp/jsonrpc.go` moves to a shared package that ACP and RPC use.
- **Names:** `initialize`, then `<domain>/<method>`: `agent/*`, `session/*`, `admin/<service>/*`, `tool/*`, and the notification `event`.

## 2. The operation table (one definition)
An operation is a record with a typed function: `Method`, `Doc`, `Call func(ctx, P) (R, error)`. From the table come the dispatch, the validation of params (`DisallowUnknownFields`) and each operation's JSON Schema (by reflection over the same Go types); `go run ./rpc/gen` writes the schema (committed, a test fails if stale) and the TS types derive from it.
- Params and results are the EXISTING public Go types (`store.SessionMeta`, `types.Quota`, `admin.ProviderInfo`…); new ones only where none exists.
- **Parity test:** every public method of `agent.Agent`, `agent.Session` and `admin` is in the table or in an explicit exclusion list with a reason.
- `harness.RunRpc` joins the facade (as `RunAcp` is).

## 3. Lifecycle and `initialize`
```
client → initialize {protocol_version, client_info, config}
server ← {protocol_version, harness_version, capabilities}
```
`config` is the serializable `AgentOptions`: `thinking_level`, `system_prompt`, `directives`, `max_iterations`, `max_tokens`, `disallowed_tools`, `mcp_servers`, `profiles` (with declared tools), `tools` (declared), `enable_scheduler`, `enable_web_search`, `terminal`, and the input channels asked for by name (`slack`, `telegram`, `colleagues`). After it, `agent/register_tool` and `agent/register_profile` add more.

**Store, memory and resources are chosen, not passed:**
| Field | `kind` | `path` | Omitted |
|---|---|---|---|
| `store` | `file`, `memory` | `file`: base directory | `file`, `~/.harness/agent/sessions` |
| `memory` | `sqlite`, `none` | `sqlite`: database file | **`none`** (the `agent.New` default, not the CLI's) |
| `resources` | `file`, `none` | — | `file` |
Another kind for a field is an error. The binary opens them and always closes them at shutdown.

## 4. Sessions and events
Opening a session returns its detail and from then on the binary sends that session's events; there is no separate subscribe.
- `session/new {cwd, model, name?, enabled_profiles?, directives?, channels?}`, `session/resume {id, …}`, `session/fork {id, …}`, `session/import {path|content, cwd?, channels?}`, `session/close {id}`. The three options keep the Go semantics (`enabled_profiles`: absent = unchanged, names = replace, empty = remove all).
- Operate: `session/prompt` (`text`, `images?`, `origin?`, `display_text?`; answers `started`/`queued`), `session/ask` (waits for the final text), `stop`, `compact`, `reset`, `switch_model`, `switch_thinking`, `set_max_iterations`, `rename`; read: `info`, `context`, `sysprompt`, `tools`, `messages`.
- `session/exec_command` is KEPT only for commands with no method of their own (`/skill:<name>`); compact, reset, rename, model, thinking, max-iter, export and import are explicit methods.
- **`event`** notification: `{session_id, type, …}`, with the type and fields of the HTTP `formatEvent` (one definition of events for both channels). Built on `Session.Watch`: deltas are dropped without waiting when the client is slow; control events wait up to 500 ms, then are dropped with a warning on stderr.

## 5. Tools by callback
Declared as `{name, description, input_schema}` (= `tools.Tool.Def`) in `initialize`, `agent/register_tool` or a profile. When the model calls one:
```
binary → client: tool/call {session_id, tool_call_id, name, input}
client → binary: {content, images?, is_error?}
```
The registered `Execute` is a closure making that request. **Cancel:** `session/stop` cancels the turn's ctx and the binary sends the notification `tool/cancel {tool_call_id}`; a late answer is ignored. **Concurrency:** parallel tool calls arrive as concurrent `tool/call` requests, answered in any order by id. `is_error: true` reaches the model as the tool's error; a client that disconnects mid-call makes the tool fail and the turn continue. **Go change:** the ctx carries the session; new public `agent.SessionID(ctx)` (as `SessionName(ctx)` is for channels).

## 6. Administration
`admin/<service>/<method>`, one per method of `admin`, same params and results: `sessions` (`list get messages rename delete export`), `providers` (`list models model connect disconnect quota oauth_start oauth_exchange list_custom put_custom delete_custom reload_custom register_openai register_anthropic unregister`), `settings` (`get set_active_model set_thinking_level`), `mcp` (`list get put delete set_enabled`), `memory` (`search list get put delete`), `schedules` (`list`). `sessions/delete` and `rename` use the agent's `ActiveSessions`, so they fail with `session_active` while the session is open. `sessions/export` writes to `path` and answers `{path, messages}` (sessions reach hundreds of MB) or, with no path, returns the JSONL as a string.

## 7. Errors
Standard JSON-RPC codes (-32700…-32603) plus: -32000 `not_found`, -32001 `invalid`/`unprocessable`, -32002 `session_active`/`conflict`, -32003 `not_connected`, -32004 `upstream`, -32005 `not_configured`, -32006 `protocol_version`. `error.data` carries `{kind: "<sentinel>"}` so a client tells them apart without the number. Same table as the HTTP `adminStatus` (shared, not copied).

## 8. Types and tests
`rpc/gen` writes the JSON Schema of every operation, event and error (committed; a test fails if stale). Tests: a contract test per operation over the real protocol with a fake provider (no network); the parity test; protocol cases (bad version, unknown method, extra fields, a request before `initialize`, stdin closing with a session in flight, `tool/call` with cancel and with disconnect); `-race` with many tools and events in flight; mutants on the error table, the version rule and tool cancellation.

## 9. Decided details
- `session/exec_command` stays, only for commands without their own method.
- Reconnecting after the binary dies (relaunch + `session/resume`) is a decision of the TS SDK, not of the protocol: the binary only offers the methods.

## 10. Future work (NOT in v1)
**Custom Store, Memory and ResourceLoader implemented in the client**, by reverse calls exactly like `tool/call`: new domains `store/<method>`, `memory/<method>` and `resources/<method>` that the binary calls on the client, selected with `kind: "custom"`. Left out of v1 because the three interfaces are large and move big data, and putting them in the first version would turn the protocol into a remote storage system. With the operation table it is additive: new methods, nothing published breaks.

## 11. Out of scope
HTTP in this path, camelCase, more than one client, a Notifier, the TS SDK itself (it comes next, on this).
