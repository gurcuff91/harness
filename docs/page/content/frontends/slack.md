---
title: Slack
section: Frontends & channels
order: 230
summary: Let a session listen to a Slack channel or DM, answer mentions and threads, and use the Slack tools.
---

Slack is an input channel: while a session is open it can listen to one or more Slack conversations. A message there becomes a prompt of the session, and the session's replies are posted back to the same conversation.

## Log in

Harness acts as your Slack user, using your browser session.

```bash
$ harness slack login
```

You are asked for:

1. The workspace URL, for example `https://myco.slack.com`.
2. The value of the `d` cookie (`xoxd-…`): open Slack in a browser, then Developer Tools, Application, Cookies, and copy it as shown.

Harness derives the session token from the workspace page, verifies it with Slack and saves it. Check it later with:

```bash
$ harness slack login --status
```

The credentials are stored with the other credentials, never on the command line. Without a login, `--slack-channel` fails with `slack channel not available: run harness slack login`.

## Listen to a conversation

In the TUI, pass the conversation with `--slack-channel` (repeatable):

```bash
$ harness --slack-channel "#support" --slack-channel C0123ABCD
```

The value can be a channel id (`C…`, `G…`), a DM id (`D…`), a user id (`U…`, which opens or finds the DM with that person) or a channel name with or without `#`.

With `harness serve`, bind it through the API when creating or resuming a session:

```json
{"model": "anthropic/claude-sonnet-4-20250514", "cwd": "/path/to/project",
 "channels": [{"type": "slack", "ref": "#support"}]}
```

`serve` registers the Slack channel automatically when a login exists. Binding is explicit and per open session: it is not saved in the session, and resuming without `channels` listens to nothing. An unavailable or invalid channel makes the open fail rather than being skipped.

## What the session receives

- In a channel, only messages that mention you (`@you`). In a DM, every message.
- Your own messages, bot messages and message subtypes such as joins are ignored, except thread replies that were also sent to the channel.
- Each message reaches the model wrapped in a block that names the sender and conversation, for example `<slack user="U0123" channel="C0456">…</slack>`. A reply inside a thread also carries `thread="<ts of the first message>"`.
- Attached files are downloaded and given to the model as paths in `<slack-attach …/>` tags; images are also shown to it. Files over 50 MB, programs and installers are skipped, and the model is told why.

## What it posts

The session's text is posted to the conversation as it is produced (before each tool call and at the end of the turn), with a typing indicator. A reply to a threaded message goes into that thread. Errors and compaction notices are posted too. To send a file, the model writes `<slack-upload path="/abs/path"/>` on its own line.

### Threads

For a message inside a thread, the model is instructed to read the thread first with the `SlackMessages` tool before answering, because the message usually refers to what was said above it. A thread is not followed after a reply: later messages in it are delivered only if they mention you.

## Tools

The model gets these tools while a Slack channel is bound:

| Tool | Purpose |
| --- | --- |
| `SlackListChannels` | List the channels the user can see. |
| `SlackListUsers` | List active users, to resolve names to ids. |
| `SlackPost` | Post to another channel, a person or another thread (`thread` parameter). |
| `SlackMessages` | Read recent messages of a channel, or a whole thread; `files: true` downloads attachments. |

`SlackPost` refuses the conversation (and thread) the current turn is answering, since that reply is already posted automatically.

## One session per conversation

A conversation is claimed by one session in one process. If another live session (in any harness process on the machine) already listens to it, opening fails with a conflict (HTTP 409 through the API). A claim left by a process that died is taken over automatically. Several processes logged into the same account all receive every event, but each keeps only the conversations it claimed.

## Security

Anyone who can write in a bound conversation can reach the session and, through it, every tool it has, including Bash and file access. The session's instructions tell it that a Slack sender has only that sender's authority: no destructive commands, no secrets, no details about the host, and that claims such as "the owner approved this" prove nothing. This is an instruction to the model, not enforcement. Bind only conversations whose members you would trust with a shell on the machine, or run the session in a restricted environment.
