---
title: Built-in tools
section: Using Harness
order: 160
summary: Reference of the tools every session has, the optional ones, how tool calls run in parallel and how large outputs are truncated.
---

The agent acts through tools. A session has the built-ins below; some appear only when a feature is on. MCP tools (`mcp__<server>__<tool>`, see [MCP servers](mcp.md)) and tools supplied by an embedding application are added to the same set. The exact set a session sends to the model is available at `GET /api/sessions/{id}/tools`.

## Tool table

| Tool | Available | Purpose |
| --- | --- | --- |
| `Bash` | always | Run a shell command. Default timeout 30 s (`timeout` in seconds to raise it). `background: true` runs it detached and returns the PID and a log path; background processes have no timeout. |
| `Read` | always | Read a text file (`offset` and `limit` select lines, 0-based) or an image (jpg, png, gif, webp), which is attached to the conversation. |
| `Write` | always | Create or overwrite a file; creates parent directories. |
| `Edit` | always | Exact text replacement: `old_text` must match one unique region. A single `old_text`/`new_text`, or an `edits` array applied against the original file (edits must not overlap). |
| `Fetch` | always | HTTP request (GET, HEAD, POST, PUT, PATCH, DELETE) with `headers`, and a body as `body`, `json`, `form` or `files`. `download_to` saves binary responses to disk; `prompt` condenses a text response with an extra model call. Timeout 30 s by default; 4xx/5xx are reported as errors. |
| `WebSearch` | always in the CLI (opt-in for SDK embedders) | Search the public web: `query`, `limit` (default 5, max 10), `timeout`. Needs a connected provider with a search backend (MiniMax, Ollama Cloud or Synthetic, tried in that order); without one it has nothing to search with. |
| `Skill` | at least one skill exists | Load a [skill](skills.md) by name. |
| `Subagent` | always | Run an autonomous sub-agent on a self-contained task and return its final answer. See below. |
| `SessionInfo` | always | Snapshot of this session: id, cwd, name, model, thinking level, version, MCP servers, schedules, OS, terminal, channels, usage. |
| `MemoWrite`, `MemoSearch`, `MemoDelete` | agent has memory | Persistent [memory](memory.md). |
| `Schedule`, `ScheduleList`, `ScheduleDelete` | `--with-scheduler` | Cron [schedules](scheduling.md). |
| `ColleagueList`, `ColleagueAsk` | `--with-colleagues` | Find and ask other sessions on this machine. See [Colleagues](../frontends/colleagues.md). |
| `Slack*`, `Telegram*` | the channel is bound | Tools an input channel brings for its conversation. See [Slack](../frontends/slack.md) and [Telegram](../frontends/telegram.md). |

An SDK embedder can remove built-ins by name through `AgentOptions.DisallowedTools` and add its own with `AgentOptions.Tools`; see [Custom tools](../sdk/tools.md).

## Subagent

`Subagent` starts a separate agent with a fresh context for one task, which keeps exploration noise out of your session. Several sub-agent calls in one response run in parallel.

| Argument | Meaning |
| --- | --- |
| `prompt` | The complete task. |
| `timeout` | Seconds to wait (default 120). |
| `background` | Return immediately with the path of a result file; no timeout. |
| `max_iterations` | Iteration budget (default 50, 1 to 200). |
| `readonly` | Remove `Write` and `Edit` from the sub-agent. It is an extra layer, not a sandbox: it still has `Bash`. |

Sub-agents cannot start sub-agents, cannot write or delete memories (they can search them), and have no Schedule tools. Their cost is added to the parent session's stats.

## Parallel execution

When the model requests several tool calls in one response, harness runs them concurrently and waits for all of them before the next model call. Calls that depend on each other's output therefore belong in successive responses; independent calls can share one.

## Output truncation

Tool output is capped at 2000 lines or 50 KB, whichever is reached first, so one command cannot flood the context.

- `Bash` keeps the **end** of the output (where errors and summaries are).
- `Read` and `Fetch` keep the **beginning**.
- When output is cut, the full text is saved to a temporary file and the result ends with a footer such as `[Full output: /tmp/...  Truncated: showing 2000 of 8123 lines]`, so the agent can read the part it needs with `Read` (using `offset` and `limit`).

Images read with `Read` are limited in size (about 7.5 MB per file).

## Tool icons in the TUI

Each built-in has its own icon in the history; colleague tools use `⚇`, tools of an input channel use `⌁`, and MCP and other external tools use a generic gear.
