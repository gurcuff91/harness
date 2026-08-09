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
    { "name": "memory",    "description": "Persistent memory store" },
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
        "description": "Returns all registered server instances from ~/.harness/instances.json. Dead PIDs are pruned on load.",
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
    "/api/memories": {
      "get": {
        "tags": ["memory"],
        "summary": "List memories",
        "operationId": "listMemories",
        "parameters": [
          { "name": "q", "in": "query", "schema": { "type": "string" }, "description": "FTS search query; omit to list all" },
          { "name": "global", "in": "query", "schema": { "type": "boolean" }, "description": "Include global memories" },
          { "name": "skip", "in": "query", "schema": { "type": "integer" } },
          { "name": "limit", "in": "query", "schema": { "type": "integer" } }
        ],
        "responses": {
          "200": { "description": "Memory search result", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/MemorySearchResult" } } } }
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
        "description": "Reactivates a persisted session, loading its history from disk.",
        "operationId": "resumeSession",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
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
        "description": "Returns the full message history for the session (used by TUI on resume).",
        "operationId": "getMessages",
        "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string", "format": "uuid" } }],
        "responses": {
          "200": { "description": "Message list", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Message" } } } } }
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
          "thinking_level": { "type": "string", "enum": ["off","low","medium","high","xhigh"] }
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
          "content":    { "type": "string" },
          "global":     { "type": "boolean" },
          "created_at": { "type": "string", "format": "date-time" },
          "updated_at": { "type": "string", "format": "date-time" }
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
              "max_iterations": { "type": "integer" }
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
          "name":  { "type": "string" }
        }
      },
      "PromptRequest": {
        "type": "object",
        "required": ["text"],
        "properties": {
          "text":   { "type": "string" },
          "images": { "type": "array", "items": { "$ref": "#/components/schemas/ImageData" } }
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
          "command": { "type": "string", "example": "compact", "enum": ["compact","reset","rename","model","thinking","skill:<name>"] },
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
