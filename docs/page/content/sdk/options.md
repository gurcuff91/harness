---
title: Options
section: SDK
order: 310
summary: Every agent, session, prompt and provider option, the naming convention, and how the system prompt is assembled.
---

## Naming convention

Options are functional options whose names carry the thing they configure:

| Prefix | Applies to | Where |
|---|---|---|
| `AgentWith*` | Constructing the agent | `harness.AgentWith*`, or the `agent.AgentOptions` struct |
| `SessionWith*` | Opening one session | `agent.SessionWith*` (and the same names in package `client`) |
| `PromptWith*` | One prompt | `agent.PromptWith*` (and `client.PromptWithOrigin`) |
| `ProviderWith*` | Registering a custom provider | `harness.ProviderWith*` |
| `ServerWith*`, `AcpWith*` | A frontend runner | `harness.ServerWith*`, `harness.AcpWith*` |

Packages with a single kind of option use the plain `With*` form (`server.WithAddr`, `client.WithSecret`, `slack.WithLogger`).

The rule behind the split: **the agent configures, the session obeys.** Directives, tools, disallowed tools, MCP servers, memory and the system prompt belong to the agent and apply to every session. A session option exists only for what is particular to one session: which conversations it listens to and which profiles are enabled for it. There is no per-session directive, tool or disallowed-tools option. A subset of sessions that needs extra text or tools is what a [profile](profiles.md) is.

## Agent options

`harness.NewAgent(opts...)` applies options in order; later ones win. `harness.AgentWithOptions(o)` replaces the whole struct with a prebuilt `agent.AgentOptions`; options applied after it still win.

| Option | `AgentOptions` field | Meaning |
|---|---|---|
| `AgentWithThinking(level)` | `ThinkingLevel` | `off`, `low`, `medium`, `high`, `xhigh` or `max`. Empty: the saved default, else `off`. |
| `AgentWithSystemPrompt(s)` | `SystemPrompt` | Base system prompt for all sessions. See [precedence](#system-prompt). |
| `AgentWithDirectives(d...)` | `Directives` | Extra instruction blocks appended to every session's prompt. Repeated calls accumulate. |
| `AgentWithMaxIterations(n)` | `MaxIterations` | ReAct iterations per turn. Default 50; values above 1000 are clamped. |
| `AgentWithMaxTokens(n)` | `MaxTokens` | Output-token cap per request. Default: the model's maximum (capped at 64k). |
| `AgentWithTools(t...)` | `Tools` | Extra tools next to the built-ins. Repeated calls accumulate. See [Custom tools](tools.md). |
| `AgentWithDisallowedTools(n...)` | `DisallowedTools` | Tool names to exclude, built-in, custom or MCP. Repeated calls accumulate. |
| `AgentWithMCPs(servers)` | `MCPServers` | MCP servers by name. Presence is the switch; empty means no MCP. |
| `AgentWithStore(s)` | `Store` | Session store (`store.SessionStore`). Default: file store, falling back to in-memory. |
| `AgentWithResourceLoader(l)` | `ResourceLoader` | Skill and resource discovery. Default: filesystem per session; `resources.NilLoader{}` disables discovery. |
| `AgentWithMemory(m)` | `Memory` | Persistent memory backend (`memory.Store`). Presence is the switch; the agent closes it. |
| `AgentWithScheduler()` | `EnableScheduler` | Schedule tools plus the engine that fires them. Off by default. |
| `AgentWithWebSearch()` | `EnableWebSearch` | Registers the `WebSearch` tool. Off by default. |
| `AgentWithChannels(c...)` | `Channels` | Register input channels. See [Channels in Go](channels.md). |
| `AgentWithProfiles(map)` | `Profiles` | Register named profiles. See [Profiles](profiles.md). |
| `AgentWithTerminal(name)` | `Terminal` | Name of the terminal the sessions run in, reported by the `SessionInfo` tool. Leave unset for a server or bot. |

Notes:

- `AgentWithMCPs` connects the servers at construction and registers their tools as `mcp__<server>__<tool>`. An invalid or unreachable server never fails the agent; read `Agent.MCPStatuses()`. Use `mcp.ServersFromSettings()` for the servers saved by `harness mcp add`. Server fields are in [MCP servers](../using/mcp.md).
- `AgentWithMemory` takes an opened store. For the default SQLite one, `memory.OpenSQLite("")` uses `~/.harness/agent/memory.db`; check its error yourself.
- `AgentWithScheduler` should be enabled by at most one agent per process. See [Scheduled prompts](../using/scheduling.md).
- A session's output ceiling is `MaxTokens` when set, otherwise the model's catalog value capped at 64k.

```go
a := harness.NewAgent(
	harness.AgentWithThinking("high"),
	harness.AgentWithDisallowedTools("Bash", "Write", "Edit"), // read-only sandbox
	harness.AgentWithDirectives("Answer in one paragraph."),
	harness.AgentWithMaxIterations(20),
)
```

### Process-global registration

Two settings are global to the process rather than to an agent. Register them once, in `main`, before building any agent:

- `harness.SetSettingsStore(store)` for non-sensitive settings
- `harness.SetCredentialsStore(store)` for secrets

Both take the ports from package `configstore`. Without them Harness uses `~/.harness/settings.json` and `~/.harness/credentials.json`. Custom providers are also process-global: see [Custom providers](providers.md).

## System prompt

The base prompt is chosen in this order:

1. `AgentOptions.SystemPrompt` (`AgentWithSystemPrompt`), when set
2. the project's `.harness/agent/SYSTEM.md`, falling back to `~/.harness/agent/SYSTEM.md`
3. the built-in default prompt

A prompt you set is never overridden by a project's `SYSTEM.md`.

The final prompt is assembled in this order: base prompt, skills, memory, tool sections, working directory, `AGENTS.md`, the agent's directives, the directives of the session's enabled profiles, then the directives of its channels.

## Directives, tools and disallowed tools

- **Directives** (`AgentWithDirectives`) are plain text blocks, added to every session of the agent.
- **Tools** (`AgentWithTools`, or `Agent.RegisterTool` before opening sessions) are added to every session. Re-registering a name replaces the earlier tool.
- **DisallowedTools** is a blocklist by name. It removes built-ins (`Bash`, `Read`, `Write`, `Edit`, `Fetch`, `Skill`, `Subagent`, `SessionInfo`, the `Memo*` and `Schedule*` tools, `WebSearch`), custom tools and MCP tools (by their `mcp__server__tool` name). Constants for the built-in names are in `agent/tools` (`tools.ToolBash`, and so on).

> **Note** Restrictions are not stored per session, on purpose: a restriction that is not persisted would be lost when the scheduler or a colleague reopens the session. Put it on the agent, or use a profile for the sessions that need extra capabilities.

## Session options

Passed to `NewSession`, `ResumeSession`, `ForkSession` and `ImportSession`.

| Option | Meaning |
|---|---|
| `agent.SessionWithChannels(types.InputChannel{Type, Ref}...)` | Conversations the session listens to while it is open. Never persisted. See [Channels in Go](channels.md). |
| `agent.SessionWithEnabledProfiles(names...)` | Profiles enabled for the session. Replaces the session's profile list. See [Profiles](profiles.md). |

On `ResumeSession` of a session that is already open, options are ignored except profiles, which rebind it.

## Prompt options

Passed to `Session.Prompt` and `Session.PromptAndWait`.

| Option | Meaning |
|---|---|
| `agent.PromptWithImages(imgs...)` | Attach images (`types.ImageData{MimeType, Base64}`). |
| `agent.PromptWithOrigin(origin)` | Tag the prompt's source; echoed as `Event.Origin`, live only. An empty string keeps the default. |
| `agent.PromptWithOriginUser()` | Tag as user-originated (the default, `agent.OriginUser`). |
| `agent.PromptWithDisplayText(text)` | Change what observers see echoed; the model still receives the full text. |

## Provider options

`harness.ProviderWith*` configure a custom provider registration (`ProviderWithDisplay`, `ProviderWithHeaders`, `ProviderWithFetchModels`, `ProviderWithReasoningSplit`). They are described in [Custom providers](providers.md).
