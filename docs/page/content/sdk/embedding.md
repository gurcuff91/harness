---
title: Embedding an agent
section: SDK
order: 300
summary: Build an agent in Go, open sessions, send prompts, receive events and close everything.
---

The agent is the SDK. `harness.NewAgent` builds an `*agent.Agent`; the agent opens sessions; a session runs the ReAct loop and emits events. Everything else in Harness (the TUI, `harness serve`, ACP, Slack) is built on this same API.

```bash
$ go get github.com/gurcuff91/harness
```

## A complete program

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/gurcuff91/harness"
	"github.com/gurcuff91/harness/types"
)

func main() {
	a := harness.NewAgent(harness.AgentWithThinking("medium"))
	defer a.Close()

	cwd, _ := os.Getwd()
	sess, err := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514")
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()

	sess.Subscribe(func(e types.Event) {
		switch e.Type {
		case types.EventStreamTextDelta:
			fmt.Print(e.Delta)
		case types.EventToolCall:
			fmt.Fprintf(os.Stderr, "\n[%s %s]\n", e.ToolName, e.ToolArgs)
		case types.EventError:
			fmt.Fprintln(os.Stderr, "error:", e.Message)
		}
	})

	if _, err := sess.PromptAndWait(context.Background(), "List the Go files in this directory."); err != nil {
		log.Fatal(err)
	}
	fmt.Println()
}
```

The model must belong to a connected provider (see [Connect a provider](../start/connect-a-provider.md)). Credentials are read from the same place the CLI uses.

## Building the agent

`harness.NewAgent(opts ...AgentOption)` never fails: a provider is resolved per session, not per agent. With no options you get the built-in tools (Bash, Read, Write, Edit, Fetch, Subagent, SessionInfo, plus Skill when skills are discovered), no MCP, no memory, no scheduler and no web search. Every option is listed in [Options](options.md).

The same agent can also be built from the struct with `agent.New(agent.AgentOptions{...})`.

An agent owns:

- the session store (default: files under `~/.harness/agent/sessions/`, in-memory if the directory is unusable)
- the MCP server processes passed with `AgentWithMCPs`
- the memory store passed with `AgentWithMemory`
- the scheduler engine, when enabled
- the listeners of every open session that is bound to an input channel
- the registered profiles and tools

`Agent.Close()` releases all of that. It is idempotent. Call it once, when the process is done with the agent.

## Sessions

A session is one conversation: a working directory, a model, a history and a thinking level.

| Method | What it does |
|---|---|
| `a.NewSession(cwd, model, opts...)` | New session. `model` is required, in `provider/model` form. |
| `a.ResumeSession(id, opts...)` | Reopen a stored session. Idempotent: an already-open session returns its live handle. |
| `a.ForkSession(id, opts...)` | Copy a session (history, stats, compaction state) into a new one. Fails with `agent.ErrBusy` if the parent has a turn in flight. |
| `a.ImportSession(r, cwd, opts...)` | Create a new session from an export read from `r`. |
| `a.ActiveSessions()` | Ids of the sessions open in this process. |
| `a.Admin()` | The [admin](admin.md) service bound to this agent's stores. |

`opts` are `agent.SessionOption` values: `agent.SessionWithChannels`, `agent.SessionWithEnabledProfiles` and `agent.SessionWithDirectives`. See [Options](options.md#session-options).

The agent operates **live** sessions. Everything that does not need one (listing, renaming, deleting and exporting stored sessions, providers, settings, memory, schedules) is in [admin](admin.md).

When a resumed or forked session's saved model is unavailable (its provider is not connected any more), the agent falls back to the active model, then to the first available one, and reports it with `Session.ModelFallback()`. It returns `agent.ErrNoModelAvailable` only when nothing is usable.

A session handle is closed with `sess.Close()`. That stops its channel listeners, removes it from the agent's active set and closes its store.

## Sending prompts

```go
status := sess.Prompt(ctx, "Refactor main.go")      // types.PromptStarted or types.PromptQueued
text, err := sess.PromptAndWait(ctx, "Now add tests") // blocks, returns the final assistant text
sess.Wait()                                           // blocks until the session is idle
sess.Stop()                                           // cancels the running turn
```

- `Prompt` returns immediately. If a turn is running, the message is queued and processed in order.
- `PromptAndWait` waits for that prompt's own turn and returns its final text, or the turn's error.
- `ctx` is the parent context of the turns that follow. Cancelling it stops them.
- `Stop` cancels the running turn only; queued prompts continue.
- `IsBusy()` and `FollowUpCount()` report the state of the queue.

Prompt options:

```go
sess.Prompt(ctx, "What is in this image?",
	agent.PromptWithImages(types.ImageData{MimeType: "image/png", Base64: b64}),
	agent.PromptWithOrigin("my-app"),
)
```

`PromptWithOrigin` tags where a prompt came from. It is echoed on `EventReceivedPrompt` and `EventFollowUpStart` as `Event.Origin` (default `"user"`). `PromptWithDisplayText` changes only the text echoed to observers, never what the model receives.

## Events

`Session.Subscribe(handler)` registers the one handler of the session; a second call replaces the first. Register it before the first prompt.

> **Warning** The handler runs inline on the session's event path and tools run in parallel, so it can be called from several goroutines. Keep it fast and make it safe for concurrent use: queue work instead of blocking.

| Event | Meaning | Fields used |
|---|---|---|
| `EventTurnStart` / `EventTurnEnd` | A turn began / finished. Always balanced. | |
| `EventLoopStart` / `EventLoopEnd` | One ReAct iteration. | `Loop` |
| `EventStreamTextDelta` / `EventStreamTextEnd` | Response text fragment / end of text. | `Delta` |
| `EventStreamThinkingDelta` / `EventStreamThinkingEnd` | Reasoning fragment / end. | `Delta` |
| `EventToolStart` | A tool call was announced; arguments not complete yet. | `ToolID`, `ToolName` |
| `EventToolArgsDelta` | Arguments arriving in fragments. | `ToolID`, `Delta` |
| `EventToolCall` | Arguments complete, tool about to run. | `ToolID`, `ToolName`, `ToolArgs` |
| `EventToolResult` | Tool finished. | `ToolID`, `Output`, `Duration`, `IsError` |
| `EventTokens` | Usage update. | `Tokens` (`types.TokenUsage`) |
| `EventError` | The turn failed. | `Message`, `Details` |
| `EventRetry` | A transient network failure is being retried. | `Attempt`, `MaxAttempts`, `Message` |
| `EventMaxIterationsReached` | Iteration budget exhausted; the model summarizes progress. | `MaxIterations` |
| `EventReceivedPrompt` | A prompt that did not queue was received. | `Output` (text), `Origin`, `MessageIndex`, `Images` |
| `EventFollowUpStart` | A queued prompt is about to run. | `Output`, `Origin`, `MessageIndex`, `Images` |
| `EventCompactStart` / `EventCompactEnd` | Compaction started / finished. | `Summary` on end |
| `EventStop` | The turn was stopped with `Stop` or a cancelled context. Never an `EventError`. | |

`ToolID` correlates `ToolStart`, `ToolArgsDelta`, `ToolCall` and `ToolResult` of one call.

## Reading session state

These getters never block, even while a turn is running, so they are safe from handlers and tools:

`ID()`, `CWD()`, `Name()`, `CreatedAt()`, `Model()`, `Thinking()`, `MaxIterations()`, `MaxTokens()`, `Stats()`, `Meta()`, `ContextBreakdown()`, `Channels()`, `MissingProfiles()`, `ModelFallback()`, `SystemPrompt()`.

Changing a session: `SwitchModel(ctx, "provider/model")`, `SwitchThinking(level)`, `SetMaxIterations(n)` (1 to 1000), `Rename(name)`, `Compact(ctx)` and `Reset()`. `Compact` and `Reset` return `agent.ErrBusy` while a turn is running.

## Several sessions, one agent

One agent serves any number of sessions at once; each runs its own turns serially. To expose the same agent to other processes or UIs, wrap it in a server: see [Channels in Go](channels.md#running-frontends-on-an-embedded-agent) and [HTTP/SSE server](../frontends/server.md).
