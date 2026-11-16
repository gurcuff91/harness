package types

import (
	"strings"
	"testing"
)

func TestNewProviderAPIErrorParsesJSON(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
	e := NewProviderAPIError("anthropic", 400, body)
	if e.Message != "anthropic API error 400" {
		t.Errorf("message: %q", e.Message)
	}
	if e.Details == nil || e.Details["type"] != "error" {
		t.Errorf("details not parsed: %v", e.Details)
	}
	// Error() includes details as JSON.
	if got := e.Error(); got == e.Message {
		t.Errorf("Error() should append details: %q", got)
	}
}

func TestNewProviderAPIErrorPlainBody(t *testing.T) {
	e := NewProviderAPIError("openai", 500, []byte("internal error"))
	if e.Details != nil {
		t.Errorf("non-JSON body should not populate details: %v", e.Details)
	}
	if e.Message != "openai API error 500: internal error" {
		t.Errorf("message: %q", e.Message)
	}
}

func TestNewProviderAPIErrorNoBody(t *testing.T) {
	e := NewProviderAPIError("minimax", 429, nil)
	if e.Error() != "minimax API error 429" {
		t.Errorf("got %q", e.Error())
	}
}

func TestAPlainTextProviderBodyCannotCarryTerminalControls(t *testing.T) {
	// what a gateway's HTML/text error page could contain
	body := []byte("gateway error \x1b]52;c;UFdO\x07 and \x1b[6n and \x1bPq#X\x1b\\ and \u009b6n and \x00\x7f\r end")
	e := NewProviderAPIError("openai", 502, body)
	for _, bad := range []string{"\x1b", "\x07", "\x00", "\x7f", "\r", "\u009b"} {
		if strings.Contains(e.Message, bad) || strings.Contains(e.Error(), bad) {
			t.Errorf("the message still carries %q: %q", bad, e.Message)
		}
	}
	// nothing the provider said is lost, only made inert
	for _, keep := range []string{"openai API error 502: gateway error", "]52;c;UFdO", "[6n", "end"} {
		if !strings.Contains(e.Message, keep) {
			t.Errorf("the message lost %q: %q", keep, e.Message)
		}
	}
}

func TestStripControlKeepsLayoutAndText(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "",
		"plain":                 "plain",
		"two\nlines\tand tab":   "two\nlines\tand tab",
		"ñandú 日本語 🚀":           "ñandú 日本語 🚀",
		"a\x1bb":                "ab",
		"a\x07b\x00c\x7fd\re":   "abcde",
		"x\u009b6ny\u0085z":     "x6nyz",
		"ok\xff\xfe!":           "ok!",
		"\x1b]0;t\x07":          "]0;t",
		"already clean string.": "already clean string.",
	} {
		if got := stripControl(in); got != want {
			t.Errorf("stripControl(%q) = %q, want %q", in, got, want)
		}
	}
}

// The structured path: json.Marshal escapes ESC (as \u001b) but lets the 8-bit C1 controls through.
func TestDetailsInErrorCannotCarryAnEightBitControl(t *testing.T) {
	e := NewProviderAPIError("anthropic", 400, []byte(`{"error":{"message":"bad \u009b6n \u001b[6n"}}`))
	if s := e.Error(); strings.Contains(s, "\u009b") || strings.Contains(s, "\x1b") {
		t.Errorf("Error() carries a control: %q", s)
	}
	if !strings.Contains(e.Error(), "bad") {
		t.Errorf("the details were lost: %q", e.Error())
	}
}
