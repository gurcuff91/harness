# `Goal` tool — adversarial builder/tester loop engineering — Design

**Date:** 2026-08-31
**Status:** Approved, ready for implementation planning
**Area:** `agent/tools/goal.go` (new), `agent/agent.go` (wiring), `agent/prompts.go`
(`/goal` command prompt), `internal/tui/commands.go` (`/goal <prompt>` command)

## Problem

Requested by Gus: a `/goal <prompt>` command (Claude-Code-style) that drives an
**adversarial build/test loop** — a Builder sub-agent implements, a Tester
sub-agent independently verifies against acceptance criteria, and if it finds
failures the loop hands the failure report back to the Builder for another
round, repeating until the Tester reports every criterion passes (or the
parent decides to stop).

This is a well-documented pattern in the agentic literature — **Generator-
Critic** / **Evaluator-Optimizer** (a specialization of Orchestrator-Workers),
also shipped in production as "Builder/Adversary" loop pairs. Researched via
web search before designing (see conversation history): the load-bearing rules
every serious implementation agrees on are (1) strict context isolation
between generator and critic — the critic never sees the generator's
reasoning trace, only the artifact + criteria; (2) the critic's verdict is
structured per-criterion evidence, never a single collapsed score; (3) the
critic must be allowed to say "no changes needed" as a first-class outcome;
(4) the critic never edits — separation of duties; (5) an explicit round/
budget cap, because an unbounded critic degrades into manufactured findings.

## Key design tension (resolved via brainstorm)

Two naive designs were considered and rejected before landing on the one below:

1. **Fully Go-driven loop** (`session.Goal(prompt)` on the Session type,
   mirroring `session.Compact()`) — the round-trip loop (launch builder →
   wait → launch tester → wait → decide → repeat) lives entirely in
   deterministic Go code. **Rejected**: the parent session's LIVE context
   (decisions made this conversation, project conventions not yet written to
   memory, prior turns) is exactly what makes composing a good builder/tester
   brief possible — a Go driver with no LLM in the loop has no way to
   originate or refine that brief round to round without either (a) baking
   a static one-shot brief at round 0 and losing all context after that, or
   (b) reimplementing a second LLM call inside Go just to refine it, at which
   point you've built a worse, less controllable version of the parent's own
   ReAct loop.
2. **Prompt-only orchestration** (inject instructions telling the parent to
   call `Subagent(background:true)` twice and poll the result files itself)
   — **Rejected**: burns real parent turns/tokens purely on "is it done yet?"
   polling, is non-deterministic (the model can deviate from the protocol,
   misread a half-written file), and contradicts the project's own principle
   that business logic (like `/compact`, `/model`) lives in the backend, not
   improvised by the model via tool calls each time.

**The resolution**: split what's *mechanical* (fragile if improvised —
launching a sub-agent, waiting without polling, handing the builder's raw
output to the tester untouched) from what's *strategic* (best decided by the
model with full session context — how to phrase this round's builder
instructions, whether to iterate again, when to give up and ask the user).

`Goal` is a **tool that runs exactly ONE round** of Builder → Tester,
blocking (foreground only, like `Subagent` without `background`). The
**parent's own ReAct loop is the round-trip loop** — it sees the Tester's full
report as an ordinary tool result each round, and decides (with everything it
knows about the conversation) how to compose the next round's prompts, or
whether to stop. No new orchestration primitive is needed: the parent calling
`Goal` repeatedly, each time refining `builder_prompt` based on the previous
`tester_prompt`'s report, *is* the adversarial loop — driven by the same
ReAct mechanism (and iteration budget) already governing every other tool use.

## The `Goal` tool

### Input schema

```go
type goalInput struct {
    BuilderPrompt string `json:"builder_prompt" validate:"required"`
    TesterPrompt  string `json:"tester_prompt" validate:"required"`
    Timeout       int    `json:"timeout,omitempty"`        // seconds, default 600 (10 min)
    MaxIterations int    `json:"max_iterations,omitempty"` // shared ReAct budget for BOTH sub-agents this round, 0 = default, else validated [1,200] exactly like Subagent
}
```

- **Foreground only** — no `background` field. A round's result is exactly
  what the parent needs before deciding the next round; there is nothing
  useful to do with a result file here (unlike `Subagent`, there's no "come
  back later" use case — the parent must see this round's verdict to compose
  the next one).
- `Timeout` covers the round's FULL wall-clock time (builder run + tester
  run, sequential) — default higher than `Subagent`'s 120s because a real
  build+test cycle is expected to take longer. 600s (10 min) default,
  overridable by the caller exactly like `Subagent.timeout`.
- `MaxIterations` reuses `Subagent`'s exact validation contract
  (`subagentMinIterations=1`, `subagentMaxIterationsCeiling=200`, 0 = "use
  the default") — shared across both the builder and tester sub-agent calls
  this round, not split into two separate budgets. Simpler contract, and a
  round that needs a bigger budget needs it for both roles roughly together
  (a big implementation needs a thorough verification to match).
- `BuilderPrompt` and `TesterPrompt` are the OPERATIONAL instructions the
  parent composes each round — "what to do", not "how to behave as a role".
  The schema description for `TesterPrompt` explicitly frames it as **a list
  of specific, checkable acceptance criteria**, not a vague instruction:
  *"A list of specific, checkable acceptance criteria the builder's work must
  satisfy this round — not a vague instruction. Each criterion should be
  independently verifiable (a command to run, a file to check, a behavior to
  reproduce)."*

### Description (tool-level) — the critical gate

The tool's `Description` explicitly forbids self-initiated use:

> "Run one round of an adversarial build/test loop: a Builder sub-agent
> implements 'builder_prompt', then a Tester sub-agent independently verifies
> the result against the acceptance criteria in 'tester_prompt' — without
> seeing the builder's reasoning, only its final report. Returns the
> Tester's full report; read it to decide whether to call Goal again with a
> refined builder_prompt (if criteria failed) or the task is done (if all
> passed). **Never invoke this tool on your own initiative** — only when the
> user has explicitly requested this adversarial build/test workflow in
> their current message (e.g. via the `/goal` command)."

This mirrors the constraint already enforced today via prompt discipline for
other high-cost tools, but stated as an explicit negative constraint since
misuse here is unusually expensive (two full sub-agent runs per round).

### Fixed internal role prompts (`agent/tools/goal.go`)

Two prompt constants — the "how each role behaves", never touched by the
caller, only the operational content varies:

**`builderRolePrompt`** (prepended to `req.BuilderPrompt` for the builder
sub-agent's system prompt or first user message):

```
You are the Builder in an adversarial build/test loop. Implement exactly
what's asked below, completely and correctly. When done, end your response
with a report in this exact format so an independent Tester (who has NOT
seen your reasoning) can verify your work:

## Builder Report
- Changes made: <concrete summary of what you did>
- Files touched: <list>
- How to verify: <concrete steps/commands the tester can run to check your work>
```

**`testerRolePrompt`** (prepended to `req.TesterPrompt` + the builder's raw
report, for the tester sub-agent):

```
You are the Tester in an adversarial build/test loop. Do NOT trust the
Builder's claims — verify each criterion below independently (run commands,
read the actual code/files). Never fix or edit anything yourself — only
report. Evaluate EACH criterion separately; do not collapse them into a
single score.

Acceptance criteria:
<req.TesterPrompt>

Builder's report:
<builder's raw output>

Respond in exactly this format:

## Test Report
- Criterion "<criterion text>": PASS/FAIL — <evidence>
- Criterion "<criterion text>": PASS/FAIL — <evidence>
...
Overall: PASS   (only if every criterion above is PASS)
Overall: FAIL   (if at least one criterion failed)
```

### Role/tool restrictions (separation of duties)

- **Builder sub-agent**: full tool access inherited from the parent, same as
  a normal `Subagent` call (Bash/Read/Write/Edit/Fetch/MCP tools).
- **Tester sub-agent**: **no `Write`/`Edit`** — `Read`/`Bash`(to run
  tests/builds/lint)/`Fetch` only. This is the load-bearing "separation of
  duties" rule from the research: a tester that can edit stops being an
  independent judge and starts silently "fixing" instead of reporting,
  defeating the entire point of adversarial verification.
- Both sub-agents: `Subagent`, `Goal` itself, `MemoWrite`, `MemoDelete`,
  `Schedule*` all disallowed — identical recursion-prevention rule already
  applied to `Subagent`'s own ephemeral sub-agents.

### Output / error contract — the corrected design

**The tool does NOT parse the verdict for control flow.** A `FAIL` from the
Tester is a completely normal, successful tool execution — it's valid
business information ("the builder's work doesn't meet criteria yet"), not a
system error. `Execute` returns:

- `(testerFullReport, nil)` on any successful round — whether the report
  says `Overall: PASS` or `Overall: FAIL`. The parent reads the report text
  itself and decides what to do; the tool imposes no interpretation.
- `(errMsg, err)` — a genuine error — ONLY for real system failures: the
  builder or tester sub-agent erroring out, a timeout (reusing `isTimeout`
  exactly like `Subagent`/`ColleagueAsk`/`Fetch` — no partial output kept,
  same reasoning: a cut-off LLM sub-agent's half-formed reasoning shouldn't
  contaminate the parent), or `ctx` cancellation (a user Stop).

The tool's output to the parent is **only the Tester's full `## Test Report`**
— never the Builder's raw report. The Builder's report already served its
purpose (handed to the Tester as evidence); what the parent needs is the
Tester's independent verdict, not a re-statement of what the Builder claims.

### Cost accounting

Both ephemeral sub-agents' `Stats()` fold into the parent session's totals via
the existing `Session.addDelegatedCost()` mechanism (same as `Subagent` and
`Fetch`'s summarizer) — a `Goal` round can be expensive (two full sub-agent
runs), and that cost must be visible in the parent's own `Stats()`/`Meta()`,
never silently discarded when the ephemeral sub-agents close.

## The `/goal <prompt>` command

A new TUI (and any transport reusing the same session-command machinery)
slash command. Unlike the dynamic session commands (`rename`, `thinking`,
`model`, `compact`, `reset`, `skill:*`) that call a fixed server-side handler
directly, `/goal <prompt>` is closer to how `skill:<name>` already works: it
**injects a specially-composed prompt into the parent session** and lets the
existing `Prompt()`/ReAct machinery take over — no new server-side "goal
engine" state machine, no new session lifecycle. Composing the injected
prompt is a new fixed template (`goalCommandPrompt`, `agent/prompts.go`,
sibling to `maxIterationsPrompt`) explaining, at a high level, the protocol:

- Use the `Goal` tool to run one round at a time.
- Compose `builder_prompt` for round 1 from the user's original `<prompt>`
  and everything relevant already known about this project/conversation.
- Compose `tester_prompt` as a concrete list of acceptance criteria derived
  from the user's goal.
- After each round, read the Tester's report: if `Overall: PASS`, the goal is
  achieved — stop and report success. If `Overall: FAIL`, refine
  `builder_prompt` for the next round using the specific per-criterion
  failures (not a vague "try again"), and call `Goal` again.
- If progress stalls (the same criteria keep failing with no improvement
  across rounds) or the task turns out ambiguous/infeasible, stop and ask the
  user rather than looping indefinitely — the parent's own iteration budget
  is the ultimate backstop, but stalling should be caught earlier than that.

This keeps `/goal` implementable as "compose one more fixed prompt template
and call `sess.Prompt()`" — mirrors how `skill:<name>` is wired server-side
today, no new endpoint needed beyond what `handleExecCommand`'s existing
`default:` skill-prompt-injection branch already demonstrates the shape of
(though `/goal` needs its own case, since it carries a free-text prompt
rather than a skill name).

## Testing strategy (mirrors `Subagent`'s existing test shape)

- Unit tests (mock executor pattern like `subagent_test.go`): input
  validation (required fields, `max_iterations` range, `timeout` default vs.
  override), builder/tester role prompts assembled correctly, tester's
  restricted tool set enforced, output is exactly the tester's report (not
  the builder's), a `FAIL` verdict returns `(report, nil)` — never a non-nil
  `error` — proving the corrected error contract.
- Live integration test (mirrors `TestSubagentMaxIterationsOverrideReachesEphemeralSubAgent`'s
  approach — real connected provider, behavior-shape assertions instead of
  brittle substring matches): a genuinely simple, verifiable task (e.g. "write
  a file containing exactly X") with a real acceptance criterion, confirming
  a first-round PASS produces `Overall: PASS` in the output and a
  deliberately-impossible criterion produces `Overall: FAIL`.
- Full suite + `-race` + `go vet ./...` green before commit, per project
  standard.

## Open items deferred to implementation planning

- Exact TUI rendering treatment for a `Goal` tool call in progress (icon,
  primary param shown — likely `builder_prompt` truncated, following
  `toolfmt.go`'s existing pattern for other tools).
- Whether `/goal`'s injected prompt needs any special session-history
  framing (e.g. marked as system-generated) — to be resolved the same way
  `skill:` prompts are handled today.
