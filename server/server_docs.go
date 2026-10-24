package server

import (
	"strings"

	"github.com/gurcuff91/harness/internal/version"
)

// docsHTML is the Scalar API reference UI — a single self-contained HTML page
// that loads the OpenAPI spec from /api/docs/openapi.json (same origin, no CORS)
// and renders it using the Scalar CDN bundle. No build step, no extra assets.
const docsHTML = `<!doctype html>
<html lang="en">
  <head>
    <title>Harness API Reference</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="/api/docs/openapi.json"></script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`

// openAPISpecJSON returns the OpenAPI 3.0 specification for the Harness HTTP API,
// with version and server address injected at runtime.
func openAPISpecJSON(addr string) string {
	s := strings.ReplaceAll(openAPISpecTemplate, "{{VERSION}}", version.Version)
	s = strings.ReplaceAll(s, "{{ADDR}}", addr)
	return s
}

// openAPISpecTemplate is the hand-written OpenAPI 3.0 spec with {{VERSION}} as
// a placeholder replaced at runtime by openAPISpecJSON().
const openAPISpecTemplate = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Harness API",
    "description": "HTTP/SSE API for the Harness AI agent — sessions, providers, models, schedules, memory, and MCP.",
    "version": "{{VERSION}}"
  },
  "servers": [
    { "url": "http://{{ADDR}}", "description": "Local in-process server" }
  ],
  "tags": [
    { "name": "server",    "description": "Server metadata" },
    { "name": "settings",  "description": "Configuration management" },
    { "name": "providers", "description": "LLM provider connect/disconnect" },
    { "name": "models",    "description": "Available models" },
    { "name": "mcp",       "description": "MCP server status" },
    { "name": "memory",    "description": "Persistent memory: list, search, read, create/update and delete (project-scoped or global)" },
    { "name": "schedules", "description": "Cron-scheduled prompts" },
    { "name": "sessions",  "description": "Agent session lifecycle" }
  ],
  "paths": {
    "/api/server": {
      "get": {
        "tags": ["server"],
        "summary": "Server info",
        "description": "Returns harness version, instance name, transport, URL, and process metadata.",
        "operationId": "getServerInfo",
        "responses": {
          "200": { "description": "Server info", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ServerInfo" } } } }
        }
      }
    },
    "/api/instances": {
      "get": {
        "tags": ["server"],
        "summary": "List running instances",
        "description": "Returns all registered server instances (the colleague registry, kept in the settings store). Dead instances are purged whenever an instance registers.",
        "operationId": "listInstances",
        "responses": {
          "200": { "description": "Instance registry", "content": { "application/json": { "schema": { "type": "object", "additionalProperties": { "$ref": "#/components/schemas/InstanceInfo" } } } } }
        }
      }
    },
    "/api/settings": {
      "get": {
        "tags": ["settings"],
        "summary": "Get settings",
        "operationId": "getSettings",
        "responses": {
          "200": { "description": "Current settings", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Settings" } } } }
        }
      },
      "patch": {
        "tags": ["settings"],
        "summary": "Update settings",
        "operationId": "patchSettings",
        "requestBody": { "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Settings" } } } },
        "responses": {
          "200": { "description": "Updated settings", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Settings" } } } }
        }
      }
    },
    "/api/settings/mcp": {
      "get": {
        "tags": ["settings"],
        "summary": "List MCP server configs",
        "operationId": "listMCPServers",
        "responses": {
          "200": { "description": "MCP server config map", "content": { "application/json": { "schema": { "type": "object" } } } }
        }
      }
    },
    "/api/settings/mcp/{name}": {
      "put": {
        "tags": ["settings"],
        "summary": "Upsert MCP server config",
        "operationId": "putMCPServer",
        "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
        "requestBody": { "content": { "application/json": { "schema": { "type": "object" } } } },
        "responses": { "200": { "description": "Saved config" } }
      },
      "delete": {
        "tags": ["settings"],
        "summary": "Delete MCP server config",
        "operationId": "deleteMCPServer",
        "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
        "responses": { "200": { "description": "Deleted", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } } }
      }
    },
    "/api/mcp/status": {
      "get": {
        "tags": ["mcp"],
        "summary": "MCP connection status",
        "description": "Returns live connection status and tool count for each configured MCP server.",
        "operationId": "getMCPStatus",
        "responses": {
          "200": { "description": "MCP status list", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/MCPStatus" } } } } }
        }
      }
    },
    "/api/settings/provider": {
      "get": {
        "tags": ["settings"],
        "summary": "List custom provider configs",
        "description": "Custom OpenAI-compatible providers (proxies, gateways, self-hosted endpoints) configured under settings.json's \"provider\" collection. Changes take effect after POST /api/settings/provider/reload (or on the next process start).",
        "operationId": "listCustomProviders",
        "responses": {
          "200": { "description": "Custom provider config map", "content": { "application/json": { "schema": { "type": "object" } } } }
        }
      }
    },
    "/api/settings/provider/reload": {
      "post": {
        "tags": ["settings"],
        "summary": "Apply custom providers live",
        "description": "Re-reads settings.json's \"provider\" collection and applies it to the running process: added/edited/disabled/deleted entries take effect without a restart (sessions pick them up at their next turn). Built-in and programmatic providers are untouched; an entry whose name is used by a programmatic provider is skipped (code \"reloaded_with_conflicts\", message lists them).",
        "operationId": "reloadCustomProviders",
        "responses": { "200": { "description": "Applied", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } } }
      }
    },
    "/api/settings/provider/{name}": {
      "put": {
        "tags": ["settings"],
        "summary": "Upsert custom provider config",
        "operationId": "putCustomProvider",
        "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" }, "description": "Provider name — must not collide with a built-in provider" }],
        "requestBody": { "content": { "application/json": { "schema": { "$ref": "#/components/schemas/CustomProvider" } } } },
        "responses": {
          "200": { "description": "Saved config", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/CustomProvider" } } } },
          "422": { "description": "Invalid config (unsupported type, missing url, or reserved name)", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } }
        }
      },
      "delete": {
        "tags": ["settings"],
        "summary": "Delete custom provider config",
        "operationId": "deleteCustomProvider",
        "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
        "responses": { "200": { "description": "Deleted", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } } }
      }
    },
    "/api/memories": {
      "get": {
        "tags": ["memory"],
        "summary": "List or search memories",
        "description": "Paginated listing (most recently updated first) or full-text search (ranked by relevance). Returns an empty result when the agent has no memory.",
        "operationId": "listMemories",
        "parameters": [
          { "name": "cwd", "in": "query", "schema": { "type": "string" }, "description": "Scope filter: omit for all projects; \"<global>\" for global memories only; a project path for that project's memories plus the global ones" },
          { "name": "query", "in": "query", "schema": { "type": "string" }, "description": "Full-text search (prefix-matched terms, ANDed); omit to list" },
          { "name": "include_content", "in": "query", "schema": { "type": "boolean", "default": true }, "description": "false = lightweight listing without content" },
          { "name": "skip", "in": "query", "schema": { "type": "integer", "default": 0 } },
          { "name": "limit", "in": "query", "schema": { "type": "integer", "default": 10 } }
        ],
        "responses": {
          "200": { "description": "Memory search result", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/MemorySearchResult" } } } }
        }
      }
    },
    "/api/memories/{slug}": {
      "get": {
        "tags": ["memory"],
        "summary": "Get one memory",
        "operationId": "getMemory",
        "parameters": [
          { "name": "slug", "in": "path", "required": true, "schema": { "type": "string" }, "description": "Memory id within its scope (kebab-case by convention; URL-escape anything else)" },
          { "name": "cwd", "in": "query", "required": true, "schema": { "type": "string" }, "description": "Scope: the project path, or \"<global>\" for a global (cross-project) memory" }
        ],
        "responses": {
          "200": { "description": "The memory", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/MemoryEntry" } } } },
          "400": { "description": "Missing cwd or slug", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "404": { "description": "Memory not found", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "503": { "description": "Memory is not enabled on this agent", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } }
        }
      },
      "put": {
        "tags": ["memory"],
        "summary": "Create or update a memory",
        "description": "Upsert by scope + slug (same semantics as the agent's MemoWrite tool). Returns the stored memory.",
        "operationId": "putMemory",
        "parameters": [
          { "name": "slug", "in": "path", "required": true, "schema": { "type": "string" }, "description": "Memory id within its scope (kebab-case by convention; URL-escape anything else)" },
          { "name": "cwd", "in": "query", "required": true, "schema": { "type": "string" }, "description": "Scope: the project path, or \"<global>\" for a global (cross-project) memory" }
        ],
        "requestBody": {
          "required": true,
          "content": { "application/json": { "schema": { "type": "object", "required": ["content"], "properties": { "content": { "type": "string", "description": "The memory text (must not be blank)" } } } } }
        },
        "responses": {
          "200": { "description": "Updated", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/MemoryEntry" } } } },
          "201": { "description": "Created", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/MemoryEntry" } } } },
          "400": { "description": "Missing cwd/slug or malformed JSON", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "422": { "description": "Blank content", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "503": { "description": "Memory is not enabled on this agent", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } }
        }
      },
      "delete": {
        "tags": ["memory"],
        "summary": "Delete a memory",
        "operationId": "deleteMemory",
        "parameters": [
          { "name": "slug", "in": "path", "required": true, "schema": { "type": "string" }, "description": "Memory id within its scope (kebab-case by convention; URL-escape anything else)" },
          { "name": "cwd", "in": "query", "required": true, "schema": { "type": "string" }, "description": "Scope: the project path, or \"<global>\" for a global (cross-project) memory" }
        ],
        "responses": {
          "200": { "description": "Deleted", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } },
          "400": { "description": "Missing cwd or slug", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "404": { "description": "Memory not found", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "503": { "description": "Memory is not enabled on this agent", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } }
        }
      }
    },
    "/api/schedules": {
      "get": {
        "tags": ["schedules"],
        "summary": "List schedules",
        "operationId": "listSchedules",
        "parameters": [
          { "name": "owner", "in": "query", "schema": { "type": "string" }, "description": "Filter by owner session ID" }
        ],
        "responses": {
          "200": { "description": "Schedule list", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Schedule" } } } } }
        }
      }
    },
    "/api/providers": {
      "get": {
        "tags": ["providers"],
        "summary": "List providers",
        "operationId": "listProviders",
        "responses": {
          "200": { "description": "Provider list", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Provider" } } } } }
        }
      }
    },
    "/api/providers/{name}/connect": {
      "post": {
        "tags": ["providers"],
        "summary": "Connect provider",
        "description": "Connects an LLM provider using an API key or OAuth credentials. Validates by fetching models.",
        "operationId": "connectProvider",
        "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
        "requestBody": {
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ConnectRequest" } } }
        },
        "responses": {
          "200": { "description": "Connected", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } },
          "400": { "description": "Invalid credentials" }
        }
      }
    },
    "/api/providers/{name}/disconnect": {
      "post": {
        "tags": ["providers"],
        "summary": "Disconnect provider",
        "operationId": "disconnectProvider",
        "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
        "responses": {
          "200": { "description": "Disconnected", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } }
        }
      }
    },
    "/api/providers/{name}/proxy/{path}": {
      "post": {
        "tags": ["providers"],
        "summary": "Transparent proxy to a provider's native API",
        "description": "Calls a connected provider's own API (Anthropic Messages, OpenAI Chat Completions, Codex Responses, …) in ITS dialect, without a session: the request body and the response — SSE streams included, flushed as they arrive — pass through untouched, and harness supplies the credentials (OAuth refresh included) and identity headers. Point a vendor SDK's base URL at /api/providers/{name}/proxy (any API key; Authorization/x-api-key headers sent by the caller are dropped). {path} is relative to the provider's native base URL: anthropic and claude-oauth → /v1/messages, /v1/messages/count_tokens (POST), /v1/models (GET); openai-style providers (openai, minimax, opencode-go, ollama-cloud and custom ones) → /chat/completions, /models; codex-oauth → /responses, /models; ollama → /v1/chat/completions, /v1/models, /api/chat, /api/generate, /api/tags, /api/show. Anything else is 403. What the caller omits (tools, system prompt, max_tokens) is simply absent: the provider answers with its own error where its API requires it. Nothing is created or persisted. The one adapted provider is claude-oauth, whose backend only accepts Claude Code-shaped requests: harness prepends the Claude Code identity block to system, maps tool names to Claude Code's (Fetch→WebFetch, others→mcp__x__<name>) and back in the response, and adds the billing header. The upstream's own status and body are returned as-is, 4xx/5xx included; harness's own errors use the standard error shape: 404 unknown provider, 409 provider not connected, 403 path/method not allowed, 400 unusable path or (claude-oauth) body, 413 body over the limit (32 MiB by default), 502 upstream unreachable. A stream that goes silent for 3 minutes is aborted, and the upstream call is cancelled when the caller disconnects.",
        "operationId": "providerProxy",
        "parameters": [
          { "name": "name", "in": "path", "required": true, "schema": { "type": "string" } },
          { "name": "path", "in": "path", "required": true, "description": "Path relative to the provider's native base URL (may contain slashes).", "schema": { "type": "string" }, "allowReserved": true }
        ],
        "requestBody": { "required": false, "content": { "application/json": { "schema": { "type": "object", "description": "The provider's own request body, verbatim." } } } },
        "responses": {
          "200": { "description": "The provider's own response (JSON or text/event-stream), verbatim. Any other status the upstream returns is passed through too." },
          "400": { "description": "Invalid path, or (claude-oauth) a body that is not a Messages request" },
          "403": { "description": "Method/path not allowed through this provider's proxy" },
          "404": { "description": "Unknown provider" },
          "409": { "description": "Provider not connected" },
          "413": { "description": "Request body over the proxy limit" },
          "502": { "description": "Provider unreachable" }
        }
      },
      "get": {
        "tags": ["providers"],
        "summary": "Transparent proxy to a provider's native API (GET, e.g. /v1/models)",
        "description": "Same proxy as the POST operation, for the provider's read-only endpoints (model listing).",
        "operationId": "providerProxyGet",
        "parameters": [
          { "name": "name", "in": "path", "required": true, "schema": { "type": "string" } },
          { "name": "path", "in": "path", "required": true, "schema": { "type": "string" }, "allowReserved": true }
        ],
        "responses": { "200": { "description": "The provider's own response, verbatim." } }
      }
    },
    "/api/models": {
      "get": {
        "tags": ["models"],
        "summary": "List models",
        "description": "Returns all available models across active providers.",
        "operationId": "listModels",
        "responses": {
          "200": { "description": "Model list", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Model" } } } } }
        }
      }
    },
    "/api/sessions": {
      "get": {
        "tags": ["sessions"],
        "summary": "List sessions",
        "operationId": "listSessions",
        "parameters": [
          { "name": "cwd", "in": "query", "schema": { "type": "string" }, "description": "Filter by working directory" }
        ],
        "responses": {
          "200": { "description": "Session list", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/SessionMeta" } } } } }
        }
      },
      "post": {
        "tags": ["sessions"],
        "summary": "Create session",
        "operationId": "createSession",
        "requestBody": {
          "required": true,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/CreateSessionRequest" } } }
        },
        "responses": {
          "201": { "description": "Session created", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/SessionDetail" } } } }
        }
      }
    },
    "/api/sessions/{id}": {
      "get": {
        "tags": ["sessions"],
        "summary": "Get session",
        "operationId": "getSession",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Session detail", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/SessionDetail" } } } }
        }
      },
      "delete": {
        "tags": ["sessions"],
        "summary": "Delete session",
        "operationId": "deleteSession",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Deleted", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } }
        }
      }
    },
    "/api/sessions/{id}/close": {
      "post": {
        "tags": ["sessions"],
        "summary": "Close session",
        "description": "Flushes the session to disk and deactivates it.",
        "operationId": "closeSession",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Closed", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } }
        }
      }
    },
    "/api/sessions/{id}/resume": {
      "post": {
        "tags": ["sessions"],
        "summary": "Resume session",
        "description": "Reactivates a persisted session, loading its history from disk. The body is optional.",
        "operationId": "resumeSession",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "requestBody": {
          "required": false,
          "content": { "application/json": { "schema": { "type": "object", "properties": {
            "directives": { "type": "array", "items": { "type": "string" }, "description": "Instruction blocks for the reopened session's system prompt only, not persisted. Ignored if the session is already active." },
            "profiles": { "type": "array", "items": { "type": "string" }, "description": "Profile names to ADD to the session's persisted binding (additive). If the session is already active, a changed binding applies at its next turn boundary, never mid-turn. Unknown name → 400." }
          } } } }
        },
        "responses": {
          "200": { "description": "Resumed (or already active — idempotent)", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/SessionDetail" } } } },
          "404": { "description": "Session not found" }
        }
      }
    },
    "/api/sessions/{id}/fork": {
      "post": {
        "tags": ["sessions"],
        "summary": "Fork session",
        "description": "Creates a new session that is an exact copy of the parent (same history, model, stats) with a new ID and fresh timestamps. Returns 409 if the parent is busy.",
        "operationId": "forkSession",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "201": { "description": "Fork created", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/SessionDetail" } } } },
          "404": { "description": "Session not found" },
          "409": { "description": "Session is busy" }
        }
      }
    },
    "/api/sessions/{id}/prompt": {
      "post": {
        "tags": ["sessions"],
        "summary": "Send prompt (async)",
        "description": "Queues a user prompt into the session. Returns immediately with status 'started' or 'queued'.",
        "operationId": "sendPrompt",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "requestBody": {
          "required": true,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/PromptRequest" } } }
        },
        "responses": {
          "202": { "description": "Prompt accepted", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } }
        }
      }
    },
    "/api/sessions/{id}/ask": {
      "post": {
        "tags": ["sessions"],
        "summary": "Send prompt (sync)",
        "description": "Sends a prompt and blocks until the agent's turn completes, returning the final assistant text. The synchronous counterpart to /prompt (fire-and-forget). The HTTP client controls the timeout via its own request context.",
        "operationId": "ask",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "requestBody": {
          "required": true,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/PromptRequest" } } }
        },
        "responses": {
          "200": { "description": "Agent response", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/AskResponse" } } } },
          "400": { "description": "Bad request (missing text, images unsupported)", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "404": { "description": "Session not found", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "500": { "description": "Agent error", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } }
        }
      }
    },
    "/api/sessions/{id}/events": {
      "get": {
        "tags": ["sessions"],
        "summary": "Stream events (SSE)",
        "description": "Opens a Server-Sent Events stream for the session. Emits turn_start, text_delta, tool_call, tool_result, turn_end, error, and more.",
        "operationId": "streamEvents",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "SSE stream", "content": { "text/event-stream": { "schema": { "type": "string" } } } }
        }
      }
    },
    "/api/sessions/{id}/commands": {
      "get": {
        "tags": ["sessions"],
        "summary": "List commands",
        "description": "Returns the dynamic command set the session accepts (built-ins + loaded skills).",
        "operationId": "listCommands",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Command list", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/CommandDef" } } } } }
        }
      },
      "post": {
        "tags": ["sessions"],
        "summary": "Execute command",
        "description": "Runs a session command. Built-ins: compact, reset, rename, model, thinking. Skills: skill:<name>.",
        "operationId": "execCommand",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "requestBody": {
          "required": true,
          "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ExecCommandRequest" } } }
        },
        "responses": {
          "200": { "description": "Command result", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } },
          "202": { "description": "Command started async (compact, skill)" },
          "400": { "description": "Unknown command or bad params" },
          "409": { "description": "Session is busy" }
        }
      }
    },
    "/api/sessions/{id}/messages": {
      "get": {
        "tags": ["sessions"],
        "summary": "Get message history",
        "description": "Returns the session's message history as a plain JSON array — all of it by default, or a suffix: ?from=<n> starts at absolute index n (n >= total returns []), ?compactions=<k> starts just after the k-th-to-last compaction checkpoint (k >= 1; with fewer checkpoints than k the whole history is returned). from and compactions are mutually exclusive (400 if both). X-Messages-From is the absolute index of the first returned message (the {index} the image endpoint counts from) and X-Messages-Total the length of the full log. The skipped part of the log is never parsed, so the tail of a huge compacted session is cheap. Works for active and persisted sessions.",
        "operationId": "getMessages",
        "parameters": [
          { "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } },
          { "name": "from", "in": "query", "required": false, "description": "Start at this absolute message index (>= 0). Mutually exclusive with compactions.", "schema": { "type": "integer", "minimum": 0 } },
          { "name": "compactions", "in": "query", "required": false, "description": "Start just after the k-th-to-last compaction checkpoint (k >= 1; the TUI uses 2). Mutually exclusive with from.", "schema": { "type": "integer", "minimum": 1 } }
        ],
        "responses": {
          "200": {
            "description": "Message list",
            "headers": {
              "X-Messages-From": { "description": "Absolute index of the first returned message.", "schema": { "type": "integer" } },
              "X-Messages-Total": { "description": "Number of messages in the full log.", "schema": { "type": "integer" } }
            },
            "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Message" } } } }
          },
          "400": { "description": "Invalid from/compactions (negative, non-numeric, compactions < 1) or both given" },
          "404": { "description": "Session not found" }
        }
      }
    },
    "/api/sessions/{id}/messages/{index}/images/{n}": {
      "get": {
        "tags": ["sessions"],
        "summary": "Get a message image",
        "description": "Raw bytes of the n-th image (0-based, in message order) of the message at absolute history index {index} — what received_prompt/follow_up_start's message_index + images point at, and an alternative to decoding base64 from /messages. Works for active and persisted sessions. Cached as immutable; the ETag changes after a /reset (which reuses indices).",
        "operationId": "getMessageImage",
        "parameters": [
          { "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } },
          { "name": "index", "in": "path", "required": true, "schema": { "type": "integer", "minimum": 0 } },
          { "name": "n", "in": "path", "required": true, "schema": { "type": "integer", "minimum": 0 } }
        ],
        "responses": {
          "200": { "description": "The image (Content-Type is the image's own MIME type; Cache-Control: private, max-age=31536000, immutable; ETag)", "content": { "image/*": { "schema": { "type": "string", "format": "binary" } } } },
          "304": { "description": "Not modified (If-None-Match matched the ETag)" },
          "400": { "description": "index or n is not a non-negative integer", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } },
          "404": { "description": "Session, message or image not found", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } }
        }
      }
    },
    "/api/sessions/{id}/stop": {
      "post": {
        "tags": ["sessions"],
        "summary": "Stop session",
        "description": "Cancels the current in-flight turn.",
        "operationId": "stopSession",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Stopped", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Status" } } } }
        }
      }
    },
    "/api/sessions/{id}/info": {
      "get": {
        "tags": ["sessions"],
        "summary": "Session info",
        "description": "Returns a single-round-trip snapshot of session metadata, stats, MCP count, schedule count, and busy state.",
        "operationId": "getSessionInfo",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Session info", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/SessionInfo" } } } }
        }
      }
    },
    "/api/sessions/{id}/context": {
      "get": {
        "tags": ["sessions"],
        "summary": "Context breakdown",
        "description": "Returns estimated token counts for system, tools, and conversation slots in the model's context window.",
        "operationId": "getSessionContext",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Context breakdown", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ContextBreakdown" } } } }
        }
      }
    },
    "/api/sessions/{id}/sysprompt": {
      "get": {
        "tags": ["sessions"],
        "summary": "System prompt",
        "description": "Returns the full system prompt the session sends to the provider — base prompt, skills, project context, memory block and every directive (agent-wide plus the session's own) — as raw markdown. Only valid for active sessions; safe while the session is busy.",
        "operationId": "getSessionSystemPrompt",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "The system prompt", "content": { "text/markdown": { "schema": { "type": "string" } } } },
          "400": { "description": "Session is not active", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ErrorResponse" } } } }
        }
      }
    },
    "/api/sessions/{id}/tools": {
      "get": {
        "tags": ["sessions"],
        "summary": "List session tools",
        "description": "Returns the full set of tool definitions (name, description, input_schema) currently registered for the session — the exact set sent to the provider on the next turn, including any MCP tools and SDK-supplied tools alongside the built-ins. Pure introspection, not tied to any transport.",
        "operationId": "getSessionTools",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Tool definitions", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/ToolDef" } } } } }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "Status": {
        "type": "object",
        "properties": {
          "status": {
            "type": "object",
            "properties": {
              "code":    { "type": "string", "example": "ok" },
              "message": { "type": "string" }
            }
          }
        }
      },
      "ServerInfo": {
        "type": "object",
        "properties": {
          "name":       { "type": "string", "example": "harness" },
          "version":    { "type": "string", "example": "v0.73.74" },
          "instance":   { "type": "string", "example": "jade-warrior", "description": "Unique MK11-themed instance name" },
          "transport":  { "type": "string", "example": "tui", "description": "Calling transport (tui, telegram, slack, server, cli)" },
          "url":        { "type": "string", "example": "http://127.0.0.1:52341" },
          "cwd":        { "type": "string", "example": "/Users/gustavo/Workspace/harness" },
          "pid":        { "type": "integer", "example": 92807 },
          "started_at": { "type": "string", "format": "date-time", "example": "2026-07-31T00:22:16Z" }
        }
      },
      "InstanceInfo": {
        "type": "object",
        "properties": {
          "version":    { "type": "string", "example": "v0.73.74" },
          "transport":  { "type": "string", "example": "tui" },
          "url":        { "type": "string", "example": "http://127.0.0.1:52341" },
          "cwd":        { "type": "string" },
          "pid":        { "type": "integer" },
          "started_at": { "type": "string", "format": "date-time" }
        }
      },
      "Settings": {
        "type": "object",
        "properties": {
          "active_model":   { "type": "string", "example": "anthropic/claude-opus-4-8" },
          "thinking_level": { "type": "string", "enum": ["off","low","medium","high","xhigh","max"] }
        }
      },
      "Provider": {
        "type": "object",
        "properties": {
          "name":            { "type": "string", "example": "claude-oauth" },
          "display_name":    { "type": "string" },
          "description":     { "type": "string" },
          "is_active":       { "type": "boolean" },
          "is_subscription": { "type": "boolean" },
          "credential_type": { "type": "string", "enum": ["api_key","oauth","none"] }
        }
      },
      "ConnectRequest": {
        "type": "object",
        "properties": {
          "api_key":       { "type": "string" },
          "access_token":  { "type": "string" },
          "refresh_token": { "type": "string" },
          "expires_at":    { "type": "integer", "format": "int64", "description": "Unix milliseconds" }
        }
      },
      "Model": {
        "type": "object",
        "properties": {
          "model":           { "type": "string", "example": "anthropic/claude-opus-4-8" },
          "provider":        { "type": "string", "example": "anthropic" },
          "is_subscription": { "type": "boolean" }
        }
      },
      "MCPStatus": {
        "type": "object",
        "properties": {
          "name":       { "type": "string" },
          "connected":  { "type": "boolean" },
          "tool_count": { "type": "integer" },
          "error":      { "type": "string" }
        }
      },
      "CustomProvider": {
        "type": "object",
        "required": ["type", "url"],
        "properties": {
          "type":       { "type": "string", "enum": ["openai"], "description": "API dialect — only \"openai\" is supported for now" },
          "url":        { "type": "string", "example": "https://my-proxy.internal/v1", "description": "Base URL for chat completions" },
          "models_url": { "type": "string", "description": "Models listing URL; defaults to \"<url>/models\" when omitted" },
          "headers":    { "type": "object", "additionalProperties": { "type": "string" }, "description": "Extra HTTP headers sent on every request" },
          "display":    { "type": "string", "description": "Human-friendly display name; defaults to the provider name (the map key) when omitted" },
          "disabled":   { "type": "boolean", "description": "Enabled by default; set true to skip loading this provider" }
        }
      },
      "Schedule": {
        "type": "object",
        "properties": {
          "slug":     { "type": "string" },
          "cron":     { "type": "string", "example": "30 21 * * 0-4" },
          "prompt":   { "type": "string" },
          "runs":     { "type": "integer" },
          "last_run": { "type": "integer", "format": "int64", "description": "Unix milliseconds" }
        }
      },
      "MemorySearchResult": {
        "type": "object",
        "properties": {
          "total":     { "type": "integer" },
          "returned":  { "type": "integer" },
          "skip":      { "type": "integer" },
          "limit":     { "type": "integer" },
          "results":   { "type": "array", "items": { "$ref": "#/components/schemas/MemoryEntry" } }
        }
      },
      "MemoryEntry": {
        "type": "object",
        "properties": {
          "slug":       { "type": "string" },
          "cwd":        { "type": "string", "description": "Project path, or \"<global>\" for a global memory" },
          "content":    { "type": "string", "description": "Omitted when include_content=false" },
          "score":      { "type": "number", "description": "Relevance (search mode only; higher = more relevant)" },
          "created_at": { "type": "integer", "format": "int64", "description": "Unix milliseconds" },
          "updated_at": { "type": "integer", "format": "int64", "description": "Unix milliseconds" }
        }
      },
      "SessionMeta": {
        "type": "object",
        "properties": {
          "id":             { "type": "string", "format": "uuid" },
          "cwd":            { "type": "string" },
          "name":           { "type": "string" },
          "model":          { "type": "string" },
          "thinking":       { "type": "string" },
          "compact_offset": { "type": "integer" },
          "compact_count":  { "type": "integer" },
          "stats":          { "$ref": "#/components/schemas/SessionStats" },
          "created_at":     { "type": "string", "format": "date-time" },
          "last_active_at": { "type": "string", "format": "date-time" }
        }
      },
      "SessionDetail": {
        "allOf": [
          { "$ref": "#/components/schemas/SessionMeta" },
          {
            "type": "object",
            "properties": {
              "max_iterations": { "type": "integer" },
              "max_tokens": { "type": "integer", "description": "Output-token ceiling: the agent's MaxTokens when set, else the model's catalog value capped at 64k (each request is further bounded by the room left in the window)" }
            }
          }
        ]
      },
      "SessionStats": {
        "type": "object",
        "properties": {
          "input_tokens":    { "type": "integer" },
          "output_tokens":   { "type": "integer" },
          "cache_read":      { "type": "integer" },
          "cache_write":     { "type": "integer" },
          "cost_usd":        { "type": "number" },
          "context_usage":   { "type": "number", "description": "0.0–1.0" },
          "context_window":  { "type": "integer" }
        }
      },
      "SessionInfo": {
        "type": "object",
        "properties": {
          "version":       { "type": "string" },
          "session":       { "$ref": "#/components/schemas/SessionDetail" },
          "mcp_connected": { "type": "integer" },
          "schedule_count":{ "type": "integer" },
          "busy":          { "type": "boolean" },
          "queue_depth":   { "type": "integer" }
        }
      },
      "ContextBreakdown": {
        "type": "object",
        "properties": {
          "system":           { "type": "integer", "description": "Estimated system prompt tokens" },
          "tools":            { "type": "integer", "description": "Estimated tool schema tokens" },
          "conversation":     { "type": "integer", "description": "Estimated conversation tokens" },
          "free_space":       { "type": "integer" },
          "estimated_total":  { "type": "integer" },
          "last_real_total":  { "type": "integer", "description": "Actual token count from last API response" },
          "context_window":   { "type": "integer" }
        }
      },
      "CreateSessionRequest": {
        "type": "object",
        "required": ["model"],
        "properties": {
          "model": { "type": "string", "example": "claude-oauth/claude-opus-4-8" },
          "cwd":   { "type": "string" },
          "name":  { "type": "string" },
          "directives": { "type": "array", "items": { "type": "string" }, "description": "Instruction blocks appended to THIS session's system prompt only (not the agent's other sessions). Not persisted — pass them again on resume." },
          "profiles": { "type": "array", "items": { "type": "string" }, "description": "Bind the session to registered server profiles by name (e.g. \"slack\") — their directives and tools apply to it. The names are persisted: every later open of the session, by any client, re-applies whichever are registered then. Unknown name → 400." }
        }
      },
      "PromptRequest": {
        "type": "object",
        "required": ["text"],
        "properties": {
          "text":   { "type": "string" },
          "images": { "type": "array", "items": { "$ref": "#/components/schemas/ImageData" } },
          "origin": { "type": "string", "description": "Where the prompt came from — any short string: \"user\" (default), \"scheduled\", a transport (\"telegram\", \"slack\", \"acp\") or your own. Echoed on received_prompt/follow_up_start." }
        }
      },
      "AskResponse": {
        "type": "object",
        "properties": {
          "text": { "type": "string", "description": "Final assistant response text" }
        }
      },
      "ErrorResponse": {
        "type": "object",
        "properties": {
          "error": {
            "type": "object",
            "properties": {
              "message": { "type": "string" },
              "details": { "type": "object", "additionalProperties": true }
            },
            "required": ["message"]
          }
        }
      },
      "ImageData": {
        "type": "object",
        "properties": {
          "mime_type": { "type": "string", "example": "image/png" },
          "base64":    { "type": "string", "description": "Base64-encoded image data" }
        }
      },
      "CommandDef": {
        "type": "object",
        "properties": {
          "name":        { "type": "string" },
          "description": { "type": "string" },
          "params":      { "type": "array", "items": { "$ref": "#/components/schemas/ParamDef" } }
        }
      },
      "ToolDef": {
        "type": "object",
        "properties": {
          "name":         { "type": "string", "example": "Bash" },
          "description":  { "type": "string" },
          "input_schema": { "type": "object", "description": "JSON Schema for the tool's input arguments", "additionalProperties": true }
        }
      },
      "ParamDef": {
        "type": "object",
        "properties": {
          "name":     { "type": "string" },
          "type":     { "type": "string" },
          "required": { "type": "boolean" },
          "values":   { "type": "array", "items": { "type": "string" } }
        }
      },
      "ExecCommandRequest": {
        "type": "object",
        "required": ["command"],
        "properties": {
          "command": { "type": "string", "example": "compact", "enum": ["compact","reset","rename","model","thinking","max-iter","skill:<name>"] },
          "params":  { "type": "object", "additionalProperties": true }
        }
      },
      "Message": {
        "type": "object",
        "properties": {
          "role":  { "type": "string", "enum": ["user","assistant"] },
          "parts": { "type": "array", "items": { "type": "object" } }
        }
      }
    }
  }
}`
