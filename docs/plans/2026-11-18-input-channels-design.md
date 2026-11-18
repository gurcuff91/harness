# Input channels (Slack first) — design

Status: approved by the owner; implemented in v0.108.0.

## Problem

Several harness instances, each working in its own project, post to Slack with
the same account (jade-kaiban binds a profile with the Slack tools to its
sessions). Only the process running `harness slack` listens (RTM), so when a
human mentions the account in a channel where instance A posted a report, the
answer comes from the `harness slack` bot, which runs in another cwd and knows
nothing about A's project.

## Idea

A **channel** is a way prompts reach a session. Today three exist implicitly
through the prompt origin: `user`, `scheduled`, `colleague`. This design adds
external channels, starting with Slack: a session can be told, when it is
created or resumed, to listen to Slack channel `C0123`. Mentions there become
prompts of THAT session, which has the project context, and its answers go back
to that channel. Exclusive: one channel, one session (1:1).

It is purely additive. `harness slack` is not touched; it may be deprecated
later in favour of channels.

## Decisions

1. **Unit = a whole Slack conversation id** (`C…` channel or `D…` DM — a DM is
   just another channel). No thread binding.
2. **Exclusive 1:1.** A channel claimed by a live session cannot be claimed by
   another one; the attempt fails and names the owner.
3. **Only mentions** (`<@me>`) become prompts in a channel; in a DM every
   message does (there is no mention there). Own messages, bot messages and
   messages with a subtype are ignored, as in the transport.
4. **Several channels per session.** The prompt origin carries the id:
   `slack:<ID>`. No new field.
5. **Replies follow the origin.** Only turns whose origin is `slack:<ID>` are
   published, and only to `<ID>`. Other origins (user, scheduled, colleague)
   never go to Slack.
6. **What is published = what the transport publishes today:** all the turn's
   text (accumulated from deltas with a typing indicator, flushed at
   `text_end`, before each `tool_call`, at `turn_end` and at
   `max_iterations_reached`), processed before sending (file-upload tags,
   mrkdwn, splitting), harness errors (e.g. a provider 429) and the compaction
   notices. In the channel, not in a thread (the bot never uses `thread_ts`).
7. **Explicit only, never persisted.** Channels are active only when passed to
   `NewSession`/`ResumeSession` (TUI: `--slack-channel`). Nothing is stored in
   the session meta; a resume without channels listens to nothing. An explicit
   channel is honored or the open fails (same rule as `--model`).
8. **Bound at open time only** (create/resume). Binding/unbinding a live
   session is out of scope for now.
9. **Ref = id or name** (`C0123`, `producto-y`, `#producto-y`), resolved once
   to the id when the session opens. The claim and the origin always use the id.
   Unknown or ambiguous name: the open fails. `--slack-channel` is repeatable.
10. **Independent package `channels/slack`** with its OWN copy of the bot,
    the tag parser and the attachment handling (the tags will change). It does
    not import `transports/slack`.
11. **Credentials are explicit** (`slack.New(workspace, xoxc, xoxd)`). The CLI
    reads them with `transports/slack.LoadCredentials()` — the only point that
    touches the transport. A NOTE in the code: when the transport is
    deprecated, `channels/slack` will likely get its own `LoadCredentials`
    reading the same stored credentials.
12. **Coexistence with `harness slack` on the same account is not supported**
    (both would answer a mention). Documented, not prevented.
13. **`serve` has no flag.** It (and the TUI) register the Slack channel
    implementation automatically when stored credentials exist; registering
    opens nothing. Each client asks per session through the API. Without
    credentials a session asking for `slack` fails with
    `slack channel not available: run harness slack login`.
14. **Tools and directive come from the channel, not from a profile.** A
    session that receives at least one Slack input channel gets the Slack tools
    (`SlackListChannels`, `SlackListUsers`, `SlackPost`, `SlackMessages`, bound
    to the channel's own bot) and the channel's system-prompt section directly.
    Profiles stay for user extensions. A tool name that already exists in the
    session (e.g. a profile that also brings Slack tools) makes the open fail
    instead of silently shadowing it. `SlackAsk` is out of this cut: the awaited
    DM reply could be claimed by another session and every instance receives
    every event.
15. **Tags.** Inbound prompt (Herdr style), `channel` always present (DMs
    included, since a session may listen to several):
    `<slack user="U0123" channel="C0456">text\n<slack-attach path="/tmp/f.csv"/></slack>`.
    Outbound file upload: `<slack-upload path="/abs/file.png"/>` (removed from
    the posted text). Images still travel as prompt images, without a tag.
16. **Slack first.** Telegram comes later with its own design: the Bot API
    allows one `getUpdates` consumer per token (409 otherwise), so its
    exclusivity is per token, not per chat.

## Design

### Contract (`agent/`)

```go
type Channel interface {
    Type() string                                         // "slack"
    Resolve(ctx context.Context, ref string) (id string, err error)
    Listen(ctx context.Context, id string, deliver func(Inbound)) (stop func(), err error)
    HandleEvent(ctx context.Context, id string, evt types.Event) // events of its own turns only
    Directive(ids []string) string // system-prompt section naming the bound channels
    Tools() []tools.Tool           // tools bound to this channel's own client
}

type Inbound struct {
    Text   string
    Images []types.ImageData
}
```

- `AgentOptions.Channels []Channel` / `harness.AgentWithChannels(...)`:
  implementations available by type. The agent closes them in `Close`.
- `types.InputChannel{Type, Ref string}` and
  `agent.WithSessionChannels(...InputChannel)` — a `SessionOption` valid for
  `NewSession` and `ResumeSession`.
- Opening: for each channel `Resolve` → claim → `Listen`. Any failure undoes
  what was done (stop, release) and the open fails.
- `deliver` calls `Session.Prompt(text, WithOrigin("slack:<id>"), images)`:
  normal queue and order (a mention during a turn waits its turn).
- The session forwards to `HandleEvent` the events of turns whose origin is
  `slack:<id>` (from the prompt echo — `received_prompt` / `follow_up_start` —
  to `turn_end`), to that id's channel only.
- Closing the session or the agent: `stop()` and release the claim.

### Exclusivity registry

- `SettingsStore` namespace `channels`, key `entryKey("slack", id)`, value
  `{type, id, session, pid, cwd, claimed_at}`; claim and release through
  `SwapValue` (atomic, also across processes), via typed `internal/config`
  manager methods.
- A claim is free when its pid no longer exists. Not the `instances` registry:
  that one belongs to `server`, above `agent`, and an embedded agent without a
  server never appears in it. The default store is machine-local, so a pid is
  meaningful. Accepted risk: a recycled pid keeps a dead claim busy until it is
  released by hand.
- Release only if the claim is still ours (session and pid match).
- The conflict error names the owner session, pid and cwd.

### `channels/slack`

- `New(workspace, xoxc, xoxd) agent.Channel`.
- One RTM socket per Channel, opened at the first `Listen`, closed with the last
  `stop`, events dispatched by id; reconnects with backoff without closing any
  session.
- `Resolve` with the same lookup `SlackPost` uses today (own copy).
- `HandleEvent` implements decision 6.

### API and clients

- `POST /api/sessions` and `POST /api/sessions/{id}/resume` accept
  `channels: [{type, ref}]`; `client.WithChannels(...)`.
- TUI: `--slack-channel <ref>` (repeatable), sent through that API.
- `origin` `slack:<id>` must be understood wherever an origin is compared
  today (TUI icon, transport `quiet` logic, etc.) without changing the
  transport's own `"slack"` origin.

### Out of scope (later)

Telegram; live bind/unbind; `SlackAsk`; `channels/slack.LoadCredentials`.

## Tests

- Fake Slack (HTTP + websocket): filters (mention, DM, own/bot/subtype),
  resolution by id/name/ambiguous, reconnection, publishing (cuts, typing,
  errors, compaction, tags/uploads).
- Registry: two claims on one channel, orphaned claim (dead pid), release does
  not delete another owner's claim, cross-process `SwapValue`.
- Agent with a fake `Channel`: only `slack:<id>` turns reach `HandleEvent`, on
  the right id; open failure undoes everything; close releases.
- API/TUI flag parsing; hand-made mutants; a live check with the owner's account
  before the release.

## Addendum (same release): the Slack transport is removed

Decided before v0.108.0 shipped: `transports/slack` (the `harness slack` RTM
bot, its admin list, slash commands, SlackAsk and per-channel sessions) is
removed rather than deprecated. `channels/slack` took over the login
(`harness slack login` stays), so decisions 10–12 above describe a transition
that never shipped: there is no transport to coexist with or to depend on.
