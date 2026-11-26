---
title: Telegram
section: Frontends & channels
order: 240
summary: Connect a Telegram bot to a session, restrict it to trusted usernames, and exchange files and read-only commands in chat.
---

Telegram is an input channel: a session can listen to a bot, and messages from trusted users become prompts of that session. Replies are sent back to the chat the message came from.

## Set up the bot

Create a bot with Telegram's BotFather and keep its token. The token is looked up in this order:

1. `--telegram-token` on the command line.
2. The `TELEGRAM_BOT_TOKEN` environment variable.
3. The token saved with `harness telegram token`.

```bash
$ harness telegram token <token>
$ harness telegram token --status
$ harness telegram token --delete
```

Saving verifies the token with Telegram first.

## Listen to the bot

In the TUI, list the trusted usernames with `--telegram-allow` (repeatable; commas also work; or `TELEGRAM_ALLOW`, comma separated):

```bash
$ harness --telegram-allow @gus --telegram-allow @ana
```

Rules checked before anything opens: `--telegram-allow` without any bot token is an error, and so is `--telegram-token` without `--telegram-allow`.

With `harness serve`, bind the bot through the API, with the usernames as `ref`:

```json
{"model": "anthropic/claude-sonnet-4-20250514", "cwd": "/path/to/project",
 "channels": [{"type": "telegram", "ref": "gus,ana"}]}
```

`serve` registers the channel when a token exists (`--telegram-token`, the environment or the saved one).

## Trusted users

- A username is 5 to 32 characters of `a-z`, `0-9` and `_`, without `@`. It is lowercased, deduplicated and sorted.
- Only messages from those usernames reach the session, in a private chat or a group. Bots are never trusted.
- Anyone else gets one notice per chat and user per listening run, explaining how to be added; the list of allowed users is not revealed.
- The conversation is the bot itself, so one session in one process listens to a bot at a time. Opening a second session on the same bot fails with a conflict.

Each prompt reaches the model as `<telegram chat="…" user="@name">…</telegram>`.

## Chat commands

Two read-only commands are answered in the chat without involving the model:

| Command | Answer |
| --- | --- |
| `/info` | Session id, model, tokens, cost, the provider's quota, MCP servers, schedules and channels. |
| `/context` | Context window breakdown. |

They cannot change the session. `/start` is ignored, and any other `/command`, or one addressed to another bot (`/x@otherbot`), is treated as a message.

## Files

- Photos are given to the model as images and also saved as files. Documents of any type up to 20 MB are saved and passed as `<telegram-attach …/>` paths. Programs are skipped by name and by content, and the model is told why.
- An album (several photos sent together) is delivered as one prompt.
- To send a file, the model writes `<telegram-upload path="/abs/path"/>` on its own line. Images (jpg, png, webp) go as photos, GIF and MP4 as animations, anything else as documents.

Output is formatted as Telegram MarkdownV2, tables become code blocks, long replies are split into chunks of 4096 characters, and a message that Telegram rejects is resent as plain text.

## Schedules

A schedule created while answering a Telegram message fires back into that chat. See [Scheduled prompts](../using/scheduling.md).

## Security

Trusted users can make the session use every tool it has. The session is told that a Telegram sender has only that sender's authority (no destructive commands, no secrets, no host details), but this is an instruction to the model, not enforcement. Only list usernames you would trust with a shell on the machine.
