---
title: HTTP/SSE server
section: Frontends & channels
order: 210
summary: Run harness serve, secure it with a secret, drive sessions over HTTP and SSE, share a provider subscription through the proxy, and read provider quotas.
---

`harness serve` runs the agent headless and exposes it over HTTP. Every other frontend (TUI, ACP, `-p`) talks to the same API in-process, so anything they do can be done by your own client. The Go client is described in [Client](../sdk/client.md); the full route list is in the [HTTP API](../reference/http-api.md) reference.

## Starting the server

```bash
$ harness serve :8080
$ harness serve 127.0.0.1:8080
```

A missing host means this machine only: `:8080` listens on `127.0.0.1:8080`. A bare port (`8080`) is an error. To be reachable from the network, give an explicit host such as `0.0.0.0:8080`.

`serve` accepts these flags:

| Flag | Meaning |
| --- | --- |
| `--secret=STRING` | Require this secret on every request. |
| `--insecure` | Allow listening beyond localhost without a secret. |
| `--with-colleagues` | Make every session a [colleague](colleagues.md). |
| `--with-scheduler` | Run the cron scheduler and give the agent the Schedule tools. |
| `--telegram-token=STRING` | Telegram bot token for this process. |

`HARNESS_WITH_COLLEAGUES` and `HARNESS_WITH_SCHEDULER` (`1`, `true` or `yes` to enable) work as alternatives to the flags. `serve` has no channel flag: clients bind sessions to Slack or Telegram conversations through the API (see [Binding channels](#binding-channels)).

## The secret

The server has no user accounts. Anyone who reaches it can use your sessions, your connected providers (through the proxy) and the tools, including Bash. Protect it with a secret.

```bash
$ harness gen-secret
hs_…
$ export HARNESS_SECRET=hs_…
$ harness serve 0.0.0.0:8080
```

- Prefer the `HARNESS_SECRET` environment variable: a `--secret` flag is visible in the process list. If both are set, the flag wins.
- Listening beyond localhost without a secret is refused unless you pass `--insecure`.
- On localhost without a secret the server starts and logs a warning.
- Without TLS the secret travels in the clear. It keeps casual callers out; put the server behind a TLS proxy if the network is not trusted.

Clients send the secret in either header:

```bash
$ curl -H "X-Harness-Sec: $HARNESS_SECRET" http://127.0.0.1:8080/api/sessions
$ curl -H "Authorization: Bearer $HARNESS_SECRET" http://127.0.0.1:8080/api/sessions
```

A missing or wrong secret gets `401` with `{"error":{"message":"missing or invalid secret"}}` and a `WWW-Authenticate` header; the response never says which part was wrong. Three paths are open without it: `GET /api/server` (server name, version and pid; also how a client checks the server is up), `GET /api/docs` and `GET /api/docs/openapi.json`. CORS preflight (`OPTIONS`) requests are answered without credentials.

The TUI, ACP and the channels start their own in-process server on localhost with no secret.

## API documentation

The running server documents itself: open `http://127.0.0.1:8080/api/docs` in a browser, or fetch the OpenAPI document from `/api/docs/openapi.json`.

## A first session

Create a session, subscribe to its events, then send a prompt. Subscribe before prompting so no event is missed.

```bash
$ BASE=http://127.0.0.1:8080
$ H="Authorization: Bearer $HARNESS_SECRET"
$ curl -s -H "$H" -X POST $BASE/api/sessions \
    -d '{"model":"anthropic/claude-sonnet-4-20250514","cwd":"/path/to/project"}'
```

The response (`201`) is the session detail; use its `id`:

```json
{"id":"de2f679d-…","cwd":"/path/to/project","name":"New Session 2026-11-26 10:15","model":"anthropic/claude-sonnet-4-20250514","thinking":"medium","max_iterations":120, "…":"…"}
```

`model` (in `provider/model` form) is required; `cwd`, `name`, `profiles` and `channels` are optional. In one terminal, stream events:

```bash
$ curl -N -H "$H" $BASE/api/sessions/$ID/events
```

In another, send a prompt:

```bash
$ curl -s -H "$H" -X POST $BASE/api/sessions/$ID/prompt -d '{"text":"list the Go files in this project"}'
```

```json
{"status":{"code":"started"}}
```

The stream prints one `data:` line per event:

```text
data: {"type":"turn_start"}

data: {"type":"tool_start","tool_name":"Bash","tool_id":"toolu_01"}

data: {"type":"text","delta":"Found 12 files"}

data: {"type":"turn_end"}
```

If the session is already running a turn, the prompt is queued and the status code is `queued`. To get one answer without streaming, use `POST /api/sessions/{id}/ask`: it blocks until the turn ends and returns `{"text":"…"}`.

```bash
$ curl -s -H "$H" -X POST $BASE/api/sessions/$ID/ask -d '{"text":"what does main.go do?"}'
```

A prompt body accepts `text`, `images` and `origin`. `origin` is a free-form label for where the prompt came from (`user` by default, `slack:C1`, `colleague:x`, or your own); it is echoed on the `received_prompt` and `follow_up_start` events. Requests for an unknown session id answer `404` with `{"error":{"message":"session not found"}}`.

Other session routes cover resuming, forking, closing and deleting sessions, reading messages, session commands (`rename`, `thinking`, `model`, `max-iter`, `compact`, `reset`, `export`, `import`), `info`, `context`, the system prompt and the tool list. See the [HTTP API](../reference/http-api.md) reference.

### Events

Every SSE `data:` line is a JSON object with a `type`:

| Type | Meaning |
| --- | --- |
| `turn_start`, `turn_end` | A turn began or finished. |
| `loop_start`, `loop_end` | One ReAct iteration (`loop` is its 0-based index). |
| `thinking`, `thinking_end` | Reasoning text fragment (`delta`) and its end. |
| `text`, `text_end` | Response text fragment (`delta`) and its end. |
| `tool_start`, `tool_args`, `tool_call`, `tool_result` | A tool call's life cycle, correlated by `tool_id`. `tool_result` carries `output`, `duration` (ms) and `is_error`. |
| `received_prompt`, `follow_up_start` | A prompt was received or a queued one is starting; carries `text`, `origin`, `message_index` and `images` metadata. |
| `tokens` | Usage: `input`, `context_usage`, `context_window`, `total_input`, `total_output`, `cache_read`, `cache_write`, `cost_usd`. |
| `retry` | A transient network failure is being retried (`attempt`, `max`, `error`). |
| `error` | A failure (`message`, optional `details`). |
| `max_iterations_reached` | The iteration budget ran out (`max_iterations`). |
| `subagent_start`, `subagent_stop` | A sub-agent was launched or ended (`name`; `result` on the stop is `ok`, `error` or `cancelled`). The stop always arrives, before the answer reaches the session as a prompt with origin `subagent:<name>`. |
| `compact_start`, `compact_end` | Compaction started or finished (`summary`). |
| `stop` | The turn was stopped by the user; not an error. |

A turn ends with `turn_end` in every case, including errors and stops.

The session detail (create, resume, fork, `GET /api/sessions/{id}` and `/info`) lists the sub-agents running right now in `subagents` (launch order, omitted when none). A client that connects late starts from it and then applies `subagent_start` and `subagent_stop`.

## Binding channels

A session can listen to a Slack or Telegram conversation while it is open. Pass `channels` when creating or resuming it:

```bash
$ curl -s -H "$H" -X POST $BASE/api/sessions \
    -d '{"model":"anthropic/claude-sonnet-4-20250514","cwd":"/path/to/project","channels":[{"type":"slack","ref":"#support"}]}'
```

For Telegram the `ref` is the comma-separated trusted usernames. See [Slack](slack.md) and [Telegram](telegram.md). A conversation that another live session already listens to answers `409`; an unregistered channel type or an unresolvable `ref` answers `400`. Channel bindings are never persisted: a resume without `channels` listens to nothing.

## Sharing a provider through the proxy

`ANY /api/providers/{name}/proxy/<path>` forwards your request, unchanged, to the provider's native API (Anthropic Messages, OpenAI Chat Completions, Codex Responses, Ollama). Harness adds the credentials, refreshing OAuth tokens as needed, and the identity headers the provider requires, and drops the ones you sent. Responses, including SSE streams, are passed through as they arrive. Nothing is created: no session, no stored state.

This lets an official vendor SDK use a subscription connected to the server: point its base URL at the proxy path.

```bash
$ curl -s -H "$H" $BASE/api/providers/claude-oauth/proxy/v1/models
```

`GET /api/providers` lists each provider with its `dialect` and `proxy_path`.

Harness answers its own failures with these statuses; anything the provider answers (4xx and 5xx included) is passed through as is:

| Status | Cause |
| --- | --- |
| 400 | Invalid proxy path or unusable body. |
| 403 | Method and path not allowed for that provider. |
| 404 | Unknown provider. |
| 409 | Provider not connected or without valid credentials. |
| 413 | Body over the limit (32 MiB by default). |
| 502 | Provider unreachable. |

### Using another instance's subscription

A second harness instance can use the first one's subscription by declaring a custom provider that points at the proxy. For Claude, use the `anthropic` type and the proxy path without `/v1`:

```bash
$ harness provider add shared-claude --type anthropic \
    --url http://host:8080/api/providers/claude-oauth/proxy \
    --header "X-Harness-Sec:$HARNESS_SECRET"
```

The second instance sends plain Messages requests; the first applies the Claude Code identity once. Custom providers are described in [Custom providers](../sdk/providers.md).

## Provider quotas

`GET /api/providers/{name}/quota` reports the usage windows of a provider's subscription in one format. It is available for `claude-oauth`, `codex-oauth`, `minimax`, `synthetic`, `opencode-go` and `ollama-cloud`; any other provider answers `404`.

```bash
$ curl -s -H "$H" "$BASE/api/providers/claude-oauth/quota"
```

The response has `provider`, optional `plan`, `fetched_at`, optional `stale`, `windows` and an optional `balance`. Each window has `name`, `label`, optional `scope`, `used_percent` (0 to 100) and `resets_at` (UTC). A `balance` has `remaining_usd` and `allowance_usd`.

Caching rules:

- A good reading is served for 60 seconds.
- `?refresh=true` forces a new reading, but a provider is never called more than once every 5 seconds.
- If a fetch fails and an earlier reading exists, that reading is served with `"stale": true`.
- A `Retry-After` from the provider (or 60 seconds after a bare 429) blocks every call, `refresh` included, and is returned in the `Retry-After` header.
- Connecting or disconnecting a provider drops its cached reading, so a previous account's numbers are never served.

Errors: `404` unknown provider, a provider without quota, or an account without a subscription; `409` provider not connected; `502` the provider could not be read and there is no earlier reading.

## Related options

When you embed the server in Go instead, `server.Run(ctx, agent, ...)` takes the same address and secret as options; see [Embedding an agent](../sdk/embedding.md).
