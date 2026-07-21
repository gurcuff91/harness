package types

import "testing"

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
