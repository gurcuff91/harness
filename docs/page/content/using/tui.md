---
title: The TUI
section: Using Harness
order: 100
summary: Run the interactive terminal UI, its slash commands, keys, shell escape, image paste, thinking levels, the footer and the /info and /context panels.
---

Running `harness` with no subcommand opens the interactive terminal UI. It talks to an in-process server, so everything it does is also available over the [HTTP/SSE server](../frontends/server.md).

```bash
$ harness
$ harness --model anthropic/claude-sonnet-4-20250514 --thinking high
$ harness --resume <session-id>
$ harness "fix the failing test in ./agent"
```

## Starting the TUI

| Flag | Meaning |
| --- | --- |
| `--model provider/model` | Model for the session. Used exactly as given; if it cannot be honored, the process exits with code 1 before the UI opens. |
| `--thinking LEVEL` | Thinking level: `off`, `low`, `medium`, `high`, `xhigh`, `max`. Same strictness as `--model`. |
| `--resume ID` | Resume a saved session instead of starting a new one. |
| `--with-scheduler` | Run the cron engine and give the agent the Schedule tools. See [Scheduled prompts](scheduling.md). |
| `--with-colleagues` | Make every session a colleague. See [Colleagues](../frontends/colleagues.md). |
| `--slack-channel`, `--telegram-allow`, `--telegram-token` | Listen to input channels. See [Slack](../frontends/slack.md) and [Telegram](../frontends/telegram.md). |

Without `--model`, the model saved in settings is used, then the first available one.

### A first message on the command line

Anything after the flags is joined into a first message that is submitted as soon as the session is ready:

```bash
$ harness explain the retry logic in agent/llm_retry.go
$ harness --resume <session-id> "continue with the tests"
```

- A subcommand name wins over a prompt: `harness sessions` runs the command. To send a prompt that starts with a command name, put it after `--`: `harness -- serve the page`.
- A single word within one typo of a command (`harness sesions`) is rejected with `unknown command` and a suggestion, not sent to the model. Use `harness -- "sesions"` or `-p` to force a prompt.
- `-p` and a positional prompt cannot be combined. For non-interactive use see [One-shot mode](../frontends/one-shot.md).
- The text goes to the agent as-is: `harness "/quit"` does not exit and `harness "!ls"` does not run a shell command.

## The screen

From top to bottom: the conversation history, a spinner while the model is working, the input editor between two rules, the command palette (only when open), a `channels` line (only when the session listens to Slack, Telegram or colleagues), the status line and the footer.

The status line shows the working directory (with the git branch), the session name, the iteration counter while a turn runs (`(3/50)`) and the number of queued prompts (`[2 queued]`).

The footer shows:

```text
↑12.3k ↓1.2k R80k W5k $0.142 (sub) 18.4%/200k anthropic/claude-sonnet-4-20250514 (high) • [2 mcps] [3 schedules]
```

- `↑` input tokens, `↓` output tokens.
- `R` / `W` cache read / write tokens, only when non-zero.
- Cost in USD; `(sub)` marks a subscription provider.
- Context used, as a percentage and the window size.
- The model, plus the thinking level in parentheses unless it is `off`.
- Badges for connected MCP servers and, with `--with-scheduler`, the number of schedules.

Prompts are echoed with an icon: `❯` typed at this terminal, `◷` a fired schedule, `⇌` anything from outside (a colleague, a Slack or Telegram conversation).

## Sending prompts

Type and press Enter. If a turn is already running, the prompt is queued and runs when the turn ends.

| Key | Action |
| --- | --- |
| Enter | Submit |
| Ctrl+J, Alt+Enter | Insert a newline |
| Esc | Stop the running turn and clear the editor (also cancels a pending value prompt) |
| Ctrl+C | Quit |
| Ctrl+D | Quit when the editor is empty |
| Ctrl+V | Paste an image from the clipboard |
| Tab | Autocomplete the highlighted palette entry without running it |
| Up / Down | Move the cursor, or the palette selection when it is open |
| Ctrl+A / Home, Ctrl+E / End | Start / end of line |
| Ctrl+U, Ctrl+K | Delete to start / end of line |
| Ctrl+W, Alt+Backspace | Delete the previous word |
| Ctrl+Left / Right, Alt+Left / Right | Move by word |

Text pasted into the terminal arrives through bracketed paste and goes into the editor as typed text.

### Shell escape: `!`

A line starting with `!` runs the rest as a shell command directly, without involving the model:

```text
!git status
```

The rules and separators turn amber while the input starts with `!`. The command runs with `sh -c` in the current directory, with a 30 second limit, and its output is shown in the history. It is not added to the conversation, so the model does not see it.

### Pasting images

Press Ctrl+V with a PNG on the clipboard. Harness writes it to a temporary file and inserts the file path into the editor; the agent receives the image when it reads that path with the `Read` tool. If the clipboard has no image you get `no image in clipboard`.

## Slash commands

Typing `/` opens the command palette; matches are ranked (name starts with the filter, then name contains it, then description). Enter runs the highlighted command, Tab only completes it. A command that needs a value and gets none asks for it in the editor (Esc cancels).

| Command | Effect |
| --- | --- |
| `/model <provider/model>` | Switch the session's model. |
| `/thinking <level>` | Set the thinking level: `off`, `low`, `medium`, `high`, `xhigh`, `max`. |
| `/rename <name>` | Rename the session. |
| `/max-iter <n>` | Iteration limit per turn for this session (1 to 1000). |
| `/compact` | Summarize the conversation to free context. See [Sessions](sessions.md). |
| `/reset` | Wipe the history and stats, keeping model, thinking level and name. |
| `/new` | Close the session and start a new empty one. |
| `/fork` | Copy the session into a new one and switch to it. |
| `/resume <id>` | Switch to a saved session. |
| `/delete <id>` | Delete a saved session. |
| `/export` | Write the session to `~/.harness/exports/`. |
| `/import <path>` | Import an export as a new session and switch to it. |
| `/info` | Show a snapshot of the session. |
| `/context` | Show the context window breakdown. |
| `/connect <provider> [key]` | Connect a provider. See [Connect a provider](../start/connect-a-provider.md). |
| `/disconnect <provider>` | Disconnect a provider. |
| `/skill:<name> [text]` | Run a skill. See [Skills](skills.md). |
| `/quit`, `/exit` | Close the session and exit. |

`/compact`, `/reset` and the other session commands are refused while a turn is running.

## Thinking levels

`off`, `low`, `medium`, `high`, `xhigh` and `max` are accepted by `--thinking`, `/thinking` and `harness settings set thinking`. Each provider maps the level to its own mechanism (for example Anthropic's `effort`, OpenAI's reasoning effort, or on/off for Ollama), so the same name does not mean the same budget everywhere.

```bash
$ harness settings set thinking high
$ harness settings set model anthropic/claude-sonnet-4-20250514
```

`harness settings` with no arguments prints the saved model and thinking level; those are the defaults for new sessions. A running session keeps its own values, which `/model` and `/thinking` change and persist with the session.

## /info

`/info` prints a snapshot: harness version, session name and id, model, thinking level, iteration limit, context use, tokens, cache and cost, the usage quota of the session's provider (when the provider reports one), the OS and terminal, connected MCP servers, the schedule count, the channels the session listens to, and whether it is busy with queued prompts.

## /context

`/context` draws the context window as an 8x8 grid where each cell is 1/64 of the window, split into `S` (system prompt), `T` (tool definitions), `C` (conversation) and free space, followed by the exact token counts. Use it to see what is filling the window before deciding to `/compact`.

## Gotchas

- Esc stops the turn; it does not undo tool calls that already ran.
- Terminal control sequences in model output, tool output or session names are neutralized before they reach your terminal, so a reply cannot retitle the window or write to your clipboard.
- The TUI scrolls with the terminal's own scrollback; there is no in-app scrolling.
- Inside the Herdr terminal multiplexer, the TUI reports its working/idle state and session name to it.
