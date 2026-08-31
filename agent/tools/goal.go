package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gurcuff91/harness/types"
)

// GoalExecutor is the closure the Agent builds and passes to the Goal tool.
// It runs ONE full Builder→Tester round: constructs BOTH ephemeral
// sub-agents (with different tool restrictions per role — see agent.go's
// wiring), hands the Builder's raw report to the Tester untouched, and
// returns the Tester's report. ctx carries a single deadline shared by both
// sub-agent calls — see Goal's doc comment for why the timeout is never
// split or reset between them. maxIterations is the caller's requested
// shared ReAct budget for BOTH sub-agents this round; 0 means "use the
// Agent's own default" — the Goal tool has already validated a non-zero
// value is within [subagentMinIterations, subagentMaxIterationsCeiling]
// before this is ever called (the same range Subagent uses).
type GoalExecutor func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error)

// goalInput is the JSON input schema for the Goal tool.
type goalInput struct {
	BuilderPrompt string `json:"builder_prompt" validate:"required"`
	TesterPrompt  string `json:"tester_prompt" validate:"required"`
	Timeout       int    `json:"timeout,omitempty"`
	MaxIterations int    `json:"max_iterations,omitempty"`
}

// goalTimeout is the default wall-clock budget for one full round (Builder
// run + Tester run, sequential) — deliberately much larger than
// subagentTimeout (120s): a real build-then-verify cycle is expected to take
// longer than a single delegated question.
const goalTimeout = 600 * time.Second

// builderRolePrompt is prepended to the caller's builder_prompt for the
// Builder sub-agent. Fixed — defines HOW the Builder behaves; the caller
// only supplies WHAT to do (builder_prompt). Ends by requiring a fixed
// report format so the Tester (which never sees the Builder's reasoning,
// only this report) can verify without re-deriving what was done.
const builderRolePrompt = `You are the Builder in an adversarial build/test loop. Implement exactly what's asked below, completely and correctly. When done, end your response with a report in this exact format so an independent Tester (who has NOT seen your reasoning) can verify your work:

## Builder Report
- Changes made: <concrete summary of what you did>
- Files touched: <list>
- How to verify: <concrete steps/commands the tester can run to check your work>

Task:
`

// testerRolePrompt is prepended to the caller's tester_prompt (the
// acceptance criteria) plus the Builder's raw report, for the Tester
// sub-agent. Fixed — defines HOW the Tester behaves: verify independently,
// never trust claims, never fix anything, evaluate each criterion
// separately (never collapse into one score), and respond in a fixed format
// ending with an explicit Overall verdict.
const testerRolePrompt = `You are the Tester in an adversarial build/test loop. Do NOT trust the Builder's claims — verify each criterion below independently (run commands, read the actual code/files). Never fix or edit anything yourself — only report. Evaluate EACH criterion separately; do not collapse them into a single score.

Acceptance criteria:
%s

Builder's report:
%s

Respond in exactly this format:

## Test Report
- Criterion "<criterion text>": PASS/FAIL — <evidence>
- Criterion "<criterion text>": PASS/FAIL — <evidence>
...
Overall: PASS   (only if every criterion above is PASS)
Overall: FAIL   (if at least one criterion failed)
`

// GoalBuilderPrompt assembles the Builder sub-agent's full prompt for one
// Goal round: the fixed role prompt (defining HOW the Builder behaves and
// its required report format) followed by the caller's operational
// instructions (WHAT to do this round). Exported so agent.go's Goal
// executor — a different package — can build the exact prompt the tool's
// own doc comments describe, without duplicating the fixed prompt text.
func GoalBuilderPrompt(builderPrompt string) string {
	return builderRolePrompt + builderPrompt
}

// GoalTesterPrompt assembles the Tester sub-agent's full prompt for one Goal
// round: the fixed role prompt (defining HOW the Tester behaves — verify
// independently, never fix, per-criterion verdicts) with the caller's
// acceptance criteria and the Builder's raw, unaltered report interpolated
// in. Exported for the same reason as GoalBuilderPrompt.
func GoalTesterPrompt(testerPrompt, builderReport string) string {
	return fmt.Sprintf(testerRolePrompt, testerPrompt, builderReport)
}

// GoalRoundResult assembles what the Goal tool returns to the caller after
// one round: BOTH the Builder's full raw response and the Tester's verdict,
// clearly separated by a blank line. Originally the tool returned only the
// Tester's report (the verdict is what decides whether to iterate) — but
// for content-producing tasks (a written report, an analysis, anything that
// isn't files the caller can just Read back later) the Builder's actual
// deliverable only ever exists in its own response text. Discarding it
// meant a caller who received an 'Overall: PASS' had the VERDICT but not
// the actual content — observed in practice as a model trying to
// reconstruct/guess the content from the Tester's summary instead of having
// the real thing, a real hallucination risk. Returning both costs nothing
// new: the Builder's response was already paid for in tokens once (it's
// handed to the Tester as evidence) — surfacing it to the caller too is
// free reuse, not a new call.
//
// No extra wrapping header is added around builderReport: per
// builderRolePrompt, the Builder's own response already ends with its own
// "## Builder Report" section (changes made / files touched / how to
// verify), which is already a clear enough delimiter on its own — an
// additional header here would be redundant.
func GoalRoundResult(builderReport, testerReport string) string {
	return builderReport + "\n\n" + testerReport
}

// Goal returns a Tool that runs one round of an adversarial build/test loop:
// a Builder sub-agent implements 'builder_prompt', then a Tester sub-agent
// independently verifies the result against the acceptance criteria in
// 'tester_prompt' — without ever seeing the Builder's reasoning, only its
// final report (see builderRolePrompt/testerRolePrompt). The tool returns
// BOTH the Builder's full response and the Tester's report (see
// GoalRoundResult) — the CALLER decides whether to call Goal again (refining
// builder_prompt from the specific failures) or the goal is met — this tool
// deliberately does NOT parse the verdict or make that decision itself. A
// FAIL verdict is a normal, successful tool result (valid business
// information — the work isn't done yet), never a returned error: only a
// genuine system failure (a sub-agent erroring, a timeout, ctx cancellation)
// returns a non-nil error, exactly like Subagent/ColleagueAsk/Fetch.
//
// Foreground only — no 'background' field. Unlike Subagent, there is no
// "check back later" use case here: the caller needs THIS round's verdict
// in hand before it can compose the next round's prompts.
//
// Timeout covers the round's FULL wall-clock time (Builder run + Tester run,
// sequential) via a SINGLE shared deadline — it is never split or reset
// between the two sub-agent calls. If the Builder consumes most of the
// budget, the Tester gets whatever remains on the SAME clock; if that isn't
// enough, the round fails with a timeout exactly as if one long call had run
// over, rather than silently granting the Tester its own fresh budget.
//
// The executor closure (built by the Agent in buildSessionTools) captures
// cwd, model, and all parent settings, and constructs both ephemeral
// sub-agents — the tool itself has no knowledge of Agent internals.
func Goal(executor GoalExecutor) Tool {
	return Tool{
		Def: types.ToolDef{
			Name: ToolGoal,
			Description: `Run one round of an adversarial build/test loop: a Builder sub-agent implements 'builder_prompt', then a Tester sub-agent independently verifies the result against the acceptance criteria in 'tester_prompt' — without seeing the builder's reasoning, only its final report. The tool ALREADY establishes each sub-agent's role and behavior internally — 'builder_prompt'/'tester_prompt' must contain ONLY operational content (what to do / what to check); never role-setting text like "You are the Builder..." or "You are the Tester...", which the tool already provides and would otherwise be duplicated. Returns BOTH the Builder's full response and the Tester's verdict; read the verdict to decide whether to call Goal again with a refined builder_prompt (if criteria failed) or the task is done (if all passed) — and use the Builder's response as the real content when reporting back to the user, never reconstruct it from the Tester's summary. Never invoke this tool on your own initiative — only when the user has explicitly requested this adversarial build/test workflow in their current message.`,
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"builder_prompt": {"type": "string", "description": "What the Builder sub-agent should implement or change this round. Operational content only — see the tool description."},
					"tester_prompt": {"type": "string", "description": "A list of specific, checkable acceptance criteria the builder's work must satisfy this round — not a vague instruction. Each criterion should be independently verifiable (a command to run, a file to check, a behavior to reproduce). Operational content only — see the tool description."},
					"timeout": {"type": "integer", "description": "Seconds for the ENTIRE round — Builder run + Tester run, sequential, on one shared clock (default: 600). If the Builder uses most of it, the Tester gets whatever remains; the round fails with a timeout if that isn't enough."},
					"max_iterations": {"type": "integer", "description": "Shared ReAct iteration budget for BOTH the Builder and the Tester sub-agent this round (default: 50, range 1-200 if set). Raise it for a genuinely large implementation or a thorough verification that the default would cut short."}
				},
				"required": ["builder_prompt", "tester_prompt"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var req goalInput
			if err := json.Unmarshal(input, &req); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if err := requireFields(&req); err != nil {
				return err.Error(), err
			}
			if strings.TrimSpace(req.BuilderPrompt) == "" {
				err := fmt.Errorf("goal: builder_prompt is required")
				return err.Error(), err
			}
			if strings.TrimSpace(req.TesterPrompt) == "" {
				err := fmt.Errorf("goal: tester_prompt is required")
				return err.Error(), err
			}
			// Same range/semantics as Subagent's max_iterations: 0 = omitted
			// (use the default), anything else must fall inside [1, 200],
			// rejected explicitly rather than silently clamped.
			if req.MaxIterations != 0 && (req.MaxIterations < subagentMinIterations || req.MaxIterations > subagentMaxIterationsCeiling) {
				err := fmt.Errorf("goal: max_iterations must be between %d and %d (got %d) — omit it to use the default",
					subagentMinIterations, subagentMaxIterationsCeiling, req.MaxIterations)
				return err.Error(), err
			}

			timeout := goalTimeout
			if req.Timeout > 0 {
				timeout = time.Duration(req.Timeout) * time.Second
			}
			// A single shared deadline for BOTH sub-agent calls — see the
			// doc comment above for why this is never split or reset.
			ctx2, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			out, err := executor(ctx2, req.BuilderPrompt, req.TesterPrompt, req.MaxIterations)
			// On a genuine system timeout, discard any partial output — same
			// rule as Subagent: a round cut off mid-flight may carry a
			// half-finished Builder report never actually verified by the
			// Tester, which would be misleading if surfaced as if it were a
			// real verdict.
			if isTimeout(err) {
				msg := fmt.Sprintf("Timeout after %v — retry with a larger 'timeout', or a smaller 'max_iterations' if the round is doing more work than it needs to.", timeout)
				return msg, err
			}
			return out, err
		},
	}
}
