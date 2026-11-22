# Telegram as an input channel — design

Status: approved by the owner (2026-11-22). Target: v0.116.0 (breaking).

## Goal

Replace `transports/telegram` (a bot that creates and owns one session per
paired chat) with `channels/telegram`, an `agent.Channel` like Slack's: an
existing session listens to a Telegram bot. Same model as every other channel:
the channel never creates sessions.

## 1. Architecture and activation

- New package `channels/telegram`: `agent.Channel` with the Bot API client, the
  long-polling reader, publishing, files, directive, token storage
  (`LoadToken`/`SaveToken`/`DeleteToken`). `transports/telegram` is deleted with
  its `harness.go` aliases; an independence test forbids importing it again.
- `Type()` = `"telegram"`. `New(token)` takes the token explicitly.
- **The conversation is the bot.** `Resolve(ref)` validates the ref and returns
  `<bot_id>` (via `getMe`): the claim is `telegram:<bot_id>`, so one process per
  bot (Telegram allows one getUpdates consumer per token) and one session per bot.
- **The ref is the list of trusted Telegram usernames** (normalized: lower case,
  no `@`, deduplicated, comma-joined). Every message from another user is ignored
  silently (logged). An empty list is invalid.
- **Activation.** The channel is REGISTERED when a token exists: `--telegram-token`,
  then `TELEGRAM_BOT_TOKEN`, then the stored one (`harness telegram token`).
  Registering opens nothing. It is BOUND by:
  - TUI: `--telegram-allow @gus` (repeatable; commas also accepted; env
    `TELEGRAM_ALLOW`, comma-separated).
  - API: `channels:[{type:"telegram", ref:"gus,ana"}]` (serve registers the channel
    with the same token rules; the client binds it).
  - Startup errors (exit 1): `--telegram-allow` with no token anywhere;
    `--telegram-token` without `--telegram-allow`.
- CLI: `harness telegram` keeps only `token [tok]`, `token --status`, `token --delete`.

## 2. Message flow

- **In.** Allowed sender → `<telegram chat="123" user="@gus">text</telegram>`, origin
  `telegram:<bot_id>:<chat_id>`. Images: prompt images AND saved files. Albums
  grouped (1 s). Documents: any type, ≤ 50 MB, never written executable:
  `<telegram-attach name="x" path="/tmp/…"/>`. `/info` and `/context` are answered
  directly (read-only, `agent.SessionView`); `/start` ignored; any other `/x` is a
  normal message. Voice/audio/video out of scope.
- **Routing by prefix.** The claimed conversation is the bot (`telegram:<bot>`) but a
  turn's origin carries the chat (`telegram:<bot>:<chat>`). The tap routes a turn to
  a bound conversation whose origin equals the echo's or is its prefix followed by
  `:`. `listensTo` uses the same rule.
- **Out.** `HandleEvent` reads the chat from the echo's origin and publishes to it
  through a per-chat queue goroutine (never blocks). Flush at `text_end`, before a
  tool, at `turn_end`, at max iterations. Typing while the turn runs; compaction
  notices; errors. MarkdownV2 in ≤ 4096-char chunks, plain-text fallback; tables →
  code blocks. Uploads: `<telegram-upload path="/abs/x"/>`; images/GIFs as
  photo/animation, the rest as documents.
- **Directive** `## Telegram`: the envelope, attach/upload tags, the commands, and
  "Who you obey from Telegram" (an instruction, not enforcement). No tools.

## 3. Agent changes

1. `agent.SessionViewOf(ctx) (SessionView, bool)` — read-only (`Info`, `Context`,
   `Quota`), via the Listen ctx like `SessionName`. `Channel` unchanged.
2. The quota cache moves from `server/` to `internal/providers`
   (`CachedQuota(name, refresh)`), shared by the endpoint and `SessionView`.
3. Plain-text `/info` and `/context` formatters move to `internal/infoview`
   (shared by ACP and Telegram).
4. Prefix routing in `sessionChannels` (above).
5. `Session.turnOrigin` (lock-free), set from the prompt echo, cleared at turn_end.
6. Schedules: `Schedule.Origin` (JSON `origin`), saved by the Schedule tool only
   when the session listens to the turn's origin. `schedule.FireFunc` becomes
   `func(Schedule)` (breaking). `fireScheduledPrompt` ALWAYS wraps the prompt in
   `<scheduled slug="…">…</scheduled>` and uses `PromptWithOrigin(s.Origin)` when the
   session listens to it, else `PromptWithOriginScheduled()`.
7. TUI: `Telegram` in `channelToolPrefixes`.

## 4. Removal, migration, errors, tests

- Removed: `transports/telegram`, `harness.RunTelegram`/`TelegramOption`/`TelegramWith*`,
  the `harness telegram` bot and `pair`/`unpair`/`list`, the `telegram_allowlist` and
  `telegram_sessions` namespaces and the chat-binding helpers. The stored token stays.
- Migration: `harness telegram` → `harness --telegram-allow @gus`; pairing → the allow
  list; a server bot → `serve` + `channels:[{type:"telegram",ref:"gus"}]`; SDK
  `RunTelegram` → `AgentWithChannels(telegram.New(token))` + `WithSessionChannels`.
- Errors: invalid token → open fails (API 400); bot taken by another process → 409;
  Telegram 409 (a foreign consumer) → log + backoff; read failure → 3 s backoff;
  publish failure → plain-text retry, then log.
- Tests: fake Bot API (filter, envelope, files, split, MarkdownV2 fallback, uploads,
  commands, other-origin turns publish nothing), prefix routing, schedules (from
  Telegram back to the chat, from the TUI as scheduled, fallback, envelope), CLI
  activation rules, independence, moved quota cache, mutants, live with a real bot.
