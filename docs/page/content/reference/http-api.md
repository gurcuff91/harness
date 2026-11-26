---
title: HTTP API
section: Reference
order: 410
summary: Every endpoint of the Harness HTTP/SSE server, generated from its OpenAPI document.
---
This page is generated from the OpenAPI document the server serves at `/api/docs/openapi.json`. An interactive version is served at `/api/docs`. Regenerate it with `make docs-api`.

## Conventions

- **Base URL.** `harness serve <addr>` listens on the address you give it; the in-process servers of the TUI and ACP use a random localhost port. Every path below starts with `/api`.
- **Authentication.** A server started with a secret (`harness serve --secret`, `HARNESS_SECRET`, or `server.WithSecret`) requires it on every request except `GET /api/server` and the API docs. Send it in the `X-Harness-Sec` header or as `Authorization: Bearer <secret>`. A missing or wrong secret is answered with 401. Without a secret the server has no authentication.
- **Errors.** Error responses are JSON: `{"error": {"message": "...", "details": {...}}}`. `details` is optional.
- **Events.** A session's events are delivered as Server-Sent Events (`text/event-stream`): each event is a `data: <json>` line followed by a blank line, and the JSON has a `type` field (`turn_start`, `text`, `thinking`, `tool_call`, `tool_result`, `tokens`, `error`, `turn_end`, and so on). See the events endpoint below.

## Server

Server metadata.

### `GET /api/server`

**Server info**

Returns harness version, frontend, URL, and process metadata.

No secret required.

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Server info | `application/json` `ServerInfo` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `cwd` | string | no |  |
| `frontend` | string | no | Calling frontend (tui, acp, server, cli) |
| `name` | string | no |  |
| `pid` | integer | no |  |
| `started_at` | string (date-time) | no |  |
| `url` | string | no |  |
| `version` | string | no |  |

## Settings

Configuration management.

### `GET /api/settings`

**Get settings**

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Current settings | `application/json` `Settings` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `active_model` | string | no |  |
| `thinking_level` | string | no | One of: `off`, `low`, `medium`, `high`, `xhigh`, `max`. |

### `PATCH /api/settings`

**Update settings**

Request body:

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `active_model` | string | no |  |
| `thinking_level` | string | no | One of: `off`, `low`, `medium`, `high`, `xhigh`, `max`. |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Updated settings | `application/json` `Settings` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `active_model` | string | no |  |
| `thinking_level` | string | no | One of: `off`, `low`, `medium`, `high`, `xhigh`, `max`. |

### `GET /api/settings/mcp`

**List MCP server configs**

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | MCP server config map | `application/json` object |

### `PUT /api/settings/mcp/{name}`

**Upsert MCP server config**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes |  |

Request body:

Content type: `application/json`.

Type: object.

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Saved config | - |

### `DELETE /api/settings/mcp/{name}`

**Delete MCP server config**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Deleted | `application/json` `Status` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `GET /api/settings/provider`

**List custom provider configs**

Custom OpenAI-compatible providers (proxies, gateways, self-hosted endpoints) configured under settings.json's "provider" collection. Changes take effect after POST /api/settings/provider/reload (or on the next process start).

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Custom provider config map | `application/json` object |

### `POST /api/settings/provider/reload`

**Apply custom providers live**

Re-reads settings.json's "provider" collection and applies it to the running process: added/edited/disabled/deleted entries take effect without a restart (sessions pick them up at their next turn). Built-in and programmatic providers are untouched; an entry whose name is used by a programmatic provider is skipped (code "reloaded_with_conflicts", message lists them).

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Applied | `application/json` `Status` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `PUT /api/settings/provider/{name}`

**Upsert custom provider config**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | Provider name — must not collide with a built-in provider |

Request body:

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `disabled` | boolean | no | Enabled by default; set true to skip loading this provider |
| `display` | string | no | Human-friendly display name; defaults to the provider name (the map key) when omitted |
| `headers` | object | no | Extra HTTP headers sent on every request |
| `models_url` | string | no | Models listing URL; defaults to "<url>/models" (openai) or "<url>/v1/models" (anthropic) when omitted |
| `reasoning_split` | boolean | no | type openai only (rejected for anthropic): send "reasoning_split": true on every request — needed for MiniMax-compatible backends to separate thinking from the answer. The provider proxy adds it too. |
| `type` | string | yes | API dialect: openai (Chat Completions) or anthropic (Messages — Anthropic itself, a gateway, or another harness instance's provider proxy, e.g. url http://host:8080/api/providers/claude-oauth/proxy to share that instance's subscription) One of: `openai`, `anthropic`. |
| `url` | string | yes | openai: base URL for chat completions (including any /v1). anthropic: the API root WITHOUT /v1 — /v1/messages and /v1/models are appended |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Saved config | `application/json` `CustomProvider` |
| 422 | Invalid config (unsupported type, missing url, or reserved name) | `application/json` `ErrorResponse` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `disabled` | boolean | no | Enabled by default; set true to skip loading this provider |
| `display` | string | no | Human-friendly display name; defaults to the provider name (the map key) when omitted |
| `headers` | object | no | Extra HTTP headers sent on every request |
| `models_url` | string | no | Models listing URL; defaults to "<url>/models" (openai) or "<url>/v1/models" (anthropic) when omitted |
| `reasoning_split` | boolean | no | type openai only (rejected for anthropic): send "reasoning_split": true on every request — needed for MiniMax-compatible backends to separate thinking from the answer. The provider proxy adds it too. |
| `type` | string | yes | API dialect: openai (Chat Completions) or anthropic (Messages — Anthropic itself, a gateway, or another harness instance's provider proxy, e.g. url http://host:8080/api/providers/claude-oauth/proxy to share that instance's subscription) One of: `openai`, `anthropic`. |
| `url` | string | yes | openai: base URL for chat completions (including any /v1). anthropic: the API root WITHOUT /v1 — /v1/messages and /v1/models are appended |

### `DELETE /api/settings/provider/{name}`

**Delete custom provider config**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Deleted | `application/json` `Status` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

## Providers

LLM provider connect/disconnect.

### `GET /api/providers`

**List providers**

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Provider list | `application/json` array of `Provider` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `activation` | string | no | One of: `credentials`, `env`, `auto`, `inactive`. |
| `active` | boolean | no |  |
| `credential_type` | string | no | One of: `api_key`, `oauth`, `none`. |
| `description` | string | no |  |
| `dialect` | string | no | Wire dialect of the provider's native API — what its proxy speaks. One of: `anthropic`, `openai`, `openai-responses`, `ollama`. |
| `display_name` | string | no |  |
| `error` | string | no | Why an ACTIVE provider has no models right now (its model fetch failed: expired token, unreachable, rejected key…) — what model_count 0 alone hides. Omitted when there is nothing to report. |
| `is_subscription` | boolean | no |  |
| `model_count` | integer | no |  |
| `name` | string | no |  |
| `proxy_path` | string | no | The provider's transparent-proxy root on this server. |

### `POST /api/providers/{name}/connect`

**Connect provider**

Connects an LLM provider using an API key or OAuth credentials. Validates by fetching models.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes |  |

Request body:

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `access_token` | string | no |  |
| `api_key` | string | no |  |
| `expires_at` | integer (int64) | no | Unix milliseconds |
| `refresh_token` | string | no |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Connected | `application/json` `Status` |
| 400 | Invalid credentials | - |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `POST /api/providers/{name}/disconnect`

**Disconnect provider**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Disconnected | `application/json` `Status` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `GET /api/providers/{name}/proxy/{path}`

**Transparent proxy to a provider's native API (GET, e.g. /v1/models)**

Same proxy as the POST operation, for the provider's read-only endpoints (model listing).

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes |  |
| `path` | path | string | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | The provider's own response, verbatim. | - |

### `POST /api/providers/{name}/proxy/{path}`

**Transparent proxy to a provider's native API**

Calls a connected provider's own API (Anthropic Messages, OpenAI Chat Completions, Codex Responses, …) in ITS dialect, without a session: the request body and the response — SSE streams included, flushed as they arrive — pass through untouched, and harness supplies the credentials (OAuth refresh included) and identity headers. Point a vendor SDK's base URL at /api/providers/{name}/proxy (any API key; Authorization/x-api-key headers sent by the caller are dropped). {path} is relative to the provider's native base URL: anthropic and claude-oauth → /v1/messages, /v1/messages/count_tokens (POST), /v1/models (GET); openai-style providers (openai, minimax, opencode-go, ollama-cloud and custom ones) → /chat/completions, /models; codex-oauth → /responses, /models; ollama → /v1/chat/completions, /v1/models, /api/chat, /api/generate, /api/tags, /api/show. Anything else is 403. What the caller omits (tools, system prompt, max_tokens) is simply absent: the provider answers with its own error where its API requires it. Nothing is created or persisted. The one adapted provider is claude-oauth, whose backend only accepts Claude Code-shaped requests: harness prepends the Claude Code identity block to system, maps tool names to Claude Code's (Fetch→WebFetch, others→mcp__x__<name>) and back in the response, and adds the billing header. The upstream's own status and body are returned as-is, 4xx/5xx included; harness's own errors use the standard error shape: 404 unknown provider, 409 provider not connected, 403 path/method not allowed, 400 unusable path or (claude-oauth) body, 413 body over the limit (32 MiB by default), 502 upstream unreachable. A stream that goes silent for 3 minutes is aborted, and the upstream call is cancelled when the caller disconnects.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes |  |
| `path` | path | string | yes | Path relative to the provider's native base URL (may contain slashes). |

Request body:

Content type: `application/json`.

Type: object.

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | The provider's own response (JSON or text/event-stream), verbatim. Any other status the upstream returns is passed through too. | - |
| 400 | Invalid path, or (claude-oauth) a body that is not a Messages request | - |
| 403 | Method/path not allowed through this provider's proxy | - |
| 404 | Unknown provider | - |
| 409 | Provider not connected | - |
| 413 | Request body over the proxy limit | - |
| 502 | Provider unreachable | - |

### `GET /api/providers/{name}/quota`

**Provider subscription usage**

The provider's subscription usage in ONE format, whatever its own API looks like: usage windows (used_percent 0-100 — providers that report what remains are converted — and resets_at, UTC) and, for providers that meter dollars, a balance. Supported: claude-oauth, codex-oauth, minimax, synthetic, opencode-go, ollama-cloud. Cached in memory per provider for 60 s; refresh=true asks the provider again, but never more than once every 5 s. When the provider cannot be read (rate limited, failing) and an earlier reading exists, that reading is returned with stale: true (fetched_at says how old it is) and a Retry-After header when the provider set one. 404 unknown provider, a provider that reports no quota, or an account without one; 409 not connected; 502 the provider could not be read and there is no earlier reading.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes |  |
| `refresh` | query | boolean | no | Bypass the 60 s cache (at most one provider call every 5 s). |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Usage | `application/json` `Quota` |
| 404 | Unknown provider, or no quota for it / this account | - |
| 409 | Provider not connected | - |
| 502 | The provider could not be read and there is no earlier reading | - |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `balance` | object | no | A dollar allowance (ollama-cloud). Omitted for providers that meter percentages only. |
| `fetched_at` | string (date-time) | yes | When this reading was taken from the provider. |
| `plan` | string | no | Plan name the provider reports (free, plus, pro…); omitted when it says none. |
| `provider` | string | yes |  |
| `stale` | boolean | no | true when the provider could not be read just now and this is the last good reading. |
| `windows` | array of object | yes |  |

## Models

Available models.

### `GET /api/models`

**List models**

The model registry: every model of every active provider with its capabilities, pricing and provenance (see the Model schema). An active provider whose model fetch fails simply contributes nothing here — GET /api/providers reports why (its 'error' field), and ?provider= turns it into an error response.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `provider` | query | string | no | Restrict to one provider. With it, a problem is reported instead of hidden: 404 unknown provider, 409 not connected, 502 its model fetch failed (with the provider's own message). |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Model list | `application/json` array of `Model` |
| 400 | Empty provider | - |
| 404 | Unknown provider (with ?provider=) | - |
| 409 | Provider not connected (with ?provider=) | - |
| 502 | The provider's model fetch failed (with ?provider=) | - |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `cache_read` | number | no | USD per million cache-read tokens. |
| `cache_write` | number | no | USD per million cache-write tokens. |
| `context_window` | integer | no | Tokens. |
| `dialect` | string | no | Wire dialect of the provider's native API — what its proxy (/api/providers/{provider}/proxy) speaks. One of: `anthropic`, `openai`, `openai-responses`, `ollama`. |
| `display_name` | string | no | Human-friendly name; falls back to id (sources.display_name = default). |
| `effort_levels` | object | no | Which of low\|medium\|high\|xhigh\|max this model supports. Absent or missing key = not reported, NOT unsupported. |
| `id` | string | no | Provider-local model id (what the provider's own API and the proxy take). |
| `input_price` | number | no | USD per million input tokens. Read with sources.pricing: 0 is free only when the source is catalog/provider, unknown otherwise. |
| `is_subscription` | boolean | no | Billed as a flat fee (Claude Max, Codex, OpenCode Go, MiniMax subscription keys) — prices then are informational. |
| `max_tokens` | integer | no | Output-token ceiling of the model's catalog entry. |
| `model` | string | no | provider/model — what NewSession and /api/sessions take. |
| `output_price` | number | no | USD per million output tokens. |
| `provider` | string | no |  |
| `sources` | object | no | Where each datum came from, by group: display_name, context_window, max_tokens, vision, thinking, pricing. provider = the provider's own API (authoritative); catalog = OpenRouter (a fallback, may describe a different deployment); registry = harness's built-in table; inferred = guessed from the name; default = nothing knew, a generic default (128k context, 32k output, capability false, name = id); unknown = pricing only, no source had prices so the 0s mean no data. Omitted for models built without provenance (a custom provider's own fetch hook). |
| `thinking` | boolean | no | Supports reasoning/thinking. |
| `thinking_adaptive` | boolean | no | Anthropic adaptive thinking (effort-driven). |
| `thinking_legacy` | boolean | no | Anthropic legacy budget_tokens thinking. |
| `vision` | boolean | no | Accepts image input. |

### `GET /api/models/{provider}/{id}`

**Get one model**

One model's full listing (see the Model schema). {id} is the provider-local model id and may contain slashes or colons (org/model, gemma3:4b).

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `provider` | path | string | yes |  |
| `id` | path | string | yes | Provider-local model id (may contain slashes). |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | The model | `application/json` `Model` |
| 404 | Unknown provider or model | - |
| 409 | Provider not connected | - |
| 502 | The provider's model fetch failed | - |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `cache_read` | number | no | USD per million cache-read tokens. |
| `cache_write` | number | no | USD per million cache-write tokens. |
| `context_window` | integer | no | Tokens. |
| `dialect` | string | no | Wire dialect of the provider's native API — what its proxy (/api/providers/{provider}/proxy) speaks. One of: `anthropic`, `openai`, `openai-responses`, `ollama`. |
| `display_name` | string | no | Human-friendly name; falls back to id (sources.display_name = default). |
| `effort_levels` | object | no | Which of low\|medium\|high\|xhigh\|max this model supports. Absent or missing key = not reported, NOT unsupported. |
| `id` | string | no | Provider-local model id (what the provider's own API and the proxy take). |
| `input_price` | number | no | USD per million input tokens. Read with sources.pricing: 0 is free only when the source is catalog/provider, unknown otherwise. |
| `is_subscription` | boolean | no | Billed as a flat fee (Claude Max, Codex, OpenCode Go, MiniMax subscription keys) — prices then are informational. |
| `max_tokens` | integer | no | Output-token ceiling of the model's catalog entry. |
| `model` | string | no | provider/model — what NewSession and /api/sessions take. |
| `output_price` | number | no | USD per million output tokens. |
| `provider` | string | no |  |
| `sources` | object | no | Where each datum came from, by group: display_name, context_window, max_tokens, vision, thinking, pricing. provider = the provider's own API (authoritative); catalog = OpenRouter (a fallback, may describe a different deployment); registry = harness's built-in table; inferred = guessed from the name; default = nothing knew, a generic default (128k context, 32k output, capability false, name = id); unknown = pricing only, no source had prices so the 0s mean no data. Omitted for models built without provenance (a custom provider's own fetch hook). |
| `thinking` | boolean | no | Supports reasoning/thinking. |
| `thinking_adaptive` | boolean | no | Anthropic adaptive thinking (effort-driven). |
| `thinking_legacy` | boolean | no | Anthropic legacy budget_tokens thinking. |
| `vision` | boolean | no | Accepts image input. |

## Mcp

MCP server status.

### `GET /api/mcp/status`

**MCP connection status**

Returns live connection status and tool count for each configured MCP server.

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | MCP status list | `application/json` array of `MCPStatus` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `connected` | boolean | no |  |
| `error` | string | no |  |
| `name` | string | no |  |
| `tool_count` | integer | no |  |

## Memory

Persistent memory: list, search, read, create/update and delete (project-scoped or global).

### `GET /api/memories`

**List or search memories**

Paginated listing (most recently updated first) or full-text search (ranked by relevance). Returns an empty result when the agent has no memory.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `cwd` | query | string | no | Scope filter: omit for all projects; "<global>" for global memories only; a project path for that project's memories plus the global ones |
| `query` | query | string | no | Full-text search (prefix-matched terms, ANDed); omit to list |
| `include_content` | query | boolean | no | false = lightweight listing without content |
| `skip` | query | integer | no |  |
| `limit` | query | integer | no |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Memory search result | `application/json` `MemorySearchResult` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `limit` | integer | no |  |
| `results` | array of `MemoryEntry` | no |  |
| `returned` | integer | no |  |
| `skip` | integer | no |  |
| `total` | integer | no |  |

### `GET /api/memories/{slug}`

**Get one memory**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `slug` | path | string | yes | Memory id within its scope (kebab-case by convention; URL-escape anything else) |
| `cwd` | query | string | yes | Scope: the project path, or "<global>" for a global (cross-project) memory |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | The memory | `application/json` `MemoryEntry` |
| 400 | Missing cwd or slug | `application/json` `ErrorResponse` |
| 404 | Memory not found | `application/json` `ErrorResponse` |
| 503 | Memory is not enabled on this agent | `application/json` `ErrorResponse` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `content` | string | no | Omitted when include_content=false |
| `created_at` | integer (int64) | no | Unix milliseconds |
| `cwd` | string | no | Project path, or "<global>" for a global memory |
| `score` | number | no | Relevance (search mode only; higher = more relevant) |
| `slug` | string | no |  |
| `updated_at` | integer (int64) | no | Unix milliseconds |

### `PUT /api/memories/{slug}`

**Create or update a memory**

Upsert by scope + slug (same semantics as the agent's MemoWrite tool). Returns the stored memory.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `slug` | path | string | yes | Memory id within its scope (kebab-case by convention; URL-escape anything else) |
| `cwd` | query | string | yes | Scope: the project path, or "<global>" for a global (cross-project) memory |

Request body (required):

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `content` | string | yes | The memory text (must not be blank) |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Updated | `application/json` `MemoryEntry` |
| 201 | Created | `application/json` `MemoryEntry` |
| 400 | Missing cwd/slug or malformed JSON | `application/json` `ErrorResponse` |
| 422 | Blank content | `application/json` `ErrorResponse` |
| 503 | Memory is not enabled on this agent | `application/json` `ErrorResponse` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `content` | string | no | Omitted when include_content=false |
| `created_at` | integer (int64) | no | Unix milliseconds |
| `cwd` | string | no | Project path, or "<global>" for a global memory |
| `score` | number | no | Relevance (search mode only; higher = more relevant) |
| `slug` | string | no |  |
| `updated_at` | integer (int64) | no | Unix milliseconds |

Response 201 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `content` | string | no | Omitted when include_content=false |
| `created_at` | integer (int64) | no | Unix milliseconds |
| `cwd` | string | no | Project path, or "<global>" for a global memory |
| `score` | number | no | Relevance (search mode only; higher = more relevant) |
| `slug` | string | no |  |
| `updated_at` | integer (int64) | no | Unix milliseconds |

### `DELETE /api/memories/{slug}`

**Delete a memory**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `slug` | path | string | yes | Memory id within its scope (kebab-case by convention; URL-escape anything else) |
| `cwd` | query | string | yes | Scope: the project path, or "<global>" for a global (cross-project) memory |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Deleted | `application/json` `Status` |
| 400 | Missing cwd or slug | `application/json` `ErrorResponse` |
| 404 | Memory not found | `application/json` `ErrorResponse` |
| 503 | Memory is not enabled on this agent | `application/json` `ErrorResponse` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

## Schedules

Cron-scheduled prompts.

### `GET /api/schedules`

**List schedules**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `owner` | query | string | no | Filter by owner session ID |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Schedule list | `application/json` array of `Schedule` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `cron` | string | no |  |
| `last_run` | integer (int64) | no | Unix milliseconds |
| `prompt` | string | no |  |
| `runs` | integer | no |  |
| `slug` | string | no |  |

## Sessions

Agent session lifecycle.

### `GET /api/sessions`

**List sessions**

Persisted sessions (every session ever saved, active or not).

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `cwd` | query | string | no | Filter by working directory |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Session list | `application/json` array of `SessionMeta` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `compact_count` | integer | no |  |
| `compact_offset` | integer | no |  |
| `created_at` | string (date-time) | no |  |
| `cwd` | string | no |  |
| `id` | string (uuid) | no |  |
| `last_active_at` | string (date-time) | no |  |
| `model` | string | no |  |
| `name` | string | no |  |
| `stats` | `SessionStats` | no |  |
| `thinking` | string | no |  |

### `POST /api/sessions`

**Create session**

Request body (required):

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `channels` | array of object | no | External conversations this session listens to while it is open: their messages for it (Slack: mentions in a channel, every message in a DM) become prompts with origin "<type>:<id>", and the turns they start are posted back there. The channel's tools and system-prompt section are added. Never persisted. One conversation, one live session (across processes): taken → 409; unknown type (no such channel registered, e.g. no Slack login) or unresolvable ref → 400. |
| `cwd` | string | no |  |
| `model` | string | yes |  |
| `name` | string | no |  |
| `profiles` | array of string | no | Bind the session to the agent's registered profiles by name (e.g. "slack") — their directives and tools apply to it. The names are persisted: every later open of the session, by any client, re-applies whichever are registered then. Unknown name → 400. |

Responses:

| Status | Description | Body |
|---|---|---|
| 201 | Session created | `application/json` `SessionDetail` |

### `POST /api/sessions/import`

**Import a session export**

The body is a session export (what GET /api/sessions/{id}/export returns). It is opened as a NEW session — a new id, created now, the same conversation, name, model (falling back like a resume when unavailable), thinking, compaction state and stats; nothing existing is touched. 400 when the body is not a valid export (the message names the line; nothing is created), 413 when it exceeds the server's limit (1 GiB by default).

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `cwd` | query | string | no | Project directory of the new session (default: the exported one's). |
| `channel` | query | string | no | type:ref of an input channel to bind (repeatable); none is inherited. |

Request body (required):

Content type: `application/x-ndjson`.

Type: string.

Responses:

| Status | Description | Body |
|---|---|---|
| 201 | The new session | `application/json` `SessionDetail` |
| 400 | Not a valid session export | - |
| 413 | Body over the import limit | - |

### `GET /api/sessions/{id}`

**Get session**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Session detail | `application/json` `SessionDetail` |

### `DELETE /api/sessions/{id}`

**Delete session**

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Deleted | `application/json` `Status` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `POST /api/sessions/{id}/ask`

**Send prompt (sync)**

Sends a prompt and blocks until the agent's turn completes, returning the final assistant text. The synchronous counterpart to /prompt (fire-and-forget). The HTTP client controls the timeout via its own request context.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Request body (required):

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `images` | array of `ImageData` | no |  |
| `origin` | string | no | Where the prompt came from — any short string: "user" (default; also a fired schedule, which is wrapped in <scheduled slug="…"> instead of having an origin), "colleague:<id>" (another agent's question through the colleague channel; "colleague:<id>#reply" for its answer to this session's), "herdr" (a message Herdr typed into the TUI), "slack:<id>" (an input channel), a transport ("telegram", "acp") or your own. Echoed on received_prompt/follow_up_start and stored with the message; frontends use it to pick an icon. The model never sees it. |
| `text` | string | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Agent response | `application/json` `AskResponse` |
| 400 | Bad request (missing text, images unsupported) | `application/json` `ErrorResponse` |
| 404 | Session not found | `application/json` `ErrorResponse` |
| 500 | Agent error | `application/json` `ErrorResponse` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `text` | string | no | Final assistant response text |

### `POST /api/sessions/{id}/close`

**Close session**

Flushes the session to disk and deactivates it.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Closed | `application/json` `Status` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `GET /api/sessions/{id}/commands`

**List commands**

Returns the dynamic command set the session accepts (built-ins + loaded skills).

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Command list | `application/json` array of `CommandDef` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `description` | string | no |  |
| `name` | string | no |  |
| `params` | array of `ParamDef` | no |  |

### `POST /api/sessions/{id}/commands`

**Execute command**

Runs a session command. Built-ins: compact, reset, rename, model, thinking. Skills: skill:<name>.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Request body (required):

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `command` | string | yes | One of: `compact`, `reset`, `rename`, `model`, `thinking`, `max-iter`, `export`, `import`, `skill:<name>`. |
| `params` | object | no |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Command result | `application/json` `Status` |
| 202 | Command started async (compact, skill) | - |
| 400 | Unknown command or bad params | - |
| 409 | Session is busy | - |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `GET /api/sessions/{id}/context`

**Context breakdown**

Returns estimated token counts for system, tools, and conversation slots in the model's context window.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Context breakdown | `application/json` `ContextBreakdown` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `context_window` | integer | no |  |
| `conversation` | integer | no | Estimated conversation tokens |
| `estimated_total` | integer | no |  |
| `free_space` | integer | no |  |
| `last_real_total` | integer | no | Actual token count from last API response |
| `system` | integer | no | Estimated system prompt tokens |
| `tools` | integer | no | Estimated tool schema tokens |

### `GET /api/sessions/{id}/events`

**Stream events (SSE)**

Opens a Server-Sent Events stream for the session. Emits turn_start, loop_start, thinking, text, tool_start, tool_args, tool_call, tool_result, tokens, turn_end, error, and more.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | SSE stream | `text/event-stream` string |

### `GET /api/sessions/{id}/export`

**Export a session**

Streams the session (open or closed) as a JSONL export: line 1 is a header {format: "harness-session-export", version: 1, exported_at, harness, meta}, every following line is one message of its log, exactly as stored. Content-Disposition carries the proposed file name (<name>-<id8>.export.jsonl). 404 when the session does not exist.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | The export | `application/x-ndjson` string |
| 404 | No such session | - |

### `POST /api/sessions/{id}/fork`

**Fork session**

Creates a new session that is an exact copy of the parent (same history, model, stats) with a new ID and fresh timestamps. Returns 409 if the parent is busy.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Request body:

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `channels` | array of object | no | Conversations the fork listens to (it never inherits its parent's). |
| `profiles` | array of string | no | Profile names that REPLACE the ones the fork inherits from its parent: absent or null = the parent's, [] = none. Unknown name → 400. |

Responses:

| Status | Description | Body |
|---|---|---|
| 201 | Fork created | `application/json` `SessionDetail` |
| 400 | Unknown profile or invalid channel | - |
| 404 | Session not found | - |
| 409 | Session is busy | - |

### `GET /api/sessions/{id}/info`

**Session info**

Returns a single-round-trip snapshot of session metadata, stats, MCP count, schedule count, and busy state.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Session info | `application/json` `SessionInfo` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `arch` | string | no |  |
| `busy` | boolean | no |  |
| `mcp_connected` | integer | no |  |
| `os` | string | no |  |
| `queue_depth` | integer | no |  |
| `schedule_count` | integer | no |  |
| `session` | `SessionDetail` | no |  |
| `terminal` | string | no | The terminal the session runs in, when the server's agent runs in one (the TUI); omitted otherwise. |
| `version` | string | no |  |

### `GET /api/sessions/{id}/messages`

**Get message history**

Returns the session's message history as a plain JSON array — all of it by default, or a suffix: ?from=<n> starts at absolute index n (n >= total returns []), ?compactions=<k> starts just after the k-th-to-last compaction checkpoint (k >= 1; with fewer checkpoints than k the whole history is returned). from and compactions are mutually exclusive (400 if both). X-Messages-From is the absolute index of the first returned message (the {index} the image endpoint counts from) and X-Messages-Total the length of the full log. The skipped part of the log is never parsed, so the tail of a huge compacted session is cheap. Works for active and persisted sessions.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |
| `from` | query | integer | no | Start at this absolute message index (>= 0). Mutually exclusive with compactions. |
| `compactions` | query | integer | no | Start just after the k-th-to-last compaction checkpoint (k >= 1; the TUI uses 2). Mutually exclusive with from. |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Message list | `application/json` array of `Message` |
| 400 | Invalid from/compactions (negative, non-numeric, compactions < 1) or both given | - |
| 404 | Session not found | - |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `created_at` | string (date-time) | no | When the message was added to the log (UTC, millisecond precision). Absent in messages written before v0.109.0. Never sent to the model. |
| `meta` | object | no | Present only when something is set: origin (where a prompt came from), is_compaction, is_system_generated. |
| `parts` | array of object | no |  |
| `role` | string | no | One of: `user`, `assistant`. |

### `GET /api/sessions/{id}/messages/{index}/images/{n}`

**Get a message image**

Raw bytes of the n-th image (0-based, in message order) of the message at absolute history index {index} — what received_prompt/follow_up_start's message_index + images point at, and an alternative to decoding base64 from /messages. Works for active and persisted sessions. Cached as immutable; the ETag changes after a /reset (which reuses indices).

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |
| `index` | path | integer | yes |  |
| `n` | path | integer | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | The image (Content-Type is the image's own MIME type; Cache-Control: private, max-age=31536000, immutable; ETag) | `image/*` string (binary) |
| 304 | Not modified (If-None-Match matched the ETag) | - |
| 400 | index or n is not a non-negative integer | `application/json` `ErrorResponse` |
| 404 | Session, message or image not found | `application/json` `ErrorResponse` |

### `POST /api/sessions/{id}/prompt`

**Send prompt (async)**

Queues a user prompt into the session. Returns immediately with status 'started' or 'queued'.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Request body (required):

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `images` | array of `ImageData` | no |  |
| `origin` | string | no | Where the prompt came from — any short string: "user" (default; also a fired schedule, which is wrapped in <scheduled slug="…"> instead of having an origin), "colleague:<id>" (another agent's question through the colleague channel; "colleague:<id>#reply" for its answer to this session's), "herdr" (a message Herdr typed into the TUI), "slack:<id>" (an input channel), a transport ("telegram", "acp") or your own. Echoed on received_prompt/follow_up_start and stored with the message; frontends use it to pick an icon. The model never sees it. |
| `text` | string | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 202 | Prompt accepted | `application/json` `Status` |

Response 202 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `POST /api/sessions/{id}/resume`

**Resume session**

Reactivates a persisted session, loading its history from disk. The body is optional. Models are scoped per session, so the session's own model is used when it is available; if its provider is not (disconnected, credentials removed, model retired) the session still opens — on the harness's default model (settings 'active_model', else the first available model of any provider) — and the response carries 'model_fallback' saying what changed. The new model is persisted. Only when neither the session's model nor any fallback is available is the resume refused (409).

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Request body:

Content type: `application/json`.

| Field | Type | Required | Description |
|---|---|---|---|
| `channels` | array of object | no | External conversations the reopened session listens to while it is open (never persisted; a resume without them listens to none). Taken by another live session → 409; unknown type or unresolvable ref → 400; asking an already-open session for a conversation it does not listen to → 400. |
| `profiles` | array of string | no | Profile names that REPLACE the session's persisted binding: absent or null = unchanged, [] = remove them all, names = exactly those. If the session is already active, a changed binding applies at its next turn boundary, never mid-turn. Unknown name → 400. |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Resumed (or already active — idempotent). May carry 'model_fallback'. | `application/json` `SessionDetail` |
| 404 | Session not found | - |
| 409 | The session's model is unavailable AND there is no model to fall back to — connect a provider or set a default model | - |

### `POST /api/sessions/{id}/stop`

**Stop session**

Cancels the current in-flight turn.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Stopped | `application/json` `Status` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `status` | object | no |  |

### `GET /api/sessions/{id}/sysprompt`

**System prompt**

Returns the full system prompt the session sends to the provider — base prompt, skills, project context, memory block and every directive (agent-wide plus the session's own) — as raw markdown. Only valid for active sessions; safe while the session is busy.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | The system prompt | `text/markdown` string |
| 400 | Session is not active | `application/json` `ErrorResponse` |

### `GET /api/sessions/{id}/tools`

**List session tools**

Returns the full set of tool definitions (name, description, input_schema) currently registered for the session — the exact set sent to the provider on the next turn, including any MCP tools and SDK-supplied tools alongside the built-ins. Pure introspection, not tied to any transport.

Parameters:

| Name | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string (uuid) | yes |  |

Responses:

| Status | Description | Body |
|---|---|---|
| 200 | Tool definitions | `application/json` array of `ToolDef` |

Response 200 fields:

| Field | Type | Required | Description |
|---|---|---|---|
| `description` | string | no |  |
| `input_schema` | object | no | JSON Schema for the tool's input arguments |
| `name` | string | no |  |
