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
// (NOT to the summary shown to the user) for whichever recovery tool(s) are
// actually enabled for this session (EnableMemory / EnableSessionInfo —
// independent flags; either, both, or neither may be on). Right after
// compaction, the model's nearest context is this dense summary, not the
// system prompt further up — exactly the kind of "lack context about
// earlier work" moment where a reminder pays off.
//
// Each branch mentions ONLY the tool(s) actually available to this session
// — never the other one, even when both happen to be true. A reference to
// a tool the model doesn't have registered is worse than no reference at
// all: confusing, and useless since it can't call something that isn't in
// its own tool list.
func memoryCompactionReminder(hasMemory, sessionSearchEnabled bool) string {
	switch {
	case hasMemory && sessionSearchEnabled:
		return "\n\n---\nReminder: you have persistent, project-scoped memory (MemoSearch) and full-text search over this session's own conversation history (SessionSearch). If this summary is missing something you need, one of them may have it — searching before assuming the context is gone is often worth it."
	case hasMemory:
		return "\n\n---\nReminder: you have persistent, project-scoped memory (MemoSearch). If this summary is missing something you need, it may be worth searching memory before assuming the context is gone."
	case sessionSearchEnabled:
		return "\n\n---\nReminder: you have full-text search over this session's own conversation history (SessionSearch). If this summary is missing something you need, it may be worth searching before assuming the context is gone."
	default:
		return ""
	}
}

// buildCompactionCheckpoint appends memoryCompactionReminder to summary when
// either recovery tool is enabled, leaving summary untouched otherwise. Split
// out from Session.compact as a pure function so the reminder behavior is
// directly testable without standing up a Session (provider, store, tools, …).
func buildCompactionCheckpoint(summary string, hasMemory, sessionSearchEnabled bool) string {
	if reminder := memoryCompactionReminder(hasMemory, sessionSearchEnabled); reminder != "" {
		return summary + reminder
	}
	return summary
}

// maxIterationsPrompt is injected as a user message when the agent hits the
// max ReAct iterations limit. It asks the model to report progress and check
// with the user before continuing.
const maxIterationsPrompt = "You've reached the maximum number of tool calls allowed for this turn. " +
	"Please summarize: (1) what you have completed so far, (2) what still needs to be done, " +
	"and (3) ask the user if they want you to continue or if they'd like to change direction."
