# The admin layer: `agent` operates live sessions, `admin` does everything else

Date: 2026-11-26 · Target: v0.129.0 (breaking, one release) · Owner-approved.

## Why
Administration is scattered. Connecting a provider runs CLI → an in-memory server → HTTP client → a handler that holds the real logic (`Connect` + `forgetQuota`). The server's handlers embed business rules; the CLI starts a server just to list sessions; an SDK embedder has no public way to do any of it. A new frontend (a JSON-RPC one for a TS/JS SDK) would copy all of it.

## Rule
- **`agent`** operates with LIVE sessions: open/resume/fork/import, prompts, events, tools, profiles, channels, the scheduler engine.
- **`admin`** does everything that does not need a live session. Public package (part of the SDK). It does NOT import `agent`.
- Simple on purpose: it protects only the current process (see "Active sessions").

## Shape
```go
ad := admin.New()                                   // what the CLI uses: no agent
ad := admin.New(admin.WithActiveSessions(a.ActiveSessions))
ad.Providers.Connect(...)  ad.Sessions.List(cwd)  ad.Settings.SetModel(...)  ad.MCP.Add(...)  ad.Memory.Search(...)
```
One service per domain: `Sessions`, `Providers`, `Settings`, `MCP`, `Memory`, `Schedules`, `Credentials`.

Options are plain `With*` (the package has ONE kind of option) and mirror the agent's: `WithSessionStore(store.SessionStore)`, `WithMemory(memory.Store)`, `WithActiveSessions(func() []string)`. Not passed → the same defaults `agent.New` uses (file store under `~/.harness`, in-memory fallback; no memory store unless given). Settings and credentials stay process-global (`SetSettingsStore`/`SetCredentialsStore`), so they need no option. `admin` closes what it opened, like the agent.

## What moves
Out of `Agent` (public methods 28 → ~14): `ListSessions`, `ListAllSessions`, `DeleteSession`, `RenameSession`, `SessionMetaOf`, `LoadMessages*`, `SessionProfiles`, `SessionResetCount`, `ExportSession`, `ListModels`, `Models`, `MaxTokensFor`, `Memory`, `Schedules`, and the package funcs `NewOpenAIProvider`, `NewAnthropicProvider`, `ReloadCustomProviders`, `UnregisterCustomProvider`.
Stays: `New`, `Close`, `Options`, `NewSession`, `ResumeSession`, `ForkSession`, `ImportSession` (it creates a live session), `RegisterProfile`/`UnregisterProfile`/`ProfileNames`, `RegisterTool`, `MCPTools`/`MCPStatuses`, and the new **`ActiveSessions()`** (ids of the sessions open in this process).

## Coordination — no Notifier
Providers, settings, MCP, memory and credentials are SHARED STATE: `admin` changes it and running sessions re-read it (a session re-resolves its provider by name at every turn). Nothing to notify, nothing to lose.
The one real interaction: deleting or renaming a stored session that is open here. `admin.Sessions.Delete/Rename` ask `ActiveSessions()` first and return `ErrSessionActive`. Without the function (the CLI) nothing is checked. A session open in ANOTHER process is not protected — as today; documented, not solved.

## Errors
Typed: `ErrNotFound`, `ErrInvalid`, `ErrSessionActive`, `ErrConflict`, `ErrNotConfigured`. The server maps them to 404/400/409 with `errors.Is`; no more picking a status by reading error text.

## Server and CLI after the move
Each server handler = decode → call `admin`/`agent` → encode. The CLI stops starting an in-memory server for `connect`, `sessions`, `delete`, `memo`, `schedules`, `settings`, `mcp`, `provider`. The wire types the server returns for these come from `admin` (the client reuses them).

## What else moves out of the server (stage 4)
Two rules the server owns that any frontend needs, so they go down to the agent: (1) fan-out of a live session's events to N observers, with deltas droppable and control events never dropped; (2) one live instance per session however many clients open it at once. The server keeps HTTP-only things (secret, CORS, OpenAPI, the provider proxy, pprof).

## Out of scope
A `Notifier`, cross-process protection of open sessions, code generation of types, the JSON-RPC frontend and the TS SDK (they come next, on top of this), moving channels or profiles.

## Testing
Direct contract tests of each `admin` service (no HTTP, in-memory stores); the existing server/CLI tests keep proving the HTTP/CLI behavior did not change; mutants on the active-session check and the error mapping.
