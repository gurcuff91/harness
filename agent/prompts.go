package agent

// defaultSystemPrompt is used when AgentOptions.SystemPrompt is empty.
const defaultSystemPrompt = `You are an expert coding agent working directly in the user's codebase. You have access to tools for reading, writing, and editing files, running shell commands, and fetching URLs.`

// subagentSystemPrompt is the system prompt for ephemeral sub-agents spawned via the Subagent tool.
const subagentSystemPrompt = `You are a focused sub-agent. Execute the delegated task completely and autonomously.
Make reasonable assumptions. Return full results — do not truncate. Never ask questions.`

// fetchSummarizeSystemPrompt is the system prompt for the ephemeral, tool-less
// sub-agent Fetch's FetchSummarizer spins up to condense fetched content
// (agent.go's buildSessionTools wiring). Deliberately narrower than
// subagentSystemPrompt: this sub-agent gets no tools at all (see
// AgentOptions.DisallowedTools at the call site) — its only job is to read
// the content it's handed and answer according to the instruction, in one
// turn, never to go explore or fetch anything itself.
const fetchSummarizeSystemPrompt = `You are given the text content of a web page or API response, and an instruction for what to extract or summarize from it. Respond with ONLY the requested information — no preamble, no meta-commentary about the page itself, no "Here is..." framing. If the requested information genuinely isn't present in the content, say so in one short sentence.`

// compactSystemPrompt is used when generating a compaction summary of the conversation.
// The summary replaces the full history when context usage reaches ~98%.
const compactSystemPrompt = `Your task is to produce a concise but complete summary of the conversation so far.
This summary will REPLACE the full conversation history — it must contain everything
needed to continue the work without losing context.

Include:
1. What was asked / the goal
2. What has been done (decisions made, files changed, commands run, key findings)
3. Current state — what is working, what is pending
4. Any critical context (errors encountered, constraints, important details)

Be specific and factual. Use bullet points. Do NOT ask questions or add commentary.
Respond with ONLY the summary text.`

// compactRequestPrompt is appended as a final user message when generating a
// compaction summary. It makes the request explicit and ensures the conversation
// ends on a user turn (required by providers that reject assistant prefill).
const compactRequestPrompt = "Summarize the conversation so far following the instructions above."

// memoryCompactionReminder is appended to the persisted compaction checkpoint
// (NOT to the summary shown to the user) when the session has memory enabled
// (mirrors the "## Memory" block in buildSystemPrompt — same condition,
// a.memStore != nil). Right after compaction, the model's nearest context is
// this dense summary, not the system prompt further up — exactly the kind of
// "lack context about earlier work" moment the Memory section already tells
// the model to use MemoSearch for. Worded as a nudge, not an instruction to
// always search, so it doesn't induce needless MemoSearch calls.
const memoryCompactionReminder = "\n\n---\nReminder: you have persistent, project-scoped memory (MemoSearch/MemoWrite/MemoDelete). If this summary is missing something you need, it may be worth searching memory before assuming the context is gone."

// buildCompactionCheckpoint appends memoryCompactionReminder to summary when
// hasMemory is true, leaving summary untouched otherwise. Split out from
// Session.compact as a pure function so the memory-nudge behavior is directly
// testable without standing up a Session (provider, store, tools, …).
func buildCompactionCheckpoint(summary string, hasMemory bool) string {
	if hasMemory {
		return summary + memoryCompactionReminder
	}
	return summary
}

// maxIterationsPrompt is injected as a user message when the agent hits the
// max ReAct iterations limit. It asks the model to report progress and check
// with the user before continuing.
const maxIterationsPrompt = "You've reached the maximum number of tool calls allowed for this turn. " +
	"Please summarize: (1) what you have completed so far, (2) what still needs to be done, " +
	"and (3) ask the user if they want you to continue or if they'd like to change direction."

// GoalCommandPrompt builds the message injected into the session when the
// user runs the /goal command — it does NOT implement the loop itself (that
// stays the model's own ReAct loop, driven by repeated Goal tool calls); it
// only explains the protocol at a high level and hands off the user's own
// goal text. Exported (unlike the const prompts above) because it needs to
// interpolate the user's goal — server.go's handleExecCommand composes it
// for the "goal" command the same way it already composes the skill: prompt.
//
// Step 0 (surface conversation-only context FIRST, before round 1, WHEN
// THERE IS ANY) is deliberately narrow, not a general "gather everything"
// prelude: the Builder and Tester are ephemeral sub-agents, but they are NOT
// blank slates about the PROJECT — they run with the same cwd, the same
// tools (Bash/Read/Fetch/etc.), the same skills, and the same read-only
// memory search the parent has, so buildSystemPrompt's SYSTEM.md/AGENTS.md/
// memory sections are already available to them, and they can investigate
// the codebase themselves exactly as the parent could. What they genuinely
// cannot get on their own is anything that only exists in THIS
// conversation's history — a decision already made, a constraint the user
// stated, something already tried and ruled out. Step 0 is scoped to
// exactly that, so the parent doesn't waste its own turn re-discovering or
// restating project context a sub-agent can find by itself.
func GoalCommandPrompt(userGoal string) string {
	return "Use the Goal tool to drive an adversarial build/test loop toward this objective:\n\n" +
		userGoal +
		"\n\n" +
		"Protocol:\n" +
		"0. Before composing round 1's prompts, check whether THIS CONVERSATION holds anything the Builder/Tester have no other way to know — a decision already made, a constraint the user stated, something already tried and ruled out. They run in the SAME project (same cwd, same tools, same skills, same read-only memory) so they can explore the codebase, read files, and search memory themselves — don't re-discover or restate any of that for them. Only surface what's specific to this conversation; if there's nothing like that, skip straight to step 1.\n" +
		"1. Compose this round's 'builder_prompt' — operational content only, what to do — including any conversation-only context from step 0, if there was any.\n" +
		"2. Compose 'tester_prompt' as a concrete, checkable list of acceptance criteria derived from the objective — not a vague instruction.\n" +
		"3. Call the Goal tool. Read the Tester's report it returns.\n" +
		"4. If 'Overall: PASS' — the goal is achieved. Stop and report success to the user.\n" +
		"5. If 'Overall: FAIL' — refine 'builder_prompt' for the next round using the SPECIFIC per-criterion failures the Tester reported (not a generic \"try again\").\n" +
		"6. If progress stalls (the same criteria keep failing across rounds with no improvement) or the objective turns out ambiguous or infeasible, STOP and ask the user rather than looping indefinitely."
}
