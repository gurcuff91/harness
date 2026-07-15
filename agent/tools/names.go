package tools

// Built-in tool names — use these constants in AgentOptions.AllowedTools
// to avoid typos and enable IDE autocompletion.
// Names match Claude Code canonical names where possible.
const (
	ToolBash         = "Bash"
	ToolRead         = "Read"
	ToolWrite        = "Write"
	ToolEdit         = "Edit"
	ToolFetch        = "Fetch"
	ToolSkill        = "Skill"
	ToolSubagent     = "Subagent"
	ToolMemoWrite  = "MemoWrite"
	ToolMemoSearch = "MemoSearch"
	ToolMemoDelete = "MemoDelete"
)
