# Harness

A small AI agent runtime in pure Go: one binary, many providers, streaming first.

```
Agent = LLM + Harness
If you're not the model, you're the harness.
```

**Documentation: https://gurcuff91.github.io/harness/**

## Quick start

```bash
go install github.com/gurcuff91/harness/cmd/harness@latest

harness connect claude-oauth     # or: anthropic, openai, ollama-cloud, … (a local Ollama is detected)
harness                          # interactive terminal UI, in your project directory
harness "explain this repo"      # same, with a first message
harness -p "list the TODOs"      # one-shot, no TUI
```

Prebuilt binaries for macOS, Linux and Windows are on the [releases page](https://github.com/gurcuff91/harness/releases).

## What it is

- **Multi-provider, always streaming.** Claude and ChatGPT subscriptions, Anthropic, OpenAI, OpenCode Go, Synthetic, MiniMax, Ollama, and any OpenAI- or Anthropic-compatible endpoint.
- **Real tools.** Bash, Read, Write, Edit, Fetch, WebSearch, Skills, Subagents, persistent memory, MCP servers, scheduled prompts.
- **Persistent sessions.** Resume, fork, export and import them.
- **Several frontends.** Terminal UI, one-shot CLI, HTTP/SSE server, ACP for editors such as Zed.
- **Input channels.** A session can listen to a Slack or Telegram conversation, or to other Harness sessions on your machine.
- **Embeddable.** The agent is a Go library; the server, ACP and channels are importable packages.

## Embedding

```go
a := harness.NewAgent(harness.AgentWithThinking("medium"))
defer a.Close()

sess, _ := a.NewSession(cwd, "anthropic/claude-sonnet-4-20250514")
reply, _ := sess.PromptAndWait(ctx, "summarise this repo")
```

See the [SDK documentation](https://gurcuff91.github.io/harness/sdk/embedding.html).

## Learn more

| | |
|---|---|
| [Start](https://gurcuff91.github.io/harness/start/introduction.html) | Install, connect a provider, first steps |
| [Using Harness](https://gurcuff91.github.io/harness/using/tui.html) | The TUI, sessions, memory, skills, scheduling, MCP, tools |
| [Frontends & channels](https://gurcuff91.github.io/harness/frontends/one-shot.html) | One-shot, HTTP server, ACP, Slack, Telegram, colleagues |
| [SDK](https://gurcuff91.github.io/harness/sdk/embedding.html) | Embedding, options, profiles, custom providers and tools |
| [Reference](https://gurcuff91.github.io/harness/reference/cli.html) | CLI, HTTP API, settings, architecture |

## Development

```bash
make build      # build ./harness
make test       # run the tests
make docs-serve # build and serve the documentation locally
```

`AGENTS.md` holds the conventions for working on this codebase.

## License

MIT — see [LICENSE](LICENSE).
