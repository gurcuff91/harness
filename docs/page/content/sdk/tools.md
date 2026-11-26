---
title: Custom tools
section: SDK
order: 340
summary: Define a tool with a JSON schema and a Go function, register it on the agent, and control which tools a session has.
---

A tool is a name, a JSON schema the model fills in, and a Go function that runs when the model calls it.

```go
package tools // agent/tools

type Tool struct {
	Def         types.ToolDef
	Execute     func(ctx context.Context, input json.RawMessage) (string, error)
	ExecuteRich func(ctx context.Context, input json.RawMessage) (string, []types.ImageData, error)
}

// package types
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
```

- `Def.Name` is what the model calls; it must be unique in the session.
- `Def.Description` is what the model reads to decide when to use the tool. Write it for the model.
- `Def.InputSchema` is a JSON Schema object describing the arguments.
- `Execute` receives the arguments as raw JSON. Decode them into your own struct.
- `ExecuteRich` is optional. When set it takes priority over `Execute` and can also return images to the model.

## A complete example

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gurcuff91/harness"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

var clock = tools.Tool{
	Def: types.ToolDef{
		Name:        "Clock",
		Description: "Returns the current time in a given IANA time zone.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"zone": {"type": "string", "description": "IANA zone, e.g. Europe/Madrid"}
			},
			"required": ["zone"]
		}`),
	},
	Execute: func(ctx context.Context, in json.RawMessage) (string, error) {
		var args struct {
			Zone string `json:"zone"`
		}
		if err := json.Unmarshal(in, &args); err != nil {
			return "", err
		}
		loc, err := time.LoadLocation(args.Zone)
		if err != nil {
			return "", fmt.Errorf("unknown zone %q", args.Zone)
		}
		return time.Now().In(loc).Format(time.RFC3339), nil
	},
}

func main() {
	a := harness.NewAgent(harness.AgentWithTools(clock))
	defer a.Close()

	cwd, _ := os.Getwd()
	sess, err := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514")
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()

	out, err := sess.PromptAndWait(context.Background(), "What time is it in Tokyo?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)
}
```

## Registering

| Way | Scope |
|---|---|
| `harness.AgentWithTools(t...)` or `AgentOptions.Tools` | Every session of the agent. |
| `Agent.RegisterTool(t)` | Every session opened afterwards. Call it before `NewSession` and `ResumeSession`. Re-registering a name replaces the earlier tool. |
| A [profile](profiles.md)'s `Tools` | Only the sessions that enable the profile. |
| An [input channel](channels.md)'s `Tools` | Only the sessions bound to that channel. |

There is no per-session tool option.

## Results and errors

- The string you return is the tool result the model sees.
- Return a non-nil error to mark the result as an error. If the output is empty, the error text becomes the output, so the model always gets an explanation. The turn continues; the model decides what to do next.
- The `ctx` is cancelled when the turn is stopped. Respect `ctx.Done()` in anything that can block.
- A tool call whose arguments the model streamed as malformed JSON is rejected before your function runs, with a message telling the model to retry.
- All tool calls the model makes in one iteration run in parallel, one goroutine each. Your function must be safe for concurrent use, and the loop waits for all of them before the next iteration.

## Truncation

Every tool output is truncated before the model sees it, whichever limit is reached first: 2000 lines or 50 KB, keeping the beginning. The full output is saved to a temporary file and the model is told its path, so it can read the rest with the `Read` tool. Return less, or paginate, when the useful part comes at the end.

`tools.ApplyTruncation(label, content, head)` and `TruncateHead`/`TruncateTail` are exported if you want to apply a different strategy yourself first.

## Disallowing tools

`AgentWithDisallowedTools(names...)` removes tools by name from every session: built-ins, your own, and MCP tools.

```go
a := harness.NewAgent(harness.AgentWithDisallowedTools("Bash", "Write", "Edit"))
```

The built-in names are constants in `agent/tools` (`tools.ToolBash`, `ToolRead`, `ToolWrite`, `ToolEdit`, `ToolFetch`, `ToolSkill`, `ToolSubagent`, `ToolMemoWrite`, `ToolMemoSearch`, `ToolMemoDelete`, `ToolSchedule`, `ToolScheduleList`, `ToolScheduleDelete`, `ToolWebSearch`, `ToolSessionInfo`).

> **Note** Sub-agents started by the `Subagent` tool cannot start sub-agents, write memory or manage schedules, and a read-only sub-agent also loses `Write` and `Edit`.

## MCP servers

Tools from [MCP servers](../using/mcp.md) are registered automatically and namespaced `mcp__<server>__<tool>`.

```go
import (
	"github.com/gurcuff91/harness/mcp"
	"github.com/gurcuff91/harness/types"
)

servers := mcp.ServersFromSettings() // what `harness mcp add` saved
servers["docs"] = types.MCPServer{URL: "https://example.com/mcp"}

a := harness.NewAgent(harness.AgentWithMCPs(servers))
defer a.Close() // terminates the MCP processes

for _, st := range a.MCPStatuses() {
	fmt.Println(st.Name, st.Connected)
}
```

A `types.MCPServer` is either local (`Command`, `Args`, `Env`, `Cwd`) or remote (`URL`, `Headers`); exactly one of `Command` or `URL` must be set. `Timeout` is milliseconds for one tool call (default 5 minutes) and `ConnectionTimeout` is milliseconds for connecting (default 30 seconds). `Disabled: true` skips the server. An invalid or unreachable server never fails the agent; it is skipped and reported by `MCPStatuses()`.

MCP servers are spawned only by the root agent. Without `AgentWithMCPs` the agent has no MCP at all, regardless of what is saved in settings.
