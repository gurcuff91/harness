package agent

import (
	"encoding/json"
	"errors"
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

// TestCompactionCharBudgetCalibratesToRealRatio verifies the budget targets
// autoCompactThreshold of the window in TOKENS, converted to chars using the
// session's REAL chars-per-token ratio (approxSize/lastInputTokens) rather
// than a fixed guess. This is the fix a real field study of kaiban-api-v2
// demanded: that session measured 2.237 chars/token, where a fixed-4 budget
// would have failed to prune anything and overflowed.
func TestCompactionCharBudgetCalibratesToRealRatio(t *testing.T) {
	// Dense session (code/JSON): 2.237 chars/token, like the real one.
	// lastInputTokens = 995,087; currentChars = 2,226,183.
	s := &Session{contextWindow: 1_000_000, lastInputTokens: 995_087}
	got := s.compactionCharBudget(2_226_183)
	// budget_tokens = 0.95 * 1M = 950,000 ; ratio = 2226183/995087 = 2.237
	// budget_chars ≈ 950,000 * 2.237 ≈ 2,125,315
	if got < 2_100_000 || got > 2_150_000 {
		t.Errorf("dense-session budget = %d, want ~2,125,315 (0.95*1M tokens * 2.237 chars/token)", got)
	}
	// Crucially: the budget must be BELOW the real history size, so pruning
	// actually happens (the fixed-4 bug produced 3.8M > 2.22M and pruned
	// nothing).
	if got >= 2_226_183 {
		t.Errorf("budget %d >= history 2,226,183 — would prune nothing and overflow, the exact field bug", got)
	}
}

// TestCompactionCharBudgetFallsBackWhenNoMeasurement verifies the ratio falls
// back to fallbackCharsPerToken (4) only when there's no real token count yet
// (lastInputTokens == 0, e.g. a resumed session before its first turn), and
// never returns 0 for an unknown window.
func TestCompactionCharBudgetFallsBackWhenNoMeasurement(t *testing.T) {
	// No measurement yet → fixed 4 chars/token. 0.95 * 1M * 4 = 3,800,000.
	s := &Session{contextWindow: 1_000_000, lastInputTokens: 0}
	if got := s.compactionCharBudget(0); got != 3_800_000 {
		t.Errorf("fallback budget = %d, want 3,800,000 (0.95*1M*4)", got)
	}
	// Unknown window falls back to a positive default, never 0 (which would
	// make pruneOldestTurns drop everything but the last turn always).
	s2 := &Session{contextWindow: 0, lastInputTokens: 0}
	if got := s2.compactionCharBudget(0); got <= 0 {
		t.Errorf("unknown-window budget = %d, want a positive default", got)
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

// TestCompactionBudgetReproducesFieldStudy reproduces the real kaiban-api-v2
// study end-to-end against the actual code: a dense session (ratio 2.237)
// stuck at 99.5% must, at the 0.95 threshold, prune only a handful of oldest
// turns while keeping ~94% of the context — NOT the ~50% the old fixed-budget
// design threw away, and crucially NOT zero (which the fixed-4 ratio produced,
// re-overflowing). This locks in the whole chain: budget calibration + turn
// pruning together.
func TestCompactionBudgetReproducesFieldStudy(t *testing.T) {
	const window = 1_000_000
	const realTokens = 995_087 // from the stuck session's meta context_usage

	// Build a history whose approxSize matches the real one (~2.22M chars)
	// across many turns, so the char/token ratio lands at the measured 2.237.
	// ~2.22M chars / 88 turns ≈ 25,300 chars per turn.
	const numTurns = 88
	const charsPerTurn = 25_300
	var msgs []types.Message
	for i := 0; i < numTurns; i++ {
		big := strings.Repeat("x", charsPerTurn-200) // leave room for the prompt text
		msgs = append(msgs,
			types.Message{Role: types.RoleUser, Parts: []types.ContentPart{{Text: "prompt turn"}}},
			types.Message{Role: types.RoleAssistant, Parts: []types.ContentPart{
				{ToolCall: &types.ToolCall{ID: "t", Name: "Bash", Input: json.RawMessage(`{}`)}},
			}},
			types.Message{Role: types.RoleUser, Parts: []types.ContentPart{
				{ToolResult: &types.ToolResult{ID: "t", Output: big}},
			}},
			types.Message{Role: types.RoleAssistant, Parts: []types.ContentPart{{Text: "ok"}}},
		)
	}

	chars := approxSize(msgs)
	s := &Session{contextWindow: window, lastInputTokens: realTokens}
	budget := s.compactionCharBudget(chars)

	ratio := float64(chars) / float64(realTokens)
	t.Logf("history: %d chars, ratio %.3f chars/token, budget %d chars", chars, ratio, budget)

	// The ratio must be in the dense-session range this test is built around.
	if ratio < 2.0 || ratio > 2.6 {
		t.Fatalf("test fixture ratio %.3f is off — expected a dense ~2.2 like the real session", ratio)
	}

	kept, dropped := pruneOldestTurns(msgs, budget)

	// Must prune SOMETHING (the fixed-4 bug pruned nothing → overflow)...
	if dropped == 0 {
		t.Fatal("pruned nothing — this is the fixed-ratio overflow bug the calibration fixes")
	}
	// ...but only a handful, keeping the large majority of context.
	keptFraction := float64(approxSize(kept)) / float64(chars)
	if keptFraction < 0.90 {
		t.Errorf("kept only %.1f%% of context — too aggressive; the 0.95 threshold should keep ~94%%", keptFraction*100)
	}
	if dropped > 12 {
		t.Errorf("dropped %d turns — more than the study's ~6; budget may be miscalibrated", dropped)
	}
	t.Logf("dropped %d of %d turns, kept %.1f%% of context", dropped, numTurns, keptFraction*100)
}
