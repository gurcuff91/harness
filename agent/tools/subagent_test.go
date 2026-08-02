package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// callSubagent is a small helper: builds the tool with the given executor,
// runs it with the given input JSON, and returns (output, error).
func callSubagent(t *testing.T, executor SubagentExecutor, input map[string]any) (string, error) {
	t.Helper()
	return callSubagentWithContext(t, context.Background(), executor, input)
}

func callSubagentWithContext(t *testing.T, ctx context.Context, executor SubagentExecutor, input map[string]any) (string, error) {
	t.Helper()
	tool := Subagent(executor)
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(ctx, raw)
}

func TestSubagentMissingPromptErrors(t *testing.T) {
	_, err := callSubagent(t, func(ctx context.Context, prompt string) (string, error) {
		return "should not be called", nil
	}, map[string]any{})
	if err == nil {
		t.Fatal("expected an error for a missing prompt")
	}
}

func TestSubagentForegroundReturnsExecutorResult(t *testing.T) {
	out, err := callSubagent(t, func(ctx context.Context, prompt string) (string, error) {
		return "the answer for: " + prompt, nil
	}, map[string]any{"prompt": "what is 2+2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "the answer for: what is 2+2" {
		t.Errorf("out = %q", out)
	}
}

func TestSubagentForegroundDefaultTimeoutDoesNotCutAFastCall(t *testing.T) {
	// No "timeout" in input — must fall back to subagentTimeout (5 min), not
	// something so short a normal test call would spuriously fail.
	out, err := callSubagent(t, func(ctx context.Context, prompt string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("expected the foreground path to set a deadline on ctx")
		}
		return "ok", nil
	}, map[string]any{"prompt": "hi"})
	if err != nil || out != "ok" {
		t.Errorf("out=%q err=%v", out, err)
	}
}

func TestSubagentForegroundCustomTimeoutIsApplied(t *testing.T) {
	out, err := callSubagent(t, func(ctx context.Context, prompt string) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected a deadline")
		}
		if time.Until(deadline) > 2*time.Second {
			t.Errorf("deadline too far out for a 1s timeout: %v", time.Until(deadline))
		}
		return "ok", nil
	}, map[string]any{"prompt": "hi", "timeout": 1})
	if err != nil || out != "ok" {
		t.Errorf("out=%q err=%v", out, err)
	}
}

func TestSubagentForegroundTimeoutExpires(t *testing.T) {
	_, err := callSubagent(t, func(ctx context.Context, prompt string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, map[string]any{"prompt": "hi", "timeout": 1})
	if err == nil {
		t.Fatal("expected the 1s timeout to fire")
	}
}

func TestSubagentBackgroundReturnsImmediatelyWithFilePath(t *testing.T) {
	release := make(chan struct{})
	out, err := callSubagent(t, func(ctx context.Context, prompt string) (string, error) {
		<-release // would hang forever if this ever ran synchronously
		return "background result", nil
	}, map[string]any{"prompt": "hi", "background": true})
	close(release)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "background") {
		t.Errorf("out = %q, want a background-delegation message", out)
	}
}

func TestSubagentBackgroundWritesResultToFile(t *testing.T) {
	out, err := callSubagent(t, func(ctx context.Context, prompt string) (string, error) {
		return "the real answer", nil
	}, map[string]any{"prompt": "hi", "background": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := extractFilePath(t, out)
	var content []byte
	for i := 0; i < 50; i++ { // poll — the goroutine writes asynchronously
		content, err = os.ReadFile(path)
		if err == nil && len(content) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("reading result file: %v", err)
	}
	if string(content) != "the real answer" {
		t.Errorf("file content = %q", content)
	}
	os.Remove(path)
}

func TestSubagentBackgroundIgnoresTimeoutField(t *testing.T) {
	// background:true + a tiny timeout must NOT cut the executor off — timeout
	// is only meaningful on the foreground path (same rule as ColleagueAsk).
	release := make(chan struct{})
	out, err := callSubagent(t, func(ctx context.Context, prompt string) (string, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Error("background executor's ctx must not carry an artificial deadline")
		}
		<-release
		return "done", nil
	}, map[string]any{"prompt": "hi", "background": true, "timeout": 1})
	close(release)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	path := extractFilePath(t, out)
	os.Remove(path)
}

// TestSubagentBackgroundSurvivesCallerContextCancellation is the regression
// test for the real production bug: agent/session.go's drainFollowUps
// unconditionally cancels each turn's context the instant that turn's own
// promptSync returns — completely unaware of (and not waiting for) any
// goroutine a tool call kicked off in the background. Since Execute's ctx
// IS that per-turn context, a background Subagent call used to die with
// "context canceled" the moment its OWN launching turn ended — which is
// always, since that's the normal, fast, expected end of a turn — making
// the whole feature unusable: the goroutine writing the result file was
// killed before it could ever get there. Simulates exactly that: the ctx
// passed to Execute is cancelled right after the tool returns (mirroring
// drainFollowUps' unconditional `cancel()`), and the background executor
// must still complete successfully.
func TestSubagentBackgroundSurvivesCallerContextCancellation(t *testing.T) {
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	executorSawCancellation := make(chan bool, 1)

	out, err := callSubagentWithContext(t, turnCtx, func(ctx context.Context, prompt string) (string, error) {
		// Give the test time to cancel turnCtx before this returns, then
		// report whether ITS ctx (which must NOT be turnCtx) got cancelled too.
		select {
		case <-ctx.Done():
			executorSawCancellation <- true
		case <-time.After(300 * time.Millisecond):
			executorSawCancellation <- false
		}
		return "sub-agent completed successfully", nil
	}, map[string]any{"prompt": "hi", "background": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	path := extractFilePath(t, out)
	defer os.Remove(path)

	// Mirror drainFollowUps: cancel the turn's context immediately after the
	// tool call that launched the background work returns — this is exactly
	// what happens in production the instant the launching turn ends.
	cancelTurn()

	if sawCancellation := <-executorSawCancellation; sawCancellation {
		t.Fatal("the background executor's ctx was cancelled when the CALLER's turn context was cancelled — background mode must be immune to this")
	}

	var content []byte
	for i := 0; i < 50; i++ {
		content, err = os.ReadFile(path)
		if err == nil && len(content) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("reading result file: %v", err)
	}
	if string(content) != "sub-agent completed successfully" {
		t.Errorf("file content = %q, want the real result — not a context-cancelled error", content)
	}
}

// extractFilePath pulls the temp file path out of the background message
// ("...Result will be written to: /tmp/harness-subagent-XXXX.txt\n...").
func extractFilePath(t *testing.T, msg string) string {
	t.Helper()
	const marker = "written to: "
	i := strings.Index(msg, marker)
	if i < 0 {
		t.Fatalf("could not find file path in message: %q", msg)
	}
	rest := msg[i+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.TrimSpace(rest)
}
