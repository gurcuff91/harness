---
title: Quick start
section: Start
order: 40
summary: Open Harness, ask it something, and learn the handful of keys that matter.
---

This page assumes Harness is [installed](install.md) and a provider is [connected](connect-a-provider.md).

## Open the TUI

```bash
$ cd your-project
$ harness
```

Harness starts in your project directory. That directory matters: sessions, memory and the `AGENTS.md` file the model reads are all scoped to it.

## Ask for something

Type in the editor at the bottom and press Enter.

```bash
$ harness "explain how authentication works in this repo"
```

Passing words after the command opens the same TUI with that first message already submitted, so you can keep steering the session.

The model streams its reply. When it needs to act, you see each tool call with its arguments, then a result with a duration:

```
❯ explain how authentication works in this repo
⚙ Read  internal/auth/session.go            ✓ 12ms
⚙ Bash  grep -rn "middleware" ./internal    ✓ 48ms
│ Authentication is handled by a middleware that…
```

## One-shot mode

For scripts, skip the TUI and get a single answer:

```bash
$ harness -p "list the exported functions in agent/session.go"
$ harness -p "summarise this diff" --output json
```

`--output` is `text` (default), `json` or `json-stream`.

## Things worth knowing on day one

| Do this | To get this |
|---|---|
| `/` in the editor | The command palette: `/model`, `/thinking`, `/resume`, `/fork`, `/new`, `/compact`, `/info`, `/context`, `/connect`, `/export`, `/import`, `/quit` |
| `!ls` | Run a shell command yourself; it is not sent to the model |
| `harness --resume <id>` | Continue an earlier session |
| `harness sessions` | List sessions of this directory (`--all` for every directory) |
| Ctrl+V | Paste an image from the clipboard |

> **Note** A message that starts with `/` or `!` *inside the editor* is a command. The same text passed as `harness "/x"` is sent to the model as plain text.

## Give the model project context

Put an `AGENTS.md` at the root of your project. Harness reads it (walking up from the working directory) and adds it to the system prompt, so the model knows your build commands, conventions and gotchas without being told every time.

## Where next

- [The TUI](../using/tui.md) — the interface in detail
- [Sessions](../using/sessions.md) — resume, fork, export
- [Memory](../using/memory.md) and [Skills](../using/skills.md) — teach it things that last
- [Embedding an agent](../sdk/embedding.md) — use it as a Go library
