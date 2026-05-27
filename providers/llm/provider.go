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
	// Name returns the provider slug (e.g. "anthropic", "openai", "ollama").
	Name() string
	// IsActive returns true if this provider has valid credentials and is reachable.
	IsActive() bool
	// Models returns the cached model list for this provider.
	// Fast, no API call — populated by FetchModels().
	Models() []ModelMeta
	// FetchModels refreshes the internal model cache from the provider API.
	// Each model is fully enriched: capabilities from the provider API
	// and pricing from llm-registry.
	FetchModels() []ModelMeta
}
