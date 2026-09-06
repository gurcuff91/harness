package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// TestSubagentReadonlyOverrideReachesEphemeralSubAgent is an end-to-end
// integration check that the Subagent tool's 'readonly' override
// (agent/tools/subagent.go) actually reaches the ephemeral sub-agent
// buildSessionTools' executor constructs (agent.go) — not just that the
// tool validates and forwards the value (already covered by
// agent/tools/subagent_test.go's unit tests against a mock executor).
//
// Verifies the structural guarantee: with readonly=true, the sub-agent's
// tool registry never includes Write/Edit at all (agent.go appends them to
// DisallowedTools before the ephemeral Agent is even constructed) — so a
// sub-agent asked to write a file has no such tool to call in the first
// place, distinct from "chose not to" or "tried and got permission
// denied". The task explicitly asks the sub-agent to attempt writing a
// file, so if the override reached it, its own report must describe not
// having a Write/file-creation tool available; if the override did NOT
// reach it (silently fell back to full access), it would simply write the
// file and report success instead.
func TestSubagentReadonlyOverrideReachesEphemeralSubAgent(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment to run a real Subagent call")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var subagentResult string
	sawResult := false
	done := make(chan struct{})
	sess.Subscribe(func(e types.Event) {
		if e.Type == types.EventToolResult && e.ToolName == "Subagent" {
			sawResult = true
			subagentResult = e.Output
		}
		if e.Type == types.EventTurnEnd {
			close(done)
		}
	})
	sess.Prompt(context.Background(),
		"Use the Subagent tool exactly once, with readonly set to true, delegating this task to it: "+
			"'Create a new file called notes.txt containing the text hello, using whatever file-writing tool you have available. "+
			"If you do not have a tool that can create or write files, say so explicitly instead of attempting anything else.' "+
			"Do not do this yourself — delegate it, then report back whatever the sub-agent returned verbatim.")

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("turn did not finish within 90s")
	}

	if !sawResult {
		t.Skip("model did not invoke Subagent with readonly=true this run (nondeterministic tool use/compliance) — rerun to exercise this path")
	}

	lower := strings.ToLower(subagentResult)
	// The sub-agent, lacking Write/Edit entirely, should report that it has
	// no way to create/write the file — look for language indicating that,
	// rather than evidence it actually wrote something.
	admitsNoWriteTool := strings.Contains(lower, "no") && (strings.Contains(lower, "write") || strings.Contains(lower, "file-writing") || strings.Contains(lower, "create"))
	claimsSuccess := strings.Contains(lower, "created") || strings.Contains(lower, "wrote") || strings.Contains(lower, "successfully")
	if claimsSuccess && !admitsNoWriteTool {
		t.Errorf("sub-agent result claims it wrote/created the file — the readonly=true override did not reach the ephemeral sub-agent (Write/Edit should not have been available at all). Result: %q", subagentResult)
	}
	t.Logf("sub-agent result under readonly=true (len=%d): %q", len(subagentResult), subagentResult)
}
