---
title: Channels in Go
section: SDK
order: 360
summary: Implement or run input channels (Slack, Telegram, colleagues) on an embedded agent, bind sessions to conversations, and run frontends on a shared server.
---

An input channel connects a session to an external conversation while the session is open. What people say there arrives as prompts, and the turns those prompts start are published back. Slack, Telegram and colleagues are the bundled channels; you can implement your own.

For the user-facing side see [Slack](../frontends/slack.md), [Telegram](../frontends/telegram.md) and [Colleagues](../frontends/colleagues.md).

## Registering and binding

Two steps. Registering opens nothing; binding a session to a conversation starts listening.

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/gurcuff91/harness"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/channels/slack"
	"github.com/gurcuff91/harness/types"
)

func main() {
	creds, err := slack.LoadCredentials() // what `harness slack login` saved; nil when none
	if err != nil || creds == nil {
		log.Fatal("no Slack login")
	}

	a := harness.NewAgent(
		harness.AgentWithChannels(slack.New(creds.Workspace, creds.XoxC, creds.XoxD)),
	)
	defer a.Close()

	cwd, _ := os.Getwd()
	sess, err := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514",
		agent.SessionWithChannels(types.InputChannel{Type: slack.Type, Ref: "#support"}),
	)
	if err != nil {
		log.Fatal(err) // unknown channel, unresolvable ref, or the conversation is taken
	}
	defer sess.Close()

	<-context.Background().Done() // the session now answers messages from #support
}
```

`types.InputChannel{Type, Ref}`: `Type` is the channel's type (`slack`, `telegram`, `colleague`) and `Ref` is a reference the channel resolves into the conversation's stable id.

Rules of binding:

- **Explicit and never persisted.** Channels are not stored in the session. A resume without `SessionWithChannels` listens to nothing.
- **Honored or the open fails.** An unregistered type fails with an error wrapping `agent.ErrChannelUnavailable`; a malformed or unresolvable ref with `agent.ErrChannelInvalid`; a conversation another live session already listens to with `agent.ErrChannelTaken`. A failed open leaves nothing behind: no claim, no listener, no stored session.
- **One session per conversation, across processes.** The agent claims `(type, id)` in the settings store before it listens. A claim held by a dead process (no such pid, or a reused pid with a different start marker) is taken over; any doubt keeps the claim.
- **Asking an already-open session for a conversation it does not listen to is an error**, never silently ignored.
- Bound conversations are reported by `Session.Channels()` as `[]agent.ChannelInfo{Type, ID, Name}`.
- Over HTTP the same binding is the `channels` field on create and resume; with the [client](client.md), `client.SessionWithChannels`.

### The bundled channels

| Channel | Constructor | Ref | Notes |
|---|---|---|---|
| Slack | `slack.New(workspaceURL, xoxc, xoxd, opts...)` | channel id, channel name (with or without `#`), user id (opens the DM) or DM id | Credentials are explicit. `slack.LoadCredentials` reads the saved login; `SaveCredentials`, `DeleteCredentials` and `VerifyAndSave` manage it. |
| Telegram | `telegram.New(token, opts...)` | comma-separated trusted usernames (`ana,gus`) | The conversation is the bot; the ref is who may talk to it. `telegram.ResolveToken(explicit)` reads a flag value, then `TELEGRAM_BOT_TOKEN`, then the saved token. |
| Colleague | `colleague.New(opts...)` | `"*"` for a fresh random id, or an explicit id | Registering it is enough: it is an `agent.AutoChannel`, so every session the agent opens becomes a colleague. |

Every channel constructor accepts `WithLogger(logx.Logger)` (default: silent). `colleague.WithRunDir(dir)` sets where its sockets live.

```go
a := harness.NewAgent(
	harness.AgentWithChannels(
		colleague.New(), // every session of this agent is a colleague
		telegram.New(telegram.ResolveToken("")),
	),
)
```

A Telegram session is bound with `agent.SessionWithChannels(types.InputChannel{Type: telegram.Type, Ref: "ana,gus"})`. Only those usernames reach the session.

## Origins

Each prompt a channel delivers has an origin, `agent.ChannelOrigin(type, id)`, e.g. `slack:C0123`. A turn's events (from the prompt echo to `turn_end`) go only to the channel that owns the turn's origin; turns of any other origin, including your own prompts, reach no channel. `Event.Origin` carries it on `EventReceivedPrompt` and `EventFollowUpStart`. In a Slack thread the origin is `slack:<channel>:<thread>`. Origins are display and routing data; they grant nothing.

## Implementing a channel

```go
type Channel interface {
	Type() string
	Resolve(ctx context.Context, ref string) (id string, err error)
	Listen(ctx context.Context, id string, deliver func(agent.Inbound)) (stop func(), err error)
	HandleEvent(id string, evt types.Event)
	Name(ctx context.Context, id string) string
	Directive(ids []string) string
	Tools(ids []string) []tools.Tool
}

type Inbound struct {
	Text   string
	Images []types.ImageData
	Origin string // optional; must start with "<type>:"
}
```

| Method | Contract |
|---|---|
| `Type` | The channel's name: the prefix of origins and of `InputChannel.Type`. |
| `Resolve` | Turn a user-facing ref into the conversation's stable id. |
| `Listen` | Start delivering messages of conversation `id` by calling `deliver`. `deliver` must not block for long: it queues a prompt. Return `stop`; it is called exactly once. `ctx` carries the session (`agent.SessionName(ctx)` returns its current name); cancelling it is not a stop signal, `stop` is. |
| `HandleEvent` | Receives the events of the turns started by a prompt from conversation `id`. Called synchronously on the session's event path: it **must not block**; queue the work. |
| `Name` | A readable name (`#support`) shown to people and the model; return `id` if unknown. Called once at bind time. |
| `Directive` | The system-prompt section for a session bound to `ids`. |
| `Tools` | Extra tools for a session bound to at least one conversation of this channel. Every tool name must start with the channel's type with the first letter upper-cased (`slack` gives `SlackPost`), or the open fails. A name that clashes with a tool the session has also fails the open. |

Optional interfaces:

- `agent.AutoChannel` (`AutoRef() string`): the channel joins **every** session the agent opens (new, resumed, forked), bound with that ref. This is how the colleague channel works.
- `agent.SenderOrigins` (`OwnsOrigin(id, origin string) bool`): for a channel whose turns carry the sender in their origin, so a turn that was not delivered in this run (a schedule fired after a restart) is still routed to the right conversation.

A channel can read a read-only view of its session from the `Listen` context with `agent.SessionViewOf(ctx)` (`Info`, `Context`, `Quota`). It has no way to change the session.

## Schedules and channels

A schedule created while answering a channel conversation fires back into that conversation, provided the session reaches it by a durable route (the conversation itself, a sub-conversation such as a thread, or an origin the channel owns through `SenderOrigins`). Otherwise the scheduled prompt fires with no origin. This needs `AgentWithScheduler`; see [Scheduled prompts](../using/scheduling.md).

## Running frontends on an embedded agent

`server`, `acp` and the channels are public so you can run any of them on the agent you built. Build **one** agent, wrap it in **one** server, and hand that server to any number of frontends and clients. Every session lives in one place, and every frontend can watch and drive the same session.

```go
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/gurcuff91/harness"
	"github.com/gurcuff91/harness/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a := harness.NewAgent()

	srv, err := harness.StartServer(a, "127.0.0.1:0", server.ServerOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close() // closes the sessions, then the agent

	// Blocks until ctx is cancelled or stdin closes.
	if err := harness.RunAcp(ctx, srv); err != nil {
		log.Fatal(err)
	}
}
```

| Function | Purpose |
|---|---|
| `harness.RunServer(ctx, agent, opts...)` | Serve the agent over HTTP/SSE until `ctx` is cancelled, then shut down gracefully and close the agent. Options: `harness.ServerWithAddr(addr)` (default `127.0.0.1:0`), `harness.ServerWithLogger(l)` (default silent). `server.WithSecret` is also available. |
| `harness.StartServer(agent, addr, server.ServerOptions{})` | Serve in the background and return the `*server.Server` to share. `srv.Addr()` is valid immediately. |
| `harness.RunAcp(ctx, srv, opts...)` | Run the ACP bridge on a running server over stdin/stdout. Options: `harness.AcpWithStdin(r)`, `harness.AcpWithStdout(w)`. It has no logger option: it never logs. |

`server.ServerOptions` fields: `Logger`, `Frontend` (a name for the caller, default `server`), `KeepAgentOpen`, `ProxyMaxBodyBytes`, `Secret`, `ImportMaxBodyBytes`.

- A frontend never closes the server or the agent. You do. `srv.Close()` closes the server's sessions and the HTTP listener, and the agent too unless `KeepAgentOpen` is set (use it when the agent is not yours).
- `server.ServerOptions.Secret` (or `server.WithSecret` with `RunServer`) makes the server require the secret in `X-Harness-Sec` or `Authorization: Bearer` on every request except `GET /api/server` and the API docs. `server.GenerateServerSec()` creates one. Without a secret, anyone who can reach the address can use the sessions, the providers and the tools, `Bash` included: keep the address on loopback unless you set a secret.
- Talk to the server from your own code with the [client](client.md): `harness.NewClient(srv.Addr())`.
- The address you pass to `server.WithAddr` is used exactly as given. `127.0.0.1:0` is loopback with an OS-assigned port; `:8080` listens on every interface.
- A frontend that adds behavior for its own sessions should register a [profile](profiles.md) and enable it per session, rather than adding tools or directives to the whole agent.

See [HTTP/SSE server](../frontends/server.md) and [ACP (editors)](../frontends/acp.md) for what these frontends do.
