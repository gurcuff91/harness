# Prompt origins, visible channels, SessionInfo environment, message times — design

Status: approved by the owner (2026-11-19); implemented in v0.109.0.

## 1. Three prompt icons

`promptIcon(origin)` (TUI, live echo and reloaded history):

- `❯` — no origin or `user`: the person at the terminal.
- `◷` — `scheduled`.
- `⇄` — everything else: it came from outside (`colleague`, `herdr`,
  `slack:<id>`, `telegram`, any future origin).

The `@` icon added for Slack in 0.108.0 goes away.

## 2. Herdr is an origin

Herdr pastes another agent's message into the TUI as if it were typed. The TUI
sends a submitted text that starts (after leading spaces) with `<hc` — `<hc
from=…>` from another agent, or a bare `<hc>` note from Herdr — with origin
`herdr`. It is persisted in `meta.origin`, so the icon survives a resume.

An origin is display information: it grants and removes nothing. Anyone who
pastes text starting with `<hc` gets this origin; keep that in mind if origins
ever gate tools.

## 3. Channel names

- `agent.Channel` gains `Name(ctx, id) string`: a readable name for a bound
  conversation (Slack: `#channel` for a channel, `@user` for a DM; the id when
  it cannot tell). Resolved once when the session opens.
- `Session.Channels()` returns `[]agent.ChannelInfo{Type, ID, Name}` (was
  `[]string` of origins, shipped the same day in 0.108.0).
- API: a live session's detail returns `channels: [{type, id, name}]`.

## 4. TUI status line

`~/proj (main) • <session name> (3/250) • ⇄ #producto-y, @gus` — only when the
session listens to something; shortened to `⇄ slack ×2` when the full form
does not fit the width. Taken from the create/resume response (channels never
change while a session is open).

## 5. SessionInfo

New fields:

- `channels`: `[{type, id, name}]`.
- `os`, `arch`: `runtime.GOOS`, `runtime.GOARCH`.
- `terminal`: a label for the terminal the session runs in, set ONLY by the TUI
  (an SDK embedder, `serve`, Telegram, ACP and `-p` have no terminal of their
  own and would only report the launcher's). Best-effort detection, first match
  wins: `HC_TERMINAL_ID` → `Herdr (<id>)` (checked first: Herdr sets
  `TERM_PROGRAM=ghostty` too); `TERM_PROGRAM` (Ghostty, WezTerm, iTerm2,
  Terminal.app, VS Code, tmux; any other value as is); `WT_SESSION` → Windows
  Terminal; `KITTY_WINDOW_ID` → kitty; `ALACRITTY_SOCKET`/`ALACRITTY_LOG` →
  Alacritty; `KONSOLE_VERSION` → Konsole; `VTE_VERSION` → VTE terminal;
  `TERMINAL_EMULATOR=JetBrains-JediTerm` → JetBrains; else `Generic terminal`.
  SDK: `AgentOptions.Terminal` / `harness.AgentWithTerminal`.

No hostname, no user name (a Slack turn can call SessionInfo).

## 6. Message times

`types.Message` gains `CreatedAt time.Time` (`created_at`, omitempty), stamped
in UTC by `store.Session` on every append (prompts, replies, tool results,
compaction and checkpoint markers) unless already set. Older messages have
none and read as before. Providers build requests from `parts`, so the model
never sees it (a test pins it). `GET /api/sessions/{id}/messages` returns it
with no other change.
