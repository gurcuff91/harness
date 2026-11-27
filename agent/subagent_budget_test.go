package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// budgetProvider plays both sides of a Subagent call without a real model.
// The PARENT (whose request lists the Subagent tool) calls Subagent once with
// the requested max_iterations, then answers with what it got back. The
// SUB-AGENT (no Subagent tool) always asks for another Bash run, so it only
// stops when its iteration budget runs out; its progress summary (the request
// with no tools) reports how many tool passes it was allowed.
type budgetProvider struct {
	fakeThinkingProbeProvider
	maxIterations int

	mu        sync.Mutex
	subPasses int         // requests the sub-agent made WITH tools (real ReAct passes)
	reply     chan string // the <subagent … reply> prompt the parent receives
}

func hasTool(req *types.Request, name string) bool {
	for _, d := range req.Tools {
		if d.Name == name {
			return true
		}
	}
	return false
}

func lastText(req *types.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if t := req.Messages[i].TextContent(); t != "" {
			return t
		}
	}
	return ""
}

func (p *budgetProvider) CompleteStream(_ context.Context, req *types.Request, cb types.StreamCallback) (*types.Response, error) {
	text := func(s string) (*types.Response, error) {
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: s})
		cb(types.StreamEvent{Type: types.StreamDone})
		return &types.Response{Text: s, Message: types.NewAssistantToolCallMessage(s, "", "", nil)}, nil
	}
	call := func(name string, args json.RawMessage) (*types.Response, error) {
		cb(types.StreamEvent{Type: types.StreamToolStart, ToolID: "c1", ToolName: name})
		cb(types.StreamEvent{Type: types.StreamToolEnd, ToolID: "c1", ToolName: name, ToolArgs: args})
		cb(types.StreamEvent{Type: types.StreamDone})
		calls := []types.ToolCall{{ID: "c1", Name: name, Input: args}}
		return &types.Response{Message: types.NewAssistantToolCallMessage("", "", "", calls), ToolCalls: calls}, nil
	}

	if hasTool(req, "Subagent") { // the parent
		if lt := lastText(req); strings.HasPrefix(lt, "<subagent") {
			p.reply <- lt
			return text("GOT THE REPLY")
		}
		for _, m := range req.Messages {
			for _, part := range m.Parts {
				if part.ToolResult != nil {
					return text("PARENT GOT: " + part.ToolResult.Output)
				}
			}
		}
		args, _ := json.Marshal(map[string]any{"prompt": "keep running bash", "max_iterations": p.maxIterations})
		return call("Subagent", args)
	}
	if len(req.Tools) == 0 { // the sub-agent's progress summary: budget exhausted
		p.mu.Lock()
		n := p.subPasses
		p.mu.Unlock()
		return text("BUDGET EXHAUSTED after " + itoa(n) + " passes")
	}
	p.mu.Lock()
	p.subPasses++
	p.mu.Unlock()
	return call("Bash", json.RawMessage(`{"command":"true"}`))
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The Subagent tool's max_iterations reaches the ephemeral sub-agent: the
// sub-agent gets exactly that many ReAct passes (one is reserved for the
// summary, as for any session) and then reports it ran out. Deterministic:
// no real model, so it can gate a release.
func TestSubagentMaxIterationsOverrideReachesEphemeralSubAgent(t *testing.T) {
	for _, budget := range []int{1, 3} {
		t.Run(itoa(budget), func(t *testing.T) {
			withCleanProviderRegistry(t)
			name := "budget-" + strings.NewReplacer("/", "-").Replace(t.Name())
			var thinking string
			p := &budgetProvider{fakeThinkingProbeProvider: fakeThinkingProbeProvider{name: name, lastThinking: &thinking}, maxIterations: budget, reply: make(chan string, 1)}
			providers.AddForTest(p)
			a := New(AgentOptions{Store: store.NewInMemoryStore()})
			defer a.Close()
			sess, err := a.NewSession(t.TempDir(), name+"/fake-model")
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := sess.PromptAndWait(ctx, "delegate")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Sub-agent ") || !strings.Contains(out, "is working on it") {
				t.Fatalf("the Subagent tool did not return at once with its name and that it is working: %q", out)
			}
			var reply string
			select {
			case reply = <-p.reply:
			case <-ctx.Done():
				t.Fatal("the sub-agent's reply never arrived")
			}
			if !strings.Contains(reply, "BUDGET EXHAUSTED") || !strings.Contains(reply, " reply error>") {
				t.Fatalf("the sub-agent did not run out of budget (reply %q)", reply)
			}
			p.mu.Lock()
			passes := p.subPasses
			p.mu.Unlock()
			want := budget - 1 // the last iteration is reserved for the summary
			if passes != want {
				t.Errorf("max_iterations=%d gave the sub-agent %d tool passes, want %d (the override did not reach it)", budget, passes, want)
			}
		})
	}
}
