---
title: Profiles
section: SDK
order: 320
summary: Give some sessions extra directives and tools by registering named profiles on the agent and enabling them per session.
---

A profile is a named bundle of directives and tools. The agent registers profiles; each session enables the ones it needs. Sessions that do not enable a profile never see its directives or tools.

Use a profile when only some sessions should get extra behavior, for example the sessions bound to one chat conversation or one frontend. Behavior for every session belongs in `AgentWithDirectives` and `AgentWithTools` instead.

```go
package profiles // agent/profiles

type Profile struct {
	Directives []string            // appended to the system prompt of every bound session
	Tools      func() []tools.Tool // returns the tools added to each bound session
}
```

`Tools` is called once each time a session is opened or reconfigured, so it can return tools bound to your application's live state. It may be nil.

## Registering

At construction:

```go
a := harness.NewAgent(harness.AgentWithProfiles(map[string]profiles.Profile{
	"support": {Directives: []string{"You answer customer questions."}},
}))
```

At any time:

```go
err := a.RegisterProfile("support", profile) // adds or replaces; a blank name is an error
a.UnregisterProfile("support")
names := a.ProfileNames()                    // sorted
```

If you run a [server](../frontends/server.md), `srv.RegisterProfile`, `UnregisterProfile` and `ProfileNames` delegate to the same agent.

## Enabling per session

```go
sess, err := a.NewSession(cwd, model, agent.SessionWithEnabledProfiles("support"))
```

The enabled names are stored in the session, so every later open of that session, by any frontend, applies them again.

`SessionWithEnabledProfiles` **replaces** the session's list, like a PATCH:

| You pass | Result |
|---|---|
| Option not passed | Unchanged. A new session has none; a fork keeps its parent's; an import keeps the export's. |
| One or more names | Exactly those names. |
| The option with no names | All profiles removed. |

It works on `NewSession`, `ResumeSession`, `ForkSession` and `ImportSession`. Resuming a session that is already open with profiles rebinds it. Over HTTP the same semantics apply to the `profiles` field (see [HTTP API](../reference/http-api.md)); the [client](client.md) has the same option.

## Unknown and missing profiles

- Asking for a name that is not registered **fails the open** with an error wrapping `agent.ErrUnknownProfile` (HTTP 400). Nothing is left behind: a failed fork or import deletes its copy.
- A name already **stored** in a session that is no longer registered is **skipped**. The session opens and works without it, the name stays in the session, and it applies again once the profile is registered.
- `Session.MissingProfiles()` returns the stored names that were not registered at the last open or reconfiguration. The API reports them as `missing_profiles`.
- A blank name is an error.

## Live changes

`RegisterProfile` and `UnregisterProfile` reconfigure every open session bound to that name. An idle session applies the change at once; a session in the middle of a turn applies it at its next turn boundary, never mid-turn. The session's directives and tools are rebuilt from the profiles registered at that moment, and its channels' directives and tools are kept.

## Example: a directive and a tool

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/gurcuff91/harness"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/profiles"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

func main() {
	a := harness.NewAgent()
	defer a.Close()

	lookup := tools.Tool{
		Def: types.ToolDef{
			Name:        "OrderStatus",
			Description: "Returns the status of an order by id.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"id": {"type": "string"}},
				"required": ["id"]
			}`),
		},
		Execute: func(ctx context.Context, in json.RawMessage) (string, error) {
			var args struct{ ID string `json:"id"` }
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			return "order " + args.ID + ": shipped", nil
		},
	}

	err := a.RegisterProfile("support", profiles.Profile{
		Directives: []string{"You are a support agent. Use OrderStatus before answering about orders."},
		Tools:      func() []tools.Tool { return []tools.Tool{lookup} },
	})
	if err != nil {
		log.Fatal(err)
	}

	cwd, _ := os.Getwd()
	sess, err := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514",
		agent.SessionWithEnabledProfiles("support"))
	if err != nil {
		log.Fatal(err)
	}
	defer sess.Close()

	out, err := sess.PromptAndWait(context.Background(), "Where is order 42?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)
}
```

A second session opened without the option has neither the directive nor the `OrderStatus` tool.

## Rules

- A profile's tool name must not clash with a built-in or another tool the session already has.
- Profiles are registered on the agent, not the server. A frontend that ships its own profile should register it on start, unregister it on stop, and enable it with `SessionWithEnabledProfiles` on every create and resume, passing the full list when it must keep other profiles.
- Prompt order: the profiles' directives come after the agent's own and before the channels'. The profiles' tools are added before any other per-session tools.
