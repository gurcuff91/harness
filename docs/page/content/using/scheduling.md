---
title: Scheduled prompts
section: Using Harness
order: 140
summary: Let the agent create cron schedules that fire prompts back into its session, and inspect them with harness schedules.
---

Scheduling lets a session run a prompt on a cron schedule: "every weekday at 9, summarize new issues". The agent creates the schedules itself with tools; a running harness process with the scheduler enabled fires them.

## Enabling the scheduler

The scheduler is off by default because its tools cost tokens on every request. Turn it on with a flag or an environment variable:

```bash
$ harness --with-scheduler
$ harness serve 127.0.0.1:8080 --with-scheduler
$ HARNESS_WITH_SCHEDULER=1 harness
```

`HARNESS_WITH_SCHEDULER` accepts `1`/`true`/`yes` and `0`/`false`/`no` (or empty); any other value is an error. `harness acp` takes no flags, so the variable is the only way to enable it there. `-p` never has the scheduler.

## Creating schedules

Ask the agent in plain language:

```text
❯ every weekday at 9:00, check the open PRs and tell me which need review
```

It calls the `Schedule` tool with a slug, a cron expression and the prompt:

| Tool | Purpose |
| --- | --- |
| `Schedule` | Create or update a schedule. Reusing a slug edits it. Arguments: `slug`, `cron`, `prompt`. |
| `ScheduleList` | List this session's schedules with cron, prompt, run count and last run time. |
| `ScheduleDelete` | Delete a schedule by slug. |

A session only sees, edits and deletes its own schedules.

### Cron syntax

A standard 5-field expression (`minute hour day-of-month month day-of-week`) or a descriptor:

| Expression | Meaning |
| --- | --- |
| `0 9 * * 1-5` | Weekdays at 09:00 |
| `*/15 * * * *` | Every 15 minutes |
| `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly` | Descriptors (`@midnight` = `@daily`) |
| `@every 1h30m` | Every 1 hour 30 minutes |

The minimum interval is one minute; `@every 30s` is rejected.

## What happens when a schedule fires

- The prompt is delivered to the **session that created it**, wrapped as `<scheduled slug="...">...</scheduled>` so the model knows nobody is waiting at the other end. The TUI echoes it with the `◷` icon.
- If the schedule was created while answering an input-channel conversation (a Slack channel or thread, a Telegram chat, a colleague's question) and the session still listens to it, the turn goes back to that conversation and its reply is published there.
- Otherwise the prompt runs in the session with no destination other than the session itself.
- The session must be open in a process running the scheduler. If it is not, nothing fires and nothing is recorded as a run; schedules are not queued while the session is closed.
- The engine checks every 30 seconds. Several processes can share the same settings; each fires only the schedules of sessions it has open.
- Sub-agents cannot use the Schedule tools.

## Inspecting schedules

```bash
$ harness schedules
$ harness schedules --json
```

The listing is read-only: slug, cron, run count, last run and the prompt. Creating and deleting is done by the agent. Schedules are stored in `~/.harness/settings.json` under their own namespace. With the scheduler on, the TUI footer shows a `[N schedules]` badge.
