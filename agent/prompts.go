package agent

// defaultSystemPrompt is used when AgentOptions.SystemPrompt is empty.
const defaultSystemPrompt = `You are an expert coding agent working directly in the user's codebase. You have access to tools for reading, writing, and editing files, running shell commands, and fetching URLs.`

// subagentSystemPrompt is the system prompt for ephemeral sub-agents spawned via the Subagent tool.
const subagentSystemPrompt = `You are a focused sub-agent. Execute the delegated task completely and autonomously.
Make reasonable assumptions. Return full results — do not truncate. Never ask questions.`

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

// maxTurnsPrompt is injected as a user message when the agent hits the max tool-call limit.
// It asks the model to report progress and check with the user before continuing.
const maxTurnsPrompt = "You've reached the maximum number of tool calls allowed for this turn. " +
	"Please summarize: (1) what you have completed so far, (2) what still needs to be done, " +
	"and (3) ask the user if they want you to continue or if they'd like to change direction."
