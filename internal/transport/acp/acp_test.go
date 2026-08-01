package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
)

// newTestAgent builds a minimal *agent.Agent for the dispatch loop tests: no
// MCP/memory/scheduler, an in-memory session store (so nothing touches
// disk), matching how internal/cli's newOneShotAgent isolates one-shot
// throwaway work.
func newTestAgent(t *testing.T) *agent.Agent {
	t.Helper()
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore()})
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// runLoop starts Run in a goroutine wired to an in-memory pipe, returning a
// requester the test drives by writing JSON-RPC lines and reading responses
// back line by line — a faithful stand-in for what an ACP client does over
// real stdio.
type harness struct {
	t         *testing.T
	toAgent   *io.PipeWriter
	fromAgent *bufio.Reader
	done      chan error
}

func startHarness(t *testing.T, a *agent.Agent) *harness {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- Run(ctx, a, stdinR, stdoutW) }()

	return &harness{t: t, toAgent: stdinW, fromAgent: bufio.NewReader(stdoutR), done: done}
}

func (h *harness) send(id int, method string, params any) {
	h.t.Helper()
	p, _ := json.Marshal(params)
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p)}
	b, _ := json.Marshal(msg)
	if _, err := h.toAgent.Write(append(b, '\n')); err != nil {
		h.t.Fatalf("write request: %v", err)
	}
}

func (h *harness) sendNotification(method string, params any) {
	h.t.Helper()
	p, _ := json.Marshal(params)
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": json.RawMessage(p)}
	b, _ := json.Marshal(msg)
	if _, err := h.toAgent.Write(append(b, '\n')); err != nil {
		h.t.Fatalf("write notification: %v", err)
	}
}

// readMessage reads one line from the agent's stdout and decodes it into a
// generic map — used both for responses (has "id") and notifications
// (no "id", has "method").
func (h *harness) readMessage() map[string]any {
	h.t.Helper()
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := h.fromAgent.ReadBytes('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			h.t.Fatalf("read message: %v", r.err)
		}
		var m map[string]any
		if err := json.Unmarshal(r.line, &m); err != nil {
			h.t.Fatalf("decode message %q: %v", r.line, err)
		}
		return m
	case <-time.After(5 * time.Second):
		h.t.Fatal("timed out waiting for a message from the agent")
		return nil
	}
}

// readResponseFor reads messages until it finds the response with the given
// id, skipping over any notifications (session/update) that arrive first —
// mirroring how a real ACP client demultiplexes the stream.
func (h *harness) readResponseFor(id int) map[string]any {
	h.t.Helper()
	for i := 0; i < 50; i++ { // generous bound: enough for any realistic notification burst
		m := h.readMessage()
		if gotID, ok := m["id"]; ok && int(gotID.(float64)) == id {
			return m
		}
	}
	h.t.Fatalf("never saw a response for request id %d", id)
	return nil
}

func TestInitializeHandshake(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "initialize", initializeParams{ProtocolVersion: 1})

	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	if int(result["protocolVersion"].(float64)) != 1 {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	caps := result["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != true {
		t.Errorf("loadSession = %v, want true", caps["loadSession"])
	}
	promptCaps := caps["promptCapabilities"].(map[string]any)
	if promptCaps["image"] != true || promptCaps["embeddedContext"] != true {
		t.Errorf("promptCapabilities = %v", promptCaps)
	}
	if promptCaps["audio"] == true {
		t.Errorf("audio must not be advertised — see design doc")
	}
}

func TestAuthenticateAlwaysSucceeds(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "authenticate", authenticateParams{MethodID: "anything"})

	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "bogus/method", map[string]any{})

	resp := h.readResponseFor(1)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error, got %v", resp)
	}
	if int(errObj["code"].(float64)) != errCodeMethodNotFound {
		t.Errorf("code = %v, want %d", errObj["code"], errCodeMethodNotFound)
	}
}

func TestUnknownNotificationIsSilentlyIgnored(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.sendNotification("bogus/notification", map[string]any{})

	// Immediately follow with a real request — if the unknown notification
	// had wedged the dispatch loop, this would time out.
	h.send(1, "initialize", initializeParams{ProtocolVersion: 1})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Fatalf("dispatch loop did not recover: %v", resp["error"])
	}
}

func TestNewSessionUnknownProviderStillRespondsWithError(t *testing.T) {
	// With no active provider configured, CreateSession fails server-side —
	// this asserts the failure surfaces as a clean JSON-RPC error rather than
	// hanging or crashing the dispatch loop.
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})

	resp := h.readResponseFor(1)
	if resp["error"] == nil {
		t.Skip("an active provider is configured in this environment — session/new succeeded, nothing to assert here")
	}
}

func TestSessionPromptUnknownSessionID(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/prompt", promptParams{SessionID: "does-not-exist", Prompt: []contentBlock{textBlock("hi")}})

	resp := h.readResponseFor(1)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error for an unknown session, got %v", resp)
	}
	if int(errObj["code"].(float64)) != errCodeInvalidParams {
		t.Errorf("code = %v, want %d", errObj["code"], errCodeInvalidParams)
	}
}

func TestSessionCancelUnknownSessionDoesNotPanicOrRespond(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.sendNotification("session/cancel", cancelParams{SessionID: "does-not-exist"})

	// Follow with a real request to prove the dispatch loop is still alive.
	h.send(1, "initialize", initializeParams{ProtocolVersion: 1})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Fatalf("dispatch loop did not recover: %v", resp["error"])
	}
}

func TestRunReturnsOnStdinClose(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	var stdout bytes.Buffer
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- Run(ctx, newTestAgent(t), stdinR, &stdout) }()

	stdinW.Close() // simulates the ACP client closing the connection

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error on clean stdin close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after stdin closed")
	}
}

// TestRunReturnsOnContextCancelWhileBlockedOnStdin is the regression test for
// the Ctrl+C hang: `harness acp` run from a real terminal (not a closed
// pipe) sits blocked in a stdin Read() waiting for the next JSON-RPC line,
// which — once the client is gone — never arrives. dispatchLoop must notice
// ctx being cancelled (SIGINT → signalContext(), in production) WHILE that
// read is still blocked, not only in between reads. stdinR here is
// deliberately never closed and nothing is ever written to it, reproducing
// exactly that "still blocked in Read()" state.
func TestRunReturnsOnContextCancelWhileBlockedOnStdin(t *testing.T) {
	stdinR, _ := io.Pipe() // writer intentionally kept open and unused — stdinR blocks forever
	var stdout bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- Run(ctx, newTestAgent(t), stdinR, &stdout) }()

	time.Sleep(100 * time.Millisecond) // let Run reach the blocking stdin read
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil — Ctrl+C/SIGTERM is a clean shutdown, not a failure (matches harness serve's behavior)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly after ctx cancel while blocked on stdin — the Ctrl+C hang regressed")
	}
}
