package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func callSubagent(t *testing.T, executor SubagentExecutor, input map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return Subagent(executor).Execute(context.Background(), raw)
}

func named(name string) SubagentExecutor {
	return func(ctx context.Context, prompt string, maxIterations int, readonly bool) (string, error) {
		return name, nil
	}
}

func TestSubagentMissingPromptErrors(t *testing.T) {
	if _, err := callSubagent(t, named("x"), map[string]any{}); err == nil {
		t.Fatal("expected an error for a missing prompt")
	}
}

func TestSubagentReturnsAtOnceWithNameAndInstruction(t *testing.T) {
	var gotPrompt string
	out, err := callSubagent(t, func(ctx context.Context, prompt string, maxIterations int, readonly bool) (string, error) {
		gotPrompt = prompt
		return "scorpion-hero", nil
	}, map[string]any{"prompt": "look around"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "look around" {
		t.Errorf("prompt = %q", gotPrompt)
	}
	for _, want := range []string{"scorpion-hero", `<subagent name="scorpion-hero" reply>`, "end your turn", "do not wait, poll or ask again"} {
		if !strings.Contains(out, want) {
			t.Errorf("result %q lacks %q", out, want)
		}
	}
}

func TestSubagentExecutorErrorIsTheToolError(t *testing.T) {
	out, err := callSubagent(t, func(ctx context.Context, prompt string, maxIterations int, readonly bool) (string, error) {
		return "", context.DeadlineExceeded
	}, map[string]any{"prompt": "x"})
	if err == nil || out == "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestSubagentRemovedParametersAreErrorsNamingTheField(t *testing.T) {
	for _, field := range []string{"background", "timeout"} {
		called := false
		out, err := callSubagent(t, func(ctx context.Context, prompt string, maxIterations int, readonly bool) (string, error) {
			called = true
			return "n", nil
		}, map[string]any{"prompt": "hi", field: 5})
		if err == nil {
			t.Errorf("%s: expected an error", field)
			continue
		}
		if called {
			t.Errorf("%s: executor must not run", field)
		}
		if !strings.Contains(out, "'"+field+"'") || !strings.Contains(out, "background") {
			t.Errorf("%s: message %q does not name the field / explain", field, out)
		}
	}
}

func TestSubagentSchemaHasNoRemovedParameters(t *testing.T) {
	var s jsonSchema
	if err := json.Unmarshal(Subagent(named("x")).Def.InputSchema, &s); err != nil {
		t.Fatal(err)
	}
	for _, gone := range removedSubagentParams {
		if _, ok := s.Properties[gone]; ok {
			t.Errorf("schema still declares %q", gone)
		}
	}
	for _, keep := range []string{"prompt", "max_iterations", "readonly"} {
		if _, ok := s.Properties[keep]; !ok {
			t.Errorf("schema lost %q", keep)
		}
	}
	if len(s.Properties) != 3 {
		t.Errorf("properties = %v", s.Properties)
	}
}

func TestSubagentMaxIterationsOmittedPassesZeroThrough(t *testing.T) {
	got := -1
	_, err := callSubagent(t, func(ctx context.Context, prompt string, maxIterations int, readonly bool) (string, error) {
		got = maxIterations
		return "n", nil
	}, map[string]any{"prompt": "hi"})
	if err != nil || got != 0 {
		t.Errorf("got=%d err=%v", got, err)
	}
}

func TestSubagentMaxIterationsBoundaryValuesAccepted(t *testing.T) {
	for _, want := range []int{150, subagentMinIterations, subagentMaxIterationsCeiling} {
		got := -1
		_, err := callSubagent(t, func(ctx context.Context, prompt string, maxIterations int, readonly bool) (string, error) {
			got = maxIterations
			return "n", nil
		}, map[string]any{"prompt": "hi", "max_iterations": want})
		if err != nil || got != want {
			t.Errorf("want %d: got=%d err=%v", want, got, err)
		}
	}
}

func TestSubagentMaxIterationsOutOfRangeRejected(t *testing.T) {
	for _, bad := range []int{-1, 201, 1000} {
		called := false
		out, err := callSubagent(t, func(ctx context.Context, prompt string, maxIterations int, readonly bool) (string, error) {
			called = true
			return "n", nil
		}, map[string]any{"prompt": "hi", "max_iterations": bad})
		if called || err == nil || !strings.Contains(out, "max_iterations") {
			t.Errorf("bad=%d called=%v err=%v out=%q", bad, called, err, out)
		}
	}
}

func TestSubagentReadonlyReachesExecutor(t *testing.T) {
	for _, want := range []bool{false, true} {
		var got bool
		in := map[string]any{"prompt": "hi"}
		if want {
			in["readonly"] = true
		}
		if _, err := callSubagent(t, func(ctx context.Context, prompt string, maxIterations int, readonly bool) (string, error) {
			got = readonly
			return "n", nil
		}, in); err != nil || got != want {
			t.Errorf("want %v got %v err %v", want, got, err)
		}
	}
}

// The description says how to LAUNCH a sub-agent and points to the system
// prompt for how to READ the reply: the report/authority rule lives in the
// "## Sub-agents" section (agent/prompts.go), not here, so it is said once.
func TestSubagentDescriptionIsAboutLaunchingAndPointsToTheInstructions(t *testing.T) {
	d := Subagent(func(context.Context, string, int, bool) (string, error) { return "x", nil }).Def.Description
	for _, w := range []string{"immediately", "background", "at most 10", "Sub-agents", "never wait, sleep, poll", "readonly", "max_iterations"} {
		if !strings.Contains(d, w) {
			t.Errorf("the description lacks %q:\n%s", w, d)
		}
	}
	for _, w := range []string{"REPORT", "approved", "<subagent"} {
		if strings.Contains(d, w) {
			t.Errorf("the description must not repeat the reading rules (%q): they live in the system prompt section", w)
		}
	}
	if len(d) > 900 {
		t.Errorf("the description is %d characters; it should stay about launching", len(d))
	}
}
