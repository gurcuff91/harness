package types

import (
	"encoding/json"
	"fmt"
)

// ProviderAPIError is a structured error from an LLM provider's HTTP API (as
// opposed to harness's own API). Message is human-readable; Details, when
// present, holds the parsed error payload the provider returned (e.g. its JSON
// body) so a transport can render it richly instead of scraping a string.
type ProviderAPIError struct {
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface. It returns the message, appending the
// details as compact JSON when present so plain error() output is still complete.
func (e *ProviderAPIError) Error() string {
	if len(e.Details) == 0 {
		return e.Message
	}
	if b, err := json.Marshal(e.Details); err == nil {
		return e.Message + ": " + string(b)
	}
	return e.Message
}

// NewProviderAPIError builds a ProviderAPIError from a provider name, HTTP
// status, and raw response body. If the body is a JSON object it's parsed into
// Details; otherwise the raw text becomes the message suffix.
func NewProviderAPIError(provider string, status int, body []byte) *ProviderAPIError {
	e := &ProviderAPIError{Message: fmt.Sprintf("%s API error %d", provider, status)}
	var obj map[string]any
	if len(body) > 0 && json.Unmarshal(body, &obj) == nil {
		e.Details = obj
	} else if len(body) > 0 {
		e.Message += ": " + string(body)
	}
	return e
}
