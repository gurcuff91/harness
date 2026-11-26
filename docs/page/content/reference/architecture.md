---
title: Architecture
section: Reference
order: 430
summary: How Harness is layered, how a prompt becomes events, and how sessions, providers and models fit together.
---
Harness is an agent library with several frontends on top of it. The agent is the core and is what you embed as an SDK; the frontends talk to it through an HTTP/SSE server.

## Layers

```text
 frontends                           input channels
 +-------+ +-------+ +-----+ +----+   +-------+ +----------+ +------------+
 |  TUI  | | serve | | acp | | -p |   | Slack | | Telegram | | colleagues |
 +---+---+ +---+---+ +--+--+ +-+--+   +---+---+ +----+-----+ +-----+------+
     |  client.Client (HTTP + SSE)         |  agent.Channel (in-process)
     v         v        v       v          v          v             v
 +------------------------------------------------------------------------+
 | server   HTTP/SSE API: sessions, providers, models, memory, schedules  |
 +------------------------------------------------------------------------+
 | agent    ReAct loop | sessions | tools | MCP | memory | scheduler       |
 +-----------------------------+------------------------------------------+
 | providers (LLM dialects)    | stores: sessions, memory, settings,      |
 |                             | credentials                              |
 +-----------------------------+------------------------------------------+
```

- **agent** runs the ReAct loop and owns sessions, tools, MCP connections, memory and the scheduler. It never imports a frontend.
- **server** exposes the agent over HTTP and streams session events as Server-Sent Events. See [HTTP/SSE server](../frontends/server.md) and the [HTTP API](http-api.md).
- **Frontends** (the TUI, `harness serve`, `harness acp`, `-p`) are clients of a server. The TUI and ACP start their own server on localhost in the same process. They all use the same typed [client](../sdk/client.md), so none of them has a private path into the agent.
- **Input channels** (Slack, Telegram, colleagues) are not frontends: a session listens to an external conversation, its messages become prompts, and the turn's output is published back. Channels are bound to a session and run inside the agent's process. See [Channels in Go](../sdk/channels.md).

## Public and internal packages

| Package | Role |
|---|---|
| `agent` | Agent and session API: the SDK. |
| `agent/tools`, `agent/store`, `agent/resources`, `agent/memory` | Built-in tools, session persistence, skill and prompt discovery, persistent memory. Each has a replaceable interface. |
| `configstore` | Persistence ports for settings and credentials. |
| `mcp` | MCP client. |
| `client` | Typed HTTP/SSE client for the server. |
| `server`, `acp`, `channels/*` | Frontend and channels that can be run programmatically on an embedded agent. |
| `logx` | Logger contract and the default line format. |
| `types` | Events, messages and model metadata shared by all of the above. |
| `internal/...` | Providers, config managers, the TUI, the CLI and other implementation detail. Not importable from outside the module and never exposed in a public signature. |

A thin root package, `harness`, re-exports the essentials. See [Embedding an agent](../sdk/embedding.md).

## Request lifecycle

1. A frontend sends a prompt to a session (`POST /api/sessions/{id}/prompt`). Prompts are queued and processed one at a time.
2. The session emits `turn_start` and enters the ReAct loop (default limit of 50 iterations, one of them reserved for a closing summary).
3. Each iteration streams one model response. Text, thinking and tool-call announcements are emitted as they arrive.
4. If the response has no tool calls, the loop ends. Otherwise all tool calls of the response run in parallel, one goroutine each, and the loop waits for every result before the next iteration. Large outputs are truncated before they enter the history.
5. When context usage reaches 95% of the model's window, the session compacts itself between iterations.
6. If the iteration budget runs out, the model is asked to summarize its progress and `max_iterations_reached` is emitted. If the turn is cancelled, `stop` is emitted, never `error`.
7. `turn_end` is emitted. `loop_start`/`loop_end` and `turn_start`/`turn_end` are always balanced.

Frontends learn about all of this through events:

| Event (SSE `type`) | Meaning |
|---|---|
| `turn_start`, `turn_end` | A turn began or finished. |
| `loop_start`, `loop_end` | One ReAct iteration began or finished. |
| `text`, `text_end` | Response text fragment; the text block is finished. |
| `thinking`, `thinking_end` | Reasoning fragment; the reasoning block is finished. |
| `tool_start` | A tool call was announced (name and id known, arguments not yet). |
| `tool_args` | Tool arguments arriving in fragments. |
| `tool_call` | Arguments complete; the tool is about to run. |
| `tool_result` | The tool finished (output, duration, error flag). |
| `tokens` | Token usage, context usage and cost. |
| `retry` | A transient network failure is being retried on a new connection. |
| `error` | Something failed. |
| `max_iterations_reached` | The iteration budget was exhausted. |
| `received_prompt`, `follow_up_start` | A prompt was received, or a queued one is starting, with its text and origin. |
| `compact_start`, `compact_end` | Compaction started or finished (the latter carries the summary). |
| `stop` | The turn was stopped by the user. |

A tool's `tool_id` ties its `tool_start`, `tool_args`, `tool_call` and `tool_result` events together.

An iteration that produces reasoning but neither an answer nor a tool call is reported as an error and not stored as a successful turn. A provider refusal (a completed stream with no content and a refusal stop reason) is also an error. Neither is retried.

## Sessions and persistence

A session is a conversation with its own model, thinking level, tools and history. The default store keeps each session as two files under `~/.harness/agent/sessions/<project>/`, where `<project>` is derived from the working directory:

- `<id>.meta.json`: metadata (name, model, thinking level, iteration limit, enabled profiles, compaction state, usage statistics).
- `<id>.jsonl`: the message log, one JSON message per line, each stamped with its creation time.

Compaction summarizes the conversation with the model and stores a checkpoint. The log keeps every message; the checkpoint marks where the live context starts. A session can be forked, exported to a JSONL file and imported again as a new session. A session whose saved model is no longer available is reopened with the active model, then with the first available one. See [Sessions](../using/sessions.md).

The store is an interface (`SessionStore`) with an in-memory implementation; an embedding program can provide its own. Settings and credentials go through the `configstore` ports in the same way. See [Settings and data](settings.md).

## Providers

Every provider implements one interface and always streams: there is no non-streaming path. A provider converts the neutral request into its wire format, parses the stream into events and reports token usage. The built-in providers are `claude-oauth`, `codex-oauth`, `anthropic`, `openai`, `opencode-go`, `synthetic`, `minimax`, `ollama-cloud` and `ollama`; custom providers add OpenAI-compatible and Anthropic-compatible endpoints from settings or from code. Models are always addressed as `provider/model`, split on the first `/`.

The server can also proxy a provider's native API (`/api/providers/{name}/proxy/...`), supplying the credentials itself.

### Model capabilities

Context window, output limit, vision support, pricing and similar facts are resolved in this order, each tier filling only what the earlier ones left empty:

1. The provider's own API, authoritative and never overwritten.
2. The OpenRouter model catalog, with the last good copy cached on disk as a fallback.
3. A small built-in registry for models the catalog does not list.
4. Inference from the model name.
5. Generic defaults.

Each datum records its source, and a price that is unknown is reported as unknown rather than as zero.

### Thinking levels

One scale, `off`, `low`, `medium`, `high`, `xhigh`, `max`, is mapped by each provider to what its API supports:

| Provider family | Mapping |
|---|---|
| Anthropic, adaptive models | Sent as the `effort` parameter, lowered to the highest effort the model reports supporting. |
| Anthropic, older models | A thinking token budget of 2048, 5000, 10240 and 32000 for `low`, `medium`, `high` and `xhigh`; `max` uses the `xhigh` budget. |
| OpenAI o-series | `low` and `medium` as themselves; every higher level becomes `high`. |
| DeepSeek models on OpenAI-style APIs | `max` stays `max`; every other level becomes `high`. |
| Ollama | `low`, `medium`, `high` and `max` as themselves; `xhigh` clamps to the highest level Ollama has. |
| Synthetic | The level is sent as it is, and `off` as `none`. |

## Concurrency

The ReAct loop is sequential. Concurrency is limited to parallel tool execution, streaming readers, MCP transports and each channel's listener. A session's read accessors (model, thinking level, statistics, metadata) never wait for a running turn, so an HTTP request about a busy session is answered immediately.
