package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/types"
)

// TestRegistryRun_RawWrapperGivesActionableError is the regression test for
// the misdiagnosed error a real user hit: when a provider streams malformed
// tool-call JSON (e.g. minimax-m3 concatenating fragments like
// {"command":"a"}{"command":"b"}{"command":"c"}), it gets wrapped as
// {"_raw_": "<the invalid JSON>"} before reaching the tool. That wrapper
// unmarshals successfully into ANY tool's typed input struct (unknown JSON
// fields are silently ignored by encoding/json), so every declared field
// reads as zero-value and the tool used to fail with "missing required
// field: command" — a technically-true but misleading diagnosis, since the
// real problem was never a missing field. Registry.Run must now intercept
// the wrapper before ever calling the tool's Execute, with a message that
// actually describes what happened.
func TestRegistryRun_RawWrapperGivesActionableError(t *testing.T) {
	reg := NewRegistry()
	called := false
	reg.Register(Tool{
		Def: types.ToolDef{Name: "Bash"},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			called = true // must never be reached for a wrapped input
			return "should not run", nil
		},
	})

	raw := `{"command":"a"}{"command":"b"}{"command":"c"}`
	wrapped, _ := json.Marshal(map[string]string{types.RawWrapperKey: raw})

	out, images, err := reg.Run(context.Background(), "Bash", wrapped)

	if called {
		t.Fatal("the tool's Execute must not run when the input is a raw wrapper")
	}
	if err == nil {
		t.Fatal("expected an error for a raw-wrapped input")
	}
	if strings.Contains(err.Error(), "missing required field") {
		t.Errorf("error still looks like the OLD misdiagnosis: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid JSON in tool-call arguments") {
		t.Errorf("error should clearly name the real problem, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "single, well-formed JSON object") {
		t.Errorf("error should name the specific fix (one object, not concatenated fragments), got: %q", err.Error())
	}
	if out != err.Error() {
		t.Errorf("output = %q, want it to match the error message", out)
	}
	if images != nil {
		t.Errorf("images = %v, want nil", images)
	}
}

// A normal, well-formed tool call must be completely unaffected — the check
// must not misfire on real input.
func TestRegistryRun_NormalInputUnaffected(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{
		Def: types.ToolDef{Name: "Bash"},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			return "ran fine", nil
		},
	})

	out, _, err := reg.Run(context.Background(), "Bash", json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "ran fine" {
		t.Errorf("out = %q", out)
	}
}
