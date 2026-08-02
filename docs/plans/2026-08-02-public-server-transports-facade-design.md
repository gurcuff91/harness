# Design: public `server`/`transports` packages + expanded SDK facade

**Date:** 2026-08-02
**Status:** Implemented (v0.75.0) — see CHANGELOG.md [0.75.0] for the full summary of what shipped

## Problem

Today only `agent` (+ its sub-packages) and `client` are public SDK surface.
`internal/server` (the HTTP/SSE backend every transport runs on top of) and
`internal/transport/{telegram,slack,acp,tui}` are `internal/`, so an SDK
consumer embedding the agent programmatically has no way to also
programmatically start the HTTP server or any of the bot/protocol transports
— they'd have to shell out to the `harness` binary instead of embedding it.

The Khan wants `harness.go` (the SDK facade) to expose constructors for all
of these, so a consumer can build an `*agent.Agent` and then hand it to a
runner for whichever transport it wants, entirely in-process.

## Decision: move `server` and `transport/{telegram,slack,acp}` to public
packages; keep `tui` internal (renamed, not moved in spirit)

### New package layout

```
harness.go                          — facade: NewAgent/AgentWith*,
                                       RunServer/RunTelegram/RunSlack/RunAcp
                                       + their With* options, Client/NewClient

server/                             — moved from internal/server (same code,
                                       same internal/{config,providers,version,logx}
                                       dependencies — see "Why this is safe" below)
├── server.go / sse.go / proxy.go / instances.go / middleware.go / server_docs.go
└── NEW: Run(ctx, *agent.Agent, ...Option) error
    (Server / ServerOptions / NewServer / Serve / Close all stay, for callers
    who want fine-grained control; Run is a new convenience wrapper on top)

transports/                         — moved from internal/transport/{telegram,slack,acp}
├── telegram/                       (Run(ctx, a, Options) was already a
│                                    blocking runner — no shape change, only
│                                    the import path changes)
├── slack/                          (same — already a blocking runner)
└── acp/                            (Run signature changes: stdin/stdout move
                                     from positional params to AcpOption,
                                     defaulting to os.Stdin/os.Stdout)

internal/
├── tui/                             — renamed from internal/transport/tui
│                                     (git mv only — stays private, no
│                                     transports/ sibling; TUI is a terminal
│                                     frontend, not something an SDK consumer
│                                     embeds programmatically)
├── cli/                             — updates imports: internal/server → server,
│                                     internal/transport/{telegram,slack,acp} →
│                                     transports/*, internal/transport/tui →
│                                     internal/tui
├── config/ providers/ version/ logx/ — UNCHANGED. server/transports keep
│                                     importing them exactly as today.
```

### Why moving `server`/`transports` without moving `config`/`providers` is safe

Go's `internal/` rule only blocks OTHER modules from importing a package
under `internal/` — it does NOT stop sibling packages in the SAME module.
Moving `server` out of `internal/` doesn't change what it's allowed to
import; it can keep importing `internal/config`, `internal/providers`,
`internal/version`, `internal/logx` unchanged.

This isn't a new kind of exposure either: `agent/agent.go` (already fully
public) already imports `internal/config` (for the default thinking level)
and `internal/providers` (for `Providers()`, `Models()`, `providers.Resolve()`)
directly. Any SDK consumer already depends on that machinery today just by
calling `agent.New()`/`harness.NewAgent()` — settings.json and
credentials.json are already read/written from there. Exposing `server`
doesn't add a conceptually new dependency; it just exposes an HTTP interface
over the same global config machinery the Agent already uses. Confirmed: no
compile-time obstacle, and no new encapsulation break beyond what already
exists.

## Facade signatures (`harness.go`)

All four `RunX` functions are uniform: `func RunX(ctx context.Context, a
*agent.Agent, opts ...XOption) error` — blocking, returns when ctx is
cancelled (or the transport's own natural end, e.g. stdin closing for ACP).
No `cancel` function is returned — redundant with ctx, which the caller
already controls (`context.WithCancel`) if they need to stop it
programmatically. This matches Go idiom: the cancellation channel is the
ctx, not a second returned mechanism.

```go
// Server
func RunServer(ctx context.Context, a *agent.Agent, opts ...ServerOption) error
func ServerWithAddr(addr string) ServerOption   // default "127.0.0.1:0"
func ServerWithVerbose() ServerOption

// Telegram
func RunTelegram(ctx context.Context, a *agent.Agent, opts ...TelegramOption) error
func TelegramWithToken(token string) TelegramOption           // required
func TelegramWithSessionModel(model string) TelegramOption    // override for sessions THIS transport creates
func TelegramWithSessionThinking(level string) TelegramOption
func TelegramWithAllowUnpair() TelegramOption

// Slack
func RunSlack(ctx context.Context, a *agent.Agent, opts ...SlackOption) error
func SlackWithWorkspace(url string) SlackOption                // required
func SlackWithXoxC(token string) SlackOption                   // required
func SlackWithXoxD(cookie string) SlackOption                  // required
func SlackWithSessionModel(model string) SlackOption
func SlackWithSessionThinking(level string) SlackOption

// ACP
func RunAcp(ctx context.Context, a *agent.Agent, opts ...AcpOption) error
func AcpWithStdin(r io.Reader) AcpOption    // default os.Stdin
func AcpWithStdout(w io.Writer) AcpOption   // default os.Stdout
```

`harness.go` doesn't reimplement logic — each `RunX`/`XWith*` is a thin
alias/wrapper that delegates to the real `Run`/`Option` in the moved
package, same pattern `NewAgent`/`AgentWith*` already use for `agent.New`.

### Key decision: no `Scheduler` option on Telegram/Slack transports

`Options.Scheduler` exists today in both `telegram.Options` and
`slack.Options`, but it only ever fed `AgentOptions.EnableScheduler` at
Agent-construction time (`newInteractiveAgent(c.Scheduler, ...)` in
`internal/cli`). Since `RunTelegram`/`RunSlack` now receive an
**already-constructed** `*agent.Agent`, that decision is made before `Run`
is ever called — via `AgentWithScheduler()` when building the agent. Adding
a `TelegramWithScheduler`/`SlackWithScheduler` option here would be dead
wiring with no effect (the transport package doesn't own the agent's
lifecycle, only runs on top of it), so it's removed entirely — both from the
facade and from the internal `Options` structs (`Scheduler` field deleted).

### Key decision: `Model`/`Thinking` renamed to `SessionModel`/`SessionThinking`

Unlike `Scheduler`, `Model`/`Thinking` are NOT agent construction concerns —
the Agent itself is model-agnostic; each `Session` has its own model. These
options genuinely belong to the transport: they're the override applied when
the transport creates a NEW session per chat/channel (`resolveModel()` in
telegram.go). Kept, but renamed (facade AND internal `Options` struct fields)
to make that scope explicit: `TelegramWithSessionModel`/
`TelegramWithSessionThinking`, `SlackWithSessionModel`/
`SlackWithSessionThinking` — signaling "this configures sessions THIS
transport creates," not "this configures the agent."

### `server.Run` — new convenience wrapper

Today starting the server is 3 manual steps living in
`internal/cli/kong_run_serve.go`: build a `net.Listener`, call
`srv.Serve(listener)` in a goroutine, wait for ctx cancellation, then call
`srv.Close()` in the right order (sessions → agent → HTTP shutdown →
unregister instance), and block until that finishes before returning (so
`main()`'s exit doesn't race the instance-registry cleanup).

`server.Run(ctx, a, opts...)` packages that exact dance into one blocking
call: resolve `addr` (default `"127.0.0.1:0"` if `ServerWithAddr` isn't
passed), `net.Listen`, launch `Serve` in a goroutine, wait for
`ctx.Done()`, call `Close()`, and only return once `Close()` has fully
finished. `Server`/`NewServer`/`ServerOptions`/`Serve`/`Close` stay exported
for advanced/test use; `Run` is the new default path both `harness.RunServer`
and `internal/cli/kong_run_serve.go` use.

### ACP: stdin/stdout become options

`acp.Run(ctx, a, stdin, stdout)` becomes `acp.Run(ctx, a, opts...)` with
`AcpOption`/`WithStdin`/`WithStdout`, defaulting to `os.Stdin`/`os.Stdout`
when not passed — the real callers (the `harness acp` CLI command) always
want the real process streams anyway, so the default covers the common case
and the option only matters for tests / embedding scenarios that want a
custom stream.

## Call site updates (mechanical, no logic changes)

- `internal/cli/kong_run_serve.go` — replaced with a single call to
  `server.Run(ctx, a, server.WithAddr(c.Addr), server.WithVerbose(true))`.
- `internal/cli/kong_run_telegram.go` / `kong_run_slack.go` /
  `kong_run_acp.go` — only the import path changes
  (`internal/transport/X` → `transports/X`); `Scheduler` wiring moves to
  `newInteractiveAgent(c.Scheduler, ...)` (already true today — no change
  needed there, since that's exactly how it already works) and the
  `Options.Scheduler` field is simply no longer set (deleted from the
  struct).
- `internal/cli/kong_run_tui.go` — import path
  `internal/transport/tui` → `internal/tui`.

## Risks / things to verify during implementation

- `instances.json`'s `Transport` field (shown by `harness colleague` / the
  instance registry) must keep being populated correctly
  (`"server"`/`"telegram"`/`"slack"`/`"acp"`) — each `RunX` sets it
  internally (not a public option), matching today's behavior.
- Existing tests in `internal/server/*_test.go` and
  `internal/transport/{telegram,slack,acp}/*_test.go` move with the code
  (`git mv`); new tests are added for `server.Run` (default addr, custom
  addr, verbose, graceful shutdown ordering) and `acp.Run`'s new
  `AcpOption`s (default stdin/stdout, custom ones).
- `AGENTS.md` needs its file-tree section and the "Adding a New Transport"
  workflow section rewritten to match the new paths.
- The project's `harness-architecture` memory needs updating after the
  change lands.
- Double-check no other internal code (schedulers, MCP, memory) imports the
  moved packages by their old `internal/...` path — grep sweep before
  declaring done.

## Out of scope (explicitly deferred)

- Moving `internal/config`, `internal/providers`, `internal/version`,
  `internal/logx` to public packages — Option B, confirmed: not needed for
  this change to work, and moving them is a separate, bigger decision (they
  currently manage credentials/OAuth/global settings state intentionally
  kept out of the compatibility surface) left for a future design if ever
  needed.
- Any refactor of `telegram.Run`/`slack.Run`'s internal shape — they already
  match the "construct + block until ctx cancelled" runner pattern this
  design settles on; only their import path and the `Scheduler`/`Model`/
  `Thinking` options field names change.
