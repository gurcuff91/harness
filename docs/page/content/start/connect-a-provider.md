---
title: Connect a provider
section: Start
order: 30
summary: Give Harness access to a model — by subscription, API key, local Ollama or your own endpoint.
---

Harness needs at least one model provider. Connect it once; the credential is stored in `~/.harness/credentials.json`.

```bash
$ harness connect <name> [<api-key>]
```

If you omit the key for an API-key provider, Harness asks for it. OAuth providers open your browser instead.

## Built-in providers

| Provider | How you authenticate |
|---|---|
| `claude-oauth` | Browser login with a Claude subscription |
| `codex-oauth` | Browser login with a ChatGPT / Codex subscription |
| `anthropic` | API key, or `ANTHROPIC_API_KEY` |
| `openai` | API key, or `OPENAI_API_KEY` |
| `opencode-go` | API key, or `OPENCODE_GO_API_KEY` |
| `synthetic` | API key, or `SYNTHETIC_API_KEY` |
| `minimax` | API key, or `MINIMAX_API_KEY` |
| `ollama-cloud` | API key, or `OLLAMA_CLOUD_API_KEY` |
| `ollama` | None — a local Ollama is detected automatically (`OLLAMA_URL` to point elsewhere) |

```bash
$ harness connect claude-oauth      # opens the browser
$ harness connect anthropic         # asks for the key
$ harness connect openai sk-...     # or pass it inline
```

> **Tip** Passing a key on the command line leaves it in your shell history and the process list. Prefer the environment variable, or let Harness prompt for it.

You can also connect providers from inside the TUI, in the command palette.

## Disconnect

```bash
$ harness disconnect <name>
```

## Choose a model

Models are always written `provider/model`, for example `anthropic/claude-sonnet-4-20250514`.

```bash
$ harness --model anthropic/claude-sonnet-4-20250514   # for this run only
$ harness settings set model anthropic/claude-sonnet-4-20250514   # your default
$ harness settings set thinking high                    # off | low | medium | high | xhigh
```

An explicit `--model` is a requirement, not a hint: if it cannot be honoured, Harness exits with an error instead of quietly using another model.

## Your own endpoint

Any proxy, gateway or self-hosted server that speaks the OpenAI Chat Completions or the Anthropic Messages dialect can be added as a *custom provider*:

```bash
$ harness provider add my-gateway --url https://llm.example.com/v1 --header X-Api-Key:...
$ harness provider list
```

Once added, `my-gateway/<model>` works like any built-in provider. See [Custom providers](../sdk/providers.md) for the details, including how to add one from Go.

## Next

You are connected. Open the terminal UI in the [Quick start](quick-start.md).
