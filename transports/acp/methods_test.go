package acp

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

func TestAcpSessionNameHasTransportPrefix(t *testing.T) {
	name := acpSessionName()
	if !strings.HasPrefix(name, "Acp ") {
		t.Errorf("acpSessionName() = %q, want prefix %q", name, "Acp ")
	}
}

// TestNotifyUsageSendsUsageUpdate is the core case for the feature this test
// file adds coverage for: handleLoadSession sends a "usage_update"
// notification after replaying history, built from the resumed session's
// accumulated stats — so a client loading a session with real history
// immediately sees how much of the context window is already used, instead
// of a blank/zero indicator until the next turn's live tokens event.
func TestNotifyUsageSendsUsageUpdate(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	notifyUsage(c, "sess1", types.SessionStats{
		ContextUsage:  0.0248,
		ContextWindow: 1_000_000,
		CostUSD:       0.027477977,
	})

	notes := decodeNotifications(t, &buf)
	if len(notes) != 1 {
		t.Fatalf("expected 1 notification, got %d: %+v", len(notes), notes)
	}
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "usage_update" {
		t.Fatalf("sessionUpdate = %v, want usage_update", update["sessionUpdate"])
	}
	// used = ContextUsage * ContextWindow = 0.0248 * 1_000_000 = 24800 — NOT
	// stats.InputTokens (a cumulative, ever-growing counter across every turn
	// the session has run — see notifyUsage's doc comment for why that would
	// be the wrong number here).
	if got := update["used"].(float64); got != 24800 {
		t.Errorf("used = %v, want 24800", got)
	}
	if got := update["size"].(float64); got != 1_000_000 {
		t.Errorf("size = %v, want 1000000", got)
	}
	cost := update["cost"].(map[string]any)
	if got := cost["amount"].(float64); got != 0.027477977 {
		t.Errorf("cost.amount = %v, want 0.027477977", got)
	}
	if cost["currency"] != "USD" {
		t.Errorf("cost.currency = %v, want USD", cost["currency"])
	}
}

// TestNotifyUsageSkipsWhenNoContextWindow verifies notifyUsage sends nothing
// for a session that has never had a real turn (ContextWindow == 0) — a
// brand-new session loaded before its first prompt has nothing meaningful to
// report, and "used: 0, size: 0" would be noise rather than information.
func TestNotifyUsageSkipsWhenNoContextWindow(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	notifyUsage(c, "sess1", types.SessionStats{})

	if buf.Len() != 0 {
		t.Errorf("expected no notification sent, got: %s", buf.String())
	}
}

// TestNotifyUsageOmitsCostWhenZero verifies the cost field is nil (omitted)
// when CostUSD is zero — mirrors events.go's live "tokens" case, which uses
// the same "only set Cost if > 0" rule.
func TestNotifyUsageOmitsCostWhenZero(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(nil, &buf)

	notifyUsage(c, "sess1", types.SessionStats{
		ContextUsage:  0.1,
		ContextWindow: 100_000,
	})

	notes := decodeNotifications(t, &buf)
	update := notes[0]["params"].(map[string]any)["update"].(map[string]any)
	if _, hasCost := update["cost"]; hasCost {
		t.Errorf("expected no 'cost' field when CostUSD is 0, got: %v", update)
	}
}

func TestFlattenPromptTextOnly(t *testing.T) {
	text, images := flattenPrompt([]contentBlock{textBlock("hello world")})
	if text != "hello world" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 0 {
		t.Errorf("expected no images, got %d", len(images))
	}
}

func TestFlattenPromptMultipleTextBlocksJoinedWithBlankLine(t *testing.T) {
	text, _ := flattenPrompt([]contentBlock{textBlock("first"), textBlock("second")})
	if text != "first\n\nsecond" {
		t.Errorf("text = %q", text)
	}
}

func TestFlattenPromptEmbeddedResource(t *testing.T) {
	blocks := []contentBlock{
		textBlock("please review:"),
		{Type: "resource", Resource: &embeddedResource{URI: "file:///a.py", Text: "def f(): pass"}},
	}
	text, _ := flattenPrompt(blocks)
	want := "please review:\n\ndef f(): pass"
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func TestFlattenPromptResourceWithoutText(t *testing.T) {
	// A resource block with no embedded text (e.g. a blob) contributes nothing —
	// this transport doesn't resolve blob/binary resources.
	blocks := []contentBlock{
		textBlock("look at this:"),
		{Type: "resource", Resource: &embeddedResource{URI: "file:///a.png", Blob: "base64=="}},
	}
	text, _ := flattenPrompt(blocks)
	if text != "look at this:" {
		t.Errorf("text = %q", text)
	}
}

func TestFlattenPromptImage(t *testing.T) {
	blocks := []contentBlock{
		textBlock("what is this?"),
		{Type: "image", Data: "base64data", MimeType: "image/png"},
	}
	text, images := flattenPrompt(blocks)
	if text != "what is this?" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 1 || images[0].Base64 != "base64data" || images[0].MimeType != "image/png" {
		t.Errorf("images = %+v", images)
	}
}

func TestFlattenPromptResourceLinkIgnored(t *testing.T) {
	// resource_link (URI-only, no embedded content) is out of scope for the
	// first cut — must not panic and must not contribute text.
	blocks := []contentBlock{
		{Type: "resource_link", URI: "file:///doc.pdf", Name: "doc.pdf"},
	}
	text, images := flattenPrompt(blocks)
	if text != "" || len(images) != 0 {
		t.Errorf("text = %q, images = %+v, want both empty", text, images)
	}
}

func TestFlattenPromptEmpty(t *testing.T) {
	text, images := flattenPrompt(nil)
	if text != "" || images != nil {
		t.Errorf("text = %q, images = %v", text, images)
	}
}

func TestExecutableCommandCompact(t *testing.T) {
	cmd, params, ok := executableCommand("/compact")
	if !ok || cmd != "compact" {
		t.Fatalf("cmd=%q ok=%v, want compact/true", cmd, ok)
	}
	if len(params) != 0 {
		t.Errorf("params = %v, want empty", params)
	}
}

func TestExecutableCommandInfo(t *testing.T) {
	cmd, _, ok := executableCommand("/info")
	if !ok || cmd != "info" {
		t.Fatalf("cmd=%q ok=%v, want info/true", cmd, ok)
	}
}

func TestExecutableCommandContext(t *testing.T) {
	cmd, _, ok := executableCommand("/context")
	if !ok || cmd != "context" {
		t.Fatalf("cmd=%q ok=%v, want context/true", cmd, ok)
	}
}

func TestExecutableCommandSkillNoArgs(t *testing.T) {
	cmd, params, ok := executableCommand("/skill:brainstorming")
	if !ok || cmd != "skill:brainstorming" {
		t.Fatalf("cmd=%q ok=%v, want skill:brainstorming/true", cmd, ok)
	}
	if _, has := params["prompt"]; has {
		t.Errorf("params = %v, want no 'prompt' key when no args given", params)
	}
}

func TestExecutableCommandSkillWithArgs(t *testing.T) {
	cmd, params, ok := executableCommand("/skill:brainstorming build me a todo app")
	if !ok || cmd != "skill:brainstorming" {
		t.Fatalf("cmd=%q ok=%v", cmd, ok)
	}
	if params["prompt"] != "build me a todo app" {
		t.Errorf(`params["prompt"] = %q`, params["prompt"])
	}
}

func TestExecutableCommandUnrecognizedFallsThrough(t *testing.T) {
	for _, text := range []string{
		"/rename foo",    // deliberately excluded — see executableCommand's doc comment
		"/model x",       // covered by configOptions instead
		"/thinking high", // covered by configOptions instead
		"/reset",         // deliberately excluded
		"/bogus",         // not a real command at all
		"not a command",  // no leading slash
		"",
	} {
		if _, _, ok := executableCommand(text); ok {
			t.Errorf("executableCommand(%q) = ok, want fall-through to a normal prompt", text)
		}
	}
}

// TestInternalErrCarriesClientErrorDetailsAsData is the regression test for
// the same class of bug fixed in pumpEvents' "error" case: client.Client
// already parses any failed HTTP call into a *client.Error{Message, Details}
// (client/error.go) — Details being the same kind of structured provider
// payload (e.g. a rate-limited session/prompt or ExecCommand call). Losing
// it here would mean the 13 internalErr call sites in this file (creating a
// session, sending a prompt, exec'ing a command, etc.) surface strictly less
// context than a live-turn EventError does for the exact same underlying
// provider failure.
func TestInternalErrCarriesClientErrorDetailsAsData(t *testing.T) {
	details := map[string]any{"error": map[string]any{"message": "Rate limit exceeded", "type": "rate_limit_error"}}
	err := internalErr("send prompt", &client.Error{Message: "openai API error 429", Details: details})

	if err.Code != errCodeInternalError {
		t.Errorf("Code = %v", err.Code)
	}
	if !strings.Contains(err.Message, "openai API error 429") {
		t.Errorf("Message = %q, want it to contain the clean client.Error message", err.Message)
	}
	if strings.Contains(err.Message, "Rate limit exceeded") {
		t.Errorf("Message = %q, Details should NOT be duplicated into the text message (they belong in Data)", err.Message)
	}
	gotData, ok := err.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data = %v (%T), want the client.Error's Details map", err.Data, err.Data)
	}
	if _, ok := gotData["error"]; !ok {
		t.Errorf("Data lost the provider payload: %v", gotData)
	}
}

func TestInternalErrPlainErrorHasNoData(t *testing.T) {
	err := internalErr("get messages", errors.New("session not found"))
	if err.Data != nil {
		t.Errorf("Data = %v, want nil for a plain (non-*client.Error) error", err.Data)
	}
	if !strings.Contains(err.Message, "session not found") {
		t.Errorf("Message = %q", err.Message)
	}
}

func TestInternalErrClientErrorWithoutDetailsHasNoData(t *testing.T) {
	err := internalErr("create session", &client.Error{Message: "session is busy"})
	if err.Data != nil {
		t.Errorf("Data = %v, want nil when *client.Error has no Details", err.Data)
	}
}
