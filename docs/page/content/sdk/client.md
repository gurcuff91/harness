---
title: Client
section: SDK
order: 350
summary: The typed Go client for a running Harness server: sessions, prompts, event streams, commands, export and import, quotas and the provider proxy.
---

Package `client` is the typed HTTP/SSE client for a running Harness server (`harness serve`, or one you start yourself with `harness.StartServer`). Every frontend in Harness uses it; so can you. Use it when the agent lives in another process. When the agent is in your own process, use the [agent API](embedding.md) directly.

```go
import "github.com/gurcuff91/harness/client"

c := client.New("127.0.0.1:8080") // "host:port" or a full URL such as "https://harness.example.com"
```

`harness.NewClient` is an alias of `client.New`, and `harness.Client` an alias of `client.Client`.

## Constructing

| Option | Meaning |
|---|---|
| `client.WithSecret(s)` | Send the server's secret in `X-Harness-Sec` on **every** request: JSON calls, event streams, the provider proxy, export and import. |
| `client.WithContext(ctx)` | Parent context of every request the client makes. Cancelling it cancels in-flight requests, including those with no timeout of their own. Default: `context.Background()`. |

```go
c := client.New("harness.example.com:8080", client.WithSecret(os.Getenv("HARNESS_SECRET")))
info, err := c.GetServerInfo() // GET /api/server
```

Without TLS the secret travels in the clear. Use an `https://` address when the server is not on the same machine.

## A session end to end

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gurcuff91/harness/client"
)

func main() {
	c := client.New("127.0.0.1:8080")
	cwd, _ := os.Getwd()

	sess, err := c.CreateSession("anthropic/claude-sonnet-4-20250514", cwd, "")
	if err != nil {
		log.Fatal(err)
	}
	defer c.CloseSession(sess.ID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.StreamEvents(ctx, sess.ID)
	if err != nil {
		log.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			switch ev.Type {
			case "text":
				fmt.Print(ev.Delta)
			case "error":
				fmt.Println("error:", ev.Message)
			case "turn_end":
				return
			}
		}
	}()

	if _, err := c.SendPrompt(sess.ID, "Say hello in five words."); err != nil {
		log.Fatal(err)
	}
	<-done
	fmt.Println()

	// Or, synchronously:
	text, err := c.Ask(sess.ID, "And in Spanish?", 2*time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(text)
}
```

Open the stream **before** sending the prompt: a prompt's echo is an event on that stream.

## Sessions

| Method | Endpoint / purpose |
|---|---|
| `CreateSession(model, cwd, name, opts...)` | Open a new session. `name` may be `""`. |
| `ResumeSession(id, opts...)` | Reopen a stored session. |
| `ForkSession(id, opts...)` | Copy a session into a new one. |
| `ListSessions(cwd)`, `GetSession(id)` | Read. |
| `CloseSession(id)` | Close the live session; it stays stored. |
| `DeleteSession(id)` | Delete a stored session. |
| `StopSession(id)` | Cancel the running turn. |
| `GetMessages(id)`, `GetMessagesWith(id, client.MessagesOptions{...})` | History; the second reads a window of it. |
| `GetSessionContext(id)`, `GetSessionInfo(id)`, `GetSystemPrompt(id)`, `GetSessionTools(id)` | Introspection. |
| `GetMessageImage(id, index, n)` | The bytes of the n-th image of a message. |

Session options have the same names as in the agent: `client.SessionWithChannels(types.InputChannel{...})` and `client.SessionWithEnabledProfiles(names...)`. Profiles use PATCH semantics: not passed leaves the binding unchanged, names replace it, passing none removes all. An unknown profile name or channel is a 400; a conversation another session already listens to is a 409.

A returned `client.Session` carries `Channels`, `MissingProfiles` and `ModelFallback` when relevant.

## Prompts

```go
c.SendPrompt(id, "text", client.PromptWithOrigin("my-app")) // *Status: Code "started" or "queued"
c.SendPromptWithImages(id, "describe", images)               // images: []types.ImageData
c.Ask(id, "question", 5*time.Minute)                         // blocks for the final text; timeout 0 = no timeout
c.AskWithImages(id, "describe", images, 5*time.Minute)
```

`Ask`'s timeout applies to that call only, never to the event stream.

## Events

`StreamEvents(ctx, id)` returns `<-chan client.Event`. The channel closes when `ctx` is cancelled or the server ends the stream. `Event` is one flat struct; `Type` selects which fields are set. The type strings are:

`turn_start`, `turn_end`, `loop_start`, `loop_end`, `text`, `text_end`, `thinking`, `thinking_end`, `tool_start`, `tool_args`, `tool_call`, `tool_result`, `tokens`, `error`, `retry`, `max_iterations_reached`, `received_prompt`, `follow_up_start`, `compact_start`, `compact_end`, `stop`.

`ev.Raw` holds the original JSON payload, unmodified, for passthrough. Do not block the consumer: read the channel continuously.

## Commands

Session commands do things that are not prompts. `ListCommands(id)` returns the set, which includes one `skill:<name>` command per skill.

```go
c.ExecCommand(id, "model", map[string]any{"model": "anthropic/claude-sonnet-4-20250514"})
c.ExecCommand(id, "thinking", map[string]any{"level": "high"})
c.ExecCommand(id, "compact", nil)
```

Built-in commands: `rename` (`name`), `thinking` (`level`), `model` (`model`), `max-iter` (`value`), `compact`, `reset`, `export` and `import` (`path`). The file commands `export` and `import` read and write `~/.harness/exports/` on the server's machine; to move content use the next section.

## Export and import

```go
f, _ := os.Create("session.export.jsonl")
name, n, err := c.ExportSession(id, f) // name is the file name the server proposes; n is bytes written

in, _ := os.Open("session.export.jsonl")
imported, err := c.ImportSession(in, cwd) // always a NEW session; cwd "" keeps the exported one's
```

The API only moves content; it never reads or writes a path from a request. A missing session on export is a 404. An invalid export on import is a 400 and a body over the server's limit (1 GiB by default) a 413.

## Providers, models, quotas

```go
c.GetProviders()
c.ListModels()
c.ListProviderModels("anthropic")
c.GetModel("anthropic", "claude-sonnet-4-20250514")
c.ProviderQuota("claude-oauth", false) // true forces a refresh
```

`ProviderQuota` returns `*types.Quota`: windows with `used_percent` and `resets_at`, an optional balance and plan, and `stale` when the last good reading is served after a failure. The server caches it for 60 seconds and calls the provider at most once every 5 seconds even with refresh. A 404 (no quota for that provider), 409 (no credentials) or 502 is returned as `*client.Error`.

Other typed calls cover settings, MCP servers, custom providers, memories, schedules and OAuth login; see the method list in package `client`.

## Provider proxy

A server proxies each provider's native API. The client does not know any dialect; it gives you the base URL or a raw request:

```go
base := c.ProviderBaseURL("claude-oauth") // <server>/api/providers/claude-oauth/proxy
// hand it to an official Anthropic or OpenAI SDK as its base URL

resp, err := c.ProviderDo(ctx, "claude-oauth", "POST", "/v1/messages", body, nil)
defer resp.Body.Close()
```

`ProviderDo` returns the upstream response as is: a 4xx or 5xx from the provider is not a Go error. Credentials in the headers you pass are ignored; the server applies its own. For streaming, send the dialect's own `"stream": true` and `Accept: text/event-stream`.

## Errors

Any 4xx or 5xx from the API comes back as `*client.Error`:

```go
var apiErr *client.Error
if errors.As(err, &apiErr) {
	switch apiErr.Status {
	case 404: // not found
	case 409: // busy, or a conversation already taken
	}
	fmt.Println(apiErr.Message, apiErr.Details)
}
```

`Status` is the HTTP status (0 when the error was not built from a response), `Message` the server's message and `Details` optional structured details such as a provider's parsed error payload. `Error()` includes the details as JSON.
