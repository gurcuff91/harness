package rpc

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gurcuff91/harness/agent"
)

// Every public method of agent.Agent and agent.Session is either an operation
// of the protocol or listed here with the reason it is not. A method added to
// the SDK without deciding fails this test, so the TS SDK cannot silently fall
// behind the Go one.
func TestEveryAgentAndSessionMethodIsDecided(t *testing.T) {
	idx := opIndex()
	has := func(m string) bool { _, ok := idx[m]; return ok }

	agentCovered := map[string]string{
		"RegisterProfile":   "agent/register_profile",
		"UnregisterProfile": "agent/unregister_profile",
		"ProfileNames":      "agent/profiles",
		"RegisterTool":      "agent/register_tool",
		"MCPStatuses":       "agent/mcp_status",
		"ActiveSessions":    "agent/active_sessions",
		"NewSession":        "session/new",
		"ResumeSession":     "session/resume",
		"ForkSession":       "session/fork",
		"ImportSession":     "session/import",
		"Close":             "agent/shutdown",
	}
	agentExcluded := map[string]string{
		"Admin":               "the admin/* operations are the Admin",
		"Options":             "the configuration is what the client sent in initialize",
		"MCPTools":            "returns Go tool values for sharing with sub-agents; not data",
		"FireScheduleForTest": "a test hook",
	}
	sessionCovered := map[string]string{
		"Prompt": "session/prompt", "PromptAndWait": "session/ask", "Stop": "session/stop",
		"Compact": "session/compact", "Reset": "session/reset",
		"SwitchModel": "session/switch_model", "SwitchThinking": "session/switch_thinking",
		"SetMaxIterations": "session/set_max_iterations", "Rename": "session/rename",
		"Close": "session/close", "ReadSkill": "session/exec_command",
		"Tools": "session/tools", "SystemPrompt": "session/sysprompt",
		"ContextBreakdown": "session/context", "MessagesWindow": "session/messages",
		"Snapshot": "session/info", "Detail": "session/new",
	}
	sessionExcluded := map[string]string{
		"Subscribe":       "events are the `event` notification (built on Watch)",
		"Watch":           "the event pump",
		"Wait":            "session/ask waits for its own turn",
		"AllMessages":     "session/messages covers it with a window",
		"IsBusy":          "in session/info (busy)",
		"FollowUpCount":   "in session/info (queue_depth)",
		"Skills":          "listed in the system prompt; read through session/exec_command skill:<name>",
		"ModelMeta":       "the model catalog is admin/providers/model",
		"Model":           "in the session detail",
		"Thinking":        "in the session detail",
		"Stats":           "in the session detail",
		"ModelFallback":   "in the session detail",
		"MissingProfiles": "in the session detail",
		"ID":              "in the session detail",
		"CWD":             "in the session detail",
		"Name":            "in the session detail",
		"CreatedAt":       "in the session detail (meta)",
		"MaxIterations":   "in the session detail",
		"Meta":            "in the session detail",
		"Channels":        "in the session detail",
		"Subagents":       "in the session detail",
	}

	check := func(typeName string, typ reflect.Type, covered, excluded map[string]string) {
		seen := map[string]bool{}
		for i := 0; i < typ.NumMethod(); i++ {
			name := typ.Method(i).Name
			seen[name] = true
			if m, ok := covered[name]; ok {
				if !has(m) {
					t.Errorf("%s.%s is mapped to %q, which is not in the table", typeName, name, m)
				}
				continue
			}
			if _, ok := excluded[name]; ok {
				continue
			}
			t.Errorf("%s.%s is public but has no operation and is not in the exclusion list: decide it", typeName, name)
		}
		var stale []string
		for n := range covered {
			if !seen[n] {
				stale = append(stale, n)
			}
		}
		for n := range excluded {
			if !seen[n] {
				stale = append(stale, n)
			}
		}
		sort.Strings(stale)
		for _, n := range stale {
			t.Errorf("%s: %q is listed but no longer exists", typeName, n)
		}
	}
	check("agent.Agent", reflect.TypeOf(&agent.Agent{}), agentCovered, agentExcluded)
	check("agent.Session", reflect.TypeOf(&agent.Session{}), sessionCovered, sessionExcluded)
}
