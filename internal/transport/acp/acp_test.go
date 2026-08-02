package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
)

// newTestAgent builds a minimal *agent.Agent for the dispatch loop tests: no
// MCP/memory/scheduler, an in-memory session store (so nothing touches
// disk), matching how internal/cli's newOneShotAgent isolates one-shot
// throwaway work.
// readMessageTimeout bounds how long readMessage waits for one line from the
// agent under test. Generous enough to cover a real /compact round trip
// against a genuinely connected provider (a real LLM call to generate the
// compaction summary, observed taking ~7s in this environment) — a plain
// event notification arrives near-instantly, so this only matters for the
// handful of tests that trigger real model calls.
const readMessageTimeout = 20 * time.Second

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
	case <-time.After(readMessageTimeout):
		h.t.Fatal("timed out waiting for a message from the agent")
		return nil
	}
}

// readResponseFor reads messages until it finds the response with the given
// id, skipping over any notifications (session/update) that arrive first —
// mirroring how a real ACP client demultiplexes the stream.
func (h *harness) readResponseFor(id int) map[string]any {
	h.t.Helper()
	resp, _ := h.readResponseForCollecting(id)
	return resp
}

// readResponseForCollecting is readResponseFor plus every notification seen
// BEFORE the response arrives — for tests that need to assert on any
// out-of-band session/update notifications a handler might send ahead of its
// own response (there currently are none — see readNotificationsAfter for
// the ones sent AFTER, which is the spec-safe order this transport uses).
func (h *harness) readResponseForCollecting(id int) (resp map[string]any, notifications []map[string]any) {
	h.t.Helper()
	for i := 0; i < 50; i++ { // generous bound: enough for any realistic notification burst
		m := h.readMessage()
		if gotID, ok := m["id"]; ok && int(gotID.(float64)) == id {
			return m, notifications
		}
		notifications = append(notifications, m)
	}
	h.t.Fatalf("never saw a response for request id %d", id)
	return nil, nil
}

// readNotificationsAfter reads whatever messages arrive within a short
// window (no fixed count — a session may legitimately send zero, one, or a
// handful of update notifications) and returns whichever are notifications
// (no "id" field). Unlike readMessage, running out of messages here is
// expected and not a test failure — it just means the burst is over. Used
// to assert on updates a handler sends strictly AFTER its own response, such
// as available_commands_update following session/new/load/resume (see
// notifyAvailableCommands's doc comment in methods.go for why that ordering
// is load-bearing, not cosmetic).
func (h *harness) readNotificationsAfter() []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for {
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
				return out
			}
			var m map[string]any
			if err := json.Unmarshal(r.line, &m); err != nil {
				return out
			}
			if _, hasID := m["id"]; !hasID {
				out = append(out, m)
			}
		case <-time.After(300 * time.Millisecond):
			return out
		}
	}
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

	// Regression: agentInfo.version was missing entirely — the spec's own
	// Implementation type carries name/title/version, and version is what
	// lets a client display or log which harness build is running.
	agentInfo := result["agentInfo"].(map[string]any)
	if agentInfo["name"] != "harness" {
		t.Errorf("agentInfo.name = %v", agentInfo["name"])
	}
	if agentInfo["title"] == nil || agentInfo["title"] == "" {
		t.Error("agentInfo.title must be set")
	}
	if agentInfo["version"] == nil || agentInfo["version"] == "" {
		t.Error("agentInfo.version must be set")
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

// TestNewSessionSendsAvailableCommandsUpdate is the regression test for
// buildAvailableCommands existing but never being called: session/new must
// be followed by a session/update notification carrying
// available_commands_update BEFORE the session/new response itself,
// otherwise the client never learns the session's slash commands exist —
// see registerSession's doc comment in methods.go.
// TestNewSessionSendsAvailableCommandsUpdateAfterResponse is the regression
// test for the Zed-visible bug where slash commands never showed up:
// available_commands_update MUST be sent strictly AFTER session/new's own
// response (see notifyAvailableCommands's doc comment in methods.go —
// Zed silently drops any session/update notification for a session it
// doesn't know about yet, i.e. one that arrives before the response
// carrying that sessionId). This test asserts BOTH halves: the response
// comes first with no notification ahead of it, and the notification
// follows with the session's commands.
func TestNewSessionSendsAvailableCommandsUpdateAfterResponse(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})

	resp, notificationsBeforeResponse := h.readResponseForCollecting(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	if len(notificationsBeforeResponse) != 0 {
		t.Fatalf("expected NO notifications before the session/new response, got %d: %v", len(notificationsBeforeResponse), notificationsBeforeResponse)
	}

	after := h.readNotificationsAfter()
	var found bool
	for _, n := range after {
		params, ok := n["params"].(map[string]any)
		if !ok {
			continue
		}
		update, ok := params["update"].(map[string]any)
		if !ok {
			continue
		}
		if update["sessionUpdate"] == "available_commands_update" {
			found = true
			cmds, ok := update["availableCommands"].([]any)
			if !ok || len(cmds) == 0 {
				t.Errorf("availableCommands = %v, want a non-empty list (compact + skills at minimum)", update["availableCommands"])
			}
			// Regression: model/thinking (redundant with native configOptions)
			// and rename/reset (no ACP equivalent — see commandsExcludedFromACP's
			// doc comment) must NOT appear here at all.
			for _, c := range cmds {
				name, _ := c.(map[string]any)["name"].(string)
				if commandsExcludedFromACP[name] {
					t.Errorf("available_commands_update must not include %q", name)
				}
			}
			break
		}
	}
	if !found {
		t.Error("session/new never sent an available_commands_update notification after its response")
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

// TestSetConfigOptionUnknownSessionID is the regression test for
// session/set_config_option not being implemented at all (it fell through to
// the dispatch loop's "method not found" default) — a client selecting a
// value in the model/thinking dropdown had no method to call.
func TestSetConfigOptionUnknownSessionID(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	raw, _ := json.Marshal("high")
	h.send(1, "session/set_config_option", setConfigOptionParams{SessionID: "does-not-exist", ConfigID: "thinking", Value: raw})

	resp := h.readResponseFor(1)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error for an unknown session, got %v", resp)
	}
	if int(errObj["code"].(float64)) != errCodeInvalidParams {
		t.Errorf("code = %v, want %d (not method-not-found — the method IS implemented)", errObj["code"], errCodeInvalidParams)
	}
}

func TestSetConfigOptionUnknownConfigID(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)

	raw, _ := json.Marshal("whatever")
	h.send(2, "session/set_config_option", setConfigOptionParams{SessionID: sessionID, ConfigID: "not-a-real-option", Value: raw})
	resp = h.readResponseFor(2)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error for an unknown configId, got %v", resp)
	}
	if int(errObj["code"].(float64)) != errCodeInvalidParams {
		t.Errorf("code = %v, want %d", errObj["code"], errCodeInvalidParams)
	}
}

func TestSetConfigOptionThinking(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)

	raw, _ := json.Marshal("low")
	h.send(2, "session/set_config_option", setConfigOptionParams{SessionID: sessionID, ConfigID: "thinking", Value: raw})
	resp = h.readResponseFor(2)
	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	opts, ok := resp["result"].(map[string]any)["configOptions"].([]any)
	if !ok || len(opts) == 0 {
		t.Fatalf("expected the complete configOptions state back, got %v", resp["result"])
	}
	var sawThinkingLow bool
	for _, o := range opts {
		opt := o.(map[string]any)
		if opt["id"] == "thinking" && opt["currentValue"] == "low" {
			sawThinkingLow = true
		}
	}
	if !sawThinkingLow {
		t.Errorf("configOptions did not reflect the new thinking value: %v", opts)
	}
}

// TestSlashCompactExecutesRealCommandNotPlainPrompt is the regression test
// for the core bug this batch of work fixes: "/compact" sent as
// session/prompt text must be EXECUTED as the session's compact command
// (client.ExecCommand) — with real compact_start/compact_end feedback in the
// stream — not forwarded to the LLM as an ordinary message.
func TestSlashCompactExecutesRealCommandNotPlainPrompt(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)

	// Compacting an essentially empty, brand-new session legitimately fails —
	// that's fine here: we're asserting it was ATTEMPTED as a real command,
	// never silently treated as chat text. It can fail two genuinely
	// different ways, both acceptable:
	//   1. ExecCommand itself fails synchronously (e.g. "session is busy")
	//      → handled as a clean "✗ ..." agent_message_chunk, stopReason
	//      end_turn (see handlePrompt's executableCommand branch).
	//   2. ExecCommand succeeds (202 accepted) but the real turn running
	//      behind it hits an EventError later (e.g. a genuine provider rate
	//      limit while generating the compaction summary) → surfaces as a
	//      JSON-RPC protocol error, exactly like any other EventError during
	//      a normal prompt turn (see pumpEvents' "error" case) — this is
	//      existing, deliberate behavior for real LLM-call failures, not
	//      something specific to /compact.
	// (No need to drain the available_commands_update notification first —
	// readResponseForCollecting below will pick it up along with everything
	// else on the way to the session/prompt response.)
	h.send(2, "session/prompt", promptParams{SessionID: sessionID, Prompt: []contentBlock{textBlock("/compact")}})
	resp, notifications := h.readResponseForCollecting(2)

	if resp["error"] != nil {
		return // path 2 above — a real provider error is an acceptable outcome here
	}
	if resp["result"].(map[string]any)["stopReason"] != stopReasonEndTurn {
		t.Errorf("stopReason = %v, want %q", resp["result"].(map[string]any)["stopReason"], stopReasonEndTurn)
	}

	var sawCompactSignal bool
	for _, n := range notifications {
		params, ok := n["params"].(map[string]any)
		if !ok {
			continue
		}
		update, ok := params["update"].(map[string]any)
		if !ok || update["sessionUpdate"] != "agent_message_chunk" {
			continue
		}
		text, _ := update["content"].(map[string]any)["text"].(string)
		if strings.Contains(text, "Compacting") || strings.Contains(text, "compacted") || strings.HasPrefix(text, "✗") {
			sawCompactSignal = true
		}
	}
	if !sawCompactSignal {
		t.Errorf("expected compact_start/compact_end feedback or a failure notice, got notifications: %v", notifications)
	}
}

func TestSlashSkillUnknownNameReportsCleanFailure(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)

	h.send(2, "session/prompt", promptParams{SessionID: sessionID, Prompt: []contentBlock{textBlock("/skill:this-skill-does-not-exist")}})
	resp, notifications := h.readResponseForCollecting(2)
	if resp["error"] != nil {
		t.Fatalf("an unknown skill must not be a protocol error, want a clean stopReason with a failure notice: %v", resp["error"])
	}
	if resp["result"].(map[string]any)["stopReason"] != stopReasonEndTurn {
		t.Errorf("stopReason = %v, want %q", resp["result"].(map[string]any)["stopReason"], stopReasonEndTurn)
	}
	var sawFailureNotice bool
	for _, n := range notifications {
		params, ok := n["params"].(map[string]any)
		if !ok {
			continue
		}
		update, ok := params["update"].(map[string]any)
		if !ok {
			continue
		}
		content, ok := update["content"].(map[string]any)
		if !ok {
			continue // e.g. available_commands_update, which has no "content" field
		}
		text, _ := content["text"].(string)
		if strings.HasPrefix(text, "✗") {
			sawFailureNotice = true
		}
	}
	if !sawFailureNotice {
		t.Errorf("expected a '✗ ...' failure notice for an unknown skill, got: %v", notifications)
	}
}

// TestSlashInfoReturnsSessionInfoAsChunk verifies that "/info" sent as
// session/prompt text is handled inline as a read-only API query — it calls
// GET /api/sessions/{id}/info, formats the result as plain text, and sends
// it as a single agent_message_chunk with stopReason end_turn. It must NOT
// be forwarded to the LLM (no SendPrompt, no event stream).
func TestSlashInfoReturnsSessionInfoAsChunk(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)

	h.send(2, "session/prompt", promptParams{SessionID: sessionID, Prompt: []contentBlock{textBlock("/info")}})
	resp, notifications := h.readResponseForCollecting(2)
	if resp["error"] != nil {
		t.Fatalf("/info must not be a protocol error: %v", resp["error"])
	}
	if resp["result"].(map[string]any)["stopReason"] != stopReasonEndTurn {
		t.Errorf("stopReason = %v, want %q", resp["result"].(map[string]any)["stopReason"], stopReasonEndTurn)
	}

	var infoText string
	for _, n := range notifications {
		params, ok := n["params"].(map[string]any)
		if !ok {
			continue
		}
		update, ok := params["update"].(map[string]any)
		if !ok || update["sessionUpdate"] != "agent_message_chunk" {
			continue
		}
		text, _ := update["content"].(map[string]any)["text"].(string)
		if strings.Contains(text, "harness") || strings.Contains(text, "model") {
			infoText = text
		}
	}
	if infoText == "" {
		t.Errorf("expected an agent_message_chunk with session info, got: %v", notifications)
	}
}

// TestSlashContextReturnsContextBreakdownAsChunk verifies that "/context"
// is handled inline as a read-only API query — same pattern as /info but
// calling GET /api/sessions/{id}/context and formatting the context
// breakdown (system, tools, conversation, free space) as plain text.
func TestSlashContextReturnsContextBreakdownAsChunk(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)

	h.send(2, "session/prompt", promptParams{SessionID: sessionID, Prompt: []contentBlock{textBlock("/context")}})
	resp, notifications := h.readResponseForCollecting(2)
	if resp["error"] != nil {
		t.Fatalf("/context must not be a protocol error: %v", resp["error"])
	}
	if resp["result"].(map[string]any)["stopReason"] != stopReasonEndTurn {
		t.Errorf("stopReason = %v, want %q", resp["result"].(map[string]any)["stopReason"], stopReasonEndTurn)
	}

	var ctxText string
	for _, n := range notifications {
		params, ok := n["params"].(map[string]any)
		if !ok {
			continue
		}
		update, ok := params["update"].(map[string]any)
		if !ok || update["sessionUpdate"] != "agent_message_chunk" {
			continue
		}
		text, _ := update["content"].(map[string]any)["text"].(string)
		if strings.Contains(text, "system") || strings.Contains(text, "tools") || strings.Contains(text, "conversation") {
			ctxText = text
		}
	}
	if ctxText == "" {
		t.Errorf("expected an agent_message_chunk with context breakdown, got: %v", notifications)
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

// TestInitializeAdvertisesAllFourSessionCapabilities is the regression test
// for sessionCapabilities coming back as an empty {} even though Harness
// genuinely backs resume/close/delete/list via client.Client for every
// transport — the fields just weren't wired to ACP's advertisement.
func TestInitializeAdvertisesAllFourSessionCapabilities(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "initialize", initializeParams{ProtocolVersion: 1})
	resp := h.readResponseFor(1)

	caps := resp["result"].(map[string]any)["agentCapabilities"].(map[string]any)
	sessionCaps, ok := caps["sessionCapabilities"].(map[string]any)
	if !ok {
		t.Fatalf("sessionCapabilities missing or wrong type: %v", caps["sessionCapabilities"])
	}
	for _, field := range []string{"resume", "close", "delete", "list"} {
		if _, ok := sessionCaps[field]; !ok {
			t.Errorf("sessionCapabilities.%s not advertised", field)
		}
	}
}

func TestSessionResumeUnknownSessionID(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/resume", resumeSessionParams{SessionID: "does-not-exist", CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] == nil {
		t.Fatal("expected an error for an unknown session")
	}
}

func TestSessionCloseAndDeleteFullRoundTrip(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/new", newSessionParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)

	h.send(2, "session/close", closeSessionParams{SessionID: sessionID})
	resp = h.readResponseFor(2)
	if resp["error"] != nil {
		t.Fatalf("session/close: %v", resp["error"])
	}

	h.send(3, "session/delete", deleteSessionParams{SessionID: sessionID})
	resp = h.readResponseFor(3)
	if resp["error"] != nil {
		t.Fatalf("session/delete: %v", resp["error"])
	}

	// Per spec, deleting again SHOULD succeed silently.
	h.send(4, "session/delete", deleteSessionParams{SessionID: sessionID})
	resp = h.readResponseFor(4)
	if resp["error"] != nil {
		t.Errorf("deleting an already-deleted session should succeed silently, got: %v", resp["error"])
	}
}

func TestSessionListReturnsCreatedSession(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	cwd := t.TempDir()
	h.send(1, "session/new", newSessionParams{CWD: cwd})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Skip("no active provider configured in this environment — cannot create a session to test against")
	}
	sessionID := resp["result"].(map[string]any)["sessionId"].(string)

	h.send(2, "session/list", listSessionsParams{CWD: cwd})
	resp = h.readResponseFor(2)
	if resp["error"] != nil {
		t.Fatalf("session/list: %v", resp["error"])
	}
	sessions, ok := resp["result"].(map[string]any)["sessions"].([]any)
	if !ok {
		t.Fatalf("sessions field missing or wrong type: %v", resp["result"])
	}
	var found bool
	for _, s := range sessions {
		info := s.(map[string]any)
		if info["sessionId"] == sessionID {
			found = true
			if info["cwd"] != cwd {
				t.Errorf("cwd = %v, want %v", info["cwd"], cwd)
			}
			if info["title"] == nil || info["title"] == "" {
				t.Error("title should be set (Harness names ACP sessions \"Acp <date>\")")
			}
		}
	}
	if !found {
		t.Errorf("session/list did not include the just-created session %s: %v", sessionID, sessions)
	}
}

func TestSessionListEmptyReturnsEmptyArrayNotNull(t *testing.T) {
	h := startHarness(t, newTestAgent(t))
	h.send(1, "session/list", listSessionsParams{CWD: t.TempDir()})
	resp := h.readResponseFor(1)
	if resp["error"] != nil {
		t.Fatalf("session/list: %v", resp["error"])
	}
	sessions, ok := resp["result"].(map[string]any)["sessions"]
	if !ok {
		t.Fatal("sessions field missing")
	}
	arr, ok := sessions.([]any)
	if !ok {
		t.Fatalf("sessions must be an array (possibly empty), got %T: %v", sessions, sessions)
	}
	if len(arr) != 0 {
		t.Errorf("expected no sessions for a brand-new empty cwd, got %d", len(arr))
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
