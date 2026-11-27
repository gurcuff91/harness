# Sub-agents always run in the background and answer by prompt

Date: 2026-11-27 · Target: v0.135.0 (breaking for the Subagent tool's parameters) · Owner-approved.

## Today (v0.134.0)
The `Subagent` tool blocks the turn until the sub-agent finishes (120 s timeout); with `background: true` it returns a `/tmp/harness-subagent-*.txt` path the model must read in a later turn. The background sub-agent runs on `context.Background()` and the parent's Stop does not cancel it. No frontend knows a sub-agent is running.

## Decisions
1. **Always background.** The `background` and `timeout` parameters are removed; `max_iterations` and `readonly` stay.
2. **The answer arrives as a new prompt to the parent**, like `ColleagueAsk`: `<subagent name="scorpion-hero" reply>…</subagent>`, delivered with `Session.Prompt` (it queues if a turn is running) and an origin of its own. The tool returns at once with the sub-agent's name and the instruction "end your turn, do not wait, poll or ask again". No socket, no inbox: the sender lives in the same process.
3. **The tool generates the name**, from the colleague names (`scorpion-hero`): the model gives none. The `characters`/`traits` lists and `RandomID` move to a small shared package `internal/names` (agent cannot import `channels/colleague`, which imports agent); both use it. The name is unique among the parent session's LIVE sub-agents: redraw if taken, and when the pool is exhausted append a suffix (`scorpion-hero-2`). A colleague and a sub-agent may share a name: they sit in different tags.
4. **Lifecycle: a sub-agent lives as long as its PARENT SESSION, not the turn that launched it.** The turn's context is cancelled when every turn ends (that was the old background bug), so each session gets a context that is cancelled by `Session.Close()`. Stop on the parent cancels only the turn in progress, never a sub-agent (not even one launched in an earlier turn). Closing the session cancels every live sub-agent: leaving the TUI, `/new`, `/resume` to another session, `CloseSession`, deleting the session. None is left spending tokens. No explicit "cancel one sub-agent" yet.
5. **Two events** (`types/event.go`, exposed by `WireEvent`, so HTTP/SSE and `harness rpc` both): `subagent_start {name}` and `subagent_stop {name, result}` with `result` ∈ `ok | error | cancelled`. A field can be added later compatibly (a duration); one cannot be taken away.
6. **TUI indicator** on the line that shows the channels, in FRONT of them: `subagents [scorpion-hero, kano-storm]`; one that ends leaves the list; none = nothing shown.

## State, not only events
Events alone fail whoever connects late (`/resume`, a reconnect, opening the TUI afterwards): a sub-agent can run for minutes, and a lost event would leave a ghost in the list for good. So `agent.SessionDetail` gains `Subagents []string` (`json:"subagents,omitempty"`, the live names, in launch order), the same pattern as `Channels`; it reaches `/info`, the client's `Session`, `session/info` and `session/new|resume` over RPC. The TUI initializes from it and then applies the events.

## Requirements
- **`subagent_stop` is ALWAYS emitted**: also when the sub-agent fails, runs out of `max_iterations`, panics or is cancelled by closing the session (a `defer`). Otherwise the list keeps ghosts.
- **The stop is emitted BEFORE the answer is delivered** and the name leaves the parent's list first, so when the model receives `<subagent … reply>` the TUI has already removed it.
- The sub-agent's token cost is still added to the parent session (`addDelegatedCost`).
- One new turn per sub-agent that finishes; several finishing together queue. Not grouped for now.

## Reply content
- ok: `<subagent name="scorpion-hero" reply>…final text…</subagent>`
- failed or out of iterations: `<subagent name="kano-storm" reply error>the reason (and any partial text)</subagent>`
- **cancelled by closing the session: nothing is delivered** (the parent no longer exists).
The reply is delivered with `context.Background()` (never a turn's context: `Session.Prompt` stores the context of the prompt that starts the loop as the parent of every later turn). **Delivering to a parent that is closing or closed is dropped silently** (as the scheduler does for an inactive session); a test covers the race.

## Limit
At most **10** live sub-agents per parent session. The 11th call returns a clear error to the model: `too many sub-agents running (10): wait for one to finish`. Without the blocking, nothing else would hold a runaway model back, and each one is a full session talking to a provider.

## Directive
The system prompt that today explains `Subagent` (tool description) is rewritten: it returns immediately, the answer arrives later as a `<subagent … reply>` prompt, the model ends its turn or works on something else and never polls; every parameter but `prompt`, `max_iterations`, `readonly` is gone. The origin of the reply prompt is `subagent:<name>`; the TUI shows it with the outside-origin icon (`⇌`), no new icon (the three-icon rule stays), and the tool icon of `Subagent` keeps its own.

## Out of scope
An explicit cancel of one sub-agent; grouping replies; nested sub-agents (still forbidden: the sub-agent has no `Subagent` tool); a different budget by session.

## Tests
Tool: returns at once with the name and the instruction; parameters gone (`background`/`timeout` are unknown fields → error); name unique and redrawn on collision, suffix when exhausted; the 11th call is refused; the reply arrives as `<subagent … reply>` with origin `subagent:<name>`; `error` attribute on failure; stop emitted in every ending and BEFORE the delivery; Stop of the parent does not cancel; closing the session cancels all and delivers nothing; the race with a closing parent; delegated cost still folded; `Subagents` in the detail and `/info`; events in `WireEvent`, the schema and the reference; the TUI line (before channels, leaves on stop, nothing when empty, initial state from the detail); RPC `event`s. `-race`; mutants on the always-stop, the order stop-then-deliver, the cap and the name uniqueness. A live check with a real model.
