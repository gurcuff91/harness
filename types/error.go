package types

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProviderAPIError is a structured error from an LLM provider's HTTP API (as
// opposed to harness's own API). Message is human-readable; Details, when
// present, holds the parsed error payload the provider returned (e.g. its JSON
// body) so a transport can render it richly instead of scraping a string.
type ProviderAPIError struct {
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	// Status is the provider's HTTP status code (0 if unknown) — lets
	// callers classify the error (e.g. retry 502/503/504) without parsing
	// the message.
	Status int `json:"status,omitempty"`
}

// Error implements the error interface. It returns the message, appending the
// details as compact JSON when present so plain error() output is still complete.
func (e *ProviderAPIError) Error() string {
	if len(e.Details) == 0 {
		return e.Message
	}
	if b, err := json.Marshal(e.Details); err == nil {
		// json.Marshal escapes ESC and the other C0 controls but passes U+0080–U+009F through, and
		// those are the 8-bit forms of CSI/OSC/DCS: they are dropped here too.
		return e.Message + ": " + stripControl(string(b))
	}
	return e.Message
}

// NewProviderAPIError builds a ProviderAPIError from a provider name, HTTP
// status, and raw response body. If the body is a JSON object it's parsed into
// Details; otherwise the raw text becomes the message suffix.
func NewProviderAPIError(provider string, status int, body []byte) *ProviderAPIError {
	e := &ProviderAPIError{Message: fmt.Sprintf("%s API error %d", provider, status), Status: status}
	var obj map[string]any
	if len(body) > 0 && json.Unmarshal(body, &obj) == nil {
		e.Details = obj
	} else if len(body) > 0 {
		// The body is text a gateway or proxy wrote, not harness's: it ends up in a message every
		// transport prints (a terminal, a chat), so control characters — ESC above all, which would
		// make a terminal INTERPRET what follows — are removed at the source.
		e.Message += ": " + stripControl(string(body))
	}
	return e
}

// stripControl drops the control characters of s that act on a terminal or hide text — C0 (ESC, BEL,
// BS, CR, NUL…), DEL and the C1 range, whose single characters are the 8-bit forms of CSI/OSC/DCS —
// plus invalid UTF-8. Line feed and TAB are kept: they are layout, not control. What follows an ESC
// stays, as plain text ("]0;title"), so nothing the provider said is lost, only made inert.
func stripControl(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\n' && c != '\t' || c == 0x7f || c >= 0x80 {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r):
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}
