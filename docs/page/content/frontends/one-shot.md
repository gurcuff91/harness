---
title: One-shot mode
section: Frontends & channels
order: 200
summary: Run a single prompt non-interactively with -p, pick text or JSON output, and start the TUI with a first message.
---

Harness has two ways to run a prompt from the command line: `-p` runs one turn and prints the result, and a positional prompt opens the interactive TUI with that message already submitted.

## Running one turn with -p

```bash
$ harness -p "summarize the failing test in ./pkg/store"
```

`-p` creates a session in the current directory, runs one turn, prints the result and exits. The session is kept in memory only: nothing is written to `~/.harness/agent/sessions/`, so there is nothing to resume afterwards.

The agent has the built-in tools, MCP servers and memory, and WebSearch. It never has the colleague channel or the scheduler, even if `HARNESS_WITH_COLLEAGUES` or `HARNESS_WITH_SCHEDULER` are set: an ephemeral process has nothing to offer another agent and no engine to keep running.

Use `--model` and `--thinking` to override the settings for this run:

```bash
$ harness -p "explain this stack trace" --model anthropic/claude-sonnet-4-20250514 --thinking high
```

Without `--model`, the active model from settings is used; if none is set, the first available model.

### Output modes

`--output` selects what is printed:

| Mode | Output |
| --- | --- |
| `text` (default) | The final assistant text, trimmed, on stdout. |
| `json` | One JSON array with every event of the turn, printed when the turn ends. |
| `json-stream` | One JSON event per line, printed as it arrives. |

The JSON modes print each event exactly as the server sends it over SSE (see [HTTP/SSE server](server.md#events)), so a script sees the same shape as an API client. The stream ends at `turn_end`.

```bash
$ harness -p "list the TODOs in main.go" --output json-stream
```

```json
{"type":"turn_start"}
{"type":"loop_start","loop":0}
{"type":"tool_start","tool_name":"Read","tool_id":"toolu_01"}
{"type":"text","delta":"There are two TODOs"}
{"type":"turn_end"}
```

If the turn fails, the error message is written to stderr and the process exits with a non-zero status.

### Restrictions

- `-p` cannot be combined with `--resume`: the error is `--resume is not supported with -p`, exit status 1.
- `-p` cannot be combined with a positional prompt (`use either -p (one-shot) or a positional prompt (interactive)`).

## Starting the TUI with a first message

Pass the message as arguments instead of `-p`:

```bash
$ harness "add a unit test for ParseDuration"
$ harness fix the flaky test in the scheduler package
```

Words are joined into one message. The TUI opens and submits it as soon as the first session is ready. It is sent as text for the agent, so `harness "/quit"` does not exit and `harness "!ls"` does not run a shell command. If a turn is already running it is queued like any typed message.

`--resume <id>` combined with a prompt resumes that session and sends the message as its next turn.

If no session can be created (no model or provider usable and no `--model` given), nothing is sent and the text stays in the editor.

### A command name wins over a prompt

`harness sessions` runs the `sessions` command, not a prompt. To send a prompt that is exactly a command name, put it after `--`:

```bash
$ harness -- serve
```

Everything after the first `--` is appended to the prompt verbatim. If what comes before `--` selects another command, the whole line goes to the command parser unchanged.

### A mistyped command is an error

The shell removes quotes, so `harness sesions` and `harness "sesions"` look the same to the process. To keep a typo from costing a model turn, the first word of the prompt is compared with the visible command names. If it is one edit away (an insertion, a deletion, a substitution or a swap of two adjacent letters, ignoring case), harness refuses and suggests the command:

```text
harness: unknown command "sesions"
  did you mean: harness sessions

Run harness --help to list commands, or harness -- "sesions" to send it as a message (harness -p "sesions" for a one-shot answer).
```

The exit status is 1 and nothing runs. Only the first word is checked, words after `--` are never checked, and a word spelled exactly like a command is never treated as a typo of another one. To send such a word as a message anyway, use `--` or `-p`.

`harness help`, `harness help <command>` and `harness version` are real commands (`harness --help`, `harness <command> --help` and `harness --version` do the same).

## Explicit flags are requirements

`--model`, `--thinking` and `--resume` are honored exactly or the process exits with status 1 before the TUI opens. They are never replaced by a default and never ignored. Fallbacks (the settings model, then the first available one) apply only to what you did not pass.

- `--model` is used as given. If the server cannot create a session with it, the server's message is printed (for example `provider not found: "zzz"`).
- `--resume` with an unknown or unreadable session id is an error, with or without a prompt. No fresh session is created in its place.
- `--resume <id> --model X` (or `--thinking X`): if applying the override fails, the resumed session is closed and the process fails.
- `--thinking` also applies to a new session.

On failure nothing is written to the terminal: no banner, no title, no status reports. Inside the running TUI, `/model`, `/resume`, `/new` and `/fork` report failures as warnings instead, because a session already exists.

## Extras

`-p` and the TUI differ in the optional extras:

| Extra | TUI | `-p` |
| --- | --- | --- |
| `--with-colleagues` / `HARNESS_WITH_COLLEAGUES` | opt-in | never |
| `--with-scheduler` / `HARNESS_WITH_SCHEDULER` | opt-in | never |
| WebSearch | always | always |

See [Colleagues](colleagues.md) and [Scheduled prompts](../using/scheduling.md).
