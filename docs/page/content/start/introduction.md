---
title: Introduction
section: Start
order: 10
summary: What Harness is, what it is made of, and how this documentation is organised.
---

Harness is a small AI agent runtime written in Go. It connects a language model to a set of tools through a ReAct loop: the model reasons, calls tools, reads their results, and repeats until the task is done.

```
Agent = LLM + Harness
If you're not the model, you're the harness.
```

It ships as **one binary with no runtime dependencies**, and the same code is also a Go library you can embed.

## What you get

- **Streaming, multi-provider.** Claude and ChatGPT subscriptions, Anthropic, OpenAI, OpenCode Go, Synthetic, MiniMax, Ollama (cloud and local) and any OpenAI- or Anthropic-compatible endpoint. Every provider streams token by token; there is no non-streaming path.
- **Real tools.** Bash, Read, Write, Edit, Fetch, WebSearch, Skill, Subagent and persistent memory. Tool calls from one model turn run in parallel.
- **Sessions that persist.** Resume, fork, rename, export and import them. Context compacts automatically as it fills.
- **Many ways in.** An interactive terminal UI, a one-shot command, an HTTP/SSE server, an [ACP](../frontends/acp.md) agent for editors, and input channels that let a session listen to a Slack or Telegram conversation.
- **Extensible.** [MCP servers](../using/mcp.md), skills, [scheduled prompts](../using/scheduling.md) and, from Go, [profiles and custom tools](../sdk/profiles.md).

## How the pieces fit

The **agent is the SDK**. Everything else is a *frontend* over it, talking to a small HTTP/SSE server, or an *input channel* that feeds a conversation into a session.

| Piece | What it is |
|---|---|
| **Agent** | The ReAct loop, tools, sessions, memory. The embeddable core. |
| **Server** | The HTTP/SSE API every frontend uses. |
| **Frontends** | The terminal UI, `harness -p`, `harness serve`, `harness acp`. |
| **Channels** | Slack, Telegram and *colleagues* (other Harness sessions on your machine). |

## Reading this documentation

| If you want to… | Start with |
|---|---|
| Try it in five minutes | [Quick start](quick-start.md) |
| Use the terminal UI day to day | [Using Harness](../using/tui.md) |
| Run it headless or from an editor | [Frontends & channels](../frontends/server.md) |
| Embed it in your own Go program | [SDK](../sdk/embedding.md) |
| Look up a command or an endpoint | [Reference](../reference/cli.md) |

> **Note** Harness is under active development and its Go API is not frozen: breaking changes are listed in the changelog with every release.
