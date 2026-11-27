package agent

import (
	"sort"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
)

// toolNames lists the tools a session was opened with.
func toolNames(s *Session) map[string]bool {
	out := map[string]bool{}
	for _, d := range s.Tools() {
		out[d.Name] = true
	}
	return out
}

func sortedKeys(m map[string]bool) string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return strings.Join(k, " ")
}

// subagentToolsOf opens the sub-agent a parent session would launch, exactly as
// launchSubagent builds it, and returns the tools it has.
func subagentToolsOf(t *testing.T, parent *Session, readonly bool) map[string]bool {
	t.Helper()
	sub, err := parent.agent.newSubagent(subagentSystemPrompt, 5, readonly)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sub.Close() })
	ss, err := sub.NewSession(t.TempDir(), parent.Model())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	return toolNames(ss)
}

// THE PROPERTY: a sub-agent never has a tool its parent does not have. The
// owner of an agent limits it with DisallowedTools (and by not enabling
// WebSearch); a model must not be able to step over that by launching a
// sub-agent. (A sub-agent may have FEWER: no Subagent, no memory writes, no
// scheduling, and no Write/Edit when readonly.)
func TestASubagentNeverHasAToolItsParentDoesNot(t *testing.T) {
	configs := map[string]AgentOptions{
		"defaults":            {},
		"web search":          {EnableWebSearch: true},
		"no shell":            {DisallowedTools: []string{tools.ToolBash}},
		"read only owner":     {DisallowedTools: []string{tools.ToolBash, tools.ToolWrite, tools.ToolEdit}},
		"no fetch, no read":   {DisallowedTools: []string{tools.ToolFetch, tools.ToolRead}},
		"web search denied":   {EnableWebSearch: true, DisallowedTools: []string{tools.ToolWebSearch}},
		"everything but Bash": {EnableWebSearch: true, DisallowedTools: []string{tools.ToolWrite, tools.ToolEdit, tools.ToolFetch, tools.ToolRead, tools.ToolWebSearch}},
	}
	for name, opts := range configs {
		for _, readonly := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				a, model := directivesTestAgent(t, store.NewInMemoryStore(), opts)
				parent, err := a.NewSession(t.TempDir(), model)
				if err != nil {
					t.Fatal(err)
				}
				p, sub := toolNames(parent), subagentToolsOf(t, parent, readonly)
				for tool := range sub {
					if !p[tool] {
						t.Errorf("readonly=%v: the sub-agent has %q, its parent does not.\nparent:    %s\nsub-agent: %s", readonly, tool, sortedKeys(p), sortedKeys(sub))
					}
				}
			})
		}
	}
}

// WebSearch is inherited: a sub-agent researches too (delegating research is
// the main use of a background sub-agent), when its parent can.
func TestASubagentHasWebSearchExactlyWhenItsParentDoes(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts AgentOptions
		want bool
	}{
		{"enabled", AgentOptions{EnableWebSearch: true}, true},
		{"not enabled", AgentOptions{}, false},
		{"enabled but disallowed", AgentOptions{EnableWebSearch: true, DisallowedTools: []string{tools.ToolWebSearch}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, model := directivesTestAgent(t, store.NewInMemoryStore(), tc.opts)
			parent, _ := a.NewSession(t.TempDir(), model)
			if got := subagentToolsOf(t, parent, false)[tools.ToolWebSearch]; got != tc.want {
				t.Errorf("sub-agent has WebSearch = %v, want %v", got, tc.want)
			}
		})
	}
}

// What a sub-agent is deliberately denied, whatever its parent has: launching
// sub-agents (no recursion), writing memory, managing schedules; and Write and
// Edit when readonly. A tool of the parent that is not on this list is inherited.
func TestASubagentKeepsItsOwnRestrictions(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{EnableWebSearch: true})
	parent, _ := a.NewSession(t.TempDir(), model)
	if !toolNames(parent)[tools.ToolSubagent] {
		t.Fatal("the parent should have Subagent")
	}
	sub := subagentToolsOf(t, parent, false)
	for _, denied := range []string{tools.ToolSubagent, tools.ToolMemoWrite, tools.ToolMemoDelete, tools.ToolSchedule, tools.ToolScheduleList, tools.ToolScheduleDelete} {
		if sub[denied] {
			t.Errorf("the sub-agent has %q", denied)
		}
	}
	ro := subagentToolsOf(t, parent, true)
	if ro[tools.ToolWrite] || ro[tools.ToolEdit] {
		t.Errorf("a readonly sub-agent can write: %s", sortedKeys(ro))
	}
	if !sub[tools.ToolWrite] || !sub[tools.ToolEdit] || !sub[tools.ToolBash] || !sub[tools.ToolRead] {
		t.Errorf("a normal sub-agent lost a tool its parent has: %s", sortedKeys(sub))
	}
}
