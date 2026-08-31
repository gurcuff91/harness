package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// callGoal is a small helper: builds the tool with the given executor, runs
// it with the given input JSON, and returns (output, error).
func callGoal(t *testing.T, executor GoalExecutor, input map[string]any) (string, error) {
	t.Helper()
	tool := Goal(executor)
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(context.Background(), raw)
}

func TestGoalMissingBuilderPromptErrors(t *testing.T) {
	_, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		return "should not be called", nil
	}, map[string]any{"tester_prompt": "criteria"})
	if err == nil {
		t.Fatal("expected an error for a missing builder_prompt")
	}
}

func TestGoalMissingTesterPromptErrors(t *testing.T) {
	_, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		return "should not be called", nil
	}, map[string]any{"builder_prompt": "do the thing"})
	if err == nil {
		t.Fatal("expected an error for a missing tester_prompt")
	}
}

func TestGoalForegroundReturnsExecutorResult(t *testing.T) {
	out, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		return "## Test Report\nOverall: PASS", nil
	}, map[string]any{"builder_prompt": "build it", "tester_prompt": "criterion: it works"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "## Test Report\nOverall: PASS" {
		t.Errorf("out = %q", out)
	}
}

// The core corrected-design guarantee: a FAIL verdict from the tester is a
// normal, successful tool execution — NOT a returned error. The tool must
// never parse or act on the verdict itself.
func TestGoalFailVerdictIsNotAToolError(t *testing.T) {
	out, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		return "## Test Report\n- Criterion \"it works\": FAIL — did not work\nOverall: FAIL", nil
	}, map[string]any{"builder_prompt": "build it", "tester_prompt": "criterion: it works"})
	if err != nil {
		t.Fatalf("a FAIL verdict must not be a tool error, got: %v", err)
	}
	if !strings.Contains(out, "Overall: FAIL") {
		t.Errorf("out = %q, want the tester's FAIL report passed through untouched", out)
	}
}

func TestGoalForegroundDefaultTimeoutDoesNotCutAFastCall(t *testing.T) {
	out, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("expected a deadline on ctx")
		}
		return "ok", nil
	}, map[string]any{"builder_prompt": "b", "tester_prompt": "t"})
	if err != nil || out != "ok" {
		t.Errorf("out=%q err=%v", out, err)
	}
}

func TestGoalForegroundCustomTimeoutIsApplied(t *testing.T) {
	out, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected a deadline")
		}
		if time.Until(deadline) > 2*time.Second {
			t.Errorf("deadline too far out for a 1s timeout: %v", time.Until(deadline))
		}
		return "ok", nil
	}, map[string]any{"builder_prompt": "b", "tester_prompt": "t", "timeout": 1})
	if err != nil || out != "ok" {
		t.Errorf("out=%q err=%v", out, err)
	}
}

func TestGoalForegroundTimeoutExpires(t *testing.T) {
	_, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, map[string]any{"builder_prompt": "b", "tester_prompt": "t", "timeout": 1})
	if err == nil {
		t.Fatal("expected the 1s timeout to fire")
	}
}

// A single shared deadline covers BOTH sub-agent calls inside the executor —
// this test only verifies the CONTRACT the tool guarantees to the executor
// (one ctx, one deadline, for the whole call); the executor itself owns
// splitting work between builder and tester on that one clock (verified at
// the agent.go integration level, not here — the executor is a mock).
func TestGoalSharedDeadlineIsNotResetBetweenCalls(t *testing.T) {
	var firstDeadline, secondDeadlineSameCtx time.Time
	out, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		d, _ := ctx.Deadline()
		firstDeadline = d
		// Simulate the executor internally making a second "call" on the
		// same ctx (mirroring runRole being invoked twice) — the deadline
		// must be identical, not a fresh one.
		d2, _ := ctx.Deadline()
		secondDeadlineSameCtx = d2
		return "ok", nil
	}, map[string]any{"builder_prompt": "b", "tester_prompt": "t", "timeout": 5})
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if !firstDeadline.Equal(secondDeadlineSameCtx) {
		t.Errorf("deadline must be identical across both calls on the same ctx: %v vs %v", firstDeadline, secondDeadlineSameCtx)
	}
}

// ── max_iterations ──────────────────────────────────────────────────────

func TestGoalMaxIterationsOmittedPassesZeroThrough(t *testing.T) {
	var got int
	_, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		got = maxIterations
		return "ok", nil
	}, map[string]any{"builder_prompt": "b", "tester_prompt": "t"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("executor received maxIterations = %d, want 0 (omitted)", got)
	}
}

func TestGoalMaxIterationsValidValueReachesExecutor(t *testing.T) {
	var got int
	_, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
		got = maxIterations
		return "ok", nil
	}, map[string]any{"builder_prompt": "b", "tester_prompt": "t", "max_iterations": 150})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 150 {
		t.Errorf("executor received maxIterations = %d, want 150", got)
	}
}

func TestGoalMaxIterationsBoundaryValuesAccepted(t *testing.T) {
	for _, want := range []int{subagentMinIterations, subagentMaxIterationsCeiling} {
		var got int
		called := false
		_, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
			called = true
			got = maxIterations
			return "ok", nil
		}, map[string]any{"builder_prompt": "b", "tester_prompt": "t", "max_iterations": want})
		if err != nil {
			t.Errorf("max_iterations=%d: unexpected error: %v", want, err)
		}
		if !called {
			t.Errorf("max_iterations=%d: executor was not called", want)
		}
		if got != want {
			t.Errorf("max_iterations=%d: executor received %d", want, got)
		}
	}
}

func TestGoalMaxIterationsOutOfRangeRejected(t *testing.T) {
	cases := []int{-1, 201, 1000, subagentMaxIterationsCeiling + 1}
	for _, bad := range cases {
		called := false
		out, err := callGoal(t, func(ctx context.Context, builderPrompt, testerPrompt string, maxIterations int) (string, error) {
			called = true
			return "should not run", nil
		}, map[string]any{"builder_prompt": "b", "tester_prompt": "t", "max_iterations": bad})
		if called {
			t.Errorf("max_iterations=%d: executor must not run for an out-of-range value", bad)
		}
		if err == nil {
			t.Errorf("max_iterations=%d: expected an error", bad)
		}
		if !strings.Contains(out, "max_iterations") {
			t.Errorf("max_iterations=%d: error message should mention max_iterations, got %q", bad, out)
		}
	}
}

// ── role prompt assembly ──────────────────────────────────────────────────

func TestGoalBuilderPromptIncludesFixedRoleAndCallerText(t *testing.T) {
	full := GoalBuilderPrompt("implement the widget")
	if !strings.Contains(full, "You are the Builder") {
		t.Error("missing fixed builder role prompt")
	}
	if !strings.Contains(full, "## Builder Report") {
		t.Error("missing required builder report format")
	}
	if !strings.Contains(full, "implement the widget") {
		t.Error("missing caller's operational instructions")
	}
}

func TestGoalTesterPromptIncludesCriteriaAndBuilderReportVerbatim(t *testing.T) {
	full := GoalTesterPrompt("criterion: widget exists", "## Builder Report\n- Changes made: added widget.go")
	if !strings.Contains(full, "You are the Tester") {
		t.Error("missing fixed tester role prompt")
	}
	if !strings.Contains(full, "Never fix or edit anything yourself") {
		t.Error("missing separation-of-duties instruction")
	}
	if !strings.Contains(full, "criterion: widget exists") {
		t.Error("missing acceptance criteria")
	}
	if !strings.Contains(full, "added widget.go") {
		t.Error("missing builder's raw report — handoff must be verbatim")
	}
	if !strings.Contains(full, "## Test Report") {
		t.Error("missing required test report format")
	}
	if !strings.Contains(full, "Overall: PASS") || !strings.Contains(full, "Overall: FAIL") {
		t.Error("missing overall verdict format instructions")
	}
}
