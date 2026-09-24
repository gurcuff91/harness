package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// turn builds a realistic turn: a user prompt, an assistant message with tool
// calls, and the user-role message carrying those tool results back — the
// exact shape pruneOldestTurns must keep together or split cleanly.
func turn(prompt string, toolID string) []types.Message {
	return []types.Message{
		{Role: types.RoleUser, Parts: []types.ContentPart{{Text: prompt}}},
		{Role: types.RoleAssistant, Parts: []types.ContentPart{
			{ToolCall: &types.ToolCall{ID: toolID, Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}},
		}},
		{Role: types.RoleUser, Parts: []types.ContentPart{
			{ToolResult: &types.ToolResult{ID: toolID, Output: "some output"}},
		}},
		{Role: types.RoleAssistant, Parts: []types.ContentPart{{Text: "done with " + prompt}}},
	}
}

// TestTurnStartsIdentifiesRealUserPrompts verifies a turn boundary is a real
// user prompt, NOT a user-role message that only carries tool results (which
// continues the assistant's turn).
func TestTurnStartsIdentifiesRealUserPrompts(t *testing.T) {
	var msgs []types.Message
	msgs = append(msgs, turn("first", "t1")...)
	msgs = append(msgs, turn("second", "t2")...)

	starts := turnStarts(msgs)
	if len(starts) != 2 {
		t.Fatalf("expected 2 turn starts, got %d (%v)", len(starts), starts)
	}
	if starts[0] != 0 || starts[1] != 4 {
		t.Errorf("turn starts = %v, want [0 4] (each turn is 4 messages)", starts)
	}
	// The tool-result-only user messages (index 2 and 6) must NOT be starts.
	for _, s := range starts {
		if isToolResultOnly(msgs[s]) {
			t.Errorf("index %d was picked as a turn start but is a tool-result-only message", s)
		}
	}
}

// TestPruneOldestTurnsNeverSplitsAToolCallFromItsResult is the core safety
// guarantee: pruning drops WHOLE turns, so a surviving history can never
// contain a tool_call whose tool_result was pruned (or vice versa) — a shape
// every provider rejects.
func TestPruneOldestTurnsNeverSplitsAToolCallFromItsResult(t *testing.T) {
	var msgs []types.Message
	for _, name := range []string{"one", "two", "three", "four"} {
		msgs = append(msgs, turn(name, "tool-"+name)...)
	}

	// A budget that forces dropping some but not all turns.
	budget := approxSize(msgs) / 2
	kept, dropped := pruneOldestTurns(msgs, budget)

	if dropped == 0 {
		t.Fatal("expected some turns to be dropped at half budget")
	}

	// Every tool_call ID in kept must have its matching tool_result ID, and
	// every tool_result ID must have its matching tool_call — no orphans.
	calls := map[string]bool{}
	results := map[string]bool{}
	for _, m := range kept {
		for _, p := range m.Parts {
			if p.ToolCall != nil {
				calls[p.ToolCall.ID] = true
			}
			if p.ToolResult != nil {
				results[p.ToolResult.ID] = true
			}
		}
	}
	for id := range calls {
		if !results[id] {
			t.Errorf("tool_call %q survived without its tool_result — pruning split a turn", id)
		}
	}
	for id := range results {
		if !calls[id] {
			t.Errorf("tool_result %q survived without its tool_call — pruning split a turn", id)
		}
	}
}

// TestPruneOldestTurnsDropsOldestFirst verifies eviction order: the earliest
// turns go first, the most recent survive.
func TestPruneOldestTurnsDropsOldestFirst(t *testing.T) {
	var msgs []types.Message
	for _, name := range []string{"oldest", "middle", "newest"} {
		msgs = append(msgs, turn(name, "t-"+name)...)
	}

	// Budget fits only the last turn.
	budget := approxSize(turn("newest", "t-newest")[:]) + 10
	kept, dropped := pruneOldestTurns(msgs, budget)

	if dropped != 2 {
		t.Fatalf("expected 2 oldest turns dropped, got %d", dropped)
	}
	// "oldest" and "middle" must be gone; "newest" must remain.
	joined := flattenText(kept)
	if strings.Contains(joined, "oldest") || strings.Contains(joined, "middle") {
		t.Errorf("an older turn survived while a newer one was expected to be the only keeper: %q", joined)
	}
	if !strings.Contains(joined, "newest") {
		t.Errorf("the most recent turn was dropped: %q", joined)
	}
}

// TestPruneOldestTurnsNeverDropsTheLastTurn verifies the indivisible-minimum
// guarantee: even a budget of 0 keeps the final turn whole (the extreme
// single-oversized-turn case is deliberately left to fail rather than split a
// turn — see the design decision).
func TestPruneOldestTurnsNeverDropsTheLastTurn(t *testing.T) {
	var msgs []types.Message
	msgs = append(msgs, turn("a", "ta")...)
	msgs = append(msgs, turn("b", "tb")...)

	kept, dropped := pruneOldestTurns(msgs, 0)
	if dropped != 1 {
		t.Fatalf("expected exactly 1 turn dropped (keep the last), got %d", dropped)
	}
	if len(kept) != 4 {
		t.Errorf("the last turn (4 messages) must survive intact, got %d messages", len(kept))
	}
	if !strings.Contains(flattenText(kept), "b") {
		t.Error("the last turn was dropped — the indivisible minimum was violated")
	}
}

// TestPruneOldestTurnsNoOpWhenItFits verifies a history already under budget
// is returned untouched.
func TestPruneOldestTurnsNoOpWhenItFits(t *testing.T) {
	msgs := turn("only", "t1")
	kept, dropped := pruneOldestTurns(msgs, approxSize(msgs)+1000)
	if dropped != 0 || len(kept) != len(msgs) {
		t.Errorf("expected a no-op when the history fits, got dropped=%d kept=%d", dropped, len(kept))
	}
}

// TestIsContextOverflowError distinguishes deterministic size errors (never
// retry) from transient ones (retry).
func TestIsContextOverflowError(t *testing.T) {
	overflow := []string{
		"anthropic API error: prompt is too long: 1012151 tokens > 1000000 maximum",
		"This model's maximum context length is 128000 tokens",
		"context_length_exceeded",
		"the context window was exceeded",
	}
	for _, m := range overflow {
		if !isContextOverflowError(errors.New(m)) {
			t.Errorf("expected overflow=true for %q", m)
		}
	}
	transient := []string{
		"429 Too Many Requests",
		"connection reset by peer",
		"oauth token refresh failed",
		"",
	}
	for _, m := range transient {
		if isContextOverflowError(errors.New(m)) {
			t.Errorf("expected overflow=false (retryable) for %q", m)
		}
	}
	if isContextOverflowError(nil) {
		t.Error("nil error must not be treated as overflow")
	}
}

// TestCompactionCharBudgetUsesFixedConservativeRatio verifies that the
// emergency prune always uses 2.0 chars/token, regardless of stale token
// measurements or the current history size. A token count from another
// request/model/image mix is not a safe calibration source.
func TestCompactionCharBudgetUsesFixedConservativeRatio(t *testing.T) {
	// 0.95 * 1M tokens * 2.0 chars/token = 1,900,000 characters.
	s := &Session{contextWindow: 1_000_000, lastInputTokens: 995_087}
	if got := s.compactionCharBudget(); got != 1_900_000 {
		t.Errorf("fixed-ratio budget = %d, want 1,900,000", got)
	}
	// The same fixed budget applies when there is no prior measurement.
	s.lastInputTokens = 0
	if got := s.compactionCharBudget(); got != 1_900_000 {
		t.Errorf("unmeasured budget = %d, want 1,900,000", got)
	}
}

// TestCompactionCharBudgetUnknownWindowUsesConservativeDefault verifies that
// an unknown context window still produces a positive fixed-ratio budget.
func TestCompactionCharBudgetUnknownWindowUsesConservativeDefault(t *testing.T) {
	s := &Session{contextWindow: 0, lastInputTokens: 0}
	// 0.95 * 128,000 * 2.0 = 243,200 characters.
	if got := s.compactionCharBudget(); got != 243_200 {
		t.Errorf("unknown-window budget = %d, want 243,200", got)
	}
}

func flattenText(msgs []types.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, p := range m.Parts {
			b.WriteString(p.Text)
			if p.ToolResult != nil {
				b.WriteString(p.ToolResult.Output)
			}
		}
	}
	return b.String()
}

// TestCompactionBudgetReproducesFieldStudy verifies the real kaiban-api-v2
// overflow shape against the fixed conservative ratio: a dense session near
// the window must prune oldest turns rather than sending the complete history.
// The fixed 2.0 ratio intentionally keeps less context than the old dynamic
// calibration, trading capacity for a safer emergency request.
func TestCompactionBudgetReproducesFieldStudy(t *testing.T) {
	const window = 1_000_000
	const realTokens = 995_087 // from the stuck session's meta context_usage

	// Build a history whose approxSize matches the real one (~2.22M chars)
	// across many turns. The old dynamic ratio would have kept most of it;
	// fixed 2.0 must prune enough oldest turns to fit its safer budget.
	const numTurns = 88
	const charsPerTurn = 25_300
	var msgs []types.Message
	for i := 0; i < numTurns; i++ {
		big := strings.Repeat("x", charsPerTurn-200) // leave room for the prompt text
		toolID := fmt.Sprintf("t-%d", i)
		msgs = append(msgs,
			types.Message{Role: types.RoleUser, Parts: []types.ContentPart{{Text: "prompt turn"}}},
			types.Message{Role: types.RoleAssistant, Parts: []types.ContentPart{
				{ToolCall: &types.ToolCall{ID: toolID, Name: "Bash", Input: json.RawMessage(`{}`)}},
			}},
			types.Message{Role: types.RoleUser, Parts: []types.ContentPart{
				{ToolResult: &types.ToolResult{ID: toolID, Output: big}},
			}},
			types.Message{Role: types.RoleAssistant, Parts: []types.ContentPart{{Text: "ok"}}},
		)
	}

	chars := approxSize(msgs)
	s := &Session{contextWindow: window, lastInputTokens: realTokens}
	budget := s.compactionCharBudget()

	ratio := float64(chars) / float64(realTokens)
	t.Logf("history: %d chars, ratio %.3f chars/token, budget %d chars", chars, ratio, budget)
	if budget != 1_900_000 {
		t.Fatalf("fixed budget = %d, want 1,900,000", budget)
	}

	kept, dropped := pruneOldestTurns(msgs, budget)
	if dropped == 0 {
		t.Fatal("pruned nothing — fixed 2.0 must catch this dense history")
	}
	if approxSize(kept) > budget {
		t.Errorf("kept history is %d chars, over budget %d", approxSize(kept), budget)
	}
	// The newest turn survives, while the oldest turns are the ones evicted.
	if !strings.Contains(flattenText(kept), "prompt turn") {
		t.Error("kept history does not contain the newest turn")
	}
	t.Logf("dropped %d of %d turns, kept %.1f%% of context", dropped, numTurns, float64(approxSize(kept))/float64(chars)*100)
}
