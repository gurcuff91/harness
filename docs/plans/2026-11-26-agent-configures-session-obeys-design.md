# The agent configures, the session obeys

Date: 2026-11-26 · Target: v0.126.0 (breaking) · Owner-approved.

## Principle
The AGENT configures everything at a high level; a session is its child and obeys it. A session option exists only for what is genuinely particular to ONE session: which external conversations it listens to, and which registered profiles are enabled for it. Everything else (directives, tools, disallowed tools, system prompt) is the agent's.

## Decisions
1. **Session options shrink to two**: `SessionWithChannels` and `SessionWithEnabledProfiles` (renamed from `SessionWithProfiles`, so it cannot be mistaken for `AgentWithProfiles`, which REGISTERS profiles). Same in `client`. They apply to NewSession, ResumeSession, ForkSession and ImportSession; the PATCH semantics of the profiles option are unchanged.
2. **Removed**: `agent.SessionWithDirectives`, `agent.SessionWithTools`, `client.SessionWithDirectives`, and the `directives` field of `POST /api/sessions` and `/resume`. Nobody in the repo used them (the TUI/colleague/Herdr directives are AGENT directives; frontends' own text+tools are profiles). A subset of sessions that needs extra text/tools is exactly what a profile is.
3. **Not added**: no `SessionWithDisallowedTools`. A restriction that is not persisted is lost when the scheduler or a colleague reopens the session; the agent's `DisallowedTools` always applies.
4. **`ReconfigureSession` becomes private** (`reconfigureSession(id)`, no options): its only job is reacting to a profile being registered/unregistered or a live session being rebound. Same for `SetSessionProfiles` (`setSessionProfiles`). Fewer public methods.
5. **System prompt precedence**: the prompt passed by the caller (`AgentOptions.SystemPrompt` / `AgentWithSystemPrompt`) > `SYSTEM.md` > the built-in default. A `SYSTEM.md` can no longer override a prompt the caller set (it did: `New` filled the default in and `SYSTEM.md` always won). This also fixes sub-agents: their own prompt was being overridden by a project's `SYSTEM.md`.

## Prompt order (unchanged in substance)
base (explicit > SYSTEM.md > default) → skills → memory → tool sections → working directory → AGENTS.md → agent directives → enabled profiles' directives → channels' directives.

## Impact
Breaking: the removed options and the API field, the rename, `ReconfigureSession`/`SetSessionProfiles` gone from the public API, and the SYSTEM.md precedence. Tests that used `SessionWithDirectives`/`SessionWithTools` move to profiles.
