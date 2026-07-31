package client

import "encoding/json"

// Error is a structured error from the harness API: a human-readable message
// plus optional structured details (e.g. a provider's parsed error payload).
// do() returns this for any 4xx/5xx response in harness's standard
// {"error": {"message": ..., "details": {...}}} shape, so a caller that wants
// to render details richly (as the TUI does for provider errors) can type-
// assert for *Error instead of parsing the message string; a caller that just
// wants text can call Error() like any other error.
type Error struct {
	Message string
	Details map[string]any
}

func (e *Error) Error() string {
	if len(e.Details) > 0 {
		if d, err := json.Marshal(e.Details); err == nil {
			return e.Message + ": " + string(d)
		}
	}
	return e.Message
}

// parseError parses a response body into an *Error if it matches harness's
// standard error shape. Returns nil (not an error) for any other shape —
// callers fall back to treating the raw body as the message.
func parseError(body []byte) *Error {
	var env struct {
		Error struct {
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		return &Error{Message: env.Error.Message, Details: env.Error.Details}
	}
	return nil
}
