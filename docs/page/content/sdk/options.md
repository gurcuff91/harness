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

The rule behind the split: **the agent configures, the session obeys.** Directives, tools, disallowed tools, MCP servers, memory and the system prompt belong to the agent and apply to every session. A session option exists only for what is particular to one session: which conversations it listens to, which profiles are enabled for it, and instruction text of its own. There is no per-session tool or disallowed-tools option. A subset of sessions that needs shared text and tools is what a [profile](profiles.md) is.

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
| `AgentWithResourceLoader(l)` | `ResourceLoader` | Discovers `SYSTEM.md`, `AGENTS.md` and skills for each session's directory. **Default: nothing is discovered.** Pass `resources.File()` to read them from disk. See [Resource loaders](#resource-loaders). |
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
2. the project's `.harness/agent/SYSTEM.md`, falling back to `~/.harness/agent/SYSTEM.md` (only when a [resource loader](#resource-loaders) reads the disk)
3. the built-in default prompt

A prompt you set is never overridden by a project's `SYSTEM.md`.

The final prompt is assembled in this order: base prompt, skills, memory, tool sections, working directory, `AGENTS.md`, the agent's directives, the directives of the session's enabled profiles, the session's own directives, then the directives of its channels.

## Resource loaders

A resource loader finds the context of a session's working directory: a `SYSTEM.md` that replaces the base prompt, an `AGENTS.md` appended as project context, and the skills the model can load. **By default an agent discovers nothing**, so a directory chosen by an end user cannot change what the model is told. Ask for it:

```go
a := harness.NewAgent(
	harness.AgentWithResourceLoader(resources.File()), // read them from the session's directory, as the harness CLI does
)
```

`resources.File()` looks in the session's directory (and upward for `AGENTS.md`) and in `~/.harness`; `resources.Nil()` discovers nothing and is what a `nil` loader means.

To load them from somewhere else (a database, an object store), implement the one-method interface. It has no state and is **not bound to a directory**: each session calls `Load` with its own `cwd`, and the same instance serves every session and sub-agent at once, so it must be safe for concurrent use.

```go
type ResourceLoader interface {
	Load(cwd string) (*resources.Resources, error)
}

type Resources struct {
	SystemMD string  // replaces the base system prompt
	AgentsMD string  // project context
	Skills   []Skill // Name, Description, Content, and Location when it lives on a disk
}
```

A skill carries its `Content` with it: `Load` returns everything in one call, and the `Skill` tool reads from what the session was opened with.

## Directives, tools and disallowed tools

- **Directives** (`AgentWithDirectives`) are plain text blocks, added to every session of the agent.
- **Tools** (`AgentWithTools`, or `Agent.RegisterTool` before opening sessions) are added to every session. Re-registering a name replaces the earlier tool.
- **DisallowedTools** is a blocklist by name. It removes built-ins (`Bash`, `Read`, `Write`, `Edit`, `Fetch`, `Skill`, `Subagent`, `SessionInfo`, the `Memo*` and `Schedule*` tools, `WebSearch`), custom tools and MCP tools (by their `mcp__server__tool` name). Constants for the built-in names are in `agent/tools` (`tools.ToolBash`, and so on). A sub-agent the model launches inherits this list (plus its own restrictions) and has `WebSearch` only when the agent does, so a model cannot step over a restriction by delegating.

> **Note** Restrictions are not stored per session, on purpose: a restriction that is not persisted would be lost when the scheduler or a colleague reopens the session. Put it on the agent, or use a profile for the sessions that need extra capabilities.

## Session options

Passed to `NewSession`, `ResumeSession`, `ForkSession` and `ImportSession`.

| Option | Meaning |
|---|---|
| `agent.SessionWithChannels(types.InputChannel{Type, Ref}...)` | Conversations the session listens to while it is open. Never persisted. See [Channels in Go](channels.md). |
| `agent.SessionWithEnabledProfiles(names...)` | Profiles enabled for the session. Replaces the session's profile list. See [Profiles](profiles.md). |
| `agent.SessionWithDirectives(text...)` | Instruction text for this session's prompt only. Never persisted. See below. |

On `ResumeSession` of a session that is already open, options are ignored except profiles, which rebind it.

### Session directives

`SessionWithDirectives` is for text that is particular to one session and changes from session to session, such as the working tree or the task that session is about. For text shared by several sessions, register a [profile](profiles.md) instead.

```go
sess, err := a.NewSession(cwd, model,
	agent.SessionWithDirectives("Your worktree is cards/42/. Edit only there."),
)
```

- **Order in the prompt:** the agent's directives, then those of the enabled profiles, then the session's own, then the channels'. Blank blocks are dropped and an identical block appears once.
- **Never persisted.** They live in the open session. Whoever reopens it passes them again; a plain `ResumeSession` returns a session without them, and a fork does not inherit its parent's. An import does not bring them either: pass the option to `ImportSession` if the copy needs text.
- **An open session keeps its prompt:** `ResumeSession` of a session that is already open ignores the option.
- **They survive reconfiguration.** When a profile is registered or removed while the session is open, the session's own text stays.
- **Over HTTP:** `directives` in the body of `POST /api/sessions`, `/resume` and `/fork`; in Go, `client.SessionWithDirectives(...)`.

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
