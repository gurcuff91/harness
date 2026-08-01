# ACP Transport — Design Document

**Date:** 2026-08-01
**Status:** Approved, ready for implementation planning

## Goal

Add `harness acp` — a new transport that lets Harness run as an [Agent Client
Protocol](https://agentclientprotocol.com) agent, so it can be used directly
from the Zed editor (and any other ACP-compatible client). ACP standardizes
communication between editors/IDEs and coding agents over JSON-RPC 2.0,
newline-delimited, over stdio — the agent runs as a sub-process of the editor.

## Non-goals (explicitly out of scope, decided during brainstorming)

- **No permission gating.** Harness remains "trusted" — tools execute
  directly, exactly like every other transport. `session/request_permission`
  is never called.
- **No filesystem delegation to the client.** `Read`/`Write`/`Edit` keep
  touching disk directly via the existing tools — no `fs/read_text_file` /
  `fs/write_text_file` calls to bring in Zed's unsaved-buffer state.
- **No terminal delegation to the client.** `Bash` keeps using its own
  subprocess — no `terminal/create` on the client side.
- **No merging Zed's MCP servers.** `session/new`'s `mcpServers` param is
  ignored; the session only uses Harness's own configured MCP servers
  (`settings.json`), exactly like TUI/Telegram/Slack.
- **No changes to `agent/`, `agent/tools/`, or `client/`.** This is a pure
  protocol-translation transport, following the same architecture as
  `internal/transport/telegram` and `internal/transport/slack`.

## Architecture

```
internal/transport/acp/
├── acp.go       // Run(ctx, agent, opts) — entry point: starts loopback server,
│                //   the internal client.Client, and the stdio NDJSON loop
├── jsonrpc.go   // NDJSON framing over stdin/stdout, request/notification dispatch
├── methods.go   // handlers: initialize, session/new, session/load,
│                //   session/prompt, session/cancel, authenticate
├── events.go    // types.Event → session/update notification translation
├── replay.go    // []types.Message (session/load) → session/update sequence
├── commands.go  // available_commands_update + configOptions
└── diff.go      // builds a "diff" ToolCallContent for the Edit tool by
                 //   reading the file before/after, without touching agent/tools/edit.go
```

Same shape as every other interactive transport: `harness acp` builds a
`*agent.Agent` via `newInteractiveAgent()`, starts an in-process
`server.Server` on a loopback port, and talks to it through `client.Client`
(HTTP + SSE) — exactly like `internal/transport/telegram`. The `acp` package
is a pure protocol bridge: JSON-RPC/stdio on one side, the existing HTTP/SSE
client API on the other. `stdout` is reserved exclusively for ACP messages —
one JSON object per line, no pretty-printing; all logging goes to `stderr`.

## Session lifecycle

1. Zed launches `harness acp` as a subprocess.
2. `initialize` → respond `protocolVersion: 1`, `agentCapabilities: {
   loadSession: true, promptCapabilities: {image: true, embeddedContext: true},
   sessionCapabilities: {resume: {}, ...} }`. No `authMethods` (Harness manages
   provider credentials out of band).
3. `session/new {cwd}` → `client.CreateSession(model, cwd, "")`. `mcpServers`
   param ignored. Track `acpSessionId → *acpSession` (internal Harness session
   ID, SSE pump goroutine, pending prompt request ID) in a mutex-guarded map —
   same pattern as `Transport.pumps` in the Telegram transport. Announce
   `configOptions` (active model + thinking) in the response.
4. `session/load {sessionId, cwd}` → resolve the existing Harness session,
   replay its full history (`client.GetMessages`) as an ordered sequence of
   `session/update` notifications (see Replay below), THEN respond to the
   request.
5. `session/prompt {sessionId, prompt: ContentBlock[]}` → convert content
   blocks to text (concatenate `text` blocks + embedded `resource.text`
   blocks) and images (base64 `image` blocks); call
   `client.SendPromptWithImages` when images are present, otherwise
   `client.SendPrompt`. Open `client.StreamEvents` BEFORE sending the prompt
   (same pattern as `internal/cli/cli.go`'s `Run`).
6. Each `types.Event` from the SSE stream translates to 0 or 1
   `session/update` notification (see Event mapping below), written to
   stdout. On `turn_end`, respond to the pending `session/prompt` with the
   resolved `stopReason`.
7. `session/cancel` (notification) → `client.StopSession`; the in-flight turn
   emits `EventStop`, which resolves the pending `session/prompt` with
   `{stopReason: "cancelled"}`.

## Content block handling (prompt → text)

- `text` → append as-is.
- `resource` (embeddedContext) → append `resource.text` (the content is
  embedded directly by the client — no path resolution needed on our side).
- `resource_link` → out of scope for the first cut (no content attached, would
  require reading the file ourselves; not needed since `resource` covers the
  primary @-mention use case).
- `image` → collected separately, passed to `client.SendPromptWithImages`.
- `audio` → not announced as a capability; never sent by a well-behaved client.

## Event mapping (`types.Event` → `session/update`)

| Harness event | ACP notification |
|---|---|
| `EventStreamTextDelta` | `agent_message_chunk`, `content: {type:"text", text: Delta}` |
| `EventStreamThinkingDelta` | `agent_thought_chunk`, `content: {type:"text", text: Delta}` |
| `EventToolStart` | new `tool_call`: `toolCallId: ToolID`, `title: ToolName`, `kind:` (mapped by tool name), `status: "pending"` |
| `EventToolCall` | `tool_call_update`: `status: "in_progress"`, `rawInput: ToolArgs` |
| `EventToolResult` (ok) | `tool_call_update`: `status: "completed"`, `content` — `diff` block for `Edit` (see below), else a `content` block with `Output` as text |
| `EventToolResult` (error) | `tool_call_update`: `status: "failed"`, `content` with `Output` as the error text |
| `EventTokens` | `usage_update`: `used/size/cost` from `Tokens.Input/ContextWindow/CostUSD` |
| `EventError` | not a `session/update` — resolves the pending `session/prompt` as a JSON-RPC error (not a stopReason — it's not a cancellation) |
| `EventStop` | resolves pending `session/prompt` with `{stopReason: "cancelled"}` |
| `EventMaxIterationsReached` | resolves pending `session/prompt` with `{stopReason: "max_turn_requests"}` |
| everything else (`EventTurnStart/End`, `EventLoopStart/End`, `EventToolArgsDelta`, `EventCompactStart/End`, `EventReceivedPrompt`, `EventFollowUpStart`, `EventStreamTextEnd/ThinkingEnd`) | ignored — internal render-control signals with no ACP equivalent |

### `ToolKind` mapping (by tool name, defined in `events.go`)

`Bash` → `execute`, `Read` → `read`, `Write`/`Edit` → `edit`, `Fetch` →
`fetch`, everything else (`Skill`, `Subagent`, `Memo*`, `Schedule*`,
`Colleague*`) → `other`.

### Diff construction for `Edit` (`diff.go`)

On `EventToolCall` for the `Edit` tool: parse `path` out of `ToolArgs` (JSON)
and read the file's content BEFORE the tool runs (the read happens at this
event, ahead of actual execution). On the matching `EventToolResult`, read the
file again (AFTER) and build `{type: "diff", path, oldText, newText}`. Any
read failure (new file via `Write`, deleted file, etc.) falls back to a plain
`content` block. This logic lives entirely in the `acp` package — the `Edit`
tool itself is untouched.

## Replay (`session/load`)

Convert the session's stored `[]types.Message` (`client.GetMessages`) into an
ordered `session/update` sequence:
- `RoleUser` text parts → `user_message_chunk`.
- `RoleAssistant` text parts → `agent_message_chunk`.
- `ThinkingPart` → `agent_thought_chunk`.
- `ToolCall`/`ToolResult` parts → a completed `tool_call` (single
  notification carrying the final state directly, no pending/in_progress
  replay needed since it already happened).

All replay notifications are sent BEFORE responding to the `session/load`
request, per spec.

## Session Config Options & Available Commands

- **`configOptions`** (in `session/new`/`session/load` responses, and via
  `config_option_update` when they change): a `model` option (category
  `model_config`, values from `client.ListModels()`) and a `thinking` option
  (select, `off|low|medium|high|xhigh`). Selecting one translates to
  `client.ExecCommand(sessionID, "model"|"thinking", params)`.
- **`available_commands_update`**: built from `client.ListCommands(sessionID)`
  — which already returns `rename`/`thinking`/`model`/`compact`/`reset` plus
  dynamically discovered skills as `skill:<name>` — translated to
  `AvailableCommand[]` with `description` and an `input.hint` derived from
  each command's `Params`.

## Concurrency

A single `harness acp` process can host multiple concurrent sessions (Zed
supports several parallel "trains of thought"). The transport keeps a
`map[string]*acpSession` (ACP session ID → internal Harness session ID, its
SSE pump goroutine, and its pending prompt request ID for resolving
`turn_end`/`EventStop`/`EventError`) guarded by a mutex — mirrors
`Transport.pumps` in `internal/transport/telegram`.

## CLI integration

New command: `harness acp` (no flags for the first cut — runs in pure ACP
mode over stdio, the command Zed's settings would point to). Added to the
Kong grammar in `internal/cli/kong.go` alongside `serve`/`telegram`/`slack`,
with its `Run()` in `internal/cli/kong_run_acp.go`, calling
`acp.Run(ctx, newInteractiveAgent(...), acp.Options{})`.
