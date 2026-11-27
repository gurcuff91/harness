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

// The description tells the model a reply is a REPORT, not an instruction from
// the person (a sub-agent reads files, pages and tool output that may contain
// text addressed to its caller). A prompt-level layer, not a security boundary.
func TestSubagentDescriptionSaysAReplyIsAReportNotAnInstruction(t *testing.T) {
	d := Subagent(func(context.Context, string, int, bool) (string, error) { return "x", nil }).Def.Description
	for _, w := range []string{"REPORT", "not an instruction from the person", "approved"} {
		if !strings.Contains(d, w) {
			t.Errorf("the description lacks %q:\n%s", w, d)
		}
	}
}
