---
title: RPC protocol
section: Reference
order: 415
summary: The JSON-RPC protocol of harness rpc: every method with its parameters and result, the events, the notifications and the errors.
---

> **Note** This page is generated from the protocol schema (`rpc/schema.json`); it cannot drift from the code. Run `make docs-rpc` to regenerate it.

`harness rpc` speaks JSON-RPC 2.0 over stdin and stdout, one JSON message per line. It is the protocol behind the SDKs for other languages: the client describes the agent in `initialize`, then drives sessions and administration. Protocol version: **1**.

## Conventions

- **Direction.** The client sends requests; the binary answers. The binary also sends the `event` and `tool/cancel` notifications and the `tool/call` request to the client.
- **Names.** `initialize`, then `<domain>/<method>`: `agent/*`, `session/*` and `admin/<service>/*`. Fields and compound names are `snake_case`, like the HTTP API.
- **Start.** `initialize` must come first. Its `protocol_version` must match; otherwise the binary answers with a `protocol_version` error and exits.
- **One client.** Closing stdin shuts the binary down: open sessions and the agent are closed.
- **Logs.** Anything that is not protocol goes to stderr; stdout carries only messages.
- **Errors.** A JSON-RPC error with the code and `data.kind` listed in [Errors](#errors).

## Methods

### admin / mcp

#### `admin/mcp/delete`

Removes a configured MCP server.

**Params (`AdminMCPNameParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |

**Result:** none.

#### `admin/mcp/get`

Returns one configured MCP server.

**Params (`AdminMCPNameParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |

**Result (`MCPServer`):**

| Field | Type | Description |
|---|---|---|
| `args` | array of string (optional) |  |
| `command` | string (optional) |  |
| `connection_timeout` | integer (optional) |  |
| `cwd` | string (optional) |  |
| `disabled` | boolean (optional) |  |
| `env` | map of string (optional) |  |
| `headers` | map of string (optional) |  |
| `timeout` | integer (optional) |  |
| `url` | string (optional) |  |

#### `admin/mcp/list`

Lists the configured MCP servers.

**Params:** none.

**Result (`AdminMCPListResult`):**

| Field | Type | Description |
|---|---|---|
| `servers` | map of `MCPServer` |  |

#### `admin/mcp/put`

Validates and stores (or replaces) an MCP server.

**Params (`AdminMCPPutParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |
| `server` | `MCPServer` |  |

**Result:** none.

#### `admin/mcp/set_enabled`

Enables or disables a configured MCP server, keeping the rest of its config.

**Params (`AdminMCPSetEnabledParams`):**

| Field | Type | Description |
|---|---|---|
| `enabled` | boolean |  |
| `name` | string |  |

**Result:** none.

### admin / memory

#### `admin/memory/configured`

Reports whether this agent has a memory store.

**Params:** none.

**Result (`AdminMemoryConfiguredResult`):**

| Field | Type | Description |
|---|---|---|
| `configured` | boolean |  |

#### `admin/memory/delete`

Removes one memory.

**Params (`AdminMemoryKeyParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string |  |
| `slug` | string |  |

**Result:** none.

#### `admin/memory/get`

Returns one memory. cwd is a project path, or "<global>" for a global memory.

**Params (`AdminMemoryKeyParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string |  |
| `slug` | string |  |

**Result (`Memory`):**

| Field | Type | Description |
|---|---|---|
| `content` | string (optional) |  |
| `created_at` | integer |  |
| `cwd` | string (optional) |  |
| `score` | number (optional) |  |
| `slug` | string |  |
| `updated_at` | integer |  |

#### `admin/memory/list`

Lists memories, paginated. Empty when memory is not configured.

**Params (`AdminMemoryListParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string (optional) |  |
| `include_content` | boolean (optional) |  |
| `limit` | integer (optional) |  |
| `skip` | integer (optional) |  |

**Result (`SearchResult`):**

| Field | Type | Description |
|---|---|---|
| `limit` | integer |  |
| `results` | array of `Memory` |  |
| `returned` | integer |  |
| `skip` | integer |  |
| `total` | integer |  |

#### `admin/memory/put`

Creates or replaces one memory (an upsert by cwd+slug).

**Params (`AdminMemoryPutParams`):**

| Field | Type | Description |
|---|---|---|
| `content` | string |  |
| `cwd` | string |  |
| `slug` | string |  |

**Result (`AdminMemoryPutResult`):**

| Field | Type | Description |
|---|---|---|
| `created` | boolean |  |
| `memory` | `Memory` |  |

#### `admin/memory/search`

Full-text searches memories (an empty query lists them), paginated. Empty when memory is not configured.

**Params (`AdminMemorySearchParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string (optional) |  |
| `include_content` | boolean (optional) |  |
| `limit` | integer (optional) |  |
| `query` | string (optional) |  |
| `skip` | integer (optional) |  |

**Result (`SearchResult`):**

| Field | Type | Description |
|---|---|---|
| `limit` | integer |  |
| `results` | array of `Memory` |  |
| `returned` | integer |  |
| `skip` | integer |  |
| `total` | integer |  |

### admin / providers

#### `admin/providers/connect`

Saves a provider's credentials, validates them and activates it; answers how many models it offers.

**Params (`AdminProvidersConnectParams`):**

| Field | Type | Description |
|---|---|---|
| `access_token` | string (optional) |  |
| `account_id` | string (optional) |  |
| `api_key` | string (optional) |  |
| `expires_at` | integer (optional) |  |
| `name` | string |  |
| `refresh_token` | string (optional) |  |
| `subscription_type` | string (optional) |  |

**Result (`AdminProvidersConnectResult`):**

| Field | Type | Description |
|---|---|---|
| `models` | integer |  |

#### `admin/providers/credential_type`

Reports the kind of credential a provider needs.

**Params (`AdminProviderNameParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |

**Result (`AdminProvidersCredentialTypeResult`):**

| Field | Type | Description |
|---|---|---|
| `credential_type` | string |  |

#### `admin/providers/delete_custom`

Removes a custom provider declaration from settings.

**Params (`AdminProviderNameParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |

**Result:** none.

#### `admin/providers/disconnect`

Clears a provider's credentials.

**Params (`AdminProviderNameParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |

**Result:** none.

#### `admin/providers/list`

Lists every registered provider (no credentials).

**Params:** none.

**Result (`AdminProvidersListResult`):**

| Field | Type | Description |
|---|---|---|
| `providers` | array of `ProviderInfo` |  |

#### `admin/providers/list_custom`

Lists the custom providers declared in settings.

**Params:** none.

**Result (`AdminProvidersListCustomResult`):**

| Field | Type | Description |
|---|---|---|
| `providers` | map of `CustomProvider` |  |

#### `admin/providers/model`

Returns one model of a provider by its provider-local id.

**Params (`AdminProvidersModelParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |
| `provider` | string |  |

**Result (`ModelListing`):**

| Field | Type | Description |
|---|---|---|
| `cache_read` | number |  |
| `cache_write` | number |  |
| `context_window` | integer |  |
| `dialect` | string (optional) |  |
| `display_name` | string |  |
| `effort_levels` | map of boolean (optional) |  |
| `id` | string |  |
| `input_price` | number |  |
| `is_subscription` | boolean |  |
| `max_tokens` | integer |  |
| `model` | string |  |
| `output_price` | number |  |
| `provider` | string |  |
| `sources` | map of string (optional) |  |
| `thinking` | boolean |  |
| `thinking_adaptive` | boolean (optional) |  |
| `thinking_legacy` | boolean (optional) |  |
| `vision` | boolean |  |

#### `admin/providers/models`

Lists the models of every active provider, or of one (with the errors the unfiltered call hides).

**Params (`AdminProvidersModelsParams`):**

| Field | Type | Description |
|---|---|---|
| `provider` | string (optional) |  |

**Result (`AdminProvidersModelsResult`):**

| Field | Type | Description |
|---|---|---|
| `models` | array of `ModelListing` |  |

#### `admin/providers/oauth_exchange`

Swaps the code the user pasted for credentials (does not connect: pass them to connect).

**Params (`AdminProvidersOAuthExchangeParams`):**

| Field | Type | Description |
|---|---|---|
| `exchange_code` | string |  |
| `provider` | string |  |
| `verifier_code` | string |  |

**Result (`Credentials`):**

| Field | Type | Description |
|---|---|---|
| `access_token` | string (optional) |  |
| `account_id` | string (optional) |  |
| `api_key` | string (optional) |  |
| `expires_at` | integer (optional) |  |
| `refresh_token` | string (optional) |  |
| `subscription_type` | string (optional) |  |
| `type` | string |  |

#### `admin/providers/oauth_start`

Begins a provider's OAuth login: answers the URL to open and the verifier to hand back.

**Params (`AdminProvidersOAuthStartParams`):**

| Field | Type | Description |
|---|---|---|
| `provider` | string |  |

**Result (`AdminProvidersOAuthStartResult`):**

| Field | Type | Description |
|---|---|---|
| `auth_url` | string |  |
| `verifier_code` | string |  |

#### `admin/providers/put_custom`

Declares (or replaces) a custom provider in settings; it takes effect on reload_custom.

**Params (`AdminProvidersPutCustomParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |
| `provider` | `CustomProvider` |  |

**Result:** none.

#### `admin/providers/quota`

Returns the provider's subscription usage (cached 60 s; refresh asks again, at most once per 5 s).

**Params (`AdminProvidersQuotaParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |
| `refresh` | boolean (optional) |  |

**Result (`AdminProvidersQuotaResult`):**

| Field | Type | Description |
|---|---|---|
| `quota` | `Quota` |  |
| `retry_after_seconds` | integer |  |

#### `admin/providers/register_anthropic`

Registers an Anthropic Messages-compatible provider for this process (url without /v1).

**Params (`AdminProvidersRegisterAnthropicParams`):**

| Field | Type | Description |
|---|---|---|
| `display` | string (optional) |  |
| `headers` | map of string (optional) |  |
| `name` | string |  |
| `url` | string |  |

**Result:** none.

#### `admin/providers/register_openai`

Registers an OpenAI Chat Completions-compatible provider for this process (authentication is headers only).

**Params (`AdminProvidersRegisterOpenAIParams`):**

| Field | Type | Description |
|---|---|---|
| `display` | string (optional) |  |
| `headers` | map of string (optional) |  |
| `name` | string |  |
| `reasoning_split` | boolean (optional) |  |
| `url` | string |  |

**Result:** none.

#### `admin/providers/reload_custom`

Applies the settings' custom providers live. Names clashing with programmatic providers are skipped and reported, not an error.

**Params:** none.

**Result (`AdminProvidersReloadCustomResult`):**

| Field | Type | Description |
|---|---|---|
| `message` | string (optional) |  |
| `status` | string |  |

#### `admin/providers/unregister`

Removes a provider registered with register_openai or register_anthropic.

**Params (`AdminProviderNameParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |

**Result:** none.

### admin / schedules

#### `admin/schedules/list`

Lists the cron-scheduled prompts, sorted by slug (read-only).

**Params (`AdminSchedulesListParams`):**

| Field | Type | Description |
|---|---|---|
| `owner` | string (optional) |  |

**Result (`AdminSchedulesListResult`):**

| Field | Type | Description |
|---|---|---|
| `schedules` | array of `AdminSchedule` |  |

### admin / sessions

#### `admin/sessions/delete`

Permanently deletes a stored session. Refused with session_active while it is open in this process.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result:** none.

#### `admin/sessions/export`

Exports a session as JSONL: written to path (a *.export.jsonl file, mode 0600) or, with no path, returned as a string.

**Params (`AdminSessionsExportParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |
| `path` | string (optional) |  |

**Result (`AdminSessionsExportResult`):**

| Field | Type | Description |
|---|---|---|
| `content` | string (optional) |  |
| `messages` | integer |  |
| `path` | string (optional) |  |

#### `admin/sessions/export_file_name`

Returns the file name an export of the session is saved under by convention.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result (`AdminSessionsExportFileNameResult`):**

| Field | Type | Description |
|---|---|---|
| `file_name` | string |  |

#### `admin/sessions/get`

Returns a stored session's meta.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result (`SessionMeta`):**

| Field | Type | Description |
|---|---|---|
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer (optional) |  |
| `model` | string |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `admin/sessions/list`

Lists the stored sessions of a directory (every directory when cwd is empty).

**Params (`AdminSessionsListParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string (optional) |  |

**Result (`AdminSessionsListResult`):**

| Field | Type | Description |
|---|---|---|
| `sessions` | array of `SessionMeta` |  |

#### `admin/sessions/messages`

Reads a window of a stored session's log: from an absolute index, or after the k-th-to-last compaction.

**Params (`AdminSessionsMessagesParams`):**

| Field | Type | Description |
|---|---|---|
| `compactions` | integer (optional) |  |
| `from` | integer (optional) |  |
| `id` | string |  |

**Result (`AdminSessionsMessagesResult`):**

| Field | Type | Description |
|---|---|---|
| `from` | integer |  |
| `messages` | array of `Message` |  |
| `total` | integer |  |

#### `admin/sessions/rename`

Renames a stored session. Refused with session_active while it is open in this process.

**Params (`AdminSessionsRenameParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |
| `name` | string |  |

**Result:** none.

### admin / settings

#### `admin/settings/get`

Returns the global defaults (active model and thinking level).

**Params:** none.

**Result (`SettingsValues`):**

| Field | Type | Description |
|---|---|---|
| `active_model` | string |  |
| `thinking_level` | string |  |

#### `admin/settings/set_active_model`

Stores the default "provider/model" and returns the settings. Does not touch open sessions.

**Params (`AdminSettingsSetActiveModelParams`):**

| Field | Type | Description |
|---|---|---|
| `model` | string |  |

**Result (`SettingsValues`):**

| Field | Type | Description |
|---|---|---|
| `active_model` | string |  |
| `thinking_level` | string |  |

#### `admin/settings/set_thinking_level`

Stores the default thinking level (off|low|medium|high|xhigh|max) and returns the settings. Does not touch open sessions.

**Params (`AdminSettingsSetThinkingLevelParams`):**

| Field | Type | Description |
|---|---|---|
| `level` | string |  |

**Result (`SettingsValues`):**

| Field | Type | Description |
|---|---|---|
| `active_model` | string |  |
| `thinking_level` | string |  |

### Agent

#### `agent/active_sessions`

Lists the ids of the sessions open in this process.

**Params:** none.

**Result (`IDsResult`):**

| Field | Type | Description |
|---|---|---|
| `ids` | array of string |  |

#### `agent/mcp_status`

Reports the connection state of each configured MCP server.

**Params:** none.

**Result (`MCPStatusResult`):**

| Field | Type | Description |
|---|---|---|
| `statuses` | array of `Status` |  |

#### `agent/profiles`

Lists the registered profile names.

**Params:** none.

**Result (`NamesResult`):**

| Field | Type | Description |
|---|---|---|
| `names` | array of string |  |

#### `agent/register_profile`

Registers (or replaces) a profile; sessions bound to it pick it up at their next turn.

**Params (`RegisterProfileParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |
| `profile` | `ProfileConfig` |  |

**Result:** none.

#### `agent/register_tool`

Registers a tool implemented by the client, for every session opened from now on.

**Params (`RegisterToolParams`):**

| Field | Type | Description |
|---|---|---|
| `tool` | `ToolDecl` |  |

**Result:** none.

#### `agent/shutdown`

Replies, then closes every session and the agent and exits.

**Params:** none.

**Result:** none.

#### `agent/unregister_profile`

Removes a profile; bound sessions keep the name and lose its directives and tools.

**Params (`ProfileNameParams`):**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |

**Result:** none.

### credentials

#### `credentials/get`

Sent BY THE BINARY when config.credentials has kind "client": reads one entry of the credentials (secrets: API keys, OAuth tokens). Answer found:false when it does not exist.

**Params (`StoreKeyParams`):**

| Field | Type | Description |
|---|---|---|
| `key` | string |  |
| `namespace` | string |  |

**Result (`StoreGetResult`):**

| Field | Type | Description |
|---|---|---|
| `found` | boolean |  |
| `value` | any (optional) |  |
| `version` | string (optional) |  |

#### `credentials/set`

Sent BY THE BINARY: upserts one entry of the credentials (secrets: API keys, OAuth tokens).

**Params (`StoreSetParams`):**

| Field | Type | Description |
|---|---|---|
| `key` | string |  |
| `namespace` | string |  |
| `value` | any |  |

**Result:** none.

#### `credentials/delete`

Sent BY THE BINARY: deletes one entry; a missing entry is not an error.

**Params (`StoreKeyParams`):**

| Field | Type | Description |
|---|---|---|
| `key` | string |  |
| `namespace` | string |  |

**Result:** none.

#### `credentials/list`

Sent BY THE BINARY: every entry of a namespace with its version (an empty object when none).

**Params (`StoreListParams`):**

| Field | Type | Description |
|---|---|---|
| `namespace` | string |  |

**Result (`StoreListResult`):**

| Field | Type | Description |
|---|---|---|
| `entries` | map of `StoreEntry` |  |

#### `credentials/swap`

Sent BY THE BINARY: a conditional write. Write value (null deletes) only if the entry's version equals expected_version (null: the entry must not exist), atomically; otherwise answer swapped:false and change nothing.

**Params (`StoreSwapParams`):**

| Field | Type | Description |
|---|---|---|
| `expected_version` | string |  |
| `key` | string |  |
| `namespace` | string |  |
| `value` | any |  |

**Result (`StoreSwapResult`):**

| Field | Type | Description |
|---|---|---|
| `swapped` | boolean |  |

### Initialize

#### `initialize`

Starts the connection: checks the protocol version and builds the agent from the configuration. Must be the first request, once.

**Params (`InitializeParams`):**

| Field | Type | Description |
|---|---|---|
| `client_info` | `ClientInfo` |  |
| `config` | `Config` |  |
| `protocol_version` | integer |  |

**Result (`InitializeResult`):**

| Field | Type | Description |
|---|---|---|
| `capabilities` | `Capabilities` |  |
| `harness_version` | string |  |
| `protocol_version` | integer |  |

### memory

#### `memory/write`

Sent BY THE BINARY when config.memory has kind "client": upserts one memory by cwd + slug (global:true stores it under the "<global>" cwd). Answer created:true the first time, false on a rewrite.

**Params (`MemoryWriteParams`):**

| Field | Type | Description |
|---|---|---|
| `content` | string |  |
| `cwd` | string |  |
| `global` | boolean |  |
| `slug` | string |  |

**Result (`MemoryWriteResult`):**

| Field | Type | Description |
|---|---|---|
| `created` | boolean |  |

#### `memory/get`

Sent BY THE BINARY: reads one memory by cwd + slug (global:true = the "<global>" scope). Answer found:false when it does not exist.

**Params (`MemoryKeyParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string |  |
| `global` | boolean |  |
| `slug` | string |  |

**Result (`MemoryGetResult`):**

| Field | Type | Description |
|---|---|---|
| `found` | boolean |  |
| `memory` | `Memory` |  |

#### `memory/search`

Sent BY THE BINARY: lists (empty query: most recently updated first) or full-text searches memories, paginated. cwd "" = all projects, "<global>" = only global ones, else that project's plus the global ones. limit <= 0 means 10, skip < 0 means 0; include_content:false omits content; total counts every match.

**Params (`MemorySearchParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string |  |
| `include_content` | boolean |  |
| `limit` | integer |  |
| `query` | string |  |
| `skip` | integer |  |

**Result (`SearchResult`):**

| Field | Type | Description |
|---|---|---|
| `limit` | integer |  |
| `results` | array of `Memory` |  |
| `returned` | integer |  |
| `skip` | integer |  |
| `total` | integer |  |

#### `memory/delete`

Sent BY THE BINARY: removes one memory by cwd + slug (global:true = the "<global>" scope). Answer deleted:true only when one existed.

**Params (`MemoryKeyParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string |  |
| `global` | boolean |  |
| `slug` | string |  |

**Result (`MemoryDeleteResult`):**

| Field | Type | Description |
|---|---|---|
| `deleted` | boolean |  |

### resources

#### `resources/load`

Sent BY THE BINARY when config.resources has kind "client": the context of a session's cwd (SYSTEM.md, AGENTS.md and every skill with its content). location is optional for a skill.

**Params (`ResourcesLoadParams`):**

| Field | Type | Description |
|---|---|---|
| `cwd` | string |  |

**Result (`Resources`):**

| Field | Type | Description |
|---|---|---|
| `agents_md` | string (optional) |  |
| `skills` | array of `Skill` |  |
| `system_md` | string (optional) |  |

### Session

#### `session/ask`

Sends a message and waits for the turn's final text. Returns early (empty text) when the turn is stopped.

**Params (`SessionPromptParams`):**

| Field | Type | Description |
|---|---|---|
| `display_text` | string (optional) |  |
| `id` | string |  |
| `images` | array of `ImageData` (optional) |  |
| `origin` | string (optional) |  |
| `text` | string |  |

**Result (`SessionAskResult`):**

| Field | Type | Description |
|---|---|---|
| `text` | string |  |

#### `session/close`

Closes an open session (its events stop).

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result:** none.

#### `session/compact`

Summarizes the conversation into a checkpoint; replies when done. Conflict while a turn runs.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result:** none.

#### `session/context`

Estimated token usage of the session's context window, per component.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result (`ContextBreakdown`):**

| Field | Type | Description |
|---|---|---|
| `context_window` | integer |  |
| `conversation` | integer |  |
| `estimated_total` | integer |  |
| `free_space` | integer |  |
| `last_real_total` | integer |  |
| `system` | integer |  |
| `tools` | integer |  |

#### `session/exec_command`

Runs a command that has no method of its own: skill:<name>.

**Params (`SessionExecCommandParams`):**

| Field | Type | Description |
|---|---|---|
| `command` | string |  |
| `id` | string |  |
| `params` | map of any (optional) |  |

**Result (`SessionExecCommandResult`):**

| Field | Type | Description |
|---|---|---|
| `message` | string (optional) |  |
| `status` | string |  |

#### `session/fork`

Copies a session at this instant into a new one and starts sending its events.

**Params (`SessionForkParams`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `InputChannel` (optional) |  |
| `directives` | array of string (optional) |  |
| `enabled_profiles` | array of string (optional) |  |
| `id` | string |  |

**Result (`SessionDetail`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `ChannelInfo` (optional) |  |
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer |  |
| `missing_profiles` | array of string (optional) |  |
| `model` | string |  |
| `model_fallback` | `ModelFallback` (optional) |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `session/import`

Imports a session export (a file path or the JSONL content) as a NEW session.

**Params (`SessionImportParams`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `InputChannel` (optional) |  |
| `content` | string (optional) |  |
| `cwd` | string (optional) |  |
| `path` | string (optional) |  |

**Result (`SessionDetail`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `ChannelInfo` (optional) |  |
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer |  |
| `missing_profiles` | array of string (optional) |  |
| `model` | string |  |
| `model_fallback` | `ModelFallback` (optional) |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `session/info`

The session's live state: detail, busy, queue depth, MCP and schedule counts, system.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result (`SessionInfoResult`):**

| Field | Type | Description |
|---|---|---|
| `arch` | string |  |
| `busy` | boolean |  |
| `mcp_connected` | integer |  |
| `os` | string |  |
| `queue_depth` | integer |  |
| `schedule_count` | integer |  |
| `session` | `SessionDetail` |  |
| `terminal` | string (optional) |  |
| `version` | string |  |

#### `session/messages`

A window of the session's complete history (from an index, or after the k-th-to-last compaction).

**Params (`SessionMessagesParams`):**

| Field | Type | Description |
|---|---|---|
| `compactions` | integer (optional) |  |
| `from` | integer (optional) |  |
| `id` | string |  |

**Result (`SessionMessagesResult`):**

| Field | Type | Description |
|---|---|---|
| `from` | integer |  |
| `messages` | array of `Message` |  |
| `total` | integer |  |

#### `session/new`

Creates a session and starts sending its events.

**Params (`SessionNewParams`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `InputChannel` (optional) |  |
| `cwd` | string |  |
| `directives` | array of string (optional) |  |
| `enabled_profiles` | array of string (optional) |  |
| `model` | string |  |
| `name` | string (optional) |  |

**Result (`SessionDetail`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `ChannelInfo` (optional) |  |
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer |  |
| `missing_profiles` | array of string (optional) |  |
| `model` | string |  |
| `model_fallback` | `ModelFallback` (optional) |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `session/prompt`

Sends a message to an open session; answers at once ('started', or 'queued' behind a running turn). The turn's events arrive as `event` notifications.

**Params (`SessionPromptParams`):**

| Field | Type | Description |
|---|---|---|
| `display_text` | string (optional) |  |
| `id` | string |  |
| `images` | array of `ImageData` (optional) |  |
| `origin` | string (optional) |  |
| `text` | string |  |

**Result (`SessionPromptResult`):**

| Field | Type | Description |
|---|---|---|
| `status` | string |  |

#### `session/rename`

Renames the session and answers its detail.

**Params (`SessionRenameParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |
| `name` | string |  |

**Result (`SessionDetail`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `ChannelInfo` (optional) |  |
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer |  |
| `missing_profiles` | array of string (optional) |  |
| `model` | string |  |
| `model_fallback` | `ModelFallback` (optional) |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `session/reset`

Wipes the session's history and stats. Conflict while a turn runs.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result:** none.

#### `session/resume`

Reopens a stored session (or returns the open one) and starts sending its events.

**Params (`SessionResumeParams`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `InputChannel` (optional) |  |
| `directives` | array of string (optional) |  |
| `enabled_profiles` | array of string (optional) |  |
| `id` | string |  |

**Result (`SessionDetail`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `ChannelInfo` (optional) |  |
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer |  |
| `missing_profiles` | array of string (optional) |  |
| `model` | string |  |
| `model_fallback` | `ModelFallback` (optional) |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `session/set_max_iterations`

Sets the session's per-turn iteration budget (1-1000) and answers its detail.

**Params (`SessionSetMaxIterationsParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |
| `n` | integer |  |

**Result (`SessionDetail`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `ChannelInfo` (optional) |  |
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer |  |
| `missing_profiles` | array of string (optional) |  |
| `model` | string |  |
| `model_fallback` | `ModelFallback` (optional) |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `session/stop`

Cancels the turn in progress; queued prompts continue.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result:** none.

#### `session/switch_model`

Switches the session's model (provider/model) and answers its detail.

**Params (`SessionSwitchModelParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |
| `model` | string |  |

**Result (`SessionDetail`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `ChannelInfo` (optional) |  |
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer |  |
| `missing_profiles` | array of string (optional) |  |
| `model` | string |  |
| `model_fallback` | `ModelFallback` (optional) |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `session/switch_thinking`

Switches the session's thinking level and answers its detail.

**Params (`SessionSwitchThinkingParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |
| `level` | string |  |

**Result (`SessionDetail`):**

| Field | Type | Description |
|---|---|---|
| `channels` | array of `ChannelInfo` (optional) |  |
| `compact_count` | integer (optional) |  |
| `compact_offset` | integer (optional) |  |
| `created_at` | string (date-time) |  |
| `cwd` | string |  |
| `id` | string |  |
| `last_active_at` | string (date-time) |  |
| `max_iterations` | integer |  |
| `missing_profiles` | array of string (optional) |  |
| `model` | string |  |
| `model_fallback` | `ModelFallback` (optional) |  |
| `name` | string (optional) |  |
| `profiles` | array of string (optional) |  |
| `reset_count` | integer (optional) |  |
| `stats` | `SessionStats` |  |
| `thinking` | string |  |

#### `session/sysprompt`

The full system prompt the session sends to the provider.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result (`SessionSyspromptResult`):**

| Field | Type | Description |
|---|---|---|
| `text` | string |  |

#### `session/tools`

The tool definitions the session sends to the provider.

**Params (`SessionIDParams`):**

| Field | Type | Description |
|---|---|---|
| `id` | string |  |

**Result (`SessionToolsResult`):**

| Field | Type | Description |
|---|---|---|
| `tools` | array of `ToolDef` |  |

### settings

#### `settings/get`

Sent BY THE BINARY when config.settings has kind "client": reads one entry of the settings (non-sensitive configuration). Answer found:false when it does not exist.

**Params (`StoreKeyParams`):**

| Field | Type | Description |
|---|---|---|
| `key` | string |  |
| `namespace` | string |  |

**Result (`StoreGetResult`):**

| Field | Type | Description |
|---|---|---|
| `found` | boolean |  |
| `value` | any (optional) |  |
| `version` | string (optional) |  |

#### `settings/set`

Sent BY THE BINARY: upserts one entry of the settings (non-sensitive configuration).

**Params (`StoreSetParams`):**

| Field | Type | Description |
|---|---|---|
| `key` | string |  |
| `namespace` | string |  |
| `value` | any |  |

**Result:** none.

#### `settings/delete`

Sent BY THE BINARY: deletes one entry; a missing entry is not an error.

**Params (`StoreKeyParams`):**

| Field | Type | Description |
|---|---|---|
| `key` | string |  |
| `namespace` | string |  |

**Result:** none.

#### `settings/list`

Sent BY THE BINARY: every entry of a namespace with its version (an empty object when none).

**Params (`StoreListParams`):**

| Field | Type | Description |
|---|---|---|
| `namespace` | string |  |

**Result (`StoreListResult`):**

| Field | Type | Description |
|---|---|---|
| `entries` | map of `StoreEntry` |  |

#### `settings/swap`

Sent BY THE BINARY: a conditional write. Write value (null deletes) only if the entry's version equals expected_version (null: the entry must not exist), atomically; otherwise answer swapped:false and change nothing.

**Params (`StoreSwapParams`):**

| Field | Type | Description |
|---|---|---|
| `expected_version` | string |  |
| `key` | string |  |
| `namespace` | string |  |
| `value` | any |  |

**Result (`StoreSwapResult`):**

| Field | Type | Description |
|---|---|---|
| `swapped` | boolean |  |

### Tools (calls to the client)

#### `tool/call`

Sent BY THE BINARY to the client when the model calls a declared tool. Answer with the tool's result.

**Params (`ToolCallParams`):**

| Field | Type | Description |
|---|---|---|
| `input` | any |  |
| `name` | string |  |
| `session_id` | string |  |
| `tool_call_id` | string |  |

**Result (`ToolCallResult`):**

| Field | Type | Description |
|---|---|---|
| `content` | string |  |
| `images` | array of `ImageData` (optional) |  |
| `is_error` | boolean (optional) |  |

## Notifications and reverse calls

### `event`

A session event: {session_id, type, …} (see events). (binary to client)

**Params:** none.

### `tool/cancel`

A pending tool/call was abandoned (session stop, shutdown); a late answer is ignored. (binary to client)

**Params (`ToolCancelParams`):**

| Field | Type | Description |
|---|---|---|
| `tool_call_id` | string |  |

## Events

Each event arrives as an `event` notification: `session_id` plus the fields below.

### `turn_start`

A turn started.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `type` | `turn_start` |  |

### `turn_end`

The turn finished; the session is ready for the next prompt.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `type` | `turn_end` |  |

### `loop_start`

One ReAct iteration started.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `loop` | integer |  |
| `session_id` | string |  |
| `type` | `loop_start` |  |

### `loop_end`

One ReAct iteration finished.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `loop` | integer |  |
| `session_id` | string |  |
| `type` | `loop_end` |  |

### `thinking`

A fragment of the model's reasoning.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `delta` | string |  |
| `session_id` | string |  |
| `type` | `thinking` |  |

### `thinking_end`

The reasoning block finished.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `type` | `thinking_end` |  |

### `text`

A fragment of the response text.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `delta` | string |  |
| `session_id` | string |  |
| `type` | `text` |  |

### `text_end`

The response text finished.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `type` | `text_end` |  |

### `tool_start`

A tool call was announced (name and id known, arguments not yet).

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `tool_id` | string |  |
| `tool_name` | string |  |
| `type` | `tool_start` |  |

### `tool_args`

A fragment of a tool call's arguments.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `delta` | string |  |
| `session_id` | string |  |
| `tool_id` | string |  |
| `tool_name` | string |  |
| `type` | `tool_args` |  |

### `tool_call`

A tool call's arguments are complete; it is about to run.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `tool_args` | string |  |
| `tool_id` | string |  |
| `tool_name` | string |  |
| `type` | `tool_call` |  |

### `tool_result`

A tool finished. duration is in milliseconds.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `duration` | number |  |
| `is_error` | boolean |  |
| `output` | string |  |
| `session_id` | string |  |
| `tool_id` | string |  |
| `tool_name` | string |  |
| `type` | `tool_result` |  |

### `received_prompt`

An immediate prompt was received (also the echo of prompts from other origins).

**Fields:**

| Field | Type | Description |
|---|---|---|
| `images` | array of any (optional) |  |
| `message_index` | integer |  |
| `origin` | string |  |
| `session_id` | string |  |
| `text` | string |  |
| `type` | `received_prompt` |  |

### `follow_up_start`

A queued prompt is about to be processed.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `images` | array of any (optional) |  |
| `message_index` | integer |  |
| `origin` | string |  |
| `session_id` | string |  |
| `text` | string |  |
| `type` | `follow_up_start` |  |

### `subagent_start`

A sub-agent was launched (its name is generated by the Subagent tool).

**Fields:**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |
| `session_id` | string |  |
| `type` | `subagent_start` |  |

### `subagent_stop`

A sub-agent ended. Always emitted, before its answer reaches the parent; result is ok, error or cancelled.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `name` | string |  |
| `result` | string |  |
| `session_id` | string |  |
| `type` | `subagent_stop` |  |

### `retry`

A transient network failure is being retried.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `attempt` | integer |  |
| `error` | string |  |
| `max` | integer |  |
| `session_id` | string |  |
| `type` | `retry` |  |

### `tokens`

Token usage: live context (input, context_usage, context_window) and session totals.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `cache_read` | integer |  |
| `cache_write` | integer |  |
| `context_usage` | number |  |
| `context_window` | integer |  |
| `cost_usd` | number |  |
| `input` | integer |  |
| `session_id` | string |  |
| `total_input` | integer |  |
| `total_output` | integer |  |
| `type` | `tokens` |  |

### `error`

Something broke in the turn.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `details` | object (optional) |  |
| `message` | string |  |
| `session_id` | string |  |
| `type` | `error` |  |

### `max_iterations_reached`

The iteration budget ran out (the model summarized its progress).

**Fields:**

| Field | Type | Description |
|---|---|---|
| `max_iterations` | integer |  |
| `session_id` | string |  |
| `type` | `max_iterations_reached` |  |

### `compact_start`

Compaction started.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `type` | `compact_start` |  |

### `compact_end`

Compaction finished.

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `summary` | string |  |
| `type` | `compact_end` |  |

### `stop`

The turn was stopped by the user (not an error).

**Fields:**

| Field | Type | Description |
|---|---|---|
| `session_id` | string |  |
| `type` | `stop` |  |

## Errors

| Code | Kind | Meaning |
|---|---|---|
| `-32000` | `not_found` | The thing named does not exist (a session, a memory, a file). |
| `-32000` | `provider_not_found` | No provider with that name. |
| `-32001` | `invalid` | Well-formed input the operation cannot accept. |
| `-32001` | `unprocessable` | Well-formed input whose content is invalid (a bad thinking level, an invalid MCP server). |
| `-32001` | `invalid_export` | The session export is not valid; the message names the line. |
| `-32002` | `session_active` | The session is open in this process; close it first. |
| `-32002` | `conflict` | The request clashes with the current state (a channel already taken, a busy session). |
| `-32002` | `provider_not_removable` | The provider is built in or declared in settings and cannot be unregistered. |
| `-32002` | `provider_name_conflict` | A custom provider name is already used by a provider registered from code. |
| `-32003` | `not_connected` | The provider has no credentials, or no model is available to run on. |
| `-32004` | `upstream` | A provider or its OAuth endpoint failed. |
| `-32005` | `not_configured` | The feature has no backing store here (e.g. no memory). |
| `-32006` | `protocol_version` | initialize carried an unsupported protocol_version; data.supported lists the ones accepted. The process exits. |
| `-32603` | `internal` | An unexpected failure. |

