---
title: Colleagues
section: Frontends & channels
order: 250
summary: Let the agents running on one machine find and message each other with ColleagueList and ColleagueAsk.
---

With colleagues enabled, every session of a harness process gets a name and can talk to the sessions of other harness processes on the same machine. A colleague is a session, so each one keeps its own model, tools and project directory.

## Enable it

Colleagues are off by default because they add a system-prompt section and two tools to every request.

```bash
$ harness --with-colleagues
$ harness serve :8080 --with-colleagues
$ HARNESS_WITH_COLLEAGUES=1 harness
```

The flag and the environment variable are equivalent; either enables it. Accepted values for the variable: `1`, `true`, `yes` (on) and `0`, `false`, `no`, empty or unset (off); anything else is an error. `harness acp` takes no flags, so the variable is the only way there. `-p` never has colleagues.

## Names

Each session opened by the process (new, resumed or forked) gets a random name made of two words, such as `scorpion-hero`. The name is chosen at open time, is unique among the live colleagues and is not configurable. It is made of `a-z`, `0-9` and `-`, up to 32 characters.

## Tools

| Tool | Purpose |
| --- | --- |
| `ColleagueList` | List colleagues online: `id`, the colleague's current session name, and the project directory (`cwd`) it works in. Your own entry is marked with `you`. |
| `ColleagueAsk` | Send a colleague a message (`colleague`, `prompt`, optional `images` as local paths). |

A colleague that does not answer the name query in time is listed without its session name; that is never an error.

## How messages flow

Asking is asynchronous. `ColleagueAsk` returns as soon as the message is delivered, and the answer arrives later as a new prompt.

1. Agent A calls `ColleagueAsk` for colleague B. B receives a prompt wrapped as `<colleague from="A-id">…</colleague>`.
2. B answers normally. When its turn ends, B's final text (or its error) is sent back to A automatically. B does not need to call `ColleagueAsk` to answer.
3. A receives it as `<colleague from="B-id" reply>…</colleague>`. The turn that handles a reply sends nothing back, so two agents cannot loop. Screens show a question and a reply as one line, `⇌ colleague:B-id ask (3 lines)` / `⇌ colleague:B-id reply (12 lines)`, live and after `/resume`; the full text stays in the session log and in the model's context.

The model is told never to wait, sleep or poll for the answer. If the colleague's session closes before replying, the reply is lost; ask again.

A colleague's question is never published to Slack or Telegram: a turn is only routed to the conversation it came from.

### Schedules

A schedule created while answering a colleague's question fires back to that colleague: each run's final answer is sent to it as a reply. This lets a colleague ask for something periodic.

## Authority

A colleague's message has that agent's authority, never the person's. The model is instructed to help with reading, answering, investigating and reporting back, and to ask the person before anything else that changes state: writing or deleting other files, destructive commands, pushes, deploys, messages to third parties, spending money or reading secrets. This is an instruction to the model, not enforcement.

## Transport

Each colleague listens on a Unix socket named `c-<id>.sock` in a directory with mode `0700`; that directory's permissions are the authentication, so only your own user can reach a colleague. The directory is `~/.harness/run`, or `/tmp/harness-<uid>` when the home directory path is too long for a socket path. A message is one connection with one JSON line each way, with a 50 MB limit. Live colleagues are tracked with the same claim registry as other input channels, so a stale entry from a dead process is reclaimed automatically.

## Related

- [Slack](slack.md) and [Telegram](telegram.md) are the other input channels.
- In Go, `colleague.New()` is registered with `harness.AgentWithChannels`; see [Channels in Go](../sdk/channels.md).
