---
title: Admin
section: SDK
order: 305
summary: The admin package: stored sessions, providers, settings, MCP configuration, memory and schedules, without a live session.
---

`agent` operates **live** sessions. `admin` does everything that does not need one. The CLI and the HTTP server are thin layers over these two packages, and so can your own code be.

```go
ad := admin.New()                                        // no agent: what the CLI uses
ad := a.Admin()                                          // bound to an agent's stores
ad := admin.New(admin.WithActiveSessions(a.ActiveSessions))

metas, err := ad.Sessions.List(cwd)                      // "" lists every directory
models, err := ad.Providers.Connect(ctx, "anthropic", admin.ConnectCredentials{APIKey: key}) // models: how many it offers
err = ad.Settings.SetActiveModel("anthropic/claude-sonnet-4-20250514")
```

`admin` does not import `agent`.

## Services

| Service | What it covers |
|---|---|
| `Sessions` | List, get, read messages, rename, delete and export stored sessions. |
| `Providers` | List, models, connect and disconnect, OAuth, quotas and custom providers. |
| `Settings` | The default model and thinking level. |
| `MCP` | MCP server configuration (add, remove, enable, disable). |
| `Memory` | List, search, read, write and delete memories. |
| `Schedules` | List cron-scheduled prompts. |

## Options

`admin.New` takes plain `With*` options, the same ones the agent has:

| Option | Meaning |
|---|---|
| `WithSessionStore(s)` | Where stored sessions live. Default: the file store `agent.New` uses, falling back to memory. |
| `WithMemory(m)` | The memory store. Default: none; memory writes return `ErrNotConfigured`. |
| `WithActiveSessions(fn)` | Which sessions are open in this process (usually `a.ActiveSessions`). |

Settings and credentials are process-global (`harness.SetSettingsStore`, `SetCredentialsStore`) and need no option. `Close` releases only a store `admin` opened itself.

## Errors

Every method returns errors you can test with `errors.Is`:

| Error | Meaning |
|---|---|
| `ErrNotFound` | The provider, session, model, memory or server does not exist. |
| `ErrInvalid`, `ErrUnprocessable` | The input is not acceptable (a bad thinking level, an invalid MCP server). |
| `ErrSessionActive` | Deleting or renaming a session that is open in this process. |
| `ErrConflict`, `ErrNotConnected` | The request clashes with the current state, or the provider is not connected. |
| `ErrUpstream` | A provider failed (a model fetch, a quota read, an OAuth exchange). |
| `ErrNotConfigured` | The feature has no store here (memory). |

## Open sessions

Providers, settings, MCP and memory are shared state: `admin` changes them and running sessions pick the change up (a session re-resolves its provider at every turn). The one check is deleting or renaming a stored session that is open **in this process**: pass `WithActiveSessions` and `admin` refuses with `ErrSessionActive`. A session open in **another** process is not protected.

## Registering a custom provider from code

`harness.NewOpenAIProvider`, `NewAnthropicProvider`, `UnregisterCustomProvider`, `ReloadCustomProviders` and the `ProviderWith*` options are the same functions as `admin.RegisterOpenAI`, `admin.RegisterAnthropic`, `admin.UnregisterProvider`, `admin.ReloadCustomProviders`. See [Custom providers](providers.md).
