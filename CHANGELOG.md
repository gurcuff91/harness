# Changelog

All notable changes to this project will be documented in this file.

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
