# Changelog

All notable changes to this project will be documented in this file.

## [0.74.2] - 2026-08-01

### Fix — `harness acp`: provider error details were dropped, leaving only a generic message
- **Root cause** — when a provider API call fails (e.g. a rate limit), `types.NewProviderAPIError` parses the provider's own JSON error body into `Details` (`agent/session.go`'s `errorEvent`, then `internal/server/sse.go`'s SSE payload, then `client.Event.Details` — the same data the TUI already pretty-prints below its error line, see `internal/transport/tui/events.go`). `pumpEvents`'s `"error"` case in `internal/transport/acp/events.go` only used `evt.Message` when building the JSON-RPC error, discarding `evt.Details` entirely — a client only ever saw a bare `{"code": -32603, "message": "openai API error 429"}`, with no indication of *why* (rate limit? invalid request? something else?).
- **Fix** — confirmed against ACP's own `Error` type (`{ code, message, data?: unknown }` — a purpose-built field for exactly this) and wired `evt.Details` through as `rpcError.Data`, a field that already existed on the struct but was never populated at this call site. A rate-limited request from Zed now surfaces the provider's complete error payload in `data`, e.g. `{"error": {"http_code": "429", "message": "Token Plan usage limit reached...", "type": "rate_limit_error"}, "request_id": "..."}` — the same depth of detail the TUI has always shown, just carried in JSON-RPC's standard field instead of a rendered text block.
- New regression test asserts `Details` survives the translation into `Data`. Verified manually against the compiled binary with a real rate-limited request. Full suite + `-race` green.

## [0.74.1] - 2026-08-01

### Fix — `harness acp`: hitting the ReAct iteration cap silently lost the agent's summary
- **Root cause** — `agent/session.go` never actually stops a turn cold at its ReAct iteration cap: it emits `EventMaxIterationsReached`, then reserves exactly ONE MORE model call (`requestProgressUpdate`) to summarize progress and hand control back to the user — the summary streams in as ordinary `text` events, followed by a genuine `turn_end` (the same UX the TUI has always shown, via `⚠ reached the N-iteration limit — summarizing progress` in `internal/transport/tui/events.go`). `pumpEvents` in `internal/transport/acp/events.go` treated `max_iterations_reached` as an immediate stopping point — returning `stopReason: "max_turn_requests"` right there — which cut the turn before any of that arrived. In Zed, this meant the user saw nothing at all once the limit was hit: no warning, and crucially no summary, silently losing exactly the thing this whole mechanism exists to deliver.
- **Fix** (`internal/transport/acp/events.go`) — `max_iterations_reached` now emits a visible `agent_message_chunk` with the same wording as the TUI's warning, then keeps pumping instead of returning. The summary's `text` events flow through normally afterward, and the turn resolves with the ordinary `stopReasonEndTurn` once the real `turn_end` arrives — `stopReasonMaxTurnRequests` is kept defined (it's part of ACP's spec vocabulary) but is now documented as deliberately unused by this transport, since Harness never actually stops there.
- New regression test asserts the full sequence: the warning fires, the pump does NOT return, the post-limit summary text still comes through, and the eventual stopReason is `end_turn`. Full suite + `-race` green.

## [0.74.0] - 2026-08-01

### Fix — `harness acp`: `rename`/`reset` were still advertised as slash commands in Zed
- **`internal/transport/acp/commands.go`** — the previous fix (0.73.98) filtered `model`/`thinking` out of `available_commands_update` because they're redundant with the native `configOptions` selectors, but never filtered `rename`/`reset` — even though neither is executed specially by `executableCommand` (only `compact` and `skill:<name>` are — see 0.73.99) and neither has any meaningful effect in an ACP context (the client owns its own session/thread naming; there's no "clear this thread" mechanism to keep a `reset` in sync with what Zed already rendered). They kept showing up in Zed's slash-command list despite doing nothing distinct from a plain prompt if actually invoked.
- Renamed the filter from `commandsCoveredByConfigOptions` to `commandsExcludedFromACP` and expanded it to all 4 IDs (`model`, `thinking`, `rename`, `reset`) — `compact` and every `skill:<name>` remain the only commands advertised, which is also the exact set `executableCommand` knows how to execute, keeping the advertised list truthful. Verified manually against the compiled binary: 29 commands now (`compact` + 28 skills), none of the 4 excluded IDs present. Full suite + `-race` green.

## [0.73.99] - 2026-08-01

### Fix — `harness acp`: slash commands (`/compact`, `/skill:<name>`) were sent to the LLM as plain text instead of being executed
- **Root cause** — ACP has no dedicated JSON-RPC method for invoking a slash command; per spec, "Commands are run as part of regular prompt requests" — the client sends the literal text (e.g. `/compact`, `/skill:brainstorming build me a todo app`) inside a normal `session/prompt`. `handlePrompt` in `internal/transport/acp/methods.go` always forwarded that text straight to `client.SendPrompt`, exactly like the TUI does NOT: the TUI's `handleSubmit` detects a leading `/` before ever touching the prompt path and calls `client.ExecCommand` instead. ACP was missing that same detection entirely — every slash command a user typed in Zed was silently misinterpreted as an ordinary chat message to the model.
- **Fix** (`internal/transport/acp/methods.go`) — new `executableCommand(text)` recognizes `/compact` and `/skill:<name> [args]` specifically and routes them through `client.ExecCommand` instead of `SendPrompt`; everything else (including a slash-prefixed but unrecognized command) falls through unchanged to the normal prompt path, per spec. A failed command (bad skill name, session busy, etc.) is reported as an `agent_message_chunk` (`"✗ <error>"`) and ends the turn cleanly with `stopReason: "end_turn"` — never a JSON-RPC protocol error, since the session itself is perfectly fine.
- **Scope, deliberately limited to `compact`/`skill:*`** — `model`/`thinking` are already covered by native `configOptions` selectors (`session/set_config_option`), and `rename`/`reset` have no meaningful effect in an ACP context: the client owns how it displays a session's name (no protocol channel exists for the agent to push one), and wiping Harness's own history via `/reset` would desync from whatever Zed already rendered on screen from the `session/update` notifications already sent — there's no "clear this thread" mechanism in ACP v1 to keep the two in sync.
- **Fixed a second, related bug this surfaced**: `EventCompactStart`/`EventCompactEnd` — fired both by an explicit `/compact` and by Harness's own AUTOMATIC compaction (mid-turn, once context usage crosses 98%) — were completely unmapped, so a client had zero visibility into context ever being rewritten out from under a conversation it was rendering. Now translated into visible `agent_message_chunk`s: `"⏳ Compacting context..."` then `"✓ Context compacted."` (the actual LLM-generated summary text is not surfaced — only the fact that it happened). A new `stopOnCompactEnd` parameter on `pumpEvents` handles the fact that a standalone `Session.Compact()` call (the `/compact` path) never emits `turn_end` — unlike `skill:*`, which is a genuine ReAct turn under the hood and ends normally — so the pump has to know to stop right after `compact_end` instead of blocking forever for a `turn_end` that will never come for that one command.
- 9 new/updated tests, including two full round trips against a real connected provider: `/compact` actually compacting (with visible start/end feedback) and `/skill:golang-pro <question>` producing a response that visibly reflects the invoked skill's persona — not just literal echoed text. Full suite + `-race` green.

## [0.73.98] - 2026-08-01

### Fix — `harness acp`: `model`/`thinking` no longer duplicated as redundant slash commands
- **`internal/transport/acp/commands.go`** — `buildAvailableCommands` was including every session command Harness exposes, including `model` and `thinking` — both of which are ALSO advertised as native `configOptions` selectors (a proper dropdown with validated values). Having both meant two different ways to do the same thing in Zed's UI, with the slash-command path being strictly worse: free-text value entry, no autocomplete, no validation against the actual option list. Now filtered out via `commandsCoveredByConfigOptions`; `rename`, `compact`, `reset`, and every discovered skill (`skill:<name>`) are unaffected and still show up as slash commands, since none of those have a config-option equivalent.
- New test locks the filter set to exactly `{model, thinking}` so it can't silently drift from `buildConfigOptions`' actual IDs; the existing `available_commands_update` regression test now also asserts `model`/`thinking` are absent from the notification. Verified manually against the compiled binary. Full suite + `-race` green.

## [0.73.97] - 2026-08-01

### Fix — `harness acp`: slash commands were silently dropped by Zed (wire ordering bug)
- **Root cause, confirmed against a known, reported Zed bug** ([zed-industries/zed#60199](https://github.com/zed-industries/zed/issues/60199)) — Zed reads JSON-RPC as a sequential line stream and only learns a session's `sessionId` once it has fully processed that session's `session/new`/`session/load` **response**. Any `session/update` notification for that `sessionId` arriving on the wire *before* the response line does gets silently dropped with an internal "notification for unknown session" warning — Zed never surfaces this to the user, it just quietly has no slash commands. `registerSession()` in `internal/transport/acp/methods.go` sent `available_commands_update` from *inside* the handler, before it had even returned its result to be written to stdout — so on the wire, the notification line always preceded the response line, and Zed discarded it every single time.
- **Fix** (`internal/transport/acp/{methods,acp}.go`) — split the notification out of `registerSession` into its own `notifyAvailableCommands`, now called from the dispatch loop in `acp.go` strictly *after* `respond()` has already written the `session/new`/`session/load`/`session/resume` response. `configOptions` was never affected by this bug — it travels inside the same JSON object as the response itself, so there's no ordering hazard for it.
- Updated regression test now asserts the full ordering explicitly: zero notifications arrive before the response, and `available_commands_update` follows it. Verified manually against the compiled binary, reading the raw stdout lines in order — response first, notification second, byte-for-byte confirmed. Full suite + `-race` green.

## [0.73.96] - 2026-08-01

### Feat — `harness acp`: advertise and implement `session/resume`, `session/close`, `session/delete`, `session/list`
- **Root cause of the question this answers**: `agentCapabilities.sessionCapabilities` was coming back as an empty `{}` — technically spec-compliant (it means "no optional session methods supported"), but understating what Harness can actually do. `client.Client` already exposes `ResumeSession`/`CloseSession`/`DeleteSession`/`ListSessions` — every other transport (TUI, Telegram, Slack) already uses them — so there was no reason ACP couldn't too.
- **`internal/transport/acp/{protocol,methods,acp}.go`** — implemented and wired all four: `session/resume` (reconnects to an existing session WITHOUT replaying history — the lighter sibling of `session/load`, which does replay), `session/close` (cancels in-flight work and frees resources, but leaves the session intact for a future load/resume — also drops it from this transport's own live-session bookkeeping), `session/delete` (permanently removes it from `session/list`; deleting an already-deleted or nonexistent session succeeds silently, per spec), `session/list` (returns every session Harness knows about, optionally filtered by `cwd`, as ACP's `SessionInfo[]` — `sessionId`, `cwd`, `title` (Harness's session name, e.g. "Acp 2026-08-01 18:30"), `updatedAt`). Cursor-based pagination is accepted on input but not required on output — Harness's per-cwd session counts are small enough that a single page is simpler and still spec-compliant (an absent `nextCursor` means "no more results").
- `agentCapabilities.sessionCapabilities` now advertises all four (`{"resume":{},"close":{},"delete":{},"list":{}}`) instead of `{}`.
- 6 new tests, including full round trips against a real connected provider (create → list → close → resume → delete → confirm it's gone from list) — verified manually against the compiled binary as well. Full suite + `-race` green.

## [0.73.95] - 2026-08-01

### Fix — `harness acp`: `agentInfo` was missing its `version` (and `title`)
- **`internal/transport/acp/methods.go`** — the `initialize` response's `agentInfo` only ever sent `{"name": "harness"}`. Per spec, `Implementation` (used for both `clientInfo` and `agentInfo`) carries `name`/`title`/`version`, and Agents **SHOULD** provide all three so a client like Zed can display or log exactly which harness build it's talking to. Now sends `{"name": "harness", "title": "Harness", "version": "<the running build's version>"}`, using the same `internal/version.Version` every other part of harness reports (injected via the Makefile's ldflags, `"dev"` for a plain `go build`).

## [0.73.94] - 2026-08-01

### Fix — `harness acp`: config options never rendered in Zed; slash commands and model/thinking selection silently broken
- **Root cause #1** (`internal/transport/acp/protocol.go`) — `sessionConfigOption` serialized its selected-value field as `"value"`. ACP's actual wire field for a `ConfigOption`'s current selection is **`currentValue`** (confirmed against the spec's own JSON examples) — `"value"` is reserved for `ConfigOptionValue.value`, one entry inside `options[]`. A client that can't find `currentValue` on an option silently declines to render a selector for it rather than erroring, which is exactly why Zed showed neither the model nor thinking dropdown despite the response looking otherwise correct. Fixed the JSON tag; also corrected the semantic `category` values to match the spec's own vocabulary — `model` for the model selector (`model_config` is reserved for model-related *parameters*, not the selector itself) and `thought_level` for the thinking selector (there is no dedicated "thinking" category — this is it).
- **Root cause #2** (`internal/transport/acp/methods.go`) — `buildAvailableCommands()` existed, fully implemented, and was **never called anywhere**. `session/new`/`session/load` now send a `session/update` notification carrying `available_commands_update` — with every session command (`rename`, `thinking`, `model`, `compact`, `reset`) plus every discovered skill as its own `skill:<name>` slash command — right after registering the session and before responding, per spec ordering.
- **Root cause #3** (`internal/transport/acp/{protocol,methods,acp}.go`) — `session/set_config_option`, the method a client calls when the user picks a new value in a config option's dropdown, was never implemented at all — it fell through to the dispatch loop's generic "method not found" error. Implemented: validates the `configId` (`model`/`thinking`, the only two this transport advertises), maps it to the session command it actually needs (`thinking`'s param is `level`, not `thinking` — matched to `internal/server/server.go`'s `handleExecCommand`), applies it via the existing `ExecCommand`, and returns the complete, current `configOptions` state, per spec (not just the one that changed).
- 8 new/updated tests covering all three fixes, including two full end-to-end round trips against a real connected provider (`session/new` → `available_commands_update` notification observed → `session/set_config_option` → updated `configOptions` observed in the response) — verified manually against the compiled binary as well, confirming Zed's exact `session/new` request now yields a spec-correct response.

## [0.73.93] - 2026-08-01

### Fix — `harness acp` sessions now get a named default, like Telegram/Slack
- **`internal/transport/acp/methods.go`** — `session/new` was creating sessions with an empty name, falling back to the server's generic "New Session <date>" — indistinguishable from a TUI session in `harness sessions`/the TUI's session list. Added `acpSessionName()` (mirrors `telegramSessionName`/`slackSessionName`), so ACP sessions now default to `"Acp 2026-08-01 18:30"` — immediately identifiable as having come from an ACP client (Zed) at a glance.

## [0.73.92] - 2026-08-01

### Fix — `harness acp` never exited on Ctrl+C; a related pre-existing data race in `internal/server`
- **Root cause** (`internal/transport/acp/acp.go`) — the JSON-RPC dispatch loop only checked whether its context had been cancelled *before* each blocking read from stdin. Once an ACP client (or a user running `harness acp` directly from a terminal) stopped sending input, the loop sat blocked inside that read forever — a plain `io.Reader`'s `Read()` call has no way to be interrupted by a `context.Context`. Ctrl+C (SIGINT) cancels the context via `signalContext()`, but the loop never got a chance to notice: the process would simply never exit.
- **Fix** — stdin reads now happen in their own goroutine feeding a channel; the main dispatch loop `select`s between that channel and `ctx.Done()`, so a cancelled context is noticed immediately even mid-read. The reader goroutine is left blocked forever in the rare case it's still waiting when `Run` returns — harmless, since the process exits right behind it. Ctrl+C/SIGTERM is treated as a clean shutdown (nil error, exit code 0), matching `harness serve`'s existing behavior, not a failure.
- **Fixing this exposed a pre-existing data race** in `internal/server/server.go`: `Serve()` writes `s.addr`/`s.instanceName`/`s.httpSrv` without holding `s.mu`, and `Close()`/two HTTP handlers (`handleServerInfo`, `handleOpenAPISpec`) read them the same way. The race always existed for every transport (`serve`, `telegram`, `slack`), but making ACP's shutdown near-instantaneous made the read-during-write window far more likely to trigger — `go test -race` on the ACP package caught it immediately. Fixed by guarding every read and write of those three fields with the existing `s.mu` (previously only used for the `sessions` map).
- Added a regression test (`TestRunReturnsOnContextCancelWhileBlockedOnStdin`) that reproduces the exact hang (a context cancelled while genuinely blocked on an unclosed, unwritten stdin pipe) and asserts `Run` returns promptly — verified it fails against the old code and passes against the fix. Full suite, including `-race`, green.

## [0.73.91] - 2026-08-01

### Feat — new transport: `harness acp` (Agent Client Protocol, for Zed and other ACP editors)
- **New command `harness acp`** (`internal/transport/acp/`) — runs Harness as an [Agent Client Protocol](https://agentclientprotocol.com) agent, the standard that lets code editors (Zed being the flagship implementer) talk to any compliant coding agent as a sub-process over JSON-RPC 2.0, newline-delimited, on stdio. Architecturally this is a pure protocol bridge, following the exact same shape as every other interactive transport (`internal/transport/telegram`, `.../slack`): it starts the same in-process HTTP/SSE server and talks to it through the same `client.Client` every transport shares — `agent/`, `agent/tools/`, and `client/` are completely untouched.
- **Session lifecycle**: `initialize` (advertises `protocolVersion: 1`, `promptCapabilities: {image, embeddedContext}`, `loadSession: true`), `session/new` (creates a real Harness session, resolving the default model the same way the CLI's `-p` and Telegram do — persisted active model if still available, else the first connected provider's first model), `session/load` (resumes an existing session and **replays its full message history** as a `session/update` sequence before responding — user/assistant turns and completed tool calls all reconstructed faithfully), `session/prompt` (submits text + optional images, streams the turn back as `session/update` notifications, resolves with the right `stopReason`), `session/cancel`.
- **Live event → protocol translation** (`events.go`): text/thinking deltas become `agent_message_chunk`/`agent_thought_chunk`; tool calls stream through the full `pending → in_progress → completed/failed` lifecycle with a `ToolKind` inferred by tool name (`Bash`→execute, `Read`→read, `Write`/`Edit`→edit, `Fetch`→fetch, everything else→other); token usage becomes `usage_update` with cost.
- **Native diffs for `Edit`, built entirely in the transport** (`diff.go`) — when the `Edit` (or `Write`) tool runs, the transport reads the target file before and after (purely observational, no interception) and emits a structured `{oldText, newText}` diff block instead of plain text, so Zed renders a real diff view. The `Edit` tool itself is completely unmodified.
- **Session config options & slash commands** (`commands.go`) — announces a `model` selector (category `model_config`, populated from every connected provider's models) and a `thinking` selector as native Zed UI controls, plus every session command Harness already exposes (`rename`, `thinking`, `model`, `compact`, `reset`, and every discovered skill as its own `skill:<name>` slash command) via `available_commands_update`.
- **Deliberately out of scope** (see the design doc, `docs/plans/2026-08-01-acp-transport-design.md`): no permission gating (`session/request_permission` is never called — Harness stays "trusted", tools execute directly like every other transport), no filesystem delegation to the client (`Read`/`Write`/`Edit` keep touching disk directly, never `fs/read_text_file`/`fs/write_text_file`), no terminal delegation (`Bash` keeps its own subprocess, never `terminal/create`), and Zed's own `mcpServers` config in `session/new` is ignored — the session only uses Harness's own configured MCP servers, exactly like every other transport.
- Verified end-to-end against a real connected provider: `initialize` → `session/new` → `session/prompt` (streamed text + tokens) → `session/load` in a fresh process (full history replay) → an `Edit` tool call producing a correct native diff and the file actually changed on disk. 43 unit/integration tests added, `-race` clean.

## [0.73.90] - 2026-08-01

### Feat — Kong CLI: enum flags now list their accepted values in `--help`
- **`internal/cli/app.go`** — a custom `kong.ValueFormatter` (`enumValuesInHelp`) appends an enum flag's actual accepted values to its help text, e.g. `--thinking=""  Thinking level (off|low|medium|high|xhigh)` and `--output="text"  Output mode (with --prompt/-p) (text|json|json-stream)`. Previously the user only discovered the valid values after typing an invalid one and reading the validation error — `--thinking`/`--output` (root, `telegram`, `slack`) and `settings set <key>` all benefit. The empty-string sentinel some `--thinking` flags accept internally (meaning "use the settings default" — see the enum gotcha documented in `kong.go`) is filtered out of the listed values, since it isn't something the user would ever type.

## [0.73.89] - 2026-08-01

### Fix — Kong CLI: parent commands' own flags had disappeared from their `--help`
- **Regression from the previous fix** (0.73.88, the hidden-default-child pattern for `telegram`/`slack`/`settings`): moving each command's flags off the parent struct and onto its hidden default child (`telegramRunCmd`, `slackRunCmd`) — necessary to stop the parent's action from double-firing after a subcommand — had an unintended side effect: those flags stopped showing up in the parent's own `--help`. `harness slack --help` no longer listed `--workspace`/`--xoxc`/`--xoxd`/`--model`/`--thinking`/`--scheduler` at all, even though they're valid and documented flags for `harness slack` itself — same for `harness telegram --help` and, less visibly, the root `harness --help` (TUI's flags).
- **Fix** (`internal/cli/app.go`) — a custom `kong.HelpPrinter` (`helpWithHiddenDefaultFlags`) wraps `kong.DefaultHelpPrinter`: when the node being displayed by `--help` has a hidden `default:"withargs"` child (the pattern above), that child's flags are temporarily merged onto the displayed node before printing, then restored. This only patches the exact node under `--help` — never its ancestors or its other (real) subcommands — so `harness slack --help` now shows the transport's flags again, while `harness slack login --help` still correctly shows only its own `--status` flag, with no leakage either direction.

## [0.73.88] - 2026-08-01

### Fix — Kong CLI: parent commands with subcommands were double-running and leaking flags
- **Root cause**: Kong's `Context.Run()` calls `Run()` on every node in the selected command chain — child *and* parent, not just the leaf — a deliberate feature for shared setup logic. Three commands (`telegram`, `slack`, `settings`) had both their own flags/`Run()` AND real subcommands (`telegram pair/unpair/list`, `slack login/admin`, `settings set`), so selecting a subcommand ran the subcommand's `Run()` and then the parent's too. `harness slack login --status` logged in and then launched the full Slack bot (RTM connect and all); `harness telegram list` printed the paired-chat list and then failed trying to start the bot; `settings set thinking high` set the value and then re-printed all settings. On top of that, every flag declared on the parent struct (`--workspace`, `--xoxc`, `--token`, etc.) leaked into each subcommand's own `--help` output, even though they meant nothing there.
- **Fix** (`internal/cli/kong.go`, `kong_run*.go`) — the same hidden-default-child pattern already used for the root `TUI` command, now applied one level down: `telegramCmd`/`slackCmd`/`settingsCmd` are now pure command groups (no flags, no `Run()`); the actual bot-launching (or settings-printing) logic moved into a new hidden `default:"withargs"` child — `telegramRunCmd`, `slackRunCmd`, `settingsShowCmd` — a sibling of the real subcommands instead of living on the parent. `harness telegram`, `harness slack`, and `harness settings` (bare) behave exactly as before; `telegram pair/unpair/list`, `slack login/admin`, and `settings set` now run in isolation, with clean `--help` output scoped to their own flags only.
- **General rule going forward** (documented in `kong.go` and `AGENTS.md`): a Kong command struct must never combine its own `Run()` with `cmd:""` children — if it needs both an action and real subcommands, the action goes in a hidden default child, never directly on the parent.

## [0.73.87] - 2026-08-01

### Refactor — CLI migrated from a hand-rolled `flag.FlagSet` dispatch to `alecthomas/kong`
- **Why**: `internal/cli/` had grown to a ~2000-line manual `flag.NewFlagSet` per command + a giant `switch` in `app.go` + a hand-maintained `help.go` that routinely drifted from the actual flags, plus a `reorderFlags()` hack to work around Go's `flag` package stopping at the first positional argument. None of it validated itself — a typo in a flag name or a missing case in the help text failed silently at runtime, if at all.
- **What changed**: `internal/cli/kong.go` now declares the ENTIRE command grammar (all 15 top-level commands and every nested subcommand — `telegram pair/unpair/list`, `slack login`, `slack admin add/list/remove`, `mcp list/add/rm/enable/disable`, `settings set`) as Kong struct tags — flags, positional args, enums, defaults, and help text all live in one place and are validated at parser-construction time (Kong panics on a bad tag — enum/default conflicts, mixing positional args with subcommands — instead of failing at some later runtime). Business logic is untouched: every command's `Run() error` method (in `kong_run.go` and its siblings) is a thin adapter delegating to the same `Run*()` functions that already existed in `commands.go`/`settings.go`/`memory.go`/`schedule.go`/`cli.go`.
- **UX preserved, with one deliberate exception**: `--env`/`--header` on `mcp add` keep their exact repeatable-flag behavior (`--env A=B --env C=D` accumulates, verified against Kong's `[]string` field type — no change to `map[string]string` mapsep syntax). The one intentional change: `harness slack admin <userID>` (bare positional = add) becomes `harness slack admin add <userID>` — Kong forbids mixing a positional argument with subcommand siblings in the same struct, and `add`/`list`/`remove` are genuine siblings.
- **New for free**: granular `--help` at every level of the tree (e.g. `harness serve --help`, `harness mcp add --help`, `harness slack admin --help` now show exactly and only that command's own flags), and every flag/enum/int argument gets Kong's built-in validation and error messages (e.g. `telegram pair abc` now reports `<chat-id>: expected a valid 64 bit int but got "abc"` instead of a raw `strconv` error).
- **Removed**: `cmd_manage.go`, `cmd_mcp.go`, `cmd_memo.go`, `cmd_serve.go`, `cmd_slack.go`, `cmd_telegram.go`, `cmd_tui.go`, `help.go` — the manual dispatch, flag sets, and hardcoded help text these files held are now generated/enforced by Kong. `app.go`'s `Main()` is now three lines: build the Kong parser, parse, run the matched command.
- New direct dependency: `github.com/alecthomas/kong`.

## [0.73.86] - 2026-07-31

### Fix — cross-process file lock for credentials.json (single-use refresh token race)
- **Root cause** (`internal/providers/claude_oauth.go`) — `getValidToken`'s token-refresh cycle (re-check expiry → call the provider → persist) was only protected by an intra-process `sync.Mutex`. With multiple harness processes commonly running at once (TUI + Telegram + Slack + `serve`), two processes whose OAuth token expired near the same moment could both read the same `refresh_token` before either wrote the new one — and Anthropic refresh tokens are single-use, so only one redemption succeeds; the other permanently fails with `invalid_grant`. This is the same class of bug fixed for `instances.json` in v0.73.75, but with a much wider collision window: `credentials.json` is read and potentially rewritten on every LLM call whose token is close to expiring, not once at process start/stop.
- **Fix** — new cross-process file lock in `internal/config/credentials.go` (`credentials.json.lock`, `O_CREATE|O_EXCL` — atomic across processes on every OS; a lock older than 5s is treated as abandoned and reclaimed automatically, same pattern as the instance registry lock). `CredentialsManager.WithLock(fn)` lets a caller hold the lock across several manager calls as one atomic unit — used by `getValidToken`'s refresh path to serialize the full re-check→refresh→persist cycle, not just the final write.
- **Read/write split by actual risk, not blanket locking**: plain reads (`Credential`, `APIKey`, `OAuth`) do NOT take the file lock — `os.WriteFile` is atomic at the OS level for a file this size, so a reader never sees a torn write, and adding cross-process locking there would add latency to every single LLM call for no correctness benefit. `SetCredential`/`DeleteCredential` (used by `/connect`, `/disconnect`) now take the lock AND re-read from disk before mutating, so they never silently discard a concurrent writer's update. `getValidToken`'s fast path (token still valid) also never takes the lock — it only acquires it once a refresh is actually needed.

## [0.73.85] - 2026-07-31

### Feat — `SlackMessages` tool: channel history visibility + JSON format for all data-returning Slack tools
- **`SlackMessages`** (`internal/transport/slack/bot.go`, `tools.go`) — new Slack tool letting the agent read recent messages posted in a channel, as JSON. Previously the agent was blind to group conversation — it only saw messages sent directly to it (a DM or an @mention) even in busy multi-person channels. Calls `conversations.history`, reverses Slack's native newest-first order into a natural chronological transcript, and filters only genuinely noisy subtypes (`message_changed`, `message_deleted`, `ekm_access_denied`) — everything else (`channel_join`, `channel_leave`, `channel_topic`, `bot_message`, `file_share`, etc.) is preserved, since those are real events in the channel's history the agent may need. Each message includes the sender's user ID, text, timestamp, subtype, and any attached files with their URLs (`SlackFile.URLPrivate`) — the agent decides whether to fetch an attachment, the tool never resolves that on its own.
- **`resolveChannelID()`** — the `#name` → channel ID resolution logic in `SlackPost` was extracted into a shared helper, now used by both `SlackPost` and `SlackMessages`.
- **JSON format for data-returning Slack tools**: `SlackListChannels` and `SlackListUsers` — previously hand-formatted plain text (`fmt.Fprintf` bullet lists) — now return `json.MarshalIndent` output, consistent with every other data-returning tool in the codebase (`ColleagueList`, `ScheduleList`, `MemoSearch`, and the new `SlackMessages`). `SlackPost` is unchanged — it returns an action-confirmation string ("Message posted to..."), not data to reason over, so plain text remains correct there. `SlackListUsers`'s description now tells the model to prefer `display_name`, falling back to `real_name`, then the handle — logic the tool used to compute internally and hide.
- **Directive updated** (`directive.go`) — documents `SlackMessages` alongside the other proactive-messaging tools, explaining when to use it (catching up on group discussion) and that it's not meaningful for DMs.

## [0.73.84] - 2026-07-31

### Fix — ColleagueAsk: background mode was still bounded by timeout; sessions leaked on disk
- **Background mode ignored its own purpose** (`agent/tools/colleague.go`) — `timeout` (default 60s, or whatever the model passed) was threaded all the way into `askColleagueBackground`'s goroutine, so a slow colleague still got cut off with `context deadline exceeded` even though nothing was blocked waiting on it. This defeated the reason `background` exists — tolerating a genuinely slow task — and the model was observed compensating by passing large `timeout` values (e.g. `background:true, timeout:300`) to work around it. Fixed: `askColleagueBackground` no longer takes a `timeout` param; it always calls `askColleague` with `0` (no limit). `timeout` is now only computed/used on the foreground (blocking) path, where it protects the CALLER from waiting indefinitely — a concern that doesn't exist in background mode. Description and schema updated to tell the model explicitly that `background` has no timeout, so it stops passing one.
- **Delegation sessions leaked on the colleague's disk** — `askColleague` called `CloseSession` (deactivates, does NOT delete `.jsonl`/`.meta.json`) but never `DeleteSession`. Every `ColleagueAsk` call — successful or not — left a permanent, never-revisited session file on the colleague's `~/.harness/agent/sessions/`. Fixed: the `defer` now runs both `CloseSession` and `DeleteSession`, unconditionally (including on timeout/error paths, since `defer` always fires) — delegation sessions are purely ephemeral and nothing in them is worth keeping once the answer is back.

## [0.73.83] - 2026-07-31

### Fix — TUI: ColleagueAsk's colleague name was hard to read next to the prompt
- **`internal/transport/tui/toolfmt.go`** — when a tool has both a bare primary and secondary param (currently only `ColleagueAsk`: `colleague` + `prompt`), the primary now renders `Muted` (brighter gray, same weight as the deferred `(N images)` summaries) instead of `Dimmed`. The short colleague name was visually disappearing next to the much longer prompt when both used the same faint tone. Single-primary tools (`Read`, `Bash`, `Fetch`, etc.) are unaffected — they keep the original `Dimmed` styling.

## [0.73.82] - 2026-07-31

### Feat — TUI: dedicated icon + dual-primary rendering for Colleague tools
- **Icon** (`internal/transport/tui/output.go`) — `ColleagueList`/`ColleagueAsk` now render with `⇄` (back-and-forth: request/response with another agent) instead of falling through to the generic `◈` MCP/external-tool icon.
- **Dual-primary param rendering** (`internal/transport/tui/toolfmt.go`) — new `secondaryPrimaryParam` map lets a tool show TWO params bare (no `key=`), in order, right after the tool name. `ColleagueAsk` uses `colleague` (primary) + `prompt` (secondary, shown in full like `Subagent`'s prompt) — showing only one would lose either WHO is being asked or WHAT is being asked, both needed at a glance. `images` is summarized to `(N images)` and deferred to the end, same treatment as `Fetch`'s `files`.
- **New test cases** in `toolfmt_test.go` covering both the bare dual-primary case and the images/timeout deferred case.

## [0.73.81] - 2026-07-31

### Refactor — Colleague Pattern: separated system-prompt reasoning from tool mechanics
- **Vocabulary fix**: removed "harness instance" wording from tool descriptions and the system prompt — the model has no reason to know the name of the software running it. Replaced with "colleague instance" / "colleague" throughout.
- **`ColleagueList` / `ColleagueAsk` descriptions** rewritten to be purely operational (what the tool does, what it returns/accepts) — all delegation *reasoning* (when to prefer a colleague, how to weigh environment vs. working directory) moved to the `## Colleagues` system-prompt section, where it belongs once instead of being duplicated across tool descriptions.
- **`environment` field replaces `transport`** in `ColleagueList`'s JSON output (the on-disk field name in `instances.json` is unchanged — this is presentation-only). New `environmentLabel()` in `agent/tools/colleague.go` maps chat-platform transports (`slack`, `telegram`) through as-is, and collapses everything else (`tui`, `server`, `cli`, and any future transport not yet a chat platform) to `"generic"` — from the model's perspective those all mean "an agent with tools and a working directory, no special messaging capability." Adding a future chat platform (e.g. Discord) only means adding one entry to `chatPlatformEnvironments`, no system-prompt change needed.
- **System prompt fully abstracted**: no longer enumerates environment types or assumes what environment the calling model itself runs in (a prior draft incorrectly said "you're in a TUI", which the agent has no way to know). Explains `environment` as "extra capabilities that colleague has, which you may not" and lets the model generalize.
- **Same-project delegation corrected**: `ColleagueAsk` creates an ephemeral session per call (`InMemoryStore`, closed immediately after — same pattern as `Subagent`), so the system prompt no longer implies a colleague "already has relevant context" from prior exchanges. It now frames same-project delegation as "an independent agent that can co-work with you on it" for a substantial, self-contained task, while being explicit that each delegation is a fresh session, not shared memory of past exchanges.

## [0.73.80] - 2026-07-31

### Feat — Colleague Pattern: ColleagueList/ColleagueAsk tools + instance registry fixes
- **`ColleagueList` / `ColleagueAsk`** (`agent/tools/colleague.go`) — new built-in tools letting the agent discover and delegate to OTHER running harness server instances on the same machine via the shared `~/.harness/instances.json` registry. `ColleagueList` reads the registry directly (own minimal `instanceEntry` parser, no dependency on `internal/server`) and excludes the caller itself by PID. `ColleagueAsk` resolves a colleague's URL by name, validates any attached image paths (same extension check as `Read`), and delegates via `client.Ask`/`client.AskWithImages` — the colleague answers with **its own** default model, MCPs, and project context, never overridden by the caller. Supports `timeout` (seconds, default 60) and `background` (returns immediately with a result-file path instead of blocking).
- **`AgentOptions.EnableColleagues`** / **`harness.WithColleagues()`** — new opt-in flag (default off), same pattern as `EnableScheduler`/`WithScheduler`. Wired to `true` in `newInteractiveAgent` (TUI, Telegram, Slack, `harness serve`); one-shot CLI commands and subagents never enable it. New `## Colleagues` section added to the system prompt when enabled.
- **Architecture note**: `agent/colleague` was considered as a new public package to share instance-registry code between `internal/server` and `agent/tools`, then reverted — the registry has a single writer (`internal/server`) and the JSON file (path + shape) is the real interop contract, not a Go type. `agent/tools/colleague.go` reads the same file with its own ~10-line parser instead of promoting the whole registry implementation (name generator, file-locking, liveness probing) to the public SDK surface.
- **Fix — instance name collision (root cause, not just a race)**: `internal/server/instances.go`'s name generator seeded `math/rand` manually with `time.Now().UnixNano()`. Two `harness serve` processes launched close together landed on the exact same seed (OS clock resolution is coarser than "nanosecond" in practice), which deterministically produces the **identical** pseudo-random sequence — not a rare collision, a mathematical certainty (reproduced and confirmed: same seed → same name, 100% of the time). Fixed by switching to `math/rand/v2`'s auto-seeded package-level functions (`rand.IntN`), which draw from real per-process OS entropy — two processes starting in the same instant now get independent streams.
- **Fix — cross-process registration race**: even with unique names, two processes could still read the registry, generate, and write concurrently, with the second write silently clobbering the first. Added a file-based advisory lock (`instances.json.lock`, `O_CREATE|O_EXCL` — atomic across processes on every OS) guarding the full read-generate-write cycle in `RegisterInstance`/`UnregisterInstance`. Self-healing: a lock file older than 5s is treated as abandoned (crashed holder) and reclaimed automatically. Verified with 5 rounds × 5 simultaneous `harness serve` launches — 25/25 unique names, zero collisions, clean lock file cleanup on shutdown every time.

## [0.73.79] - 2026-07-31

### Fix — `harness serve` left a stale entry in instances.json on Ctrl+C/SIGTERM
- **Root cause** (`internal/cli/cmd_serve.go`) — `Server.Close()` calls `httpSrv.Shutdown()` as step 3 of 4, which makes the blocking `srv.Serve(listener)` in the main goroutine return almost immediately. Step 4 (`UnregisterInstance`, removing the entry from `~/.harness/instances.json`) runs afterward, in the *other* goroutine that's executing `Close()`. `cmdServe` returned as soon as `Serve()` unblocked, without waiting for that goroutine to actually finish — `main()`'s `os.Exit()` killed the process mid-cleanup, leaving the instance registry entry orphaned every time (100% reproducible, not a race).
- **Fix** — `cmdServe` now waits on a `closeDone <-chan error` channel: when `Serve()` returns via the expected shutdown path (`ctx.Err() != nil`, i.e. Ctrl+C/SIGTERM), it blocks on `<-closeDone` until the `Close()` goroutine has fully completed, including instance unregistration, before returning. Verified with repeated SIGINT/SIGTERM cycles — `instances.json` now reliably ends up `{}` after shutdown.

## [0.73.78] - 2026-07-31

### Feat — `client` promoted to a public SDK package + `Ask`/`AskWithImages` with per-request timeout
- **`internal/client` → `client`** — the typed HTTP/SSE client is now a public package (`github.com/gurcuff91/harness/client`), not `internal/`. Any Go program can `import "github.com/gurcuff91/harness/client"` and drive a running `harness serve` (or any transport's in-process server) without embedding the agent directly — same session/prompt/event model, over the wire. 14 call sites across `internal/cli`, `internal/transport/{tui,telegram,slack}` updated to the new import path; zero behavior change.
- **`types.ProviderConfig` / `types.MCPServer`** — moved from `internal/config` to `types/` (both are pure stdlib shapes — no reason for them to live in an internal package). `internal/config` now aliases them (`type ProviderConfig = types.ProviderConfig`), staying the domain owner (validation, persistence) while the shape itself is public. This was the blocker to moving `client` out of `internal/` — its `types.go` no longer needs to reach into `internal/config`.
- **`harness.Client` / `harness.NewClient`** — re-exported in the root SDK facade (`harness.go`), alongside `Agent`/`Session`. Package doc updated to mention the client as an alternative embedding path.
- **`client.New(addr)`** — now accepts either a bare address (`"127.0.0.1:8080"`, assumed `http://`) or a full URL with scheme (`"http://…"`, `"https://…"`) — the latter is what `InstanceInfo.URL` (colleague registry, upcoming) and any user-supplied `--addr` already carry.
- **`client.Ask(sessionID, text, timeout)`** and new **`client.AskWithImages(sessionID, text, images, timeout)`** — both take a `time.Duration` timeout (0 = no limit) scoped to that single request via `context.WithTimeout`, not the client's shared `http.Client.Timeout` — which would incorrectly also cut off the unrelated, long-lived SSE stream. `client.do()`/`decode[T]` gained context-aware siblings (`doCtx`/`decodeCtx`) as the real transport primitives; the ~40 existing call sites are unaffected (they use the `context.Background()` wrapper).
- **Server-side `handleAsk`** already supported images end-to-end (validated in this pass, no code change needed) — decodes `req.Images`, checks vision support, passes `agent.WithImages(...)` to `PromptAndWait`.

## [0.73.77] - 2026-07-31

### Fix — claude-oauth: disk is unconditionally the source of truth (account-switch bug)
- **Root cause** (`internal/providers/claude_oauth.go`) — `getValidToken` and `loadCredentialsFromSources` compared `ExpiresAt` between disk and in-memory credentials to decide which was "newer", adopting disk only when its `ExpiresAt` was numerically larger. That comparison is meaningless across **different accounts**: two unrelated OAuth sessions have unrelated expiry timestamps, so a larger `ExpiresAt` does not mean "written more recently". Real-world failure: switching to a new Claude account via `claude auth login` + `/connect` in one harness instance left an older, already-running instance permanently stuck on the old account — because the old account's in-memory `ExpiresAt` happened to be numerically larger than the new account's. Every request kept using (and trying to refresh) the invalid old account, surfacing as `⚠ provider "claude-oauth" is not active (missing credentials)` even though `credentials.json` held perfectly valid new credentials.
- **Fix** — disk (`~/.harness/credentials.json`) is now **always** the source of truth, re-read unconditionally on every `getValidToken()` call (which runs on every request to Anthropic: `ResolveCredentials`, `FetchModels`, `CompleteStream`). The only exception is a new explicit `tokens.validating` flag, set for the duration of `Connect()`'s validation window (creds set in-memory, not yet persisted) — the sole legitimate case where memory must temporarily win over disk. Cleared immediately after `persistOAuthCreds` succeeds or validation fails.
- **`tokenManager.syncFromDisk()`** — new method: unconditional overwrite of `tm.creds` from disk, no expiry comparison. Replaces the old `ExpiresAt >` heuristic in both `getValidToken` (initial check + per-retry) and `loadCredentialsFromSources`.
- **Result**: switching Claude accounts in any harness instance is now picked up by every other running instance on its very next request — no restart, no manual `/connect` needed elsewhere.

## [0.73.76] - 2026-07-31

### Feat — `POST /api/sessions/{id}/ask` (synchronous prompt) + CLI `-p` text mode simplified
- **`POST /api/sessions/{id}/ask`** (`internal/server/server.go`) — new endpoint, the synchronous counterpart to `/prompt` (fire-and-forget). Blocks via `Session.PromptAndWait` until the agent's turn completes and returns the final assistant text directly: `200 {"text": "..."}`. Errors use the same standard shape as every other endpoint: `writeErr` → `{"error": {"message": ..., "details": {...}}}` (400/404/500), not a bespoke shape.
- **`client.Ask(sessionID, text)`** — new SDK method. Uses the standard `decode[T]` helper, so 4xx/5xx responses surface as `*client.Error` exactly like every other client method — no special-casing.
- **OpenAPI spec** — added `/api/sessions/{id}/ask`, `AskResponse` schema (`{text}`), and `ErrorResponse` schema (`{error: {message, details}}`) reused across error responses.
- **CLI `-p <prompt>` simplified for text mode** (`internal/cli/cli.go`) — `text` output mode (the default) now calls `Ask` directly: one blocking HTTP request, no SSE connection, no event accumulator. `json`/`json-stream` modes are unchanged — they still need individual events (tool calls, thinking, tokens, timing) via `SendPrompt` + `StreamEvents`. `renderEvents` no longer handles the `text` case.

## [0.73.75] - 2026-07-31

### Fix — Instance registry: preserve existing entries, HTTP-based liveness check
- **`loadInstances()`** (`internal/server/instances.go`) — no longer prunes entries by PID. The previous implementation used `os.FindProcess` + `signal 0` to detect dead PIDs, which is unreliable on macOS and was deleting live instances. Now reads the file as-is — existing entries are preserved.
- **`generateInstanceName()`** — when a name collision occurs, checks liveness via HTTP (`GET /api/server` with 2s timeout) instead of PID. If the existing instance responds 200 → it's alive, try another name. If it doesn't respond → it's dead, reclaim the name and remove the stale entry. This is reliable across all platforms and doesn't depend on OS process semantics.
- **`ListInstances()`** — returns all entries as-is without health checking. Consumers can verify liveness by calling each instance's `/api/server` endpoint.
- **`instanceAlive()`** — new helper: quick HTTP probe to an instance's URL.
- **Server logging** — all `logx` calls in `server.go` now gated behind `s.verbose` (including the `register_instance` warning).

## [0.73.74] - 2026-07-31

### Feat — Instance registry: track all running server instances
- **`~/.harness/instances.json`** — new file tracking every running server instance with its version, transport, URL, CWD, PID, and start time. Keyed by a unique MK11-themed name (e.g. `jade-warrior`, `scorpion-spectre`).
- **Name generator** (`internal/server/instances.go`) — 37 MK11 characters × 37 MK11-flavored adjectives = 1369 possible combinations. Random character + random adjective joined with `-`. Retries up to 50 times on collision, then falls back to numeric suffix. `loadInstances` prunes entries whose PID is no longer alive — handles crashed processes that never called `Close`.
- **`Server.Serve()`** — registers the instance on startup (name logged at info level). **`Server.Close()`** — unregisters on graceful shutdown.
- **`GET /api/server`** — now includes `instance` field (the MK11 name).
- **`GET /api/instances`** — new endpoint listing all registered instances.
- **OpenAPI spec** — updated `ServerInfo` schema with `instance` field; added `/api/instances` endpoint and `InstanceInfo` schema.

## [0.73.73] - 2026-07-31

### Feat — Graceful `Server.Close()` cascade: sessions → agent → HTTP shutdown
- **`server.Server.Close()`** (`internal/server/server.go`) — new idempotent method (`sync.Once`) that performs a clean shutdown in cascade: (1) closes all active sessions (flush stores + unregister from agent), (2) closes the agent (MCP subprocesses, memory DB, scheduler engine, session store), (3) graceful HTTP `Shutdown(ctx)` with 3s deadline. `Serve()` now stores the `*http.Server` and `net.Listener` as struct fields so `Shutdown` can be called.
- **`Agent.Close()`** (`agent/agent.go`) — made idempotent with `sync.Once` + `closeErr` field. Previously calling `Close()` twice would double-close MCP and memory DB; now the first call does all work and subsequent calls return the same error. Essential because `Server.Close` closes the agent and some call sites still have `defer a.Close()` (harmless now).
- **TUI** (`internal/transport/tui/server.go`) — `internalServer.Close()` now delegates to `srv.Close()` instead of being a no-op. The `defer srv.Close()` in `Run()` executes the full cascade on exit.
- **Telegram** (`internal/transport/telegram/telegram.go`) — `srv` stored in `Transport` struct; `t.srv.Close()` called after `pollLoop` returns.
- **Slack** (`internal/transport/slack/slack.go`) — `srv` stored in `Transport` struct; `t.srv.Close()` called after `rtmLoop` returns.
- **CLI** (`internal/cli/server.go`, `cmd_serve.go`, `cmd_telegram.go`, `cmd_slack.go`) — `internalServer.Close()` delegates to `srv.Close()`. `cmd_serve` uses `defer srv.Close()` instead of `defer a.Close()`. `cmd_telegram`/`cmd_slack` removed redundant `defer a.Close()` — `Server.Close` handles it.
- **CLI `server.go`** (`startInternalServer`) — `Transport: "cli"` added to `ServerOptions` (was missing).

## [0.73.72] - 2026-07-31

### Feat — Server info: transport + url fields; OpenAPI spec cleanup
- **`ServerOptions.Transport`** (`internal/server/server.go`) — new field identifying the calling transport (`"tui"`, `"telegram"`, `"slack"`, `"server"`). Defaults to `"server"` when empty. Set at all 4 callsites.
- **`GET /api/server`** — response now includes `transport` and `url` fields. `serverInfo` changed from a static global var to a per-request struct that combines static fields (`name`, `version`, `cwd`, `pid`, `started_at`) with instance fields (`transport` from `ServerOptions`, `url` from `s.addr` resolved in `Serve()`). Example: `{"name":"harness","version":"v0.73.72","transport":"tui","url":"http://127.0.0.1:52341","cwd":"...","pid":92807,"started_at":"2026-07-31T00:22:16Z"}`.
- **OpenAPI spec** (`internal/server/server_docs.go`) — removed self-referential `/api/docs` and `/api/docs/openapi.json` endpoints from the spec. Updated `ServerInfo` schema with `transport` and `url` fields.

## [0.73.71] - 2026-07-29

### Fix — TUI: max-iterations warning uses amber (Warn) instead of dim
- **`internal/transport/tui/events.go`** — the `⚠ reached the N-iteration limit — summarizing progress` message now uses `ansi.Warn` (amber `#E8A838`) instead of `ansi.Dimmed`. A warning should look like a warning, not fade into the background.

## [0.73.70] - 2026-07-29

### Feat — Memory slugs listed in system prompt for proactive recall
- **`agent.buildSystemPrompt()`** (`agent/agent.go`) — the Memory section now lists up to 30 memory slugs so the model knows what memories it has and can proactively decide whether to search. Fetches with `Search(cwd, "", false, 0, 31)` (list mode, no content, limit 31): if 31 results come back, there are more than 30 and the prompt notes `"- many more — use MemoSearch to find them"`. When there are zero memories, shows `"No memories yet — use MemoWrite to save durable insights as you work."` — a gentle nudge to start using memory.
- No `(global)` labels — memories are memories regardless of scope. The model sees a flat slug list, just like the Available Skills section. Content is never included (slugs are the index; `MemoSearch` fetches full content on demand). Cost: ~100-150 tokens for 30 slugs — negligible vs the full system prompt.

## [0.73.69] - 2026-07-29

### Feat — Pre-warm pumps at transport startup (Slack + Telegram)
- **Problem** — when a transport started with `--scheduler`, pumps were created lazily on the first user message. If a scheduled prompt fired before the user wrote anything, the session was auto-resumed by the agent (v0.73.68) and the response was persisted to disk, but no pump existed to deliver it to the Slack channel or Telegram chat in real time.
- **`store.allSessions()`** (`internal/transport/slack/creds.go`, `internal/transport/telegram/chats.go`) — new method returning all `(channelID/chatID → sessionID)` mappings for the current working directory.
- **`Transport.prewarmPumps(ctx)`** (both transports) — iterates `allSessions()` at startup and calls `pumpFor` for each stored mapping. Every pump opens its SSE stream and starts draining immediately, so when the scheduler fires a prompt the output is delivered to the channel in real time. Errors are logged as warnings (never fatal) — a failed pre-warm falls back to lazy creation on first user message.
- **Slack** — `prewarmPumps` called after tool registration, before `rtmLoop`.
- **Telegram** — `prewarmPumps` called after `SetMyCommands`, before `pollLoop`.
- **TUI** — no changes. The TUI is mono-session (single `sessionID`, single SSE stream); it does not have multi-chat pumps and does not need pre-warming. The agent beneath may auto-resume sessions for the scheduler, but the TUI only listens to the one session the user has open.

## [0.73.68] - 2026-07-29

### Feat — Scheduler auto-resume: scheduled prompts never lost across restarts
- **`agent.fireScheduledPrompt()`** (`agent/agent.go`) — when a schedule fires and its owner session is not active (process restarted, session never opened), the agent now auto-resumes it from disk instead of dropping the prompt. The prompt runs, the response is persisted to the session's JSONL, and when the user reconnects via any transport they see the scheduled prompt and its output in history. If the session no longer exists on disk, the prompt is still dropped silently.
- **`agent.ResumeSession()` — idempotent** — if the session is already live in `activeSessions`, returns the existing handle without reloading from disk. Safe to call from multiple paths (transport reconnect, scheduler auto-resume, manual `/resume`) without race conditions or duplicate sessions.
- **`server.handleResumeSession()` — idempotent** — returns `200` with session details when the session is already active, instead of `409 Conflict`. Transports no longer fall through to `CreateSession` when the scheduler auto-resumed the session first, eliminating orphaned duplicate sessions.
- **OpenAPI spec** (`internal/server/server_docs.go`) — `POST /api/sessions/{id}/resume` updated: removed `409` response, description now notes "Resumed (or already active — idempotent)".

## [0.73.67] - 2026-07-29

### Feat — OpenAPI 3.0 spec + Scalar API docs UI (`/api/docs`)
- **`GET /api/docs`** — Scalar-powered interactive API reference UI. Single self-contained HTML page served from Go (no build step, no extra assets). Loads spec from `/api/docs/openapi.json` via CDN `@scalar/api-reference`. Open in browser while harness is running.
- **`GET /api/docs/openapi.json`** — Hand-written OpenAPI 3.0 spec covering all 31 endpoints: server, settings, providers, models, MCP, memory, schedules, sessions (CRUD, resume, fork, prompt, SSE events, commands, messages, stop, info, context). All schemas documented (`SessionMeta`, `SessionDetail`, `SessionStats`, `ContextBreakdown`, `Provider`, `Schedule`, `MemoryEntry`, etc.).
- **Dynamic version + server address** — spec `info.version` is injected at request time from `version.Version` (set by Makefile ldflags). `servers[0].url` is set to the actual listener address (`l.Addr().String()`) resolved in `Server.Serve()` — no hardcoded port. Both use `strings.ReplaceAll` on a template constant, zero external deps.
- **`Server.addr`** field added to store the resolved listen address after `Serve()` is called.
- **`internal/server/server_docs.go`** — new file holding `docsHTML`, `openAPISpecTemplate`, and `openAPISpecJSON(addr)`.

## [0.73.66] - 2026-07-29

### Fix — Anthropic parser: malformed tool-call JSON crashes store + duplicate error message
- **Malformed tool args (Anthropic)** (`internal/providers/llm/anthropic.go`) — Claude occasionally streams malformed JSON for tool call arguments (e.g. `{"path": "...", "offset": 1186, 1240}` — a bare value with no key). The Anthropic block parser had no `json.Valid()` guard, so `resp.Message` was built with an invalid `json.RawMessage`. When `store.AddMessage` then called `json.Marshal`, it failed with a marshal error, breaking the session. Fix: same pattern already applied to the OpenAI parser — invalid args are wrapped as `{"_raw": "..."}` so the store never fails. The tool fails cleanly at execution time with `Error parsing input`; the session and history are preserved.
- **Duplicate error message in TUI** (`agent/session.go`) — a `StreamError` SSE event was emitted as `EventError` inside the stream callback AND again when `runStream` returned the same error to `promptSync`. The TUI rendered the same error twice. Fix: removed the `emit` from the `StreamError` callback — `promptSync` is the single emit point via `errorEvent(err)`.

## [0.73.65] - 2026-07-29

### Fix — TUI: tool executing icon ⧖ → ▶
- **`internal/transport/tui/events.go`** — the "Executing…" placeholder shown while a tool is running changed from `⧖` (hourglass) to `▶` (play). Clearer "in-progress" semantics, terminal-safe, and contrasts cleanly with the `✔`/`✘` result icons that follow.

## [0.73.64] - 2026-07-29

### Fix + Feat — TUI: MCP tool display name format + icon update
- **MCP tool name formatting** (`internal/transport/tui/output.go`) — MCP tool names arrive internally as `mcp__<namespace>__<ToolName>` (wire format, unchanged). In the TUI they now render as `namespace.ToolName` (e.g. `mcp__ext__WebSearch` → `ext.WebSearch`, `mcp__x__MemoWrite` → `x.MemoWrite`). Built-in tools (`Bash`, `Read`, `Write`, etc.) are unaffected. Pure rendering concern — no internal names, events, or agent contracts modified.
- **`mcpDisplayName(name)`** — new helper in `output.go`. Strips `mcp__` prefix and replaces the second `__` separator with `.`. Applied in `toolHeader` and `toolHeaderStreaming` in `events.go`.
- **Generic tool icon** `⎔` → `◈` (diamond with dot) — more readable, signals "external/plugin" semantics. Only affects the `default` case in `toolStyle`; all built-in tools keep their dedicated icons (`$`, `≡`, `✚`, `✎`, `↓`, `✦`, `⊕`, `✳`, `◷`).

## [0.73.63] - 2026-07-29

### Feat — `/fork` session command (TUI) + `POST /api/sessions/{id}/fork`
- **`/fork`** in the TUI creates an exact copy of the current session at that instant: same CWD, model, thinking level, compaction state, stats, and full message history. The fork gets a new ID and fresh `CreatedAt`/`LastActiveAt`. The TUI switches to the fork in-place — history stays on screen unchanged, footer updates to the fork's name/ID. A one-line notice `⑂ forked → <id[:8]>` is printed. Returns 409 if the parent session is busy (JSONL may be mid-write).
- **`SessionStore.CopyMessages(srcID, dstID)`** — new interface method. `FileStore`: `os.ReadFile(src.jsonl)` + `os.WriteFile(dst.jsonl)` (byte-exact copy). `InMemoryStore`: slice copy.
- **`store.Session.Fork(name)`** — clones meta (new ID, `CreatedAt = now`, name as supplied), calls `CopyMessages`, opens and returns the fork handle via `OpenSession`.
- **`agent.Agent.ForkSession(sessionID)`** — resolves parent (live in-memory or from disk), builds a full `agent.Session` on top of the forked store (provider, tools, system prompt all rebuilt for the new session ID).
- **`POST /api/sessions/{id}/fork`** — new server endpoint. 409 if busy, 201 with the fork's `SessionMeta`.
- **`client.ForkSession(id)`** — new client SDK method.

## [0.73.62] - 2026-07-29

### Fix — claude-oauth: disk credentials must never overwrite fresher in-memory tokens
- **Root cause** (`internal/providers/claude_oauth.go`) — the fix in v0.73.58 made
  `getValidToken` and `loadCredentialsFromSources` always sync from
  `~/.harness/credentials.json` unconditionally. This introduced a new bug:
  `Connect()` sets fresh credentials in memory (obtained from the keychain via
  `authflow`), then calls `FetchModels()` to validate them. Inside `FetchModels`,
  `getValidToken` ran the disk sync and overwrote the fresh in-memory tokens with
  the stale ones still on disk — causing the validation to fail with
  "invalid credentials". The user had to manually delete the `claude-oauth` entry
  from `credentials.json` to bypass it.
- **Fix** — disk credentials are now only adopted when they are **strictly newer**
  than what is already in memory (`cred.ExpiresAt > tm.creds.ExpiresAt`). Applied
  in three places: `loadCredentialsFromSources`, the initial sync in
  `getValidToken`, and the per-retry sync inside the refresh loop. This preserves
  both goals simultaneously: fresh keychain tokens from `Connect()` are never
  overwritten by stale disk data; a concurrent harness instance that successfully
  refreshed and wrote a newer token pair is still picked up correctly.

## [0.73.61] - 2026-07-28

### Feat — `/reset` in Telegram & Slack; remove redundant `/new`
- **`/reset` added to Telegram** (`internal/transport/telegram/commands.go`) — new `cmdReset` handler calls `ExecCommand("reset")` and replies `🔄 Session history and stats wiped. Starting fresh.` Registered in `botCommands` so it appears in Telegram's `/` suggestion list.
- **`/reset` added to Slack** (`internal/transport/slack/slack.go`) — admin-only (added to `adminOnlyCommands` alongside `compact`, `stop`, etc.). Non-admins receive the standard `⛔` permission message. Added to `/help` output.
- **`/new` removed from Telegram and Slack** — `/reset` is strictly superior: same end-result (empty history) but preserves the session ID, model, thinking level, and name. `/new` created a new session entity with a new ID, which was unnecessary overhead. `resetChat`/`resetChannel` internal helpers are kept — they are still used by the pump lifecycle when a stored session is no longer found on the server.

## [0.73.60] - 2026-07-28

### Feat — `/reset` session command
- **`/reset`** wipes a session's message history and accumulated stats, returning it to a freshly-created state. Identity fields (`ID`, `CWD`, `Name`, `Model`, `Thinking`, `CreatedAt`) are preserved — same session entity, empty slate.
- **`SessionStore.TruncateMessages(sessionID)`** — new method added to the port interface. `FileStore` uses `os.Truncate(path, 0)` (file preserved, 0 bytes). `InMemoryStore` sets the log slice to `nil`.
- **`store.Session.Reset()`** — clears the in-memory working set, resets `CompactOffset`, `CompactCount`, and `Stats` to zero, then persists the updated meta.
- **`agent.Session.Reset()`** — public method with `ErrBusy` guard (409 if a turn is in flight).
- **`internal/server/server.go`** — `"reset"` added to the command registry and `handleExecCommand` switch. Returns `200 ok` on success, `409` if busy.
- **TUI** — `/reset` available immediately via the existing `execSessionCommand` path, no additional wiring needed.

## [0.73.59] - 2026-07-28

### Fix — Strip images from previous turns before every provider request
- **Root cause** (`agent/session.go`) — every ReAct loop iteration sends the full message history to the provider. When multiple large images (>2000px) accumulated across several turns, Anthropic rejected the request with HTTP 400 `"image dimensions exceed max allowed size for many-image requests"`. This happened on any normal turn, not just compaction.
- **`stripOldTurnImages(msgs)`** — new function applied to the history before each provider request. Scans backward to find the last assistant message without tool_calls (end of the previous turn); everything before that boundary has its images replaced with `[image: <mime_type>]`. Everything from the boundary onward (current turn) is sent intact so the model can analyse whatever was just shared.
- **`stripImagesFromMessage(m)`** — extracted helper handles both image paths: `ContentPart.Image` (user-pasted screenshot) and `ToolResult.Images` (Read tool result on an image file). Placeholder includes the MIME type so the model knows what kind of image was there.
- **`stripImages`** (compact) refactored to reuse `stripImagesFromMessage` — no behavior change.
- **Zero impact on disk** — the JSONL store is never modified. Only the wire payload to the provider is stripped. All providers benefit (Anthropic API-key, claude-oauth, and any future provider that receives `req.Messages`).

## [0.73.58] - 2026-07-28

### Feat — Direct bash execution (`!cmd`) in TUI + Warn color updated to amber
- **`!` prefix mode** (`internal/transport/tui/commands.go`, `layout.go`, `tui.go`) — typing `!` in the TUI input activates bash mode: separators, input text, and cursor switch to amber (`HexWarn`). On Enter, the command after `!` is executed directly via `sh -c` (bypassing the agent), with stdout+stderr rendered in the history. Bare `!` with no command is a no-op. Timeout: 30s. Output rendered with `ansi.Dimmed`; exit errors in rose (`ansi.Err`).
- **Unified amber theming** — separators (`layout.go`), editor text, and block cursor all share a single `colorFn func() string` that returns `HexWarn` in bash mode and `HexPrimary` otherwise. The three components flip color atomically on every keystroke. `Editor.ColorFn` added to `components/editor.go`; `ansi.CursorColored()` added to `ansi/color.go`.
- **`HexWarn` changed from violet `#B44CA0` to amber `#E8A838`** (`ansi/color.go`) — violet is a Kaiban brand decoration color with no warning semantics (per the locked brand system). Amber is semantically unambiguous as caution/warning and reads clearly on the TUI dark background. All existing `ansi.Warn` usages (`⚠`, `⏹ Stopped`, `⚙ busy`, `T` in context grid) automatically adopt the new color.
- **OAuth token refresh hardened** (`internal/providers/claude_oauth.go`) — `loadCredentialsFromSources` now always re-reads from disk (removed the `creds != nil` early-return guard). `getValidToken` syncs full credentials from disk before checking expiry — if a concurrent harness instance already refreshed, the fresh token is used directly. `isAuthError` now includes `HTTP 400` so `invalid_grant` aborts the retry loop immediately instead of retrying 3× on a permanent error.

## [0.73.57] - 2026-07-27

### Refactor — Schedule store keyed by (owner, slug) to prevent cross-session collisions
- **Root cause** (`agent/schedule/store.go`) — the flat `map[string]Schedule` layout
  keyed only by slug meant two sessions could silently overwrite each other's
  schedules if they used the same slug. The `Owner` field was a runtime filter,
  not a structural guard.
- **New layout** — `map[string]map[string]Schedule` (`owner → slug → schedule`).
  The composite key `(owner, slug)` is now the unit of uniqueness; cross-session
  collision is structurally impossible, not just policy-blocked.
- **`store.go`** — `Delete` and `RecordRun` now take `(slug, owner)`. `Owner`
  is the outer map key, no longer stored inside the JSON value.
- **`adapter.go`** — `Delete` simplified; no longer needs to consult `Owners()`
  before delegating — a foreign slug is simply absent under the caller's bucket.
- **`engine.go`** — `RecordRun` call updated to pass `sc.Owner`.
- **`store_test.go`** — all signatures updated; added `TestStoreSameSlugDifferentOwners`
  confirming two sessions can coexist with the same slug without collision.
- **`~/.harness/schedules.json`** — manually migrated to new nested format;
  original preserved as `schedules.json.bak`.
- **Tools, types, server, and all transports** — zero changes. Pure low-level store refactor.

## [0.73.56] - 2026-07-27

### Fix — Strip inline images before compact request (Anthropic many-image 400)
- **Root cause** (`agent/session.go`) — `generateCompactionSummary` was sending
  the full message history verbatim to the compaction LLM call, including all
  inline base64 images accumulated during the session. Anthropic rejects requests
  where multiple images exceed 2000px in any dimension
  (`messages.N.content.M.image_source.base64.data: At least one of the image
  dimensions exceed max allowed size for many-image requests`), causing compact
  to fail with a 400 error and leaving the session unrecoverable.
- **Fix** — new `stripImages()` helper strips image content before building the
  compact request: top-level image parts (`ContentPart.Image`) are replaced with
  `[image omitted for compaction]`; `ToolResult` parts with inline images have
  their `Images` slice dropped while the text output is preserved. The LLM
  produces an equally good summary from text alone — it never needed the images.

## [0.73.55] - 2026-07-27

### Fix — Slack @mention delegated to agent + 5 inviolable communication rules
- **`@mention` removed from transport** (`internal/transport/slack/pump.go`,
  `slack.go`) — the mechanical `<@USER_ID>` prepend on every channel reply has
  been removed. The `lastUser` field is gone from `channelPump`. Doing it in
  code was fragile in multi-person channels (wrong person gets pinged, ordering
  issues) and removed contextual judgment from the agent.
- **Agent owns the @mention** (`internal/transport/slack/directive.go`) — the
  directive now explicitly instructs the agent: it already receives the sender's
  user ID via `<slack:user>U...</slack:user>` on every message; in channel
  replies it must open with `<@USER_ID>` itself. In DMs it must not self-mention.
- **5 inviolable Slack communication rules** added to the directive:
  1. **Lethal brevity** — 2–3 sentences max unless the deliverable is inherently long.
  2. **Never narrate the process** — deliver results only, never list tools/steps taken.
  3. **Never leak credentials** — no API keys, tokens, or secrets, not even truncated.
  4. **Mirror the language** — Spanish if user writes Spanish, English if English.
  5. **Result first** — lead with the finding; zero preamble.

## [0.73.54] - 2026-07-27

### Fix — Guaranteed turn_end + malformed tool args + IMAGE_PROCESS_FAILED
- **`promptSync` guaranteed `turn_end`** (`agent/session.go`) — a `defer`
  ensures `EventTurnEnd` is always emitted regardless of how `promptSync`
  exits: normal completion, provider error, store error, context cancellation,
  or any future panic. Previously, paths that returned early (e.g.
  `store.AddMessage` failure) never emitted `turn_end`, leaving transports
  (Telegram typing indicator, Slack typing, TUI spinner) permanently stuck.
  The `turnEnded` flag makes the defer idempotent — it fires exactly once even
  if a code path already emitted `turn_end` explicitly. Store errors now also
  emit `EventError` + `EventLoopEnd` before returning so clients receive the
  full error lifecycle.
- **Malformed tool argument JSON** (`internal/providers/llm/openai.go`) —
  some providers (e.g. minimax-m3 via ollama-cloud) occasionally stream tool
  call `arguments` as concatenated JSON fragments that fail `json.Valid`. The
  parser now checks validity before storing; invalid args are wrapped as
  `{"_raw": "..."}` so `store.AddMessage` never fails with
  `invalid character '{' after top-level value`. The tool may fail at
  execution time (args are wrong), but the error is surfaced cleanly as a
  `tool_result` with `is_error: true` rather than crashing the store.
- **`IMAGE_PROCESS_FAILED` on Telegram** (`internal/transport/telegram/upload.go`)
  — the agent sometimes downloads HTML pages (paywall, hotlink protection,
  Cloudflare challenge) and saves them with a `.jpg` extension. Telegram then
  rejects `sendPhoto` with `IMAGE_PROCESS_FAILED`. New `isRealImage()` checks
  the file's magic bytes (JPEG `FF D8 FF`, PNG `89 50 4E 47`, GIF `47 49 46
  38`, WebP `52 49 46 46…57 45 42 50`) before calling `sendPhoto`; files that
  fail the check fall back to `sendDocument` so the user still receives
  something and the log records `upload_fallback` with the reason.

## [0.73.53] - 2026-07-27

### Feature — Telegram: session scoped by CWD + named "Telegram <date>"
- **Session mapping now keyed by `(cwd, chatID)`** — `telegram.json` sessions
  field changed from flat `chatID → sessionID` to nested
  `cwd → { chatID → sessionID }`, mirroring the same change made to the Slack
  transport in v0.73.49. Running `harness telegram` from different working
  directories now creates independent sessions per chat, each with the correct
  project context (AGENTS.md, skills, working directory). Old flat keys are
  silently ignored — new sessions are created under the current CWD.
- **Sessions named `"Telegram <date>"`** — sessions created by the Telegram
  transport are named `"Telegram 2026-07-27 16:30"` instead of the generic
  `"New Session <date>"`, making them immediately identifiable in
  `harness sessions` alongside TUI and Slack sessions.
- **`unpair` updated** — removes the chat's session mapping across ALL CWD
  buckets (not just the current one) so a full unpair is truly complete.

## [0.73.52] - 2026-07-27

### Feature — Slack commands: /context, /thinking, /model, /help + admin system
- **`/context`** — context window breakdown (same data as the TUI `/context`
  command), rendered as a monospace code block matching `/info` style.
- **`/thinking [level]`** — without args: shows current level and lists all
  valid values (`off · low · medium · high · xhigh`) with current highlighted
  in bold. With `<level>`: sets it and confirms. Strips backticks so the user
  can copy-paste the formatted value directly from the list.
- **`/model [model]`** — without args: lists available models grouped by
  provider, showing full `provider/model` string ready to copy-paste, with `✓`
  on the current model. With `<model>`: switches and confirms. Strips backticks
  on the argument for the same reason.
- **`/help`** — shows the command list with descriptions (same content as the
  "unknown command" error, without the error prefix).
- **`/info` redesigned** — now renders as a monospace code block (consistent
  with `/context`), with aligned columns and includes `iters`, `cache`,
  `schedules`, and `⚙ busy` badge when the agent is working.
- **Admin system** — state-changing commands (`/new`, `/stop`, `/compact`,
  `/thinking`, `/model`) now require the sender to be in the admin list.
  Read-only commands (`/help`, `/info`, `/context`) are always public. Prompts
  to the agent are unrestricted.
  - `adminOnlyCommands` map in `slack.go` — checked before dispatch.
  - Non-admins receive: `⛔ You don't have permission... Ask an admin to run: harness slack admin <your_id>`.
  - `Admins []string` field added to `slackJSON` (persisted in `slack.json`).
  - `IsAdmin`, `AddAdmin`, `RemoveAdmin`, `ListAdmins` functions in `creds.go`.
  - CLI: `harness slack admin <userID>` (add), `harness slack admin list`,
    `harness slack admin remove <userID>`.

## [0.73.51] - 2026-07-27

### Feature — Slack sender/channel context tags in every prompt
- Every prompt sent to the agent now starts with one or two context tags
  injected by the transport (not visible to the user):
  - `<slack:channel>C...</slack:channel>` — present only for channel messages
    (omitted for DMs where the channel is implicit).
  - `<slack:user>U...</slack:user>` — always present, identifies the sender.
- `buildPrompt` updated to accept `channelID` and `userID` and prepend the
  tags before the user's text, with `<slack:attach>` tags remaining at the
  bottom. Channel tag is suppressed for DM channels (`D...` prefix).
- **Directive updated** — new `### Sender and channel context` section explains
  the two tags, instructs the agent to resolve IDs with `SlackListUsers` /
  `SlackListChannels` once per session and cache the mapping, and highlights
  that in channels the `<slack:user>` tag distinguishes multiple speakers.

## [0.73.50] - 2026-07-27

### Fix — JSONL corruption causing `compact_offset` drift and Anthropic 400 errors
- **Root cause**: `appendToJSONL` could write two JSON objects on the same line
  (without a separating `\n`) if the file was previously left without a trailing
  newline — e.g. when two harness instances had the file open simultaneously, or
  after a crash mid-write. This shifted every subsequent line number by +1,
  making `compact_offset` (which counts *messages*, not *lines*) point to the
  wrong position on resume. The working set then started at an `assistant`
  message containing a `tool_use`, causing the next `user` message (the real
  compaction checkpoint) to appear as an orphaned `tool_result` from Anthropic's
  perspective → HTTP 400 `unexpected tool_use_id found in tool_result blocks`.
- **Prevention** (`agent/store/file.go` `appendToJSONL`) — before writing each
  new message, the function now seeks to the last byte of the file and emits a
  missing `\n` if needed. This auto-repairs any pre-existing missing newline
  before the new entry, so concatenation can never occur.
- **Session repair** — session `6300e66d` was repaired manually: the JSONL was
  rewritten cleanly (one object per line) and `compact_offset` was corrected
  from `1092` to `1094` (the actual `IS_COMPACTION` message index).

## [0.73.49] - 2026-07-27

### Fix — Slack session scoped by CWD + named "Slack <date>"
- **Session mapping now keyed by `(cwd, channelID)`** — `slack.json` sessions
  field changed from flat `channelID → sessionID` to nested
  `cwd → { channelID → sessionID }`. Each `(project, channel)` pair gets its
  own independent session so the agent always has the correct project context
  (AGENTS.md, skills, working directory). Running `harness slack` from
  `/project-a` and later from `/project-b` creates separate sessions for the
  same channel instead of resuming the wrong-project session. Old flat keys are
  silently ignored — new sessions are created under the current CWD.
  The `save()` method only touches its own CWD bucket, leaving other CWDs on
  disk intact (safe for future multi-CWD scenarios).
- **Slack sessions named `"Slack <date>"`** — sessions created by the Slack
  transport are named `"Slack 2026-07-27 16:30"` instead of the generic
  `"New Session <date>"`, making them immediately identifiable in
  `harness sessions`. Implemented via an optional `name` field on
  `createSessionRequest` (server-side `POST /api/sessions`) and
  `client.CreateSession(model, cwd, name)` — all other transports pass `""`
  preserving existing behaviour.

## [0.73.48] - 2026-07-27

### Fix — claude-oauth token refresh reliability + compact retry
- **Retry with backoff on token refresh** (`claude_oauth.go`) — `getValidToken`
  now retries the OAuth refresh up to 3 times with exponential backoff
  (1 s → 2 s → 4 s) before giving up. Transient network errors and server
  timeouts no longer cause an immediate "session expired" failure. Auth errors
  (HTTP 401/403, `invalid_grant`, `token_expired`, `revoked`) short-circuit
  immediately — no point retrying a definitively rejected token.
- **Re-read refresh token from disk before each attempt** — before every refresh
  attempt the token is re-read from `credentials.json`. This prevents a race
  condition when two harness instances run simultaneously (e.g. TUI + Telegram):
  if the other instance already refreshed and wrote a newer token, we use that
  instead of the stale in-memory one. Anthropic refresh tokens are single-use —
  using a stale one causes a 401 that previously looked like a session expiry.
- **Surface the real refresh error** — previously any refresh failure produced
  the generic message `"session expired — run 'claude auth login'..."` hiding the
  actual cause (network timeout, 401 invalid_grant, revoked token, etc.). The
  error now includes the HTTP status and API response so it's diagnosable. The
  reconnect hint also points to `harness connect claude-oauth` (the correct
  harness command) instead of `claude auth login`.
- **Compact retry with backoff** (`agent/session.go`) — `generateCompactionSummary`
  retries up to 3 times with exponential backoff (2 s → 4 s → 8 s) before
  emitting a `compact failed` error. This covers the case where compact triggers
  a token refresh that transiently fails — the retry gives the token manager time
  to succeed on the next attempt. Context cancellation is respected between
  retries so a user `/stop` still interrupts immediately.

## [0.73.47] - 2026-07-27

### Feature — Slack proactive messaging tools (SlackPost, SlackListChannels, SlackListUsers)
- **`SlackListChannels`** — calls `conversations.list` (paginated) and returns
  all channels the user can see with their IDs, privacy flag and member count.
  The agent uses this to resolve `#general` → `C024BE91L` before posting.
- **`SlackListUsers`** — calls `users.list` (paginated), filters out bots and
  deleted accounts, and returns ID, handle and display name for every active
  user. The agent uses this to resolve a person's name to their user ID for
  `<@mention>` or sending a direct message.
- **`SlackPost`** — posts text (and optionally files) to any target:
  - Channel ID (`C…`) → `chat.postMessage` directly.
  - Channel name (`#general`) → resolves via `SlackListChannels` first.
  - User ID (`U…`) → opens DM with `conversations.open` to get a `D…` channel,
    then posts. Files use the existing 3-step upload API
    (`getUploadURLExternal` → PUT → `completeUploadExternal`).
- **`agent.RegisterTool`** — new public method on `*agent.Agent` that adds a
  tool to the agent's registry after construction. Allows transports to inject
  transport-specific tools without changing `AgentOptions` or `New()`. Used by
  the Slack transport to inject the three tools after credentials are verified
  and the `Bot` instance is available.
- **Directive updated** — new `## Proactive messaging` section documents the
  three tools and gives the agent an example of how to combine
  `SlackListUsers` + `SlackPost` to notify someone in a channel.

## [0.73.46] - 2026-07-27

### Feature — `harness slack login` + channel @mentions + slack.json unification
- **`harness slack login`** — interactive login flow that needs only the `xoxd`
  cookie from the browser; derives `xoxc` automatically with a single GET to the
  workspace URL (Slack embeds `api_token` in the page HTML). Validates with
  `auth.test` and saves credentials to `~/.harness/slack.json` (0600).
  - `harness slack login --status` — verifies saved credentials are still valid.
  - `harness slack` now resolves credentials with precedence:
    `--flags` > `SLACK_*` env vars > `~/.harness/slack.json`.
  - New files: `creds.go` (`Credentials`, `LoadCredentials`, `SaveCredentials`,
    `DeriveXoxC`, `VerifyAndSave`); `login.go` moved into `creds.go`.
- **Unified `slack.json`** — `store.go` merged into `creds.go` so both the auth
  credentials and the channel→session mappings live in one `slackJSON` struct.
  Previously `SaveCredentials` overwrote `sessions` and `store.save()` overwrote
  credentials; now each side reads-then-merges before writing, so neither field
  set loses data.
- **Channel @mention replies** — when a message arrives in a channel (`C…`),
  the transport records the sender's user ID in `channelPump.lastUser`. Every
  reply's first chunk is prefixed with `<@USER_ID>` so Slack notifies the user
  who asked. DMs (`D…`) are unaffected. The agent is unaware of this — it is
  handled entirely in `sendLogged`.

## [0.73.45] - 2026-07-27

### Feature — Telegram `/thinking` and `/model` commands with inline keyboards
- **`/thinking`** — sends an inline keyboard with the 5 levels (`off`, `low`,
  `medium`, `high`, `xhigh`), marking the current level with `✓`. Tapping a
  button calls `ExecCommand("thinking")`, acknowledges with a short notification
  (`✓ thinking → high`), and replaces the keyboard message with a confirmation
  (`🧠 Thinking level set to: \`high\``). Keyboard is removed on selection.
- **`/model`** — sends an inline keyboard grouped by provider, with models in
  rows of 2. Provider headers are non-clickable separators; the current model
  is marked with `✓`. Tapping a model calls `ExecCommand("model")`, acknowledges,
  and replaces the keyboard message (`🤖 Model set to: \`claude-sonnet-5\``).
  Keyboard is removed on selection.
- **Inline keyboard infrastructure** (`bot.go`) —
  - `CallbackQuery` / `InlineKeyboardMarkup` / `InlineKeyboardButton` types
  - `GetUpdates` now subscribes to `callback_query` in addition to `message`
  - `SendMessageWithKeyboard` — `sendMessage` with `parse_mode: MarkdownV2` and
    `reply_markup`; falls back to plain text on parse failure
  - `EditMessageText` — `editMessageText` with `parse_mode: MarkdownV2` and
    `reply_markup: {}` to remove the keyboard on confirmation; plain text fallback
  - `AnswerCallbackQuery` — acknowledges the tap (stops the loading spinner)
- **`pendingKb`** in `chatPump` — tracks the in-flight keyboard (message ID +
  command name) so `handleCallbackQuery` knows which command to execute when a
  button is tapped. Cleared on selection; restored on `noop` (provider header)
  so the user can still pick a model after tapping a header.
- **`handleCallbackQuery`** in `telegram.go` — dispatches `command:value`
  callback data to the correct handler (`thinking`, `model`, `noop`).
- **Directive fix** — `SendMessageWithKeyboard` and `EditMessageText` both send
  `parse_mode: MarkdownV2` so `*bold*` and `` `code` `` render correctly instead
  of showing raw markers.

## [0.73.44] - 2026-07-27

### Feature — Slack transport: render, file upload, file attach, typing, observability
- **`toMrkdwn` renderer** (`render.go`) — converts the agent's CommonMark output
  to Slack mrkdwn before sending: `**bold**` → `*bold*`, `---` stripped (with
  multi-blank-line collapse), `# headings` → `*bold line*`, `- lists` → `• lists`,
  inline code and fenced code blocks passed through verbatim.
- **Table support** (`tables.go`) — pipe tables rewritten to aligned monospace
  code blocks (same `tablesToCodeBlocks` approach as Telegram, copied locally).
- **Typing indicator** — `startTyping`/`stopTyping` goroutine sends
  `{"type":"typing"}` over the RTM WebSocket every 4 s while the agent works;
  fires on `turn_start` and `received_prompt` (scheduled), stops on `turn_end`,
  `compact_end`, `stop`, and `error`. `SendTyping` acquires `connMu` separately
  from the read goroutine — gorilla/websocket serialises writes internally.
- **Mid-turn text flush** — `tool_call` events now trigger `flushReason` so
  agent commentary written before a tool executes reaches the user in real time
  (same pattern as Telegram).
- **File upload `<slack:uploadFile>`** (`upload.go`, `bot.go`) — 3-step Slack
  API (getUploadURLExternal → PUT bytes → completeUploadExternal). The agent
  emits `<slack:uploadFile>/path</slack:uploadFile>` tags; the transport strips
  them, uploads each file, and sends the accompanying text as `initial_comment`
  on the first file.
- **File attach `<slack:attach>`** (`files.go`) — when the user shares a file
  Slack delivers `files[]` in the RTM event; `image/*` → `SendPromptWithImages`
  (base64); `text/*` / `application/json` etc. → downloaded to
  `os.CreateTemp("", "*-originalname")` and injected as `<slack:attach>` tag
  for the agent's Read tool; other types silently ignored with TODO.
- **Reply logging with trigger** — every `flush` carries a `reason` string
  (`text_end`, `tool_call`, `turn_end`, …); the `reply` log entry includes
  `trigger=<reason>` so mid-turn texts are distinguishable from end-of-turn
  replies. Upload replies log `files=N` alongside the text.
- **`files.go` rename in Telegram** — `images.go` / `images_test.go` renamed
  to `files.go` / `files_test.go` to reflect that the file now handles all
  file types (images, text documents), not just photos.
- **Temp file naming** — both Telegram and Slack now use
  `os.CreateTemp("", "*-originalname")` so the OS-assigned unique prefix
  precedes the original filename (e.g. `/tmp/1234567890-script.py`), preserving
  the extension and semantic name the model uses to infer language/context.
  Previously both used invented names (`harness-telegram-*.ext`,
  `harness-slack-*.ext`).
- **Directive updated** — Slack directive now includes the `<slack:uploadFile>`
  section (mirroring `<tel:uploadFile>` in Telegram) plus the `<slack:attach>`
  inbound section. Removed the mrkdwn syntax cheat-sheet (formatting is the
  transport's job, not the model's).

## [0.73.43] - 2026-07-26

### Feature — Slack transport (`harness slack`)
- New transport `internal/transport/slack` — same architecture as Telegram:
  in-process server + HTTP/SSE client + one harness session per Slack
  channel or DM. Driven by browser session tokens (`xoxc-` + `xoxd-`), no
  Slack app or bot token required.
- **One session per channel/DM** — `~/.harness/slack.json` maps Slack channel
  IDs (`D…` DMs, `C…` channels) to harness session IDs, exactly like
  Telegram's chat→session store. Sessions are resumed on restart; a missing
  or deleted session triggers a fresh one automatically.
- **RTM WebSocket** for real-time events — calls `rtm.connect` to get the
  `wss://` URL, then dials with `Authorization: Bearer xoxc-...` and
  `Cookie: d=xoxd-...` headers (required since Sep 2023 per slack-go #1230).
  Reconnects automatically on disconnect with a 5s backoff.
- **Listens to**: direct messages to the authenticated user and explicit
  `@mentions` in any channel the user is a member of. Own messages, bot
  messages, and sub-typed events are skipped.
- **Commands** (typed as `/new`, `/stop`, `/compact`, `/info` after
  `@mentioning` the user or directly in a DM): same set as Telegram.
- **`gorilla/websocket`** added as the only new dependency — needed for
  custom upgrade headers on the RTM WebSocket; stdlib `net/http` cannot
  inject headers into a WebSocket upgrade.
- **CLI**: `harness slack --workspace <url> --xoxc <token> --xoxd <token>`
  (all three required; also readable from `SLACK_WORKSPACE` / `SLACK_XOXC` /
  `SLACK_XOXD` env vars for convenience). Optional: `--model`, `--thinking`,
  `--scheduler`.

## [0.73.42] - 2026-07-25

### Feature — `GET /api/sessions/{id}/context` + `/context` command in TUI and Telegram
- New endpoint `GET /api/sessions/{id}/context` returns a token-usage breakdown
  of the context window, estimated per component using the chars/4 approximation.
  The response splits the context into three top-level buckets — system prompt,
  tools, and conversation — with sub-breakdowns inside each:
  - **System prompt**: `sys_base` (base prompt + mem/sched/cwd/directives blocks),
    `sys_agents_md` (AGENTS.md / Project Context block), `sys_skills`
    (Available Skills listing block), `sys_total`.
  - **Tools**: `tools_built_in` (the 13 known built-in schemas: Bash, Read,
    Write, Edit, Fetch, Skill, Subagent, Memo*, Schedule*), `tools_mcp`
    (everything else — MCP tools), `tools_total`.
  - **Conversation**: `conversation` — working-set messages only (post-compaction,
    exactly what the model sees each turn), marshalled to JSON then divided by 4.
  - **Totals**: `estimated_total` (sum of the three buckets), `last_real_total`
    (actual input tokens from the last provider response — 0 before any turn),
    `context_window`, `free_space` (`context_window − last_real_total`).
- Lens fields computed **once at session creation**, never recomputed per request.
  `buildSystemPrompt` measures each variable block while writing it (before vs
  after each `b.WriteString`) and returns `promptLens{base, agentsMD, skills}`
  alongside the string. `buildSessionTools` marshals each `ToolDef` at the end
  and accumulates into `toolLens{builtIn, mcp}`, distinguishing built-ins by the
  known-name set from `tools/names.go`. `newSession` stores all five values as
  private fields (`sysBaseLen`, `sysAgentsMDLen`, `sysSkillsLen`,
  `toolsBuiltInTk`, `toolsMCPTk`). `ContextBreakdown()` is pure arithmetic on
  those fields plus a one-shot marshal of the current working-set messages.
- `agent.ContextBreakdown` struct with explicit snake_case json tags (the wire
  contract). `internal/client.ContextBreakdown` mirrors it; `GetSessionContext`
  method on `*client.Client`.
- **TUI** `/context` command: palette entry + `showContext()` renders a panel
  in the scrollback with labelled rows (14-char padding), sub-items indented,
  and a separator line before the totals. Shows `(no turn yet)` when
  `last_real_total` is 0.
- **Telegram** `/context` command: same data in a monospace code block
  (same alignment trick as `/info`).

## [0.73.41] - 2026-07-25

### Feature — `GET /api/sessions/{id}/info` + `/info` command in TUI and Telegram
- New endpoint `GET /api/sessions/{id}/info` consolidates into one round-trip
  everything that was previously assembled by each transport from three or four
  separate calls (`GetServerInfo` + `GetSession` + `GetMCPStatus` +
  `GetSchedules`). Returns a single typed document:
  - `version` — running harness binary version
  - `session` — full `SessionMeta` (id, name, cwd, model, thinking, stats,
    timestamps) + `max_iterations`
  - `busy` — whether the agent is actively processing a turn right now
  - `queue_depth` — prompts queued behind the current turn
  - `mcp_connected` — number of MCP servers currently connected
  - `schedule_count` — cron schedules owned by this session (the ones that
    fire into it, not a global total)
  - Returns 400 (not 404) when the session is not in the active set — the
    endpoint is designed for live interactive use, not querying historical
    sessions.
- `internal/client.SessionInfo` — new typed response struct; `GetSessionInfo`
  method on `*client.Client` — one call, one typed value, no boilerplate.
- **Telegram** `/info` command: migrated from 4 independent API calls to one
  `GetSessionInfo` call. Rendered output is identical to before.
- **TUI** `/info` command (new): available in the palette and as a typed
  slash-command (`/info`). Renders a compact panel in the scrollback with the
  same data the footer shows live, formatted for readability when the footer is
  too compressed: version, session name/id, model, thinking, max iterations,
  context usage, token/cache/cost counters, MCPs connected, schedule count, and
  a busy/queued badge when the session is active. No params, executes
  immediately (`"none"` type in the palette flow — same as `/compact`).
- `sessionInfoDTO` (the pre-existing GET /api/sessions/{id} simple wrapper)
  renamed to `sessionDetailDTO` to avoid collision with the richer new DTO —
  no wire-format change (the `session` field in the new endpoint embeds the
  same shape).

## [0.73.40] - 2026-07-25

### Internal — `internal/client` is now a typed SDK over the API (no more `[]byte`)
- The previous step (0.73.39) unified the three transports onto one client,
  but that client still returned raw `[]byte` for every endpoint — so each
  caller re-decoded the same shapes by hand (the TUI and CLI with
  `json.Unmarshal` into ad-hoc structs, Telegram behind its own wrapper that
  decoded into `map[string]any`). Three places still owned the wire contract,
  and Telegram still needed a wrapper client on top of the shared one.
- `internal/client` now decodes every response into a typed Go value. New
  `types.go` defines the response shapes (`ServerInfo`, `Settings`,
  `Provider`, `Model`, `Session`, `Schedule`, `Status`, `CommandDef`/
  `ParamDef`) and reuses the owner's own wire type where it's lightweight
  (`config.ProviderConfig`/`MCPServer`, `store.SessionMeta` embedded in
  `Session`, `types.Message` for history) or mirrors it where reusing would
  drag a heavy dependency into an HTTP client (`MCPStatus` vs the mcp→tools
  graph; `Memory`/`MemorySearchResult` vs the SQLite driver). Every method
  now returns e.g. `[]Session`, `*Status`, `map[string]MCPServer` — decoding
  happens once, here, against types that match the server's.
- SSE events are now a typed, flat `client.Event` (new `event.go`): a single
  struct with every field any event kind can carry, a discriminated union on
  `Type` — exactly how consumers already treated the events (switch on the
  type, read the relevant fields), only typed instead of `map[string]any`
  lookups. It carries a `Raw json.RawMessage` (the verbatim `data:` payload)
  so the CLI's `--output json`/`json-stream` modes pass events through
  byte-for-byte instead of a lossy `omitempty` re-encode (a re-marshal would
  drop zero-valued fields like `is_error:false`; a regression test locks
  this in).
- **The Telegram wrapper is gone entirely.** `telegram.apiClient` is deleted;
  `Transport.api` is now a `*client.Client` used directly, like the TUI and
  CLI. The per-call decoding it used to do (`GetSession`→`map`, `ListModels`
  →`[]map`, the `CountConnectedMCPs`/`CountSchedules` helpers) is now either
  a typed field read at the call site or a tiny transport-local helper over
  the typed client. `harnessError` was already `client.Error`; nothing in the
  transport re-implements request/decode/stream anymore.
- TUI: `consumeEvents` and the SSE drain consume `<-chan client.Event`;
  `renderHistory` walks typed `types.Message`/`ContentPart` instead of nested
  `map[string]any`; `CommandDef`/`ParamDef` are aliases of the client types;
  the dead `intFromMap`/`floatFromMap` helpers were removed and `relativeTime`
  now takes a `time.Time`. CLI: `settings`/`mcp`/`memo`/`schedule`/`sessions`/
  `providers` read typed structs; `RunMCPAdd`/`RunMCPSetEnabled` build a typed
  `client.MCPServer` (using its `IsRemote()`/`Argv()` helpers) instead of
  assembling `map[string]any`.
- New tests in `internal/client` cover the typed paths: typed list decode,
  the `{"status":{…}}` envelope unwrap, `DeleteSession`'s 204 handling, the
  reused `config.MCPServer` transport inference, and `Event` field population
  + `Raw` byte-exactness. `go build ./...`, `go vet ./...`, and
  `go test ./... -race` are clean; `-p` was smoke-tested end to end in text,
  `json`, and `json-stream` modes, plus every read CLI command.

## [0.73.39] - 2026-07-25

### Internal — unified the three near-identical HTTP/SSE clients into `internal/client`
- Three transports (`internal/transport/tui`, `internal/transport/telegram`,
  `internal/cli`) each talked to the same `internal/server` backend through
  their own hand-rolled client: `tui.Client` (~27 methods, raw `[]byte`),
  `telegram.apiClient` (~15 methods, pre-decoded into `map[string]any`/
  `string`/`bool`/`int`), and `cli.httpClient` (~22 methods, raw `[]byte`).
  All three duplicated the same `do()` (marshal → POST → read → parse
  harness's `{"error":{"message","details"}}` shape) and the same
  `StreamEvents` SSE scanner.
- New `internal/client` (package `client`) is the one client all three now
  use. It returns raw `[]byte` for every endpoint — no opinion about how a
  caller wants to decode the response, matching what the TUI and CLI clients
  already did; `telegram.apiClient` is kept as a thin wrapper over it purely
  to decode into the specific Go values its call sites (`commands.go`,
  `pump.go`, `images.go`, `telegram.go`) were already written against, so
  those files needed no changes beyond the error type swap below.
- `internal/cli/client.go` shrank to a one-line `newClient()` alias;
  `internal/transport/tui/client.go` was deleted entirely (the TUI now holds
  a `*client.Client` directly); `CommandDef`/`ParamDef` (decoded from
  `ListCommands`'s raw bytes) moved to the TUI's own new `commanddef.go`
  since they no longer have a client type to live next to.
- `telegram`'s local `harnessError` type was replaced by `client.Error`
  (`pump.go`'s `replyError` now type-asserts `*client.Error` and reads
  `.Message`/`.Details` instead of the lower-case fields the local type had).
- **Real bug fixed by the unification**: `internal/cli/client.go`'s SSE
  reader had no `scanner.Buffer(...)` call at all (bare `bufio.Scanner`
  default: 64KB line cap), unlike the TUI and Telegram clients which both
  set a larger buffer. A single SSE event line over 64KB (e.g. a big
  `tool_result`) would have silently truncated the scan — `bufio.ErrTooLong`
  swallowed, not surfaced — only on the CLI's `-p` streaming path. The
  unified client's `streamEvents` (in `internal/client/stream.go`) sizes the
  buffer to 64KB initial / 4MB max on every transport now.
- `eventBufferSize` (channel capacity between the SSE-reading goroutine and
  the consumer) is a single constant (4096) instead of three independently
  drifted literals.
- No behavior change for any transport beyond the fixed SSE buffer bug —
  `go build ./...`, `go vet ./...`, and `go test ./... -race` all clean.

## [0.73.38] - 2026-07-25

### CLI — `harness -p <prompt>` no longer litters disk with unresumable sessions
- `-p` runs a single turn and returns — there's no `-p --resume`, so the
  session it created had no way to ever be revisited. But `cmdPrompt` used
  `newAgent()`, which (via `agent.New`'s default) persists every session to
  `~/.harness/agent/sessions/` with a `FileStore`. Every `-p` invocation left
  a small, never-to-be-read "New Session"-named session file behind — an
  audit of an existing installation found the majority of persisted sessions
  matched exactly this shape.
- New `newOneShotAgent()` (in `internal/cli/agent.go`) matches `newAgent` but
  sets `Store: store.NewInMemoryStore()`, so the session lives only for the
  process's lifetime — the turn's reply still goes to stdout exactly as
  before, there's just nothing left on disk afterward. `cmdPrompt` now uses
  it instead of `newAgent`.
- `newAgent` itself is unchanged (still `FileStore`) — it's still correct for
  `harness mcp` / `harness memo`, which don't create sessions at all (so the
  store choice was always moot for them), and its doc comment now points to
  `newOneShotAgent` for the `-p` case.
- New test `TestNewOneShotAgentDoesNotPersistToDisk` (isolates `$HOME` to a
  temp dir, creates+closes a real session through `newOneShotAgent`, asserts
  nothing was written under `~/.harness/agent/sessions/`) — verified to fail
  against the pre-fix wiring.

## [0.73.37] - 2026-07-25

### CLI — commands that never touch tools now use the lighter config agent
- `newAgent()` spawns MCP subprocesses and opens the memory DB
  (`EnableMCPs`/`EnableMemory`) — necessary for `-p` prompts, `harness mcp`
  (its `list` shows live connection status from the manager it spawns), and
  `harness memo` (reads the memory store it opens). But `cmdProviders`,
  `cmdConnect`, `cmdDisconnect`, `cmdSessions`, `cmdDelete`, and
  `cmdSchedules` only shuffle JSON over the local HTTP API
  (`/api/providers`, `/api/sessions`, `/api/schedules`) — they never create a
  session or execute a tool, so the MCP subprocesses and memory DB `newAgent`
  set up for them were pure waste. Verified: `mcp list` (real work, spawns 3
  MCP servers) burns ~0.73s of CPU; `providers` (JSON-only) now burns ~0.04s
  with `newConfigAgent`.
- All six switched from `newAgent()` to the already-existing `newConfigAgent()`
  (previously only used by `settings`) — same behavior, same output, just
  without paying for tools/MCP/memory setup they never use. No new
  function — `newAgent` and `newConfigAgent` stay separate on purpose (see
  their updated doc comments): merging them would force every config-only
  command to pay MCP-spawn/memory-DB cost that a same-process,
  interactive-transport-style agent needs but a JSON-shuffling one-shot
  command does not.

## [0.73.36] - 2026-07-25

### CLI — merged newTelegramAgent into newInteractiveAgent
- After the previous MaxIterations change, both call sites of
  `newInteractiveAgent` (`cmd_serve.go`, `cmd_tui.go`) passed `0` for its
  `maxIterations` parameter, making the parameter dead in practice — nothing
  ever overrode `interactiveMaxIterations`. Meanwhile `newTelegramAgent` was a
  near-duplicate of `newInteractiveAgent`, differing only by always passing
  `interactiveMaxIterations` and adding `telegram.Directive`.
- `newInteractiveAgent` now takes `directives ...string` instead of
  `maxIterations int` — it always builds at `interactiveMaxIterations`, and
  `directives` supplies whatever extra system-prompt blocks a transport needs
  (empty for TUI/serve, `telegram.Directive` for Telegram). `newTelegramAgent`
  is gone; `cmd_telegram.go` calls `newInteractiveAgent(*scheduler,
  telegram.Directive)`. No behavior change — same agents, same iteration cap,
  same directive wiring — just one function instead of two near-duplicates.

## [0.73.35] - 2026-07-25

### Agent — raised MaxIterations defaults; subagents get their own cap
- The 25/50 split felt tight for genuinely complex, multi-step work (explore
  code across files, edit several, run verification, iterate on failures) —
  easily 15-20+ iterations before real work even starts.
- Hitting the cap is non-destructive (the session reserves one iteration for
  a progress-summary call and tells the user via
  `EventMaxIterationsReached`/"⚠ reached the N-iteration limit"), so a higher
  ceiling has no safety cost — it only avoids interrupting real work
  needlessly. Context growth is independently guarded by auto-compaction at
  98% usage, so this isn't competing with that safeguard.
- New values:
  - **SDK / one-shot commands default: 25 → 50**
    (`agent.defaultMaxIterations`, used when `AgentOptions.MaxIterations` is
    unset — `mcp`/`memo`/`settings`/etc. commands, and any SDK caller that
    doesn't set it explicitly).
  - **Interactive transports (TUI, `harness serve`, Telegram): 50 → 120**
    (new `interactiveMaxIterations` in `internal/cli/agent.go`) — this is
    where real complex work actually happens.
  - **Subagents: now capped at 50** (new `agent.subagentMaxIterations`),
    regardless of the parent's own limit. Previously a subagent inherited the
    parent's `MaxIterations` outright, so a subagent spawned from an
    interactive session (120) had as much room as the parent driving the
    whole task — a subagent is a focused, delegated task
    (`subagentSystemPrompt`), not the primary agent, and shouldn't be able to
    silently burn through a comparable budget before the parent even knows
    it's still working. Wired as `min(parentA.maxIterations,
    subagentMaxIterations)`, so a parent configured lower (e.g. the SDK
    default 50) is never overridden upward either.
- New tests (`agent/agent_iterations_test.go`): `TestDefaultMaxIterations`,
  `TestExplicitMaxIterationsOverridesDefault`,
  `TestSubagentMaxIterationsIsCapped` pin all three values so a future change
  is a deliberate edit, not an accidental drift.

## [0.73.34] - 2026-07-24

### Breaking — renamed `MaxTurns` → `MaxIterations` everywhere
- The name was a misnomer: the value has always been the cap on ReAct
  iterations WITHIN a single turn (`for i := range s.maxIterations-1` in
  `Session.promptSync`), not a count of user↔agent turns. `MaxIterations`
  names the actual concept. (History note: an earlier release had renamed
  the opposite direction, `MaxLoops` → `MaxTurns` — this reverts that
  decision in favor of the more precise term.)
- **Public API (breaking for SDK/API consumers):**
  - `agent.AgentOptions.MaxTurns` → `AgentOptions.MaxIterations`
  - `harness.WithMaxTurns(n)` → `WithMaxIterations(n)`
  - `Agent.MaxTurns()` → `Agent.MaxIterations()`, `Session.MaxTurns()` →
    `Session.MaxIterations()`
  - `types.EventMaxTurnsReached` → `EventMaxIterationsReached`;
    `types.Event.MaxTurns` → `Event.MaxIterations`
  - HTTP/SSE wire format: the session JSON field `max_turns` → `max_iterations`
    (`GET/POST /api/sessions*`); the SSE event `"type":"max_turns_reached"`
    → `"max_iterations_reached"` with payload key `max_turns` → `max_iterations`.
    Any external client speaking harness's HTTP/SSE API directly (not just the
    in-repo TUI/Telegram transports, which were updated alongside) must adopt
    the new field/event names.
- **Internal-only (no compatibility impact):** every private field, comment,
  log/error string, and test referencing turns-as-iterations was renamed to
  match — `agent/session.go`, `agent/agent.go`, `agent/prompts.go`
  (`maxTurnsPrompt` → `maxIterationsPrompt`; the prompt TEXT shown to the model
  is unchanged, only the Go constant name), `internal/cli/agent.go`,
  `internal/transport/tui/*.go`, `internal/transport/telegram/pump.go`,
  `internal/server/proxy_test.go`, `README.md`.
- No behavior change: still defaults to 25, still reserves one iteration for
  the progress-summary call, still the same `EventMaxIterationsReached` →
  progress-summary → `EventTurnEnd` sequence. Full suite (including `-race`)
  verified clean after the rename.

## [0.73.33] - 2026-07-24

### TUI — fix off-by-one in Stop()'s cursor parking (extra blank line on exit)
- `render.TUI.Stop()` is supposed to park the cursor on the line immediately
  after the last rendered line, so whatever prints next (the TUI's
  "👋 Goodbye!" farewell) is separated by exactly one blank line — which is
  what its caller's comment in `internal/transport/tui/tui.go` assumed.
- It overshot by one: the last content line is at index `prevLen-1`, but the
  move targeted `prevLen` (already one row past the content) and *then* wrote
  a CRLF, landing two rows below. Confirmed in a pty capture as a literal
  `\x1b[1B\r\n` right before the farewell. Result: the goodbye block rendered
  one line lower than intended.
- Fixed by targeting `prevLen-1` and letting the single CRLF do the final
  step. The CRLF (not a `MoveDown`) has to be what advances the line: this
  renderer is inline, so when content fills the screen the cursor is already
  on the last physical row, where `CSI B` saturates and does nothing while a
  CRLF scrolls the terminal to create the new line.
- New tests `TestStopParksCursorOneLineBelowContent` and
  `TestStopMovesUpWhenCursorIsBelowLastLine` (in
  `internal/transport/tui/render/stop_test.go`) assert the emitted sequence in
  both directions; both were verified to fail against the pre-fix code.
- Investigated alongside this: the blank row that stays below the footer
  *during normal use* is **not** a bug. Verified via a pty capture and a
  component-tree dump that both the render tree and each frame end exactly on
  the footer line, with no trailing blank emitted — that row is where the
  cursor lives, and an inline renderer can't write the last physical row
  without a subsequent CRLF scrolling the view and desyncing the diff math.
  PI behaves the same way for the same reason.

## [0.73.32] - 2026-07-24

### TUI — fix the extra blank line below the welcome banner
- The startup banner rendered with one blank line too many below it — visible
  as an unexpected gap above the input separator (and, by extension, in the
  spacing around the footer/goodbye output).
- `welcomeBanner` builds its text with an `add` helper that appends `'\n'`
  after every line, so its final `add("")` — meant to leave "one blank line
  below the tip so the editor doesn't sit flush against it" — left the string
  ending in `"\n\n"`: one newline closing the tip's line, one from `add("")`
  itself. `RawBlock.Render` wraps that through `ansi.WrapTextWithAnsi`, which
  does `strings.Split(text, "\n")`, and a trailing newline yields a final
  empty element (Go semantics: `"a\n".Split("\n") == ["a", ""]`). Two trailing
  newlines therefore produced **two** blank lines; combined with the idle
  spinner's own blank line (`components.Spinner.Render` returns `[""]` while
  stopped), the gap was three blank lines instead of the intended two.
- Fixed by trimming exactly one trailing newline in `welcomeBanner`. The
  remaining newline still closes the tip line and still splits into the single
  blank line the banner is supposed to leave; the blank lines built above it
  are real content and untouched. Verified in a pty: the separator moved from
  content row 10 to row 9, with two blank lines above it instead of three.
- New test `TestWelcomeBannerEndsWithExactlyOneBlankLine` asserts both the
  rendered output (last line blank, second-to-last not blank) and the root
  cause at the source (the string must not end in a double newline).

## [0.73.31] - 2026-07-24

### Server/TUI — SSE control events (turn_end/stop/error) never silently dropped
- Investigated a second field-reported freeze (spinner stuck, Esc
  unresponsive) after the earlier provider-stream fix (0.73.26) — this one
  happened on the already-patched binary, so a different cause was in play.
  Found it by reading the SSE fan-out path: `SessionProxy.broadcast` sent to
  each client's channel non-blocking — `select { ch <- line; default: }` —
  and **silently dropped the event** if the channel was full, with no
  distinction between a harmless streaming delta and a critical lifecycle
  event. A `thinking:high` turn with a long response can emit thousands of
  small delta events (one per token); if the render loop on the other end
  fell behind a burst for a moment, the buffer filled and whatever event
  landed at that instant — including `turn_end` or `stop` — was lost. The
  agent had genuinely finished (or been cancelled), but the TUI never saw the
  signal: spinner stuck forever, Esc looking like it did nothing (it had
  already worked server-side).
- `broadcast` now distinguishes the two: high-volume streaming deltas
  (`thinking`/`text`/`tool_args` deltas) are still sent non-blocking — losing
  one is harmless, and s.emit calls broadcast synchronously from the agent's
  ReAct loop, so this path must never stall a turn. Everything else (new
  `isControlEvent`, a denylist of just those three droppable types — so an
  unlisted future event type defaults to protected) gets a bounded blocking
  retry (`controlBroadcastTimeout` = 500ms) before falling back to a dropped
  + logged warning as an absolute last resort, instead of a silent drop.
- Bumped both ends of the SSE pipe's per-client buffer from asymmetric,
  under-sized values (server 1024, TUI client 64 — the consumer had the
  *smaller* buffer despite being the actual bottleneck) to a matched 4096 on
  each side (`sseClientBufferSize` in `internal/server/server.go`,
  `tuiClientEventBufferSize` in `internal/transport/tui/client.go`). Sized to
  absorb a multi-thousand-delta burst from a long thinking:high response
  without either end starving the other; worst-case memory (a few hundred
  bytes per event × 4096) is a few MB — negligible.
- **The dropped-event warning is gated by the same `Server.verbose` flag that
  already gates `requestLogger`** — it does NOT log unconditionally.
  `SessionProxy` now carries `verbose` (threaded through `newSessionProxy`
  from `Server.verbose`) specifically because the TUI's in-process server
  (`internal/transport/tui/server.go`) always runs with `Verbose: false`: it
  shares stdout/stderr with the raw-mode terminal renderer, and `broadcast`
  runs on the agent's own event-emitting goroutine — an unconditional
  `log.Print` there (Go's default logger writes to stderr) would have
  corrupted the TUI's display. `harness serve` / Telegram run with
  `Verbose: true` and do want the warning visible.
- New tests (`internal/server/proxy_test.go`): `TestIsControlEvent` locks in
  the denylist; `TestBroadcastDropsStreamingDeltaOnFullChannel` verifies
  deltas never block; `TestBroadcastRetriesControlEventOnFullChannel`
  verifies a control event waits out a momentarily-full channel and still
  gets delivered; `TestBroadcastDropsControlEventAfterTimeoutOnDeadClient`
  verifies a truly dead client can't hang the agent loop forever;
  `TestBroadcastSilentWhenNotVerbose` / `TestBroadcastLogsWhenVerbose` lock in
  the verbose gating in both directions. Full suite verified clean under
  `-race`.
- Also added a **temporary** diagnostic endpoint mounted at `/debug/pprof/*`
  on the internal server (`net/http/pprof`, wired explicitly into the chi
  router since this server doesn't use `http.DefaultServeMux`) — lets a live,
  possibly-hung harness process be inspected over loopback HTTP
  (`curl http://127.0.0.1:<port>/debug/pprof/goroutine?debug=2`) with zero
  risk, unlike attaching a debugger (which killed the hung process outright
  during this investigation — a known macOS ptrace/Delve hazard). Kept in
  place for future diagnosis of freezes or leaks, not just this one.

## [0.73.30] - 2026-07-24

### TUI — footer shows "(turn/max_turns)" while the agent is working
- The footer's session-info line (`~/path • session-name`) now shows the
  current ReAct iteration out of the per-turn cap while the agent is actively
  working, e.g. `~/path • kaiban-api-v2 (2/50)`. Appears on `turn_start`
  (reset to 0) and increments on each `loop_start`; hidden again on
  `turn_end`, per the requested behavior — it's only meaningful while
  something is in flight. Sits before the existing `[N queued]` badge.
- New `Session.MaxTurns()` / `Agent.MaxTurns()` getters expose the per-turn
  ReAct cap (previously private, never surfaced to any client). `GET/POST
  /api/sessions*` now returns `max_turns` alongside the existing session
  fields (`sessionInfoDTO` wraps `store.SessionMeta`) — used by
  `handleCreateSession`, `handleResumeSession`, and `handleGetSession`.
- Found and fixed a real, pre-existing data race while adding tests for this:
  `components.TruncatedText` (the type backing the footer's info/stats lines)
  had no internal synchronization — `SetText` (called from the SSE
  event-consumer goroutine on every turn/loop/tokens event) and `Render`
  (called from the render-scheduler goroutine) raced on the same field.
  `RawBlock`/`History` already guard themselves the same way; `TruncatedText`
  now does too.
- New tests `TestTurnCounterShownWhileWorkingOnly` and
  `TestTurnCounterResetsOnNewTurn` lock in the counter's visibility and
  reset behavior. Full suite (including `internal/transport/tui`) verified
  clean under `-race -count=5`.

## [0.73.29] - 2026-07-24

### TUI — spinner stays on after a mid-turn auto-compact
- Auto-compaction fires *inside* the ReAct loop, between iterations (when
  `ContextUsage >= 0.98`), not at the end of the turn — the `for` loop in
  `promptSync` keeps going into another `loop_start` right after, and the
  model keeps working (e.g. the auto `MemoSearch` call nudged by the new
  compaction-checkpoint memory reminder). The TUI's `compact_end` handler
  correctly turns the spinner off (that sub-step finished), but nothing ever
  turned it back on for the continuing work — the TUI never handled
  `loop_start` at all, so a mid-turn compact left the agent visibly "frozen"
  (no spinner) while it kept calling tools and streaming text.
- `loop_start` now re-asserts the spinner. Covers this case and any future
  one where a mid-turn event silences it, without every such event needing to
  know to turn it back on individually.
- Found and fixed a related data race while adding tests: `TUI.spinning` was
  read/written without synchronization from multiple goroutines (the SSE
  event consumer vs. input handling). `setSpinning` now guards the field with
  the existing `t.mu`, and a new `isSpinning()` getter replaces the two
  direct reads in `commands.go`/`layout.go`.
- New tests `TestSpinnerStaysOnAfterMidTurnCompact` and
  `TestSpinnerOffAfterCompactEndThenTurnEnd` lock in both the fix and the
  complementary case (compact really is the turn's last step, e.g. a manual
  `/compact` with no follow-up tool calls) — the spinner must still turn off
  once `turn_end` arrives, not get stuck on forever.

## [0.73.28] - 2026-07-24

### Agent — compaction checkpoint nudges the model toward memory (when enabled)
- After compaction, the working set collapses to a single checkpoint message
  — the model's nearest context is now a dense summary, not the system
  prompt further up. That's exactly the kind of "lack context about earlier
  work" moment the existing `## Memory` system-prompt block already tells the
  model to use `MemoSearch` for, but a system-prompt instruction is easy to
  under-weight right after a context reset.
- The persisted compaction checkpoint now gets a short, suggestive (not
  imperative) reminder appended — "you have persistent memory, it may be
  worth searching before assuming context is gone" — **only when the session
  has memory enabled**, mirroring the exact same condition
  (`Agent.memStore != nil`) that gates the system-prompt Memory section. A
  session without memory enabled gets no reminder and no behavior change.
- The reminder is appended to what's **persisted** (the checkpoint message in
  the session log), not to what's shown to the user: `EventCompactEnd.Summary`
  still carries the LLM's clean summary text, so the TUI/CLI compaction output
  is unaffected.
- `Session` gained a `hasMemory bool` field, set once at construction (both
  `NewSession` and `ResumeSession`) from the same `a.memStore != nil` check
  `buildSystemPrompt` already uses — no new logic, just threading the existing
  decision one level down. The checkpoint text is built by a new pure
  function, `buildCompactionCheckpoint(summary, hasMemory)`, kept in
  `agent/prompts.go` next to the other compaction prompt constants and
  covered by `agent/prompts_test.go` without needing to stand up a full
  Session (provider/store/tools mocks).

## [0.73.27] - 2026-07-24

### Tools — malformed tool-call input always feeds back to the model, audited
- Investigated whether a tool-call input JSON error (the model builds a
  malformed argument, e.g. `{"path":"x","offset":183,200}` — a value where a
  key was expected) reliably reaches the model as feedback so it can
  self-correct on the next turn. Confirmed the happy path already works:
  `runStream` persists a failing tool's `{output, is_error:true}` as the
  `tool_result` message the model reads back, with a safety-net fallback
  (`output = execErr.Error()` when a tool returns `("", err)`) because
  Anthropic 400s on an `is_error` tool_result with empty content.
- **Found and fixed an inconsistency**: `MemoWrite`, `MemoSearch`,
  `MemoDelete`, `Schedule`, `ScheduleDelete`, `Skill`, and `Subagent` returned
  `("", fmt.Errorf(...))` on a bad input — relying entirely on that generic
  fallback instead of reporting their own message. Aligned all seven to the
  same explicit pattern every other built-in already used
  (`fmt.Sprintf("Error parsing input: %v", err), err`), so no tool depends on
  an implicit safety net for its most common failure.
- **New schema + behavior audit** (`agent/tools/schema_audit_test.go`),
  covering every built-in tool:
  - `TestBuiltinToolSchemasAreWellFormed` — every `InputSchema` sent to the
    model is valid JSON, declares `"type":"object"`, and every `"required"`
    name exists in `"properties"` with its own `"type"`. All 13 built-in
    schemas pass.
  - `TestBuiltinToolReturnsNonEmptyOutputOnBadInput` — feeds 4 malformed
    payloads (including the exact field-reported shape) to every built-in
    tool and asserts the output is never empty alongside an error. 52
    combinations, all pass.
  - `TestBuiltinToolNamesAndDescriptionsNonEmpty` — no empty/duplicate names
    or descriptions (both sent to the model verbatim).
- **Audited the definitions pipeline** end to end: `Registry.Definitions()` →
  provider tool builders (`defaultAnthropicTools`, `buildOAuthTools` for Claude
  Code stealth, the OpenAI-compatible builder) → `json.Marshal` of the wire
  request. Confirmed `InputSchema` is forwarded byte-for-byte everywhere; only
  `Name` is ever rewritten (Claude Code's Fetch→WebFetch / mcp__ext__
  stealth mapping), and there is no size limit or truncation on the marshaled
  request that could silently corrupt a schema in transit.

## [0.73.26] - 2026-07-24

### Providers — root-caused: LLM stream parser now respects Stop()/ctx everywhere
- Found the real cause of the field-reported freeze (spinner stuck on, Esc
  unresponsive, whole turn hung) after the prior MCP-only fix (0.73.25) didn't
  explain a repro where the only tool involved was the built-in `Read`
  (instant parse-error return, not a hang). Root cause was one level up: the
  **shared SSE parser every LLM provider streams through**,
  `internal/providers/llm.ParseSSE`, had the identical bug — a `bufio.Scanner`
  loop with no `ctx` awareness.
- In a long session (68+ turns, hundreds of MB of cache, `thinking: high`), if
  the model's response stream stalls mid-generation — connection stays open,
  keep-alives, but no more real content — `Scan()` blocks in a read syscall
  forever. Nothing observed `ctx.Done()`, so Stop()/Esc had no effect: the
  whole ReAct iteration (`runStream`'s `wg.Wait()`) waited on a stream that
  would never finish. This affects **every provider** — Anthropic, Claude
  OAuth, OpenAI, MiniMax, Ollama, Ollama Cloud, OpenCode Go — since they all
  funnel through the same `ParseSSE`.
- `ParseSSE` now takes a `ctx`: the scan runs in its own goroutine, raced
  against `ctx.Done()`. On cancellation, if the reader is an `io.Closer` (true
  for HTTP response bodies — the only real-world caller), it's closed, which
  turns the blocked `Scan()` into an I/O error and unblocks everything
  downstream. `ParseAnthropicStream` and `parseOpenAIStream` (and therefore
  `DoAnthropicStream`/`DoOpenAIStream`) now thread `ctx` through to it.
- New tests: `TestParseSSEContextCancelUnblocks`,
  `TestParseAnthropicStreamContextCancelUnblocks`,
  `TestParseOpenAIStreamContextCancelUnblocks` — each simulates a stalled
  stream (a reader that blocks forever until closed) and asserts cancellation
  unblocks the call within ~20ms instead of hanging.
- Still investigating the malformed-JSON `Read` tool call seen alongside the
  freeze in the field report — the input error itself returns instantly and
  is not the hang; it's a red herring / concurrent symptom of the same
  session's model output, not the frozen path. This fix addresses the
  confirmed, reproducible root cause (a stalled provider stream ignoring
  cancellation); Stop()/Esc should now reliably recover from it.

## [0.73.25] - 2026-07-24

### MCP — remote (HTTP/SSE) transport now respects Stop()/ctx cancellation
- `HTTPTransport.readSSEResponse` parsed the SSE stream with a plain
  `bufio.Scanner` loop that never checked `ctx.Done()`. A remote MCP server
  that opens an SSE stream and stalls (or never sends the matching response)
  left `Scan()` blocked in a read syscall forever — Stop()/Esc cancels the
  turn's `ctx`, but nothing was watching it, so the tool-call goroutine (and
  the `wg.Wait()` in `runStream` waiting on it) hung indefinitely. Symptom:
  the whole turn froze — spinner stuck on, TUI unresponsive to Esc — with a
  parallel tool call to a remote MCP server anywhere in the batch.
- The scan now runs in its own goroutine, raced against `ctx.Done()` via
  `select`. On cancellation the response body is closed, which turns the
  blocked `Scan()` into an I/O error and lets the goroutine exit — Stop()
  now actually unblocks a stalled remote MCP call. New test
  `TestHTTPSSEContextCancelUnblocks` simulates a server that opens SSE and
  never answers, and asserts the call returns near the ctx deadline instead
  of hanging.
- Investigated after a field report of a fully frozen TUI (spinner stuck,
  Esc unresponsive, tool JSON rendered raw because the model produced a
  malformed argument). That specific session used only local/stdio MCP
  servers, so this fix addresses a confirmed real bug but not necessarily
  that exact incident — root-causing the local-only case continues.

## [0.73.24] - 2026-07-23

### CLI — `mcp` command: inferred transport + enable/disable
- **`--local` / `--remote` flags removed.** `harness mcp add` now infers the
  transport from which of `--command` (local) or `--url` (remote) you pass —
  exactly one is required. Matches the settings.json inference.
- **New `harness mcp enable <name>` / `harness mcp disable <name>`.** Toggle a
  server's `disabled` flag without editing JSON or losing its config
  (command/url/env/headers are preserved). `disable` keeps the entry; `enable`
  drops the `disabled` field (enabled is the default).

  ```
  harness mcp add fs --command "npx -y @mcp/fs"
  harness mcp add api --url https://mcp.x --bearer TOKEN
  harness mcp disable everything
  harness mcp enable everything
  ```

### Config — simpler, inferred MCP server shape in settings.json
- **`type` is gone — the transport is inferred.** A server with a `command`
  is local (stdio); a server with a `url` is remote (HTTP). Setting both is
  rejected as ambiguous; setting neither is rejected as empty. No more writing
  `"type": "local"` / `"remote"` by hand.
- **`enabled` → `disabled` (opt-out).** Servers are enabled by default; add
  `"disabled": true` to turn one off without deleting it. No more tagging every
  server with `"enabled": true`.
- **Command shape is `command` (string) + `args` (array)** — the standard used
  by Claude Desktop / MCP clients: `{"command":"npx","args":["-y","@mcp/fs"]}`.
- New helpers `MCPServer.IsRemote()` and `MCPServer.Argv()` centralize the
  inference and the local command line. `harness mcp add` and `mcp list` emit
  and read this shape; `--local/--remote` and `--disabled` flags unchanged.
- **Breaking:** only this shape is accepted — no compatibility with the old
  `type` / `enabled` / command-as-array format. The struct uses plain JSON
  decoding (no custom `UnmarshalJSON`), keeping the config code minimal.
  Existing `~/.harness/settings.json` files must be migrated to the new shape.

  Before:
  ```json
  "fs": { "type": "local", "command": ["npx","-y","@mcp/fs"], "enabled": true }
  ```
  After:
  ```json
  "fs": { "command": "npx", "args": ["-y","@mcp/fs"] }
  ```

## [0.73.23] - 2026-07-23

### TUI — native scrollback stays put while the agent streams
- Reading earlier output by scrolling up (with the terminal's own mouse-wheel
  scrollback) no longer gets yanked back to the bottom every time the agent
  emits new content — a tool call, a thinking block, streamed text, or a
  completed markdown table. The view now stays exactly where you left it while
  new content flows in below, and snaps to the end only when you scroll back
  down yourself.
- Root cause: two render branches issued a full repaint that moved the cursor
  UP inside the active region and rewrote it. That makes the terminal
  re-anchor its viewport to the bottom, so a scrolled-up reader was kicked to
  the end on nearly every streaming tick. PI (whose renderer harness is ported
  from) has neither branch — it always falls through to the incremental
  per-line path, which only appends with `\r\n` or rewrites visible lines in
  place and never yanks the viewport. Harness now matches PI:
  - **clear-on-shrink is now opt-in and off by default** (`SetClearOnShrink`,
    mirroring PI's `PI_CLEAR_ON_SHRINK`). The shrink repaint fired constantly
    during streaming because content height oscillates (spinner appears /
    disappears, "Executing…" becomes a result, a thinking block collapses).
  - **the "mixed change" table-flush full-repaint branch was removed** (and the
    now-unused `isPureShift` helper). A mid-buffer line changing while lines are
    appended (e.g. a markdown table completing) now takes the incremental path.
- Net effect: streaming is smooth (no flick on table renders) and the terminal
  scrollback behaves like PI's — scroll up to read anytime, even mid-turn.

## [0.73.22] - 2026-07-23

### TUI — remove in-app history scrolling (revert the scroll pin)
- Removed all in-app history scrolling: the keyboard scroll bindings
  (PageUp/PageDown/Home/End), the `scrollOffset`/`userViewportTop` pin, the
  `renderFromTop` repaint path, the `clearRelativeFromTop` clear mode, and the
  `SetScrollOffset`/`ScrollOffset` API. This reverts the scroll-pin work from
  0.73.14–0.73.18.
- Rationale: the pin was a purely logical scroll simulation layered on an
  INLINE renderer (no alternate screen). It relied on the hardware cursor row
  staying perfectly in sync AND the terminal not moving its own scrollback.
  With content taller than the screen, streaming new text desynced the cursor
  math and terminals (e.g. Ghostty) followed the output back to the bottom —
  so the user's reading position was dragged down anyway. The feature caused
  more glitches than it was worth for a narrow window (reading history *while*
  the model streams).
- New behavior: the TUI always sticks to the bottom (tail-follow) while a
  turn streams. To read earlier output, use the terminal's own native
  scrollback (and native mouse selection) once the turn completes — the
  common, reliable path across every terminal.

## [0.73.21] - 2026-07-23

### Providers — canonical tool_use IDs (cross-provider session resume)
- Tool-call IDs returned by each provider (Anthropic `toolu_…`, OpenAI
  `call_…`, Gemini `functions.<name>:<index>`) are now canonicalized into a
  single harness format — `toolu_<24 base62 chars>` — at the moment they
  enter the harness types, before anything is persisted to the session
  store. The conversion is deterministic: the same native ID always maps
  to the same canonical ID, so `tool_use` ↔ `tool_result` correlation
  survives across sessions and providers with no mapping table.
- `toolu_<base62>` satisfies every provider's ID constraints (Anthropic's
  `^[a-zA-Z0-9_-]+$` is the strictest), so a session created with one
  provider can be resumed against any other without a 400 on a foreign
  ID. Anthropic-native IDs already use this exact shape and round-trip
  unchanged.
- New helper `internal/providers/llm/id.go` (`ToolIDFor`, `isCanonicalID`,
  `base62Encode`). Applied at the Anthropic and OpenAI stream parsers
  where `ToolCall.ID` and the streaming `ToolID` are assigned; the
  Claude OAuth path inherits the fix via the shared Anthropic parser.
- Existing on-disk sessions written before this change hold native IDs
  and must be migrated by an external script; the code assumes
  on-disk IDs are already canonical.

## [0.73.20] - 2026-07-22

### MCP — accept Claude Desktop / OpenCode `command` + `args` shape
- `MCPServer.UnmarshalJSON` now accepts both the canonical
  `command: ["uvx", "arg1"]` shape and the Claude Desktop / OpenCode
  `command: "uvx", args: ["arg1"]` shape. Without this, copying a config
  from another MCP client silently dropped the fields and the server
  failed to connect with an unhelpful "empty command" error swallowed by
  the manager.


## [0.73.19] - 2026-07-22

### TUI — fix concurrent race in `History.Render` (root cause of long-history flick)
- `History.Render` walked `blocks` without holding the slice's lock, while
  the SSE event handler appended to the same slice via `Add`. In long
  resumed sessions where many streaming chunks land during a render, the
  render loop could append a block mid-iteration, producing a torn render
  (the previous frame's characters painted on top of the new one — the
  faint "old text bleeding through" the user reported). Symptoms were most
  visible during long histories because the render loop walks more blocks
  per frame, widening the window for the race.
- `History` now carries a `sync.Mutex` and every method that touches
  `blocks` (`Add`, `Render`, `Blocks`, `Len`, `Last`, `Clear`, `Invalidate`)
  acquires it. The render is atomic w.r.t. concurrent appends.

### TUI — test helpers moved to `export_test.go`
- The two `*ForTest` helpers for `userViewportTop` were moved out of
  `render/tui.go` (production code) into `render/export_test.go`, which is
  compiled only during `go test` and never ships in the binary.

### TUI — also clear scroll state on `harness --resume` boot
- The previous fix reset the scroll pin for `/resume` (in-session switch),
  but `harness --resume SESSION` took a different code path (`autoConnect`)
  that did not call `resetForNewSession`. Although the render.TUI starts
  fresh on boot, the explicit reset is now applied in both resume paths
  so the new session always begins at the bottom with no leftover pin.

## [0.73.17] - 2026-07-22

### TUI — reset manual scroll state on session resume
- A user who scrolled up in session A and then ran `/resume B` kept the
  pinned viewport top from A. The new session's history was rendered with
  `renderFromTop(topRow=N)` against rows that didn't exist in B — producing
  an empty viewport, missing cursor, and a scroll position that silently
  jumped to the wrong place.
- `resumeInPlace` now calls `resetForNewSession`, which clears the scrollback,
  the live markdown pointer, the last-section kind, the stats, and snaps the
  scroll position to the bottom (clearing `scrollOffset` and the renderer's
  pinned `userViewportTop`). The user can scroll up again in the new session.

## [0.73.16] - 2026-07-22

### TUI — editor scrolls and shows "↑ N more" for long wrapped paragraphs
- Typing a long paragraph (no embedded newlines) used to pin the editor
  viewport to the top of the buffer once word-wrapping pushed the cursor
  past 5 rows. The "↑ N more" separator hint stayed empty and the cursor
  silently disappeared below the visible window.
- `editor.layout` now counts WRAPPED rows up to the cursor position (not
  just `\n` count), so `HiddenAbove` reports the true number of rows scrolled
  off the top and the cursor stays in view. Existing `\n`-separated behaviour
  is unchanged.

## [0.73.15] - 2026-07-22

### TUI — new thinking after tool calls no longer overwrites old blocks
- When a streaming `thinking` delta arrived after one or more tool calls,
  the renderer kept editing the FIRST thinking block in place. The new
  reasoning visually appeared above the tool calls (where the old block
  sat), not after them where it chronologically belonged.
- The frozen-state reset in `consumeEvents` now also drops the `thinkBlk`
  pointer. A new thinking fragment after a tool call creates its own block
  at the end of the history via `addSection("thinking")`.

## [0.73.14] - 2026-07-22

### TUI — pin the user's scroll position while the agent streams
- Scrolling up to read history used to drag the user's view back toward the
  bottom every time the agent emitted new content. The renderer recomputed
  the viewport top as `contentHeight - height - scrollOffset` on every render,
  so growing content pushed the read line down.
- The renderer now pins the viewport top at the line the user was reading
  (`userViewportTop`) when `scrollOffset > 0`. New streamed content fills in
  below the pin; pressing End (`scrollToBottom`) clears the pin and snaps to
  the new end. Multiple idle spinner ticks no longer drift the viewport either.

## [0.73.13] - 2026-07-22

### TUI — fix flick when streaming content crosses the wrap point
- During agent streaming (spinner active), each spinner tick fires a re-render
  at ~80ms. When the streamed text crossed the wrap point, the renderer saw
  the spinner lines as having "changed" (their position shifted down by 1) and
  took the full-repaint branch — producing a visible flick every wrap crossing.
- The renderer now distinguishes a real content change before the last line
  (e.g. a buffered markdown table flushing) from a pure positional shift
  (the lines below a new wrap just moved down by one slot). When `isPureShift`
  reports true, the incremental Strategy 3 path handles the rewrite without
  clearing the screen.

## [0.73.12] - 2026-07-22

### TUI — reserve right-side padding in markdown tables
- Each table column now reserves 1 column of right-side padding between the
  cell text and the `│` border. Some terminals render emoji ZWJ / VS-16
  sequences (e.g. 👨‍💻, 🏳️‍🌈, 🇺🇸) wider than uniseg reports — without this
  slack, the wider glyph would overwrite the border and break the column
  alignment. The reservation is baked into the column-width calculation, the
  wrap budget, the border dash count, and the trailing pad of each cell so
  every row stays flush.

## [0.73.11] - 2026-07-22

### TUI — consistent single-line tool results (success and error)
- Tool results are now formatted with a single rule that applies to both
  success and error: single-line output is shown verbatim, multi-line output
  is summarized as `(N lines)`. Previously, a successful multi-line tool
  result was summarized as `(N lines)` but a failed one was collapsed with
  `collapseWhitespace` into a single line containing the ENTIRE output,
  flooding the scrollback with build traces, test reports, fetched HTML, and
  stack traces wrapped to the terminal width.
- The full output still flows unchanged to the LLM and to persisted session
  history; only the visual summary in the TUI changed.

## [0.73.10] - 2026-07-22

### TUI — restore native text selection
- Disabled mouse button-event tracking (`\x1b[?1002h`) that was breaking the
  terminal's native click+drag text selection. With that mode active, the
  terminal intercepted every mouse event and forwarded it as an ANSI sequence
  to the TUI, so users could no longer click+drag to copy the agent's output.
- Mouse-wheel scroll is no longer supported; scroll is still available via
  keyboard (PageUp/PageDown/Home/End). Keyboard scrolling preserves native
  text selection and is the standard for inline TUIs that share the terminal
  with the shell.

## [0.73.9] - 2026-07-22

### TUI — fix interleaved thinking/tool rendering and scroll-to-bottom redraw
- Fixed a bug where resuming thinking after an intervening text or tool-call
  block duplicated earlier reasoning content, making it look like the new
  thinking overwrote prior tool-call output. The thinking buffer is now reset
  when the previous thinking block is frozen, so each resumed reasoning
  fragment starts a fresh block containing only its new deltas.
- Fixed a bug where returning from manual scroll (scrollOffset > 0) back to
  "stick to bottom" corrupted the input/session/footer area. The renderer now
  detects the transition and issues a full relative redraw, re-anchoring the
  viewport to the end of the content instead of reusing the scrolled viewport
  top.

## [0.73.8] - 2026-07-22

### TUI — show structured error details
- The TUI now renders the structured `details` payload from `EventError`, not
  just the human-readable `message`. When a provider returns a structured error
  (e.g. OpenAI rate-limit JSON), the JSON is pretty-printed and shown inline in
  a dimmed block below the error message, limited to 20 lines with an ellipsis
  if longer. This matches the behavior already present in the Telegram transport
  and removes the need to guess what the provider actually responded.

## [0.73.7] - 2026-07-22

### TUI — fix interleaved thinking style + empty summary reporting
- Replaced the `thinkingClosed` flag with `thinkingFrozen`: the current
  reasoning block is frozen in place when text/tool content starts (preventing
  the previous collapse flicker), but a later `thinking` delta now creates a
  fresh Dim+Italic block instead of being silently dropped. This fixes the
  bug where some reasoning blocks lost their style or appeared as plain text
  in models that emit interleaved thinking/content deltas
- `requestProgressUpdate` now returns an explicit error when the model emits an
  empty summary at the max-turns cap. Previously the warning
  "⚠ reached the N-turn limit — summarizing progress" was followed by silence;
  now `drainFollowUps` emits `EventError` so the user sees
  `✘ model returned an empty summary; conversation capped at N turns` instead
  of a seemingly hung turn

## [0.73.6] - 2026-07-22

### TUI — manual scrollback with mouse and keyboard
- The TUI now supports reading earlier conversation output without losing your
  place while the model is streaming. Mouse wheel scroll and PageUp/PageDown
  keys shift the viewport up; PageDown, End, or sending a new prompt snaps back
  to the bottom
- Mouse tracking is enabled via the SGR extended protocol (button-event mode),
  so scroll-wheel events are reported without flooding on every mouse
  movement. `stdin_buffer.go` already parsed these events; the TUI now maps
  scroll-up/down to `scrollBy`
- New `render.TUI.scrollOffset` state: `0` means stick to the bottom (previous
  behavior); `>0` shows that many content lines above the bottom. When
  `scrollOffset > 0`, `doRender` uses a new `renderFromTop` path that repaints
  the visible window from the desired content row instead of forcing the view
  back to the end with CRLF scroll-up
- Reset triggers: `turn_start` (new model work), sending a prompt, or explicit
  `End`/scroll-to-bottom. This prevents the "I scrolled up to read something
  and the next token pulled me back down" problem
- `keys` package gains `PgUp`/`PgDown`/`Home`/`End` constants for the keyboard
  fallback

## [0.73.5] - 2026-07-22

### TUI — freeze thinking block to prevent thinking→text flicker
- During fast streaming, some OpenAI-compatible providers emit the entire
  reasoning block very quickly and then switch to `content` deltas. The TUI
  previously set `thinkBlk = nil` on the first text delta, which collapsed the
  thinking `RawBlock` and caused the renderer to repaint the region — a
  visible micro-flicker where the text block above briefly jumped into the
  space the thinking block had just vacated
- Added `thinkingClosed` flag to the SSE event loop: once `text` or
  `tool_start` arrives, subsequent `thinking` deltas are ignored and the
  existing thinking block is left frozen in the scrollback. The new content
  then streams below it, eliminating the collapse/repaint jump. `turn_start`
  and `turn_end` reset the flag for the next turn
- `thinking_end` now only sets `thinkingClosed = true` instead of clearing
  the block pointer, so an empty thinking section that ends without text
  still stays visible (consistent behavior)

## [0.73.4] - 2026-07-22

### Patch release — TUI max-turns experience + OpenAI-compatible provider cleanup
- TUI per-turn ReAct cap raised to 50; the headless `serve` command and other
  transports keep the default 25
- `EventMaxTurnsReached` is now emitted before the progress-update summary,
  so the TUI warning reads as a forewarning and the model's streamed summary
  lands below it in natural reading order
- Errors from the final progress-update LLM call are no longer discarded;
  they propagate to `EventError` (with `ProviderAPIError` details lifted)
  instead of leaving the user with a "summarizing progress" notice and no
  output
- The max-turns summary-request message is marked
  `MessageMeta.IsSystemGenerated` and replayed in the TUI as
  `◎ progress summary requested` instead of as a `❯` user prompt the human
  never wrote
- `types.Message.MarshalJSON` now serializes `Meta`; this also fixes the
  latent bug where `IsCompaction` markers never reached the TUI over the HTTP
  API
- OpenAI-compatible stream parser (`parseOpenAIStream`) now strips leaked
  reasoning delimiters (`<thinking>`, `</thinking>`, abbreviated
  `<think>`/`</think>`, and HTML-comment variants) from `reasoning_content`,
  `reasoning`, and `content` deltas, preventing MiniMax and similar providers
  from bleeding `</thinking>` into the TUI thinking block and persisted history

## [0.73.3] - 2026-07-22

### TUI — hide system-generated summary request from history replay
- `requestProgressUpdate` (the max-turns fallback call) now marks its
  injected user message with `MessageMeta.IsSystemGenerated = true`. The TUI
  renders such messages as `◎ progress summary requested` instead of as a
  `❯` user prompt the human never typed, removing the confusing replay where
  the agent seemed to speak on behalf of the user
- `types.Message.MarshalJSON` now serializes `Meta` (previously it silently
  dropped the field). This is required for `IsSystemGenerated` to reach the
  TUI over the HTTP API, and it also fixes the latent bug where
  `IsCompaction` markers were never visible on resumed sessions rendered via
  the API either — the `◎ Compacting` / `✔ (history)` render in
  `renderHistory()` now works end-to-end

## [0.73.2] - 2026-07-22

### TUI — per-turn ReAct cap raised to 50
- The TUI now creates its agent with `MaxTurns: 50` (via
  `newInteractiveAgent(scheduler, 50)`), doubling the default 25 used by
  the headless server, one-shot CLI commands, and Telegram. Interactive
  coding tasks frequently span more tool iterations before a summary pause
  is appropriate, so the interactive terminal UI gets a longer leash without
  changing the SDK default or other transports
- `newInteractiveAgent` now takes a `maxTurns int` parameter; `0` keeps
  the default 25. The headless `serve` command still passes 0, leaving its
  sessions capped at the standard 25 iterations per turn

## [0.73.1] - 2026-07-22

### Agent — `max_turns_reached` fires before the progress-update summary
- `Session.promptSync` now emits `EventMaxTurnsReached` BEFORE calling
  `requestProgressUpdate`, not after. The TUI's
  `"⚠ reached the 25-turn limit — summarizing progress"` now arrives as a
  forewarning; the model's streamed summary lands below it in the correct
  reading order. The wording (`summarizing progress`, present participle)
  was always written for that order — only the implementation put it
  backwards
- **Bonus fix**: `requestProgressUpdate`'s error is no longer discarded.
  Previously `summary, _ := s.requestProgressUpdate(ctx)` swallowed
  failures silently, so a network/timeout/cancel during the final LLM call
  left the user staring at the "summarizing progress" warning with no
  summary and no error. Now the error propagates up through `promptSync`
  to `drainFollowUps`, which emits `EventError` (with `ProviderAPIError`
  details lifted, per v0.70.0) unless `ctx.Err() != nil` — i.e. user
  cancellation is still treated cleanly
- Telegram transport is unaffected: its drain ignores `max_turns_reached`
  for the chat reply (`case "max_turns_reached": flush + stopTyping`) and
  the streamed summary itself was already the user's visible output

## [0.73.0] - 2026-07-22

### Providers — strip leaked reasoning tags from OpenAI-compatible streams
- Some OpenAI-compatible providers (MiniMax in particular, even with
  `reasoning_split:true`) leak inline reasoning delimiters into the stream:
  the closing tag most often slips into the last `reasoning_content` delta,
  or the first `content` delta, at the thinking→answer transition. Result:
  literal `</thinking>` (and similar) bled into the TUI thinking block, the
  persisted session history, and resumption renders
- New `stripThinkingTags(s) (cleaned, stripped)` helper in
  `internal/providers/llm/openai.go` removes six delimiter variants — both
  full forms (`<thinking>...</thinking>`) and abbreviated forms
  (`<think>...</think>`), plus the HTML-comment style (`<!-- thinking -->`,
  `<!-- /thinking -->`). Applied to all three delta paths the parser
  handles (`reasoning_content`, `reasoning`, `content`), so streaming TUI
  render AND the persisted `NewAssistantToolCallMessage` both see the same
  clean text. Short-circuits with `strings.ContainsAny(s, "<")` so there's
  zero allocation when no tags are present
- When an entire delta is just a tag, the emit is dropped (no empty
  `StreamThinkingDelta`/`StreamTextDelta` reaches the SSE/TUI pipeline)
- Defense in depth: Anthropic is unaffected (wire-typed `thinking_delta`
  blocks never emit literal tags), and the strip also covers Qwen /
  DeepSeek / Ollama Cloud / OpenCode Go / OpenAI proper since they all
  funnel through the same `parseOpenAIStream`
- Seven regression tests in `internal/providers/llm/openai_test.go` lock
  every variant (closing tag in last reasoning delta, closing tag as first
  content delta, opening + closing together, HTML-comment style,
  abbreviated form, no-tags no-op, mixed-with-other-text strip)

## [0.72.0] - 2026-06-23

### Telegram — HTTP errors now render structured details too
- The API `do()` now returns a structured `harnessError{message, details}` for
  4xx/5xx instead of a plain string — a missing piece: the SSE `error` event
  already rendered `details` as pretty-JSON in a code fence (`formatError`),
  but HTTP errors were rendered as plain text. Now both paths produce the same
  rich output: `⚠️ <message>` + the details as pretty-printed JSON in a fence
- `replyError` is the single helper for showing errors in the transport:
  `harnessError` → `formatError(msg, details)`; any other error → `"⚠️ " + err.Error()`
- Removed the now-unused `errorMessage` helper from the telegram client (TUI
  and CLI keep theirs; their error display stays as plain text)

## [0.71.0] - 2026-06-23

### API — standardized action responses, clean compact-busy error
- Action endpoints now return a consistent nested shape on success:
  `{"status": {"code": "...", "message": "..."}}` (message optional), symmetric
  with the error shape. 13 sites migrated to `writeStatus(code, message?)`.
  Resource GETs stay data-direct
- **409 compact-busy is now a proper error** with a user-friendly message the
  client shows verbatim (no string-sniffing):
  `{"error":{"message":"⏳ I'm working on something — try /compact again when
  I'm done."}}`. The Telegram `/compact` shows the server's error message
  directly, trusting the structured format
- Action endpoints (connect, disconnect, delete, close, stop, commands…) now
  return a consistent nested shape on success: `{"status": {"code": "...",
  "message": "..."}}` (message optional), symmetric with the error shape.
  13 sites migrated to `writeStatus(code, message?)`. A `writeErr` helper
  lifts ProviderAPIError details (0.70.0) when present
- **409 compact-busy is now a proper error** (`{"error":{"message":"session is
  busy"}}`) instead of a status — only 2XX responses carry status; conflicts
  are errors. Telegram's `/compact` detects "busy" in the error message (the
  client's `errorMessage` parser now extracts the nested shape from all three
  clients, so the message is clean "session is busy" rather than raw JSON)
- Resource GETs (sessions, models, settings…) remain data-direct, not wrapped

## [0.70.0] - 2026-06-23

### Errors — standardized structured format end-to-end
- New `types.ProviderAPIError` ({message, details}) is the structured error
  providers return (named to distinguish it from harness's own API errors):
  `NewProviderAPIError` parses a provider's JSON body into `Details` instead of
  embedding raw JSON in a string. All LLM providers now use it
- `EventError` gained a `Details map[string]any` field; the session lifts a
  provider APIError's details into the event (`errorEvent` helper), and SSE
  serializes them
- **API error shape is now consistent and nested:** every endpoint error is
  `{"error": {"message": ..., "details": {...}}}` (details optional). Replaced
  47 hand-built `{"error": "..."}` responses with `writeError`/`writeErr`
  helpers; `writeErr` lifts APIError details automatically
- **All three clients** (CLI, TUI, Telegram) parse the nested shape via a shared
  `errorMessage` helper. Telegram renders structured details as a pretty JSON
  code block (`formatError` now takes the details map directly instead of
  regex-scraping the string)

## [0.69.0] - 2026-06-23

### Server — consistent command response shape
- The session command endpoint now returns a consistent `{"status": ...}` body
  for compact regardless of outcome; a busy conflict is `409` with
  `{"status": "busy"}` (was `{"error": "busy"}`). Clients branch on the status
  field instead of sniffing an error string for the word "busy"
- Telegram and TUI both read the status: Telegram's `/compact` shows "⏳ I'm
  working on something…" on busy; the TUI shows a friendly "busy — finish or
  stop the current turn first" instead of the raw JSON error

## [0.68.0] - 2026-06-23

### SDK — trimmed Session's public surface
- Removed the confusing dual `Compact`/`RequestCompact`: there's now a single
  public **`Compact`** (guards against running mid-turn, returns `ErrBusy`); the
  actual work lives in an unexported `compact` used by automatic compaction.
  Callers no longer have to guess which one to use
- Removed `PeekQueue` (dead code, no callers)
- Session's public API is now just what an SDK embedder needs: Prompt/
  PromptAndWait/Stop/Wait/IsBusy/FollowUpCount, ID/Name/Rename/Meta/Stats/
  AllMessages/ModelMeta, SwitchModel/SwitchThinking/Compact, Skills/ReadSkill,
  Subscribe, Close

## [0.67.0] - 2026-06-23

### Compaction — refuse manual compact mid-turn (fixes corrupted conversation)
- A manual compact requested while a turn was active used to run **concurrently**
  with it — the server launched `Compact()` in a goroutine regardless of busy
  state (the "queued" status was a lie; nothing was queued). Compacting mutates
  the message history the turn is still using, corrupting the conversation
  (e.g. follow-ups drifting mid-turn)
- New `Session.RequestCompact` is the external entry point and returns `ErrBusy`
  when a turn is in flight; the server rejects the command with 409. Automatic
  compaction is unaffected — it runs between ReAct iterations from inside the
  turn, where it's safe. Telegram `/compact` now replies "⏳ I'm working on
  something — try again when I'm done" instead of silently corrupting state

## [0.66.0] - 2026-06-23

### Telegram — ignore upload tags wrapped in quotes/parentheses too
- Completing 0.65.0: the directive tells the agent a real `<tel:uploadFile>` tag
  must be plain text — never in code fences, backticks, quotes, or parentheses.
  The parser now also honors the last two: a tag immediately wrapped in `"…"`,
  `'…'`, or `(…)` is treated as an example and passed through verbatim (not
  uploaded). Wrapping must be immediate — a parenthesis elsewhere in the sentence
  doesn't block a real tag

## [0.65.0] - 2026-06-23

### Telegram — don't act on upload tags shown as examples
- `<tel:uploadFile>` tags inside a code span (`…`) or fenced code block
  (```…```) are now ignored by the parser and passed through verbatim. The
  directive tells the agent to emit real tags as plain text (never in code), so
  a tag inside code is the agent *explaining* how tags work — not a request to
  send a file. Previously the parser stripped and tried to upload such example
  tags, failing on their placeholder paths. Real tags in normal text still work,
  even alongside an example in the same message

## [0.64.0] - 2026-06-23

### Compaction — preserve lifetime token totals
- Compacting no longer zeroes the session's accumulated input tokens. Those
  totals are historical (they happened, they cost money, they drive stats), so
  they're preserved along with the output totals. Compaction only resets the
  **context-usage gauge** (and the last-turn input it's derived from), since
  that's what actually shrinks when the active context is summarized — `/info`
  and the footer now keep showing the real cumulative usage after a compact

## [0.63.0] - 2026-06-23

### Compaction — fix failure on assistant-prefill-restricted providers
- Compacting could fail with "This model does not support assistant message
  prefill. The conversation must end with a user message." (e.g. Claude
  subscription/oauth) when the working set ended on an assistant message. The
  summary request now appends a final user message asking for the summary, so
  the conversation always ends on a user turn — fixing the 400 while making the
  request explicit. The working set isn't mutated (Messages() returns a copy)

## [0.62.0] - 2026-06-23

### Telegram — pretty error rendering
- Agent errors that embed a JSON payload (API errors) are now pretty-printed and
  wrapped in a code block, with the human-readable prefix kept on top. Telegram
  renders it monospaced and — crucially — doesn't interpret markdown inside code,
  so underscores in fields like `invalid_request_error` / `request_id` no longer
  turn into stray italics. Non-JSON errors are shown as plain text as before

## [0.61.0] - 2026-06-23

### Telegram — /compact feedback
- `/compact` now reports the full lifecycle instead of a one-off "Compacting…"
  with no closure:
  - start: "🗜 Compacting the conversation…", or "🗜 Compaction queued — it'll
    run after the current task." when the session is busy (uses the server's
    started/queued status)
  - automatic compaction (engine compacts near-full context, not user-requested):
    "🗜 Context almost full — compacting automatically…"
  - completion: "✅ Conversation compacted." (on the compact_end event)
  - failure surfaces via the existing error event
- The drain now handles compact_start/compact_end; a per-pump atomic flag
  distinguishes a user-requested compaction (already announced) from an
  automatic one

## [0.60.0] - 2026-06-23

### Scheduling — tools scoped to the owning session
- The Schedule* tools now fully honor the owner (session) boundary, matching the
  per-session counts: **ScheduleList** shows only the current session's
  schedules, and **ScheduleDelete** refuses a slug owned by another session,
  reporting it as not found (a no-op — no cross-session deletes, no info leak).
  **Schedule** already tagged new schedules with the session as owner
- `tools.ScheduleStore` gained the owner argument on `Entries(owner)` and
  `Delete(slug, owner)`; the adapter enforces it. The `harness schedules`
  operator view (no owner) still lists everything

## [0.59.0] - 2026-06-23

### Telegram — richer /info; honest per-session schedule counts
- `/info` now mirrors the TUI footer: harness version + session name, model with
  context window and % used, thinking level, token usage (↑/↓), cache R/W (when
  present), cost, connected MCPs, and schedules — grouped into readable sections
  with a 📊 title
- **Schedule counts are now per-session (by owner).** A schedule only ever fires
  in its owner session, so counting all of them was misleading. `GET
  /api/schedules?owner=<session_id>` filters to a session's own schedules; both
  the Telegram `/info` and the **TUI footer badge** now use it — "in THIS session,
  N schedules run", the honest count. Added `schedule.Store.Owners()`
- Compact number formatting drops a trailing ".0" (200k, not 200.0k) while
  keeping real fractions (1.3k, 406.6k)

## [0.58.0] - 2026-06-23

### Telegram — model resolution on resume aligned with the TUI, honest logs
- A resumed chat session now keeps its own persisted model (like the TUI),
  unless the bot was launched with an explicit `--model`, which overrides every
  session's model. Previously the connect banner implied one model while a
  resumed chat silently ran on its own (a stale one), causing e.g. an anthropic
  rate-limit error under a bot whose default was deepseek
- **Logs now report the real model in use:** the per-prompt log includes
  `model=<actual session model>`, and the startup line labels its value
  `default_model=` (what new sessions get) to avoid implying it applies to all
  sessions

## [0.57.0] - 2026-06-23

### Telegram — slash commands (phase 1: actions & info)
- Added a command system operating on each chat's own session:
  - `/new` — start a fresh session
  - `/stop` — interrupt in-flight work
  - `/compact` — summarize & compact the conversation
  - `/info` — harness version, the session's model/thinking, token usage & cost
- Commands are registered via `setMyCommands` at startup, so Telegram suggests
  them (with descriptions) when the user types "/". A `@botname` suffix (groups)
  is stripped; unknown commands get a hint
- All backed by existing server endpoints (stop, commands/compact, session meta,
  server info); the Bot API client gained setMyCommands. Removed the old
  /start; selection-list commands (/models, /thinking) come in phase 2

## [0.56.0] - 2026-06-23

### Telegram — fix table column alignment with accented text
- Table columns were misaligned when cells contained multi-byte runes (accents,
  ñ): column widths were measured in bytes, so "Categoría" (10 bytes, 9 chars)
  got under-padded and the pipe borders drifted. Width is now measured in Unicode
  code points, so columns align correctly in Telegram's monospace rendering
- Added an alignment test that asserts every bordered row has the same visual
  (rune) width AND identical pipe positions, including a case with accented cells

## [0.55.0] - 2026-06-23

### Telegram — render markdown tables as aligned code blocks
- Telegram supports neither Markdown nor HTML tables, so a model-generated pipe
  table (`| col | col |`) was showing up as raw literal text. The converter now
  detects pipe tables (header + `|---|` delimiter + rows) and wraps them in a
  fenced code block, **keeping the table structure** (pipe borders + header
  separator) but padding every column to a uniform width so it stays aligned in
  Telegram's monospace rendering. Tables inside existing code fences, and stray
  prose pipes, are left untouched

## [0.54.0] - 2026-06-23

### Logging u2014 structured backend logs (logx)
- New `internal/logx` structured logger renders one line per event in a
  consistent backend format: `LEVEL [component] event key=value` (values quoted
  only when they contain spaces). Levels: INFO/WARN/ERROR, fixed-width so lines
  align and grep cleanly
- **Telegram** transport logs migrated to logx u2014 replacing the ad-hoc mix of
  arrows/symbols (u2190 u2192 u2191 u26f7 u2699) with structured events (connected, prompt, reply,
  tool, upload, images, rejected, u2026), each carrying chat= and session= context
- **Server** (`serve`) request logging replaced chi's middleware.Logger with a
  custom middleware in the same logx format
  (`INFO [server] request method=GET path=/api/server status=200 bytes=128
  dur=80u00b5s`), and the startup line too. Dropped the chi middleware dependency

## [0.53.0] - 2026-06-23

### CLI u2014 serve --scheduler (headless transport)
- `harness serve` gained `--scheduler`, re-enabling the cron engine on the
  server. This is now sound thanks to owner routing (0.38.0): a due schedule
  fires into its owner session if that session is currently active (a connected
  client that opened it), otherwise it's skipped. `serve` is effectively a
  headless transport u2014 an agent behind an API, with clients bringing their own
  sessions u2014 so it can host the engine like any other transport
- The flag parses regardless of position relative to the address
  (`serve :8080 --scheduler` or `serve --scheduler :8080`)

## [0.52.0] - 2026-06-23

### CLI u2014 restructured into internal/cli, main.go is now a thin entry point
- `cmd/harness/main.go` shrank from ~690 lines to ~10: it just calls
  `cli.Main(os.Args)`. All parsing and dispatch moved into `internal/cli`
- Each command is its own handler with its **own `flag.FlagSet`**
  (`cmd_tui.go`, `cmd_serve.go`, `cmd_telegram.go`, `cmd_manage.go`,
  `cmd_mcp.go`, `cmd_memo.go`), replacing the hand-rolled `extract*Flags`
  parsers. Repeatable `--env`/`--header` use a small `flag.Var`. Still stdlib
  only u2014 no CLI framework dependency
- **Agent construction moved to where it's used:** `internal/cli/agent.go` has
  `newAgent`/`newInteractiveAgent`/`newTelegramAgent`/`newConfigAgent`; each
  command builds the agent it needs and hands it to its transport/server (the
  server receives the agent, as it should). The router lives in `app.go`, help
  in `help.go`

## [0.51.0] - 2026-06-23

### Telegram u2014 correct media routing for uploads
- File uploads now route by type to the right Bot API method: .jpg/.jpeg/.png/
  .webp u2192 sendPhoto (inline), .gif/.mp4 u2192 **sendAnimation** so GIFs actually play
  (sendPhoto would deliver a GIF as a single static frame), everything else u2192
  sendDocument. Fixes animated GIFs arriving frozen; the directive was updated to
  match

## [0.50.0] - 2026-06-23

### SDK u2014 WithDirectives (custom system-prompt instructions)
- New `WithDirectives(...string)` option (and `AgentOptions.Directives`) appends
  arbitrary instruction blocks to the system prompt, below the base prompt and
  the built-in sections (skills, memory, scheduling). A general mechanism for a
  caller u2014 typically a transport u2014 to teach the agent capabilities specific to
  its environment

### Telegram u2014 reply with files via action tags
- The agent can now send files/images back to the chat. Instead of a tool, a
  Telegram **directive** teaches it to emit a `<tel:uploadFile>/path</tel:uploadFile>`
  action tag in its reply; the transport's renderer parses these tags, uploads
  the files (images inline via sendPhoto, others as documents via sendDocument,
  multipart/form-data), and **strips the tags from the text** the user sees
- Parsing/upload failures are no-ops for the user u2014 the tag is always removed and
  the cleaned text still sent, so nothing leaks. The design is transport-owned
  and extensible (more `<tel:...>` actions can follow) with no change to the
  agent core
- `newTelegramAgent` injects the directive; the Bot API client gained
  photo/document upload (stdlib multipart), no new dependency

## [0.49.0] - 2026-06-23

### Telegram u2014 receive images
- The bot now accepts photos. A single photo becomes a prompt with one image;
  its caption (if any) is the prompt text. Images are downloaded via getFile +
  the file endpoint, base64-encoded, and sent to the existing vision-capable
  prompt path (the server rejects them if the model lacks vision)
- **Albums:** Telegram delivers a multi-photo album as separate messages sharing
  a media_group_id with no "album complete" signal, so photos are buffered by
  group id and debounced (~1s); when the window closes they fire as ONE prompt
  carrying all images plus the caption u2014 matching the agent's multi-image
  support
- Bot API client gained getFile/file download (stdlib); no new dependency

## [0.48.0] - 2026-06-23

### TUI u2014 consume newly-forwarded events
- The TUI now shows **`max_turns_reached`**: when the agent hits its per-turn
  ReAct cap, a dim "reached the N-turn limit u2014 summarizing progress" line is
  printed so the summarized result isnu2019t mistaken for a normal finish (previously
  the event was dropped)
- Thinking now closes on the explicit **`thinking_end`** event too (in addition
  to the existing text/tool-start close), making reasoning blocks end
  deterministically even when not followed by streamed text. Thinking rendering
  itself was audited and confirmed correct (dim+italic, streamed, closed on all
  transitions)

## [0.47.0] - 2026-06-23

### Agent u2014 balanced loop lifecycle events
- Audited that every event the SSE now forwards is actually emitted by the react
  loop. Text/thinking End events (driven by the AI provider stream) were verified
  correct across all transitions (thinkingu2192text, textu2192tool, usage/done, etc.)
- Fixed two `EventLoopStart`/`EventLoopEnd` imbalances: an iteration that ran
  tools and looped again never emitted `LoopEnd` before the next `LoopStart`, and
  a user Stop mid-iteration skipped `LoopEnd`. Both now close the loop, so
  `LoopStart`/`LoopEnd` are balanced on every exit path

## [0.46.0] - 2026-06-23

### Server u2014 SSE now forwards every agent event
- Fixed the SSE layer silently dropping four agent events it had no case for:
  `EventStreamTextEnd` (u2192 `text_end`), `EventStreamThinkingEnd`
  (u2192 `thinking_end`), `EventLoopStart` (u2192 `loop_start`), and `EventLoopEnd`
  (u2192 `loop_end`). The SSE translator now has full parity with the agent's event
  set, so transports can observe the complete turn lifecycle
- **Telegram** uses the new `text_end` to flush each text block the moment it
  finishes streaming (more precise than the previous flush-before-tool-call)

## [0.45.0] - 2026-06-23

### Telegram u2014 live typing + streamed commentary
- **Typing indicator stays alive:** Telegram clears "typingu2026" after ~5s, so the
  bot now keeps it lit with a heartbeat (re-sent every 4s) for the whole turn
  (turn_start u2192 turn_end), instead of a single call that vanished mid-work
- **Commentary streams as it happens:** text the agent writes between tool calls
  is now flushed to the chat before each tool call (and at turn end), so the user
  sees the running narration in real time rather than one lump at the end. This
  makes it clear the bot is working (calling tools) instead of looking idle

## [0.44.0] - 2026-06-23

### Telegram u2014 fix single-asterisk italic
- Fixed CommonMark italic (`*text*` / `_text_`) showing literal asterisks in
  Telegram. The converter only handled `**bold**`/`__bold__`; a single `*` fell
  through and was escaped. It now maps `*italic*`/`_italic_` to MarkdownV2u2019s
  `_italic_`, while a stray or arithmetic `*` (e.g. `2 * 3`) is still escaped and
  bold still wins when doubled

## [0.43.0] - 2026-06-23

### Telegram u2014 fix UTF-8 mojibake in replies
- Fixed garbled non-ASCII text (accents, u00f1, emoji) in bot messages u2014 e.g.
  "Du00e9jame" arriving as "Du00c3u00a9jame". The MarkdownV2 escaper was rebuilding each byte
  with string(byte), which mangles multi-byte UTF-8 runes; it now writes bytes
  verbatim and only backslash-escapes ASCII specials, so runes pass through
  intact

## [0.42.0] - 2026-06-23

### Telegram u2014 always start, reject per-chat
- The bot no longer refuses to start when no chats are paired. It starts, logs a
  warning, and rejects each unknown chat with the "run pair" message (the
  per-chat rejection already covers the safety case). With --allow-unpair it
  accepts and auto-pairs anyone, as before

## [0.41.0] - 2026-06-23

### Telegram u2014 pairing (allowlist in config, no more --allow flag)
- `~/.harness/telegram.json` now holds both the **allowlist** (paired chat ids)
  and the **sessions** map (chat u2192 session). The allowlist is managed once via
  new subcommands instead of a per-launch flag:
  - `harness telegram pair <chat_id>` u2014 allow a chat (idempotent)
  - `harness telegram unpair <chat_id>` u2014 revoke a chat AND drop its session
  - `harness telegram list` u2014 list paired chats
- **Removed `--allow`**; the bot reads the allowlist from config. It refuses to
  start with no paired chats unless `--allow-unpair` is set
- **`--allow-unpair`:** accept any chat, auto-adding it to the allowlist on first
  contact (logged). Without it, an un-paired chat is rejected with a message
  telling the user to run `harness telegram pair <chat_id>`, and the rejection is
  logged as a warning

## [0.40.0] - 2026-06-23

### Telegram u2014 operator logs
- The Telegram transport now logs the key moments to stderr so the operator can
  follow activity: an incoming user prompt (`u2190 prompt from chat`), each tool the
  agent calls (`u2699 tool`), a scheduled prompt fired into a chat (`u25f7 scheduled
  prompt`), and the reply sent back (`u2192 reply to chat`, noting when split across
  multiple messages). Prompt/reply text is collapsed to one line and truncated

## [0.39.0] - 2026-06-23

### Telegram transport
- New **`harness telegram`** transport: run the agent as a Telegram bot. Like the
  TUI it owns a root agent and an in-process HTTP/SSE server, but the display is
  a Telegram chat — incoming messages are prompts, the agent's text replies are
  outgoing messages, **one harness session per chat**
- **Stdlib only** — the Bot API client (getUpdates long-polling + sendMessage) is
  built on `net/http` + `encoding/json`; no new dependency
- **Per-chat sessions**, auto-created on first message and persisted in
  `~/.harness/telegram.json` (chat id → session id) so conversations survive a
  restart. All chats share the launch cwd
- **Scheduling works per chat:** a schedule created from a chat is owned by that
  chat's session (via the owner routing added in 0.38.0), so with `--scheduler`
  a fired prompt is delivered back to the right chat — even if the user was away,
  Telegram holds the message
- **Markdown replies:** the agent's CommonMark is converted to Telegram
  MarkdownV2 (headings → bold, escaping specials, preserving code spans/fences),
  with an automatic plain-text fallback if Telegram rejects the markup (400).
  Long replies are split across messages (4096-char cap)
- **Security:** an allowlist is required (`--allow <chat_id,...>`); the bot
  refuses to run open to everyone and ignores messages from other chats.
  Commands: `/start`, `/new` (fresh session)
- Flags: `--token` (or `TELEGRAM_BOT_TOKEN`), `--allow`, `--model`, `--thinking`,
  `--scheduler`

## [0.38.0] - 2026-06-23

### Scheduling — per-session routing (owner), multi-session ready
- A schedule now records the **owner** — the id of the session that created it.
  When a schedule fires, the engine routes the prompt back to that session if
  it's active; if not, the prompt is dropped (the run is still recorded, so
  nothing piles up). This replaces the single "scheduled prompts handler"
  session with per-session routing, so a multi-session transport (e.g. Telegram,
  one session per chat) can have each chat schedule its own prompts and receive
  them back
- The agent now tracks all live sessions in an internal active set (registered
  on `NewSession`/`ResumeSession`, removed on `Close`). The Schedule tool
  captures its session's id automatically as the owner — the model never sees it
- **Removed** the old `SetScheduledPromptsHandler` and the
  `?scheduled_prompts_handler=true` opt-in query param; routing is now implicit
  via owner. An empty owner (e.g. the single-session TUI) falls back to the sole
  active session, so existing behavior is unchanged
- `schedule.Store.Set` gained an `owner` argument; `schedules.json` entries gain
  an optional `owner` field

## [0.37.0] - 2026-06-23

### TUI
- Footer status badges are now bracketed plain text without icons
  (`[2 mcps] [1 schedule]`), keeping the dim bullet separator from the stats
  line (`... (medium) • [2 mcps]`)

## [0.36.0] - 2026-06-23

### TUI
- Footer: tightened the badge separator to a single space on each side of the
  bullet (` • `)

## [0.35.0] - 2026-06-23

### TUI
- Footer: a dim bullet (`•`) now separates the stats line from the status
  badges (e.g. `... (medium)  •  ⎔ 2 mcps`), instead of plain spacing

## [0.34.0] - 2026-06-23

### SDK facade — WithScheduler()
- Added `WithScheduler()`, the missing option to enable the cron engine
  (`EnableScheduler`) from the facade — completing the `With*` set alongside
  `WithMCPs`/`WithMemory`. The engine fires due schedules into the session the
  caller marks via `Agent.SetScheduledPromptsHandler`; the Schedule* management
  tools remain available regardless

## [0.33.0] - 2026-06-23

### Memory — agent-owned, simpler opt-in
- `WithMemory()` now takes no argument (was `WithMemory(*memory.Store)`). Memory
  is a concrete, internal store — there's no user interface to implement — so the
  agent opens and owns it internally, matching `EnableMCPs`/`EnableScheduler`.
  `AgentOptions.Memory *memory.Store` → `AgentOptions.EnableMemory bool`
- The agent tracks ownership: a root agent that opened the store closes it on
  `Close()`; a subagent shares the parent's already-open store (via an
  unexported option) and never closes it. The SDK no longer needs the
  `agent/memory` import for the common case

## [0.32.0] - 2026-06-23

### SDK facade — re-export implementable contracts
- The root `harness` package now re-exports the interfaces/types a user
  implements, so the common case needs no sub-package imports: `SessionStore`
  and `SessionMeta` (custom persistence), `ResourceLoader` (custom skill/resource
  loading), and `Tool` (custom tools, used by `WithTools`). Symmetry with the
  already re-exported output types (`Agent`, `Session`, `Event`, `Handler`,
  `PromptOption`)
- `WithStore`/`WithResourceLoader`/`WithTools` signatures now use the facade
  aliases. Verified end-to-end: an external module implements `SessionStore` and
  builds a `Tool` importing only `harness` (plus `types` for `Message`)

## [0.31.0] - 2026-06-23

### Session store — one primitive persistence port (SDK simplification)
- **Collapsed two interfaces into one.** The SDK previously required
  implementing both `SessionStoreManager` (the collection) and `SessionStore`
  (an open session), ~15 methods that leaked harness internals (compaction
  offsets, working-set vs full-history, checkpoint messages). Now SDK users
  implement a single, dumb **`SessionStore`** port — 7 primitive methods:
  `SaveMeta`, `LoadMeta`, `ListMetas`, `DeleteSession`, `AppendMessage`,
  `LoadMessages(sessionID, fromIndex)`, `Close`. A backend is just metadata +
  a flat append-only message log; files, SQLite, Postgres, S3 are all trivial
- **All session semantics moved into a `store.Session` handle** owned by
  harness (not implemented by users): it caches the working set in memory for
  the hot path, resolves `Messages()` (from the compaction checkpoint) vs
  `AllMessages()` (full history) by slicing on `fromIndex`, and owns the
  compaction-offset bookkeeping. The old `diskReadOffset`/`diskWriteCount`
  memory↔disk offset translation is gone
- **More durable:** messages now persist on every `AddMessage`
  (append-immediate) instead of batching until `Close()`, so a crash mid-session
  no longer loses the turn
- `ListMetas(cwd)` with `cwd==""` returns all sessions (replaces the separate
  `List`/`ListAll`); `Rename` is a store helper (load-modify-save), not a port
  method. Renamed constructors: `NewFileStore`, `NewInMemoryStore`
- New tests cover both backends against the same port contract plus the handle's
  compaction/resume behavior; an end-to-end resume-after-restart flow verifies
  working-set vs full-history reconstruction from disk

## [0.30.0] - 2026-06-23

### Scheduling — dynamic engine, @every fix, live badge, min-interval guard
- **Dynamic engine:** the scheduler no longer registers jobs once at startup.
  A single goroutine polls every 30s, reads the CURRENT schedules from the store
  each time, and fires those that are due. Schedules added, edited, or deleted
  (by the tools or a hand-edited file) now take effect immediately — no restart
- **Fixed `@every` never firing:** `@every` is a relative schedule
  (`Next(t) = t + interval`), so a moving cursor pushed its next run forever out
  of reach. Each job is now anchored on its OWN last run (or the engine start
  time if it never ran), which fires both absolute crons (`* * * * *`) and
  relative ones (`@every 1m`) correctly. Past-due runs are not replayed
- **Live footer badge:** a successful Schedule/ScheduleDelete refreshes the
  `◷ N schedules` badge immediately (off the SSE goroutine), so the count
  reflects the agent's changes without waiting for the next poll
- **1-minute minimum enforced:** `ValidateCron` rejects sub-minute `@every`
  (e.g. `@every 30s`) with an actionable error — the finest the engine can honor
  is one minute. The Schedule tool description now lists the supported
  descriptors (@yearly/@monthly/@weekly/@daily/@midnight/@hourly, @every) and
  states the 1-minute floor
- **System prompt:** when scheduling is available, a `## Scheduling` section
  tells the agent it can schedule recurring prompts and when to use it

## [0.29.0] - 2026-06-23

### Scheduling — cron-scheduled prompts
- The agent can schedule prompts to run automatically on a cron schedule, via
  three tools: **Schedule** (create/update by slug), **ScheduleList**, and
  **ScheduleDelete**. Persisted to `~/.harness/schedules.json` with audit fields
  (runs, last_run). Uses `robfig/cron/v3` (5-field standard + @descriptors)
- **Store vs engine split:** the agent always opens the store (so the Schedule*
  tools work anywhere); `AgentOptions.EnableScheduler` additionally runs the
  engine that fires due prompts. A transport marks its session as the target via
  `SetScheduledPromptsHandler`. Subagents get neither (parent-only, like MCP)
- **`harness --scheduler`** runs the engine in the TUI (a guaranteed session).
  A due prompt is sent tagged origin=scheduled and echoed with a clock icon (◷)
- **Origin tag:** `Session.Prompt`/`PromptAndWait` take functional options
  (`WithOriginUser`/`WithOriginScheduled`/`WithImages`). The new
  `received_prompt` event (and `follow_up_start`) carry text + origin, so the
  transport paints the right icon — the TUI no longer echoes locally, the server
  is the single source of truth
- **`GET /api/schedules`** + **`harness schedules [--json]`** list schedules
  (slug, cron, runs, relative last-run, full prompt)
- **Footer badges:** `⎔ N mcps` and (with --scheduler) `◷ N schedules`, shown
  when present
- Schedule tools render with the clock icon, slug bare, prompt summarized as
  `(prompt: N lines)`

### CLI
- `harness http <addr>` renamed to **`harness serve <addr>`**. The server is a
  passive backend and no longer accepts `--scheduler` (scheduling needs a
  guaranteed session, which only an interactive transport provides)
- `harness memo --content` now prints the full content (was first line only)

## [0.28.0] - 2026-06-23

### Internal restructure — server / cli / transport
- `internal/transport/http` → `internal/server` (package `http` → `server`): it's
  the HTTP/SSE backend the clients talk to, not a transport
- `internal/transport/cli` → `internal/cli`: CLI commands are a client, not a
  session frontend
- `internal/transport/` now holds only interactive session frontends — today the
  pure-Go `tui`, with room for future transports (telegram, slack, …)
- Purely internal — no effect on the public SDK surface

## [0.27.0] - 2026-06-23

### Skill tool — location-aware
- The Skill result now begins with the skill's absolute directory and a note that
  relative paths it references (scripts, templates, data files) resolve against
  it. Skills can live in any of four locations, so telling the model where a
  skill loaded from lets it find bundled scripts without guessing
- `ResourceLoader.ReadSkill` (and `Session.ReadSkill`) now return `(content,
  dir, error)`; the HTTP `skill:` command injects the location note too
- Skill content is now head-truncated (like Fetch) — the important guidance is at
  the top

## [0.26.0] - 2026-06-23

### Tool guidance — steer HTTP to Fetch (not curl)
- Fetch's description now claims its territory: “Prefer this over running
  curl/wget through Bash — it handles headers, JSON, forms, uploads, redirects,
  gzip, and binary downloads correctly.”
- Bash's description now redirects HTTP to Fetch (“For HTTP requests, use the
  Fetch tool instead of curl/wget”), mirroring how it already redirects file ops
  to Read/Write/Edit. This stops agents defaulting to curl out of habit

## [0.25.0] - 2026-06-23

### Fetch tool — fine-grained control
- **`follow_redirects`** (default true): set false to inspect a 3xx response (read
  its Location header) without following it
- **`timeout`** (seconds, default 30): configurable per request, consistent with Bash
- **HEAD** documented as a supported method (arbitrary methods already worked)
- Fixed the description: text is truncated to the FIRST 2000 lines/50KB (head),
  not the last — the code always did head truncation; the docs said “last”

### TUI — consistent tool-arg ordering
- All `(…)` summaries (json/form/files/headers/body/content/edits) now render
  AFTER the plain `key=value` params, for every tool — short params stay grouped
  near the primary, summaries trail at the end (matching MemoWrite's layout)

## [0.24.0] - 2026-06-23

### Fetch tool — HTTP swiss-army knife (JS fetch style)
- **Body helpers** (choose one): `body` (raw string), `json` (object → JSON +
  `application/json`), `form` (key/values → `x-www-form-urlencoded`), `files`
  (multipart upload; may combine with `form` for text fields). Content-Type is
  set automatically; mutual exclusion is validated. All via stdlib — no new deps
- **Rich response** (JS Response style): the result shows the status line, all
  response headers, and the body. 4xx/5xx are reported as errors; 3xx redirects
  are followed automatically. On truncation the full status+headers+body is saved
  to a temp file
- **`download_to`** (renamed from `output_path`) saves the raw response bytes to
  disk. On 4xx/5xx it does NOT save — it reports the failure with the body
  instead (like `wget` / `curl --fail`), so a failed download never leaves a
  bogus file
- Structured, sectioned tool description (Body / Headers / Download / Response)
- **TUI:** request helpers render as summaries — `(json: N bytes)`, `(N fields)`,
  `(N files)`, `(body: N bytes)`, `(N headers)` (header values hidden as they may
  hold secrets); `download_to` shows the path

## [0.23.0] - 2026-06-23

### TUI — tool header hygiene
- **Fetch** no longer dumps request headers (which can contain secrets like
  `Authorization` / API keys) or the request body into the header. Headers are
  summarized as `(N header[s])` with values hidden, and the body as
  `(body: N bytes)`
- **MemoWrite** summarizes its content as `(N line[s])` (deferred to the end so
  short params like `global=` stay next to the slug), matching Write/Edit
- Audited all built-in tools; Bash, Read, Skill, MemoSearch, MemoDelete already
  render short params cleanly, and Subagent's prompt stays full (it's the
  primary param)

## [0.22.0] - 2026-06-23

### TUI
- **Sub-millisecond tool timing:** the tool result `[time]` tag was inconsistent
  because durations were serialized as truncated integer milliseconds — two
  equally fast calls could show `[1ms]` and nothing. Durations now carry
  fractional milliseconds; `formatDur` renders `<1ms` for sub-ms tools (history
  replay, with no persisted timing, still omits the tag)
- **Write header** summarizes the file content as `(N line[s])` instead of
  dumping the whole file into the header, matching Edit's `(N edit[s])`

## [0.21.0] - 2026-06-23

### Edit tool — PI-level robustness
- **Multi-edit + dual shape:** pass a single `old_text`/`new_text`, or an `edits[]`
  array for several disjoint changes in one call (mutually exclusive; validated).
  Each `old_text` is matched against the original file
- **Fuzzy matching:** tolerates smart quotes, dash variants, exotic spaces, and
  trailing whitespace that models often introduce (exact match first, then fuzzy)
- **Line endings & BOM:** matches in LF space and restores the file's original
  CRLF/LF ending and leading BOM; preserves the file mode
- **Overlap detection** and actionable errors (not found / not unique / overlap /
  empty / no-change), mirroring PI
- Ported PI's edit-diff core to Go (`editdiff.go`)

### TUI — tool render polish
- Edit header summarizes edits as `(N edit[s])` instead of dumping raw JSON;
  a single flat edit shows `(1 edit)` for parity
- Tool result now shows the message verbatim for single-line outputs (Edit,
  Write, Memo*, short MCP statuses) instead of a misleading `(1 lines)`;
  multi-line outputs still summarize as `(N lines)`. The `[time]` tag is kept on
  both for consistency

## [0.20.0] - 2026-06-23

### Bash tool — native background execution
- New `background` parameter: runs a command detached (new session via `Setsid`),
  writes its output to a temp log file, and returns immediately with the PID and
  log path — no timeout applies. Replaces the fragile `setsid/nohup &` dance the
  model had to hand-roll (`setsid(1)` doesn't even exist on macOS). Stop it with
  `kill <pid>`; read the log to check progress
- Rewrote the tool description into clear sections (purpose, Timeout, Background,
  Output)

### Cross-platform process management — real Windows support
- Replaced the Windows no-ops with real implementations: `setProcessGroup` uses
  `CREATE_NEW_PROCESS_GROUP`, `setDetached` uses
  `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS`, and `killProcessGroup` uses
  `taskkill /f /t` (tree kill) — the Windows analogues of Setpgid/Setsid and a
  negative-PID group kill
- Added an explicit fallback (`bash_other.go`, `!unix && !windows`) where
  `setDetached` returns a clear “not supported” error instead of silently leaking
  a non-detached child. Build tag for the Unix file tightened from `!windows` to
  `unix`

## [0.19.0] - 2026-06-23

### Bash tool — timeout process-group kill
- Fixed the timeout not actually stopping the command when it spawned background
  children (`cmd &`, `nohup`). `exec.CommandContext` killed only the direct
  `bash`; the detached child kept the output pipe open, so the wait blocked far
  past the timeout (observed: a 30s timeout that returned after ~2058s)
- The command now runs in its own process group (`Setpgid`), and on timeout /
  cancellation the whole group is killed (`kill -pid`), reaping background jobs
  too. The wait races the timeout in a goroutine so it returns at the limit
- Cross-platform via build tags (`bash_unix.go` / `bash_windows.go`)
- Tool description notes that long-running work should pass a larger `timeout`,
  and documents how to launch a truly background/detached process
  (`setsid cmd > out.log 2>&1 < /dev/null &`) so it survives the call instead of
  holding the output pipe until the timeout

## [0.18.0] - 2026-06-23

### TUI — streaming flicker fix
- Fixed full-screen repaints during fast streaming (thinking, text, tool calls)
  that caused visible flicker. The diff's “mixed change” branch was too broad:
  the common case of the last line growing while a new line is appended fell into
  a full relative repaint on every token. Narrowed the condition
  (`firstChanged < len-1`) so that case takes the incremental per-line path; the
  table-flush case (change strictly before the last line) still full-repaints
- Added regression tests reproducing the flicker and guarding the table case

## [0.17.0] - 2026-06-23

### Defaults
- `agent.New` now resolves an empty `ThinkingLevel` from the user's settings,
  falling back to `"off"`. Centralizing this in New — the single entry point for
  the CLI, TUI, and SDK — keeps the SDK facade a thin zero-value pass-through
  while still yielding a sensible default
- Simplified `cmd/harness` call sites that no longer need to pass the thinking
  level explicitly

## [0.16.0] - 2026-06-23

### SDK — functional options
- `harness.New` now takes functional options (`...Option`) instead of an
  `Options` struct — the idiomatic Go pattern. `New()` with no args returns a
  default agent; options are applied in order (later wins)
- Added `WithThinking`, `WithSystemPrompt`, `WithMaxTurns`, `WithMaxTokens`,
  `WithTools`, `WithDisallowedTools`, `WithMCPs`, `WithStore`,
  `WithResourceLoader`, `WithMemory`, and `WithOptions` (apply a pre-built config)
- `Options` remains exported for callers who assemble a config directly
- **Breaking:** `harness.New(Options{…})` → `harness.New(With…())`

## [0.15.0] - 2026-06-23

### OAuth credentials — cross-platform support
- Claude OAuth token discovery now detects the OS and applies the correct
  strategy: macOS reads the encrypted Keychain (file fallback); Linux and Windows
  read `~/.claude/.credentials.json`
- Honors `$CLAUDE_CONFIG_DIR` for the credentials file location (per Claude Code
  docs, used on Linux/Windows). `UserHomeDir` resolves `%USERPROFILE%` on Windows
- Verified via cross-compilation for darwin, linux, and windows

## [0.14.0] - 2026-06-23

### OAuth connect — unified CLI/TUI behavior
- `authflow.ObtainOAuthCredentials` is now **silent-only**: it reads OAuth tokens
  from the keychain / credentials file and no longer spawns `claude auth login`.
  Auto-spawning an interactive login corrupted the TUI's raw-mode terminal and
  made the CLI and TUI diverge; both now behave identically
- When no credentials are found, connect returns an actionable error — “run
  'claude auth login' to authenticate, then reconnect” — instead of launching a
  subprocess. Removed `runClaudeAuthLogin` / `resetTerminal`

## [0.13.0] - 2026-06-23

### SDK ergonomics
- **`Session.Wait()`** — blocks until the prompt queue is fully drained
  (condition-variable signaling, no polling). For batch callers that fire several
  prompts and then wait for all of them
- **`Session.PromptAndWait(ctx, text, images…)`** — synchronous convenience:
  enqueues a prompt and blocks until that turn finishes, returning its final
  assistant text. The async `Prompt` + `Subscribe` model remains primary
- **`Agent.Providers()`** — read-only snapshot of every provider and its state
  (`[]types.ProviderInfo`; no credentials). Provider administration
  (connect/disconnect, API keys, OAuth) stays in the `harness` CLI
- **`Agent.Models()`** — every available model across all active providers
  (`[]types.ModelListing`, each with a ready-to-use “provider/model” id)
- New public types `types.ProviderInfo` and `types.ModelListing`

## [0.12.0] - 2026-06-23

### TUI
- **Bash tool icon** changed from `❯` to `$` (classic shell prompt), so it no
  longer collides with the user prompt's `❯`

## [0.11.0] - 2026-06-23

### TUI paste & overflow fixes
- **Paste line endings** — bracketed paste now normalizes CRLF and bare CR to LF.
  A raw `\r` returned the cursor to column 0 without advancing, so pasted lines
  overwrote each other (e.g. “Key west”+“TFCGKE” → “KeytiCGKE”) and the sent
  message lost its `❯` prompt prefix
- **Overflow indicator sync** — the “↑ N more” hint is now computed on demand from
  the current buffer, so it appears the moment you paste and clears the moment you
  submit (previously it lagged one frame because the separator renders before the
  editor)

## [0.10.0] - 2026-06-23

### TUI editor & polish
- **Ctrl+J** inserts a newline in the editor (Enter still submits; Shift+Enter is
  indistinguishable from Enter without the Kitty protocol). `\n` is now mapped to
  Ctrl+J instead of Enter
- **Overflow hint** — when the input exceeds the 5-line window, the separator above
  the editor shows a left-aligned “↑ N more” indicating hidden lines
- **Read tool icon** changed from `▤` (looked like a stop square) to `≡` (narrow
  triple bar), avoiding a double-width gap after the icon
- Fixed a double “v” in the banner version (`vv0.9.0` → `v0.9.0`); the version
  string already carries its `v` prefix

## [0.9.0] - 2026-06-23

### TUI — clipboard image paste (ported from v1)
- **Ctrl+V** pastes a clipboard PNG: it's written to a temp file and its path is
  inserted into the editor as text; the Read tool resolves the image when the
  agent reads that path (Cmd+V can't be intercepted in a raw-mode terminal, so
  Ctrl+V is the portable trigger)
- New `internal/transport/tui/clipboard.go` (`PasteImageFromClipboard`) and
  `Editor.InsertText`; dep `golang.design/x/clipboard` (approved)

### SDK boundary — the agent is now a public SDK
- Root `harness.go` facade (package `harness`) re-exports the essentials:
  `New`, `Agent`, `Session`, `Options`, `Event`, `Handler`
- **Public surface (the SDK):** `agent` (+ `agent/tools`, `agent/store`,
  `agent/resources`, `agent/memory`), `mcp`, `types` — third parties can embed the
  agent and supply custom tools, session storage, and resource loaders
- **Implementation detail moved under `internal/`** (compiler-enforced, not
  importable by external modules): `providers` (+ `llm`, `authflow`), `config`,
  `transport` (`cli`, `http`, `tui`), `version`
- `memory` consolidated under `agent/memory` (alongside `store` and `resources`
  as agent infrastructure)
- Rule enforced: no public package may expose an `internal/…` type in an exported
  signature; the module root is the `internal/` parent, so all harness code may
  import it while third parties cannot

## [0.8.0] - 2026-06-23

### TUI — Pure-Go rewrite (replaces tview)
- New from-scratch terminal UI in `transport/tui` with **zero external TUI libraries**
  (only `golang.org/x/term` + `rivo/uniseg`); removed `rivo/tview`, `gdamore/tcell`
- Differential rendering engine (`render/`) with faithful markdown, buffered tables,
  word-wrap, and a component model (markdown, history, editor, spinner, select-list)
- Welcome banner, in-place `/resume`, source-backed history blocks, chronological order
- Faithful-to-model rendering: the renderer paints, never adds/removes newlines

### Project structure
- `main.go` moved to `cmd/harness/main.go` (Go idiom); legacy tview TUI removed
- `transport/` holds the three client transports — `cli`, `http`, `tui`
- Version centralized in a dedicated `version` package (`version.Version`),
  injected via ldflags

### MCP (Model Context Protocol) — stdlib client
- Local (stdio) and remote (HTTP + SSE + header auth) servers
- `harness mcp [list | add <name> --local|--remote ... | rm <name>]`
- Tools namespaced `mcp__<server>__<tool>`; eager connect with graceful degradation
- `GET /api/mcp/status`

### Persistent memory (SQLite + FTS5)
- Project-scoped and **global** (cross-project) memories, partitioned by cwd
- Tools `MemoWrite` / `MemoSearch` / `MemoDelete` (subagents read-only)
- Prefix full-text search (`unicode61`, sanitized queries) — `kube` finds `kubernetes`
- `GET /api/memories` (optional `cwd`, `query`, `include_content`, pagination)
- CLI `harness memo [<query>] [--all | --global | --content | --limit | --skip]`
- `Agent.Memory()` exposes the store; `Agent.Close()` now closes the DB

### Settings & credentials
- Typed, agnostic managers in `config/` (settings + credentials), unified vocabulary
  (`active_model`, `thinking_level`, `providers`, `mcp`) end-to-end
- REST: `GET/PATCH /api/settings`, `/api/settings/providers/{name}`, `/api/settings/mcp/{name}`
- Thinking levels `off|low|medium|high|xhigh`; removed `HARNESS_THINKING` env var
- `harness settings [set model|thinking <val>]`

### Providers & metadata
- New **MiniMax** provider
- Immutable metadata cascade: provider → OpenRouter → hardcode → name-inference → defaults
- Fixed Claude OAuth token endpoints + actionable re-auth error; shared `authflow` package

### Server & tools
- `Serve(net.Listener)` replaces `ListenAndServe(addr)` — no close/reopen race
- PI-style tool output truncation (head/tail per tool, overflow saved to `/tmp`)
- Redesigned tool-call rendering (ordered args, distinctive icons, one-line errors)
- Queued-message redesign via `follow_up_start` event; `is_error` empty-content fix

## [0.7.0] - 2026-06-15

### TUI — Complete rewrite with tview
- Replaced raw terminal rendering with `github.com/rivo/tview` for robust layout
- Custom input via `app.SetInputCapture` (no InputField background issues)
- Persistent SSE connection — opened once at session creation, closed on quit
- Command palette with 2-level navigation, filter, Tab autocomplete, Esc to close
- All commands loaded dynamically from `/api/sessions/{id}/commands` endpoint
- Session-scoped commands: `model`, `thinking`, `rename`, `compact`, `skill:*`
- Global commands: `connect`, `disconnect`, `resume`, `delete`, `quit`
- `connect` supports OAuth flow via `transport/tui/oauth.go` (macOS keychain + `claude auth login`)
- Esc stops the current agent turn immediately (calls `POST /api/sessions/{id}/stop`)
- Resume hint printed on exit: `harness --resume <id>`
- Prompt queue display: `[N queued]` in session info line
- Spinner with 3-line reserved space (no layout jumps)
- `shortenPath` — home dir replaced with `~` everywhere

### Tool rendering — slot-based parallel display
- `reserveSlot(toolID)` — writes `⧖ Executing...` placeholder using tview region tags
- `fillSlot(toolID, result)` — replaces placeholder in-place via `SetText` when result arrives
- Results appear directly below their tool call regardless of arrival order
- Placeholder color matches tool type (amber=tools, violet=Subagent, blue=Skill)
- Tool icons: `⚙` Bash/Fetch/File, `◈` Skill, `⬡` Subagent

### Parallel tool execution
- All tool calls in a ReAct iteration run concurrently via goroutines + `sync.WaitGroup`
- Results emitted as each tool completes (not waiting for others)
- `WaitGroup.Wait()` before next ReAct iteration ensures correct ordering
- Esc cancels all parallel tools simultaneously via shared `context.Context`
- `FileResourceLoader` race condition fixed — each subagent gets its own loader instance

### Subagent tool
- New `Subagent` tool — delegates tasks to ephemeral sub-agents
- Sub-agent inherits model, thinking, maxTurns, maxTokens from parent
- Sub-agent uses `InMemorySessionStoreManager` (ephemeral, not persisted)
- Sub-agent gets its own `FileResourceLoader` (goroutine-safe)
- Sub-agent cannot spawn further sub-agents (`ToolSubagent` excluded from allowed tools)
- Closure-based design — `Agent` has zero knowledge of sub-agent mechanics
- All tools receive `context.Context` for cancellation (`Execute(ctx, input)`)

### CLI transport (`transport/cli/`)
- `harness -p "prompt"` — single-turn CLI mode
- `--output text|json|json-stream` — three output modes
- `json` mode: array of events, one per line (valid JSON + JSONL-friendly)
- `json-stream` mode: JSONL, one event per line in real time
- `turn_start` event included (SSE opened before `SendPrompt`)

### Subcommands
- `harness providers` — list all providers with status
- `harness connect <name>` — connect provider (validates existence, OAuth or API key)
- `harness disconnect <name>` — disconnect provider (validates existence)
- `harness sessions [--all]` — list sessions for CWD or all
- `harness delete <id>` — delete session (validates existence)
- `harness http <addr>` — HTTP server mode
- `harness --resume <id>` — resume session in TUI
- `harness --help` — full usage
- Unknown commands return error with suggestion to use `--help`

### HTTP API
- `POST /api/sessions/{id}/stop` — cancel current turn (Stop button)
- `GET /api/sessions/{id}/messages` — full message history via `AllMessages()`
- `POST /api/sessions/{id}/commands` — `compact` now async (returns `started/queued`)
- `GET /api/sessions/{id}/commands` — `model` param now includes all active model IDs in `values[]`
- `POST /api/providers/{name}/connect` — validates credentials in-memory before persisting
- `POST /api/providers/{name}/disconnect` — persists to settings

### Agent core
- `Session.Stop()` — cancels current turn only (queued prompts continue)
- `Session.AllMessages()` — returns full history including pre-compaction messages
- `Session.Prompt()` now returns `types.PromptStatus` (`PromptStarted` | `PromptQueued`)
- `Session.Messages()` removed from public API (use `AllMessages()` for display)
- `types.EventStop` — emitted when turn is cancelled by user
- `types.MessageMeta{IsCompaction: bool}` — marks compaction messages (no string matching)
- `store.CompactionMessage()` — moved to `store.go` as shared helper
- `FileSessionStore` fully decoupled from `InMemorySessionStore` (own fields, own lock)
- `FileSessionStore.UpdateMeta()` — immediately persists to disk (fixes rename not saving)
- `store.AllMessages()` — reads full JSONL from disk (offset 0) for history display
- `drainFollowUps` — fresh cancellable context per turn (fixes cascading cancellation bug)


### Architecture — Major Redesign

#### `types/` — New top-level shared types
- `types.Message` — provider-agnostic conversation format (replaces `[]json.RawMessage`)
- `types.ContentPart` — discriminated union: text, image, thinking, tool_call, tool_result
- `types.ThinkingPart` — reasoning content with signature for Anthropic prompt caching
- `types.TokenUsage` — named struct replacing anonymous inline struct in Event
- `types.SessionStats` — `ContextWindow` now persisted (was always 0 in meta)
- `types.Credentials` — shared credential type with `CredentialType` enum

#### `providers/` — Redesigned credential system
- `Provider` interface moved from `providers/llm/` → `providers/` (correct ownership)
- `Provider` interface now includes `CredentialType()`, `ResolveCredentials()`, `SaveCredentials()`, `ClearCredentials()`
- Each provider manages its own credential chain: cache → env var → credentials.json → keychain (OAuth)
- `config.CredentialsManager` — neutral key-value store, no provider knowledge
- `config.SettingsManager` — model, thinking level, plus generic KV for provider settings
- `GetOllamaURL()` moved from config → Ollama provider (provider owns its config)
- `/disconnect <provider>` command added to CLI

#### `providers/llm/` — Cleaned up
- `models_catalog.go` + `models_registry.go` merged → `models.go`
- `provider.go` removed (moved to `providers/`)
- `FormatUserMessage*` and `FormatToolResults` removed (replaced by `types.Message` translation)
- `BuildOpenAIBody`, `ParseOpenAIStream`, `TranslateThinkingLevel` unexported (internal only)
- `JsonFloat` unexported → `jsonFloat`
- `OpenAIRequest` struct added — wraps `*types.Request` for OpenAI-compatible providers
- `AnthropicRequest` — `tools` now include `CacheControl` + `EagerInputStreaming` fields
- `AnthropicCacheControl` exported for use by `claude_oauth.go`
- `DoOpenAIStream` signature aligned with `DoAnthropicStream`: `(ctx, client, apiURL, apiKey, req, headers, cb)`

#### `providers/llm/anthropic.go` — Thinking improvements
- `ThinkingConfig` — `output_config` is top-level in body, NOT nested inside `thinking` (was breaking adaptive models)
- `BuildAnthropicThinkingFull` / `BuildAnthropicThinkingFromMeta` — uses `ModelMeta.ThinkingAdaptive` from API
- `isAdaptiveOnly` — added `4-8`, `4-9` patterns
- `xhigh` effort level mapped to `max` for adaptive models (Anthropic API doesn't accept `xhigh`)
- `ParseAnthropicStream` — handles `redacted_thinking` blocks and inline thinking in `content_block_start`
- `ModelMeta.ThinkingAdaptive` + `ModelMeta.ThinkingLegacy` — from API `capabilities.thinking.types`
- `ModelSupportsThinking` — now checks provider cache first, then llm-registry, then name inference

#### `agent/` — Session-centric architecture
- `Agent.New()` returns `*Agent` (not error) — provider resolved per session
- `Agent.NewSession(cwd, model)` — model required, provider resolved internally
- `Session.SwitchModel(ctx, fullModel)` — now accepts `ctx` for compact-before-switch
- `loadModelMeta()` — now updates `s.maxTokens` on model switch (was keeping old model's limit)
- `s.stats.ContextWindow` — now persisted correctly (was always 0)
- `defaultSessionName()` — sessions get `"YYYY-MM-DD HH:MM"` name on creation
- `autoNameFromPrompt()` — first Prompt() auto-renames from user text (like Claude Code)
- `isDefaultSessionName()` — guards against overwriting explicit renames

#### `agent/store/` — FileSessionStore
- `FileSessionStoreManager` + `FileSessionStore` implemented
- Layout: `~/.harness/agent/sessions/<cwd-slug>/<session-id>.meta.json` + `.jsonl`
- `cwd-slug` — path sanitized (/ → -, spaces → _)
- `SessionStore.AddCheckpoint` renamed → `AddCompactionSummary` (more explicit)
- `compactionMessage()` — shared helper, no code duplication
- Write strategy: in-memory only during session, flush on `Close()` and `AddCompactionSummary()`
- `diskReadOffset` — JSONL lines skipped at Open() (pre-compact)
- `diskWriteCount` — messages already on disk, only `messages[diskWriteCount:]` needs appending
- `FileSessionStoreManager` is now the default store for Agent (fallback to InMemory if FS unavailable)
- `Rename()` added to `SessionStoreManager` interface

#### `agent/session.go` — Compact implementation
- `Compact(ctx)` — real LLM summarization via `generateCompactionSummary()`
- `compactSystemPrompt` — dedicated prompt for compaction (produces checkpoint content)
- `requestProgressUpdate()` renamed from `requestSummary()` (used for max-turns UX)
- Auto-compact at 98% context usage (in ReAct loop)
- `SwitchModel` — mandatory compact if new model's context window < current usage
- `EventCompactStart/End` — `EventCompactEnd` carries summary in `Output` field

### Bug Fixes
- `max_tokens: 128000 > 64000` error on model switch — `loadModelMeta()` now updates `maxTokens`
- `xhigh` effort level error — mapped to `max` for Anthropic adaptive models
- Thinking not shown in footer for opus-4-7/4-8 — `ModelSupportsThinking` now checks provider cache
- `ContextWindow: 0` in meta.json — `updateStats()` now syncs `s.stats.ContextWindow`
- `↑3` input tokens with heavy cache — now shows `Input + CacheRead` (total context)
- claude_oauth mutex deadlock on 2nd turn — fixed (lock released before HTTP call)
- `req.Model` empty — fixed in agent options flow
- OpenCode-Go models not showing — FetchModels missing Authorization header
- `output_config` nested inside `thinking` — moved to top-level body (adaptive thinking)

### CLI
- `/disconnect <provider>` — removes credentials and closes active session if using that provider
- No-provider startup — CLI shows hint instead of `exit 1`
- `/connect` auto-initializes session after successful connection
- `tryInitSession()` replaces `tryInitAgent()` — agent is now always available
- `ModelSupportsThinkingWithLookup` — uses provider cache for authoritative thinking detection

---

## [0.5.0] - 2025-05-28

### Agent — Session & Loop Improvements

#### Max Turns — Smart Limit with LLM Summary
- Renamed `MaxLoops` → `MaxTurns` everywhere (agent, config, session, CLI)
- `MaxTurns = 25` now means exactly 25 LLM calls total (24 ReAct + 1 summary reserved)
- When the turn limit is reached mid-task, a final summary call is made **without tools**
- The LLM summarizes: (1) what was completed, (2) what still needs to be done, (3) asks user to continue or change direction
- No error returned — `ErrMaxTurnsReached` eliminated — max turns is not an error, it's a normal flow state
- `EventMaxTurnsReached` emitted for SDK users who need to detect it programmatically
- CLI shows no warning — the LLM summary is sufficient UX

#### System Prompt — Context Engineering
- Removed redundant `## Tools` section — tool descriptions already arrive via API schema
- Added always-present tool policy line: *"Do not use bash for file operations when dedicated file tools are available"*
- Policy survives `SYSTEM.md` override (separate block, not part of identity)
- `buildSystemPrompt(cwd, res)` now receives working directory and injects it as `## Working Directory`
- Skills listed in system prompt with name + description (not just name)
- `skill` tool only registered and listed when skills are actually discovered
- Tool descriptions are the single source of guidance — no duplication

#### Tool Registry — Ordered Output
- Registry now preserves insertion order via `order []string` slice
- `Definitions()` returns tools in registration order — deterministic for system prompt and LLM
- `Clone()` preserves insertion order

#### Tool Execute Signature
- `Execute func(json.RawMessage) (string, error)` — restored clean `(string, error)` contract
- `string` always goes to LLM (even on error — descriptive error text)
- `error` is the Go-level signal for `IsError` on events/results — no string prefix conventions
- `Registry.Run()` returns `(string, error)` — clean, no `[ERROR]` prefix detection

#### Resource Loader — Redesigned Interface
- `Load()` takes no parameters — config set at construction time in each implementation
- `ReadSkill(name string) (string, error)` added to interface — loader knows how to read its own skills
- `SystemPrompt` field renamed to `SystemMD` — clearer intent
- `NilLoader.ReadSkill()` returns descriptive error
- `FileResourceLoader` placeholder ready for implementation

#### Tool `skill` — Simplified
- Renamed from `ReadSkill` → `Skill`
- Takes only `readFn func(name string) (string, error)` — no knowledge of skill list
- Description is concise: *"Read the full instructions for a skill by name"*
- No skill listing in description — that's the system prompt's job
- Agent passes `resourceLoader.ReadSkill` directly as the read function

### Event System — Cleanup & New Events

#### Removed phantom events (never emitted)
- `EventThinking` — removed
- `EventThinkingEnd` — removed  
- `EventText` — removed

#### Renamed
- `EventStreamToolBuilding` → `EventToolStart` — LLM announced a tool call (name + ID known)

#### New events
- `EventToolArgsDelta` — tool arguments arriving in streaming fragments (Option B implemented)
- `EventMaxTurnsReached` — emitted after LLM summary when turn limit hit

#### Reorganized with clear sections
```
── Turn lifecycle ──    EventTurnStart, EventTurnEnd
── ReAct loop ──        EventLoopStart, EventLoopEnd
── Streaming text ──    EventStreamTextDelta, EventStreamTextEnd
── Streaming thinking ─ EventStreamThinkingDelta, EventStreamThinkingEnd
── Tools ──             EventToolStart, EventToolArgsDelta, EventToolCall, EventToolResult
── Tokens & cost ──     EventTokens
── Errors ──            EventError
── Limits ──            EventMaxTurnsReached
── Compaction ──        EventCompactStart, EventCompactEnd
```

### Token Usage — Fixes & Cleanup

#### `TokenUsage` type (named, replaces anonymous struct)
- `Input` — last turn input tokens (= current context size sent to LLM)
- `Output` — last turn output tokens
- `CacheRead/Write` — last turn cache tokens
- `TotalOutput` — accumulated output across session
- `TotalCacheRead/Write` — accumulated cache across session
- `CostUSD` — accumulated cost (session authority)
- `ContextUsage` — last input / context window (0.0–1.0)
- `ContextWindow` — model context window size
- `TotalInput` removed from `TokenUsage` — moved to `SessionStats` only (billing reference)

#### Footer fixes
- `↑` now shows `Input` (last turn = current context size) — not accumulated
- `↓` shows `TotalOutput` (accumulated session total)
- `%/size` shows `ContextUsage × 100` + `ContextWindow` — e.g. `13.0%/1.0M`
- Renderer reads all stats from session via `EventTokens` — never recalculates
- `ContextWindow` sourced from session (via `provider.ModelMeta()`) — not from CLI config

#### `SessionStats` — billing reference
- `InputTokens` kept with clear doc comment: accumulates across turns (for billing reference only)
- `ContextWindow` added to `Stats()` snapshot

### Config
- `max_loops` → `max_turns` in `harness.json` / `config.go`

---

## [0.4.0] - 2025-05-28

### Architecture — Major Redesign

#### `types/` — Shared Core Types (new top-level package)
- New `types/` package: zero dependencies (stdlib only), foundation of the dependency graph
- Moved all shared data types here: `ToolDef`, `ToolCall`, `ToolResult`, `Request`, `Response`, `Usage`, `ImageData`, `StreamEvent`, `StreamCallback`, `ModelMeta`, `ModelInfo`, `Event`, `Handler`, `SessionStats`
- Eliminates cross-package coupling — all modules depend on `types/`, not on each other

#### `providers/` — Redesigned Provider System
- Provider model cache is now `map[string]ModelMeta` — O(1) lookup by model ID
- New `Provider.ModelMeta(modelID)` interface method — direct cache lookup, no registry bypass
- `FetchModels()` now does all enrichment work (API + registry + pricing) and fills the map
- `providers.Resolve(fullModel)` is the single entry point: splits `provider/model`, finds provider, lazy-fetches models, validates model exists — replaces `Get()` + `ParseModel()` which are now internal
- `llm.ParseModel` unexported — internal to `providers/llm/`
- Removed `ModelMetaFor()` helper — no longer needed with map-based cache

#### `agent/` — Session-based Architecture (replaces old monolithic Agent)
- **`Agent`** is now a pure factory — holds global config, spawns `Session` objects via `NewSession(cwd)` and `ResumeSession(id)`
- **`Session`** is the core of a conversation: owns store, provider, model, tools, system prompt
- Store is the **single source of truth** for messages — no in-memory history duplication
- Every `Prompt()` call reads history from store at each ReAct iteration
- `Session.SwitchModel(fullModel)` — resolves + validates model via `providers.Resolve()`
- `Session.SwitchThinking(level)` — updates thinking level mid-conversation
- `Session.Compact(ctx)` — truncates old messages, emits `EventCompactStart/End`
- `Session.Stats()` — returns `SessionStats` snapshot: tokens, cost, context usage, context window
- `Session.Subscribe(Handler)` — single event subscriber per session
- **`agent/store/`** — `SessionStore` + `SessionStoreInstance` interfaces + `InMemoryStore`
- **`agent/resources/`** — `ResourceLoader` interface + `NilLoader` (FileLoader coming soon)
- **`agent/tools/`** — full tool registry with `Clone()`, `ReadSkill` injectable per session

#### Session Stats — Single Source of Truth
- `Session` accumulates: `InputTokens`, `OutputTokens`, `CacheRead`, `CacheWrite`, `CostUSD`, `ContextUsage`, `ContextWindow`
- `CostUSD` always calculated from model pricing (no subscription special-casing)
- `ContextUsage` = last turn input tokens / model context window
- `ContextWindow` sourced from `provider.ModelMeta()` — authoritative, updated on `SwitchModel()`
- All stats emitted via `EventTokens` — renderer reads, never recalculates

#### CLI Transport — Simplified
- `NewCLI(agent)` — takes only `*Agent`, no provider param
- `Run(ctx)` — no agent/provider params
- `Session` created per CLI run via `agent.NewSession(cwd)`
- `/clear` now closes session and creates a fresh one
- `/model` uses `session.SwitchModel()` — validates model before switching
- `/thinking` uses `session.SwitchThinking()` — propagates to next LLM call
- Renderer no longer calculates cost or context% — reads from `EventTokens` (session is authority)
- Footer now shows `1.9%/1.0M` (context usage + window size) — both from session
- Footer tokens are accumulated session totals, not per-turn

#### `AgentOptions` — Clean SDK Interface
- `Model string` — `"provider/model"` format, provider resolved internally via `providers.Resolve()`
- `ExtraTools []tools.Tool` — inject custom tools without replacing defaults
- `Store`, `ResourceLoader` — optional infrastructure overrides
- Removed `Provider` field — provider resolved from `Model` string
- `New()` returns `(*Agent, error)` — fails fast if provider inactive or model not found

### SDK Usage (new)
```go
a, err := agent.New(agent.AgentOptions{
    Model:        "opencode-go/deepseek-v4-pro",
    SystemPrompt: "You are helpful.",
})
session, _ := a.NewSession(".")
session.Subscribe(func(e types.Event) { ... })
session.Prompt(ctx, "hello", nil)
stats := session.Stats() // CostUSD, ContextUsage, ContextWindow, tokens
```

### Bug Fixes
- `opencode-go` models now visible in `/model` — `FetchModels()` was missing Authorization header
- `req.Model` was empty (model not set in Request) — fixed by passing modelID through agent options
- Footer output tokens were per-turn instead of accumulated — now uses `TotalOutput` from session
- `ContextUsage` in footer was missing context window size — now shows `1.9%/1.0M`

---

## [0.3.0] - 2025-05-25

### Tools
- `fetch` now supports binary downloads via `output_path` parameter
- Binary-safe: writes raw bytes directly to disk (images, PDFs, ZIPs, any content)
- `~/` home directory expansion supported in `output_path`
- Auto-creates parent directories
- Without `output_path`: existing text behavior unchanged (JSON, HTML, APIs)
- Updated tool description to guide model toward `output_path` for binary content
- Agent no longer needs `bash + curl` for any HTTP interaction

## [0.2.0] - 2025-05-25

### Pricing & Cost Display
- Pricing sourced from **llm-registry** for all providers — no more hardcoded values
- `ModelMeta` now carries `InputCost`, `OutputCost`, `CacheReadCost`, `CacheWriteCost` ($ per 1M tokens)
- `parseRegistry()` extracts all 4 price fields: `input_cost`, `output_cost`, `cache_input_cost`, `cache_output_cost`
- `ApplyRegistryPricing()` does a second-pass pricing fill for Anthropic and Ollama after their capability APIs run
- `enrichMeta()` applies registry pricing at all 4 fallback tiers
- `stripDateSuffix()` matches versioned model IDs (`claude-sonnet-4-20250514` → `claude-sonnet-4`)
- Footer hides `$` when no pricing data is available (GLM, Kimi, MiniMax, MiMo)
- Footer shows `$0.021 (sub)` for subscription/local providers: `claude-oauth`, `opencode-go`, `ollama`, `ollama-cloud`

### Architecture — Backend/Frontend Separation
- Add `IsSubscription() bool` to `llm.Provider` interface — each provider declares its own billing model
- Add `SetThinkingLevel(level string)` to `llm.Provider` interface — runtime level propagation
- Add `Agent.Provider()` to expose current provider to transport layer
- Removed hardcoded `subPricingProviders` map from CLI — frontend just reads `provider.IsSubscription()`
- Add `ModelSupportsThinking(fullModel string)` public wrapper in providers package

### Thinking Level Fixes
- `/thinking` command now updates provider instance, renderer, and footer **immediately**
- `disable` level fully suppresses thinking: sends `think=false` / `type=disabled` to LLM and hides `• level` from footer
- Footer thinking label shown for **all** models that support it (not just Anthropic)
- `NewCLI` and `/model` switch filter `disable` so renderer never shows it as a label

### Documentation
- Added `AGENTS.md` — full AI agent development guide covering architecture, interfaces, data flow, patterns, and anti-patterns

## [0.1.0] - 2025-05-25

### 🎉 Initial Release

First public release of Harness — a minimal AI agent harness built in pure Go.

### Core
- ReAct loop (Think → Act → Observe → Repeat) with configurable max iterations
- Streaming-first architecture — all providers implement SSE streaming
- Event-driven rendering — agent emits events, transport layer renders
- Per-user conversation history with automatic compaction
- In-memory model cache populated at startup from provider APIs

### Providers
- **Claude OAuth** — use your Claude Pro/Team/Enterprise subscription via `claude auth login`
- **Anthropic** — standard API key authentication
- **OpenAI** — GPT-4o, o1, o3, o4-mini series
- **OpenCode Go** — low-cost open coding models (GLM, Kimi, DeepSeek, Qwen, MiniMax, MiMo)
- **Ollama Cloud** — cloud inference with API key
- **Ollama** — local auto-detection, no config needed

### Thinking
- Extended thinking support across all providers
- Configurable levels: `disable` / `low` / `medium` / `high` / `xhigh`
- Universal level mapping per provider (Anthropic effort, OpenAI reasoning_effort, DeepSeek max, Ollama think flag)
- Thinking displayed with gray border, output with cyan border
- `/thinking` command to view/change level at runtime
- `HARNESS_THINKING` env var override
- DeepSeek `reasoning_content` correctly passed back in multi-turn tool call history

### Tools
- `bash` — shell execution with timeout and error handling
- `read_file` — file reading with offset/limit for large files
- `write_file` — file creation with auto directory creation
- `edit` — atomic find/replace (old_text must be unique)
- `fetch` — native Go HTTP client (GET/POST/PUT/DELETE with headers and body)

### Model Management
- `/model` command — list all available models grouped by provider
- `/model <provider/model>` — switch model at runtime (no restart needed)
- Auto-detection of default model from connected providers
- Model capabilities from: Anthropic API → Ollama `/api/show` → llm-registry (GitHub) → hardcoded → inference by name
- `HARNESS_MODEL` env var override
- Persisted in `~/.harness/settings.json`

### Provider Management
- `/connect <provider>` — connect providers interactively
- `/connect` — list all providers with connection status
- API key providers: masked input with `****`
- Claude OAuth: delegates to `claude auth login`, imports tokens to `~/.harness/credentials.json`
- Ollama: auto-detected at startup (no `/connect` needed)
- Env vars take precedence over stored credentials
- Provider status exposed via `GetProviderStatuses()` for transport layer

### CLI Transport
- ASCII art banner with active model display
- Streaming text rendering with left border (cyan for output, gray for thinking)
- Animated spinner during model thinking (Jade-themed tactical phrases)
- One spinner label per agent turn
- Tool calls with icons (⚡ bash, 📄 read_file, ✏️ write_file, 🔧 edit, 🔍 fetch)
- Tool results with timing and truncation
- Compact footer: `╰ 3.2s ↑1.2k ↓156 R8.0k W1.2k $0.012 0.4%/1.0M opencode-go/deepseek-v4-pro`
- Word-wrap aware rendering (reads terminal width)
- `/help` command with full reference
- `/clear` to reset conversation
- Raw terminal input with Ctrl+V clipboard image paste (macOS/Linux/Windows)
- Image support via file paths in messages

### Configuration
- Zero-config startup — works with `./harness` out of the box
- `~/.harness/credentials.json` — single file for all provider credentials
- `~/.harness/settings.json` — active model + thinking level
- `harness.json` — optional project-level config
- All env vars documented in `/help`

### Architecture
- Single `Provider` interface — streaming only, no dual-mode
- `llm/` — core types, SSE parser, image loader
- `llm/providers/` — all provider implementations + infrastructure
- `llm/registry/` — provider factory (Resolve)
- `agent/` — ReAct loop + event system
- `transport/cli/` — terminal rendering (decoupled from core)
- `tools/` — tool registry + implementations
- Model capabilities: 3-tier resolution (API → llm-registry → hardcoded → inference)
- ~9MB single binary, 1 dependency (`charmbracelet/x/term`)
