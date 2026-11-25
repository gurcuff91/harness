# Slack threads — design

Status: approved by the owner (2026-11-25). Target: v0.122.0.

## Problem

The Slack channel has no thread support. A mention inside a thread reaches the
session as a bare message (`thread_ts` is dropped), the model cannot tell what
the thread is about, the reply is posted in the channel instead of the thread,
and `SlackMessages` (`conversations.history`) never returns thread replies.

## Decisions

- Context is on demand, not automatic: the block carries the thread, the model
  reads it with `SlackMessages` (owner's choice, B).
- Only mentions are delivered, in a thread as in a channel (owner's choice, A).
  Following a thread after replying is a possible later improvement.

## 1. Input and origin

- `RTMEvent` gains `thread_ts` and `reply_count`.
- A message is "of a thread" when `thread_ts` is set and differs from its own
  `ts`. A thread's root message (equal ids, with or without replies) is a
  channel message.
- Subtype `thread_broadcast` (a thread reply also sent to the channel) is no
  longer dropped; it is of its thread.
- The prompt block carries the thread only when there is one:
  `<slack user="U1" channel="C0456" thread="1700000000.000100">`.
- Origin of the turn: `slack:<channel>` for a channel message,
  `slack:<channel>:<thread_ts>` for a thread message (the tap already routes
  sub-conversations by prefix, so a schedule created in a thread fires back into
  it). The claimed conversation stays the channel.

## 2. Output and tools

- `HandleEvent` reads the thread from the echo's origin; the turn's replies
  (text, file uploads, errors, compaction notices) are posted with `thread_ts`
  when there is one. Typing stays in the channel (RTM cannot type in a thread).
  One publisher per `channel[:thread]`.
- `SlackMessages` gains optional `thread`: only that thread's messages
  (`conversations.replies`), chronological, root first; `limit` keeps the most
  recent and says how many earlier messages were omitted; `files` and the image
  cap work as before. Each message reports `thread`; root messages in a channel
  listing report `reply_count`.
- `SlackPost` gains optional `thread` (reply in a thread of ANOTHER channel).
  Its "do not post into the conversation you are answering" rule distinguishes
  threads: same channel and same thread is refused, another thread of the same
  channel is allowed.
- Directive: if the block has `thread=`, call `SlackMessages` with that channel
  and thread BEFORE answering; the reply goes to that thread by itself; a
  message without `thread=` is answered in the channel.

## 3. Errors, compatibility, tests

- Unknown thread or wrong channel in `SlackMessages`: a clear error
  (`thread_not_found`), not an empty list.
- A post into a thread that no longer exists falls back to the channel (logged)
  so the reply is not lost.
- `reply_count` is only displayed, never used to decide anything.
- Old origins (`slack:C0456`) keep working; claims and sessions do not change.
- Tests: input (thread block and origin; root is not a thread; broadcast
  delivered; no mention ignored), output (thread_ts on text and uploads; none
  for a channel turn; distinct publishers; fallback), SlackMessages (replies,
  root first, limit + omitted note, files, error), SlackPost rule, directive,
  end to end with a real agent and a fake Slack (mention in a thread → the model
  reads it → reply with thread_ts; schedule from a thread returns to it),
  mutants, full suite.
