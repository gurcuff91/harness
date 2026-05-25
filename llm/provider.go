package llm

import (
	"context"
	"encoding/json"
)

// Provider abstracts LLM API differences.
// All providers implement streaming — there is no non-streaming fallback.
type Provider interface {
	// CompleteStream sends a request and streams events via callback.
	// The final Response is returned when the stream completes.
	CompleteStream(ctx context.Context, req *Request, cb StreamCallback) (*Response, error)
	// FormatUserMessage wraps user text into the provider's message format.
	FormatUserMessage(text string) json.RawMessage
	// FormatUserMessageWithImages wraps user text + images.
	FormatUserMessageWithImages(text string, images []ImageData) json.RawMessage
	// FormatToolResults wraps tool results into the provider's message format.
	FormatToolResults(results []ToolResult) []json.RawMessage
	// Model returns the current model identifier.
	Model() string
}

// ImageData holds a base64-encoded image for vision requests.
type ImageData struct {
	MimeType string
	Base64   string
}
