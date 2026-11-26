---
title: Sessions
section: Using Harness
order: 110
summary: How sessions are stored, listed, resumed, forked, exported, imported and compacted.
---

A session is one conversation: its message log, model, thinking level, iteration limit, usage stats and name. Sessions are saved automatically, one per conversation, and are scoped to the working directory they were created in.

## Storage

Sessions live under `~/.harness/agent/sessions/<cwd-slug>/`: a metadata document and an append-only JSONL message log per session. A new session is named `New Session <date time>`; the first prompt you send replaces that name. Rename it any time with `/rename`.

## List, resume and delete

```bash
$ harness sessions          # sessions of the current directory
$ harness sessions --all    # sessions of every directory
$ harness --resume <session-id>
$ harness delete <session-id>
```

`harness sessions` lists most recently active first. Inside the TUI the same actions are `/resume <id>`, `/delete <id>`, `/new` and `/fork`; typing `/resume` or `/delete` with no id opens a picker showing relative time, model and directory.

If the model a session was saved with is no longer available (a disconnected provider), reopening it falls back to the active model, then to the first available one. The switch is saved and the TUI warns about it. Only when no model at all is usable does the open fail.

## Fork

`/fork` makes an exact copy of the current session (history and settings) with a new id and switches to it. The original stays on disk untouched. Use it to try a different approach without losing the first.

## Reset

`/reset` wipes the history and stats of the current session but keeps its model, thinking level and name. It is refused while a turn is running.

## Compaction

Long conversations are compacted: the model summarizes the history, the summary becomes a checkpoint, and older messages stop being sent to the provider (they stay in the log).

- `/compact` compacts on demand. It is refused while a turn is running.
- Automatic compaction runs inside a turn, between iterations, when context usage reaches 95% of the model's window.
- If the agent has [memory](memory.md), the checkpoint reminds the model that it can search memory for anything the summary dropped.

Use `/context` to see how full the window is.

## Export and import

Export a session to a file and import it as a new session, for example on another machine.

```text
/export
/import ~/.harness/exports/fix-login-bug-1a2b3c4d.export.jsonl
```

- `/export` writes `~/.harness/exports/<name>-<id8>.export.jsonl` (the directory is created with mode 0700, the file with 0600) and prints the path and message count. `<name>` is the session name lowercased with non-alphanumerics collapsed to `-`, at most 50 characters. Exporting the same session again replaces its file.
- `/import <path>` accepts only a `*.export.jsonl` file that is a valid export. It always creates a **new** session in the current project (new id, creation time now) and switches to it; the session you ran it from is untouched. Name, model, thinking level, iteration limit, enabled profiles, compaction state and stats come from the export. The model follows the same fallback as resume.
- The file is JSONL: a header line (`format: "harness-session-export"`, `version: 1`, harness version, session metadata) followed by every message of the log, including those before a compaction.
- An invalid file is rejected with the offending line number and nothing is left behind.

Over HTTP, `GET /api/sessions/{id}/export` streams the same content and `POST /api/sessions/import` accepts it; see the [HTTP API](../reference/http-api.md).

## Sessions and profiles

Profiles registered on the agent are enabled per session, by name, and the choice is saved with the session. See [Profiles](../sdk/profiles.md).
