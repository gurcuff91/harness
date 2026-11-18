package tools

// Built-in tool names — use these constants in AgentOptions.AllowedTools
// to avoid typos and enable IDE autocompletion.
// Names match Claude Code canonical names where possible.
const (
	ToolBash           = "Bash"
	ToolRead           = "Read"
	ToolWrite          = "Write"
	ToolEdit           = "Edit"
	ToolFetch          = "Fetch"
	ToolSkill          = "Skill"
	ToolSubagent       = "Subagent"
	ToolMemoWrite      = "MemoWrite"
	ToolMemoSearch     = "MemoSearch"
	ToolMemoDelete     = "MemoDelete"
	ToolSchedule       = "Schedule"
	ToolScheduleList   = "ScheduleList"
	ToolScheduleDelete = "ScheduleDelete"
	ToolColleagueList  = "ColleagueList"
	ToolColleagueAsk   = "ColleagueAsk"
	ToolWebSearch      = "WebSearch"
	ToolSessionInfo    = "SessionInfo"
)

// BuiltinNames lists every built-in tool name (whether or not a given session
// registers it): a name another source must never shadow.
func BuiltinNames() []string {
	return []string{
		ToolBash, ToolRead, ToolWrite, ToolEdit, ToolFetch, ToolSkill, ToolSubagent,
		ToolMemoWrite, ToolMemoSearch, ToolMemoDelete,
		ToolSchedule, ToolScheduleList, ToolScheduleDelete,
		ToolColleagueList, ToolColleagueAsk, ToolWebSearch, ToolSessionInfo,
	}
}
