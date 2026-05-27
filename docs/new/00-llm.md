# 00 — LLM Module Refactor

> Must be executed before 01-agent.md.
> Goal: clean up `llm/` so it has clear boundaries — protocol, types, implementations, model knowledge.

---

## Current problems

- `providers/` mixes HTTP implementations with utilities (resolve, parse, status)
- `ModelMeta` pricing lives in providers but is needed by agent/session
- `IsSubscription()` and `SetThinkingLevel()` on Provider interface don't belong there
- `status.go` is transport display data living in llm/providers
- `providers.go` has only `ParseModel()` — orphan utility
- `ImageData` lives in `provider.go` alongside the interface — wrong place

---

## Target structure

```
llm/
├── provider.go       ← Provider interface (5 methods only)
├── types.go          ← ALL types: Request, Response, StreamEvent,
│                        ToolCall, ToolResult, ToolDef, Usage, ImageData
├── sse.go            ← SSE parser (unchanged)
├── image.go          ← LoadImage(), IsImagePath() (unchanged)
├── resolve.go        ← ParseModel() + Resolve() — absorbs providers/parse + providers/resolve
│                        imports: llm/providers/, config/
├── models/           ← model knowledge: capabilities + pricing
│   ├── meta.go       ← ModelMeta struct (capabilities + pricing unified)
│   ├── catalog.go    ← in-memory model cache, RefreshModels()
│   └── registry.go   ← llm-registry remote + hardcoded + enrichMeta + ApplyRegistryPricing
└── providers/        ← HTTP implementations ONLY, nothing else
    ├── anthropic.go
    ├── claude_oauth.go
    ├── openai.go
    ├── ollama.go
    ├── ollama_cloud.go
    └── opencode_go.go
```

---

## Provider interface — simplified

Remove `IsSubscription()` and `SetThinkingLevel()` — they don't belong on the protocol interface.
`ThinkingLevel` moves to `Request`. `IsSubscription` moves to `ModelMeta`.

```go
// llm/provider.go
type Provider interface {
    CompleteStream(ctx context.Context, req *Request, cb StreamCallback) (*Response, error)
    FormatUserMessage(text string) json.RawMessage
    FormatUserMessageWithImages(text string, images []ImageData) json.RawMessage
    FormatToolResults(results []ToolResult) []json.RawMessage
    Model() string
}
```

---

## types.go — all types unified

`ImageData` moves here from `provider.go`.
`ThinkingLevel` added to `Request`.

```go
// llm/types.go

type ImageData struct {
    MimeType string
    Base64   string
}

type Request struct {
    SystemPrompt  string
    Messages      []json.RawMessage
    Tools         []ToolDef
    MaxTokens     int
    ThinkingLevel string  // "disable" | "low" | "medium" | "high" | "xhigh"
                          // each provider maps this to its own API params internally
}

type Response struct {
    Text             string
    Thinking         string
    AssistantMessage json.RawMessage
    ToolCalls        []ToolCall
    Usage            Usage
}

type ToolCall struct {
    ID    string
    Name  string
    Input json.RawMessage
}

type ToolResult struct {
    ID     string
    Output string
    IsErr  bool
}

type ToolDef struct {
    Name        string
    Description string
    InputSchema json.RawMessage
}

type Usage struct {
    InputTokens  int
    OutputTokens int
    CacheRead    int
    CacheWrite   int
}

// Streaming types unchanged
type StreamEventType int
const ( StreamTextDelta StreamEventType = iota ... )
type StreamEvent struct { ... }
type StreamCallback func(StreamEvent)
```

---

## resolve.go — ParseModel + Resolve together

```go
// llm/resolve.go

// ParseModel splits "provider/model" into (provider, model).
// Handles bare model names with provider inference.
func ParseModel(full string) (provider, model string)

// Resolve returns the appropriate Provider for a "provider/model" string.
// Reads credentials from config (env vars → ~/.harness/credentials.json).
func Resolve(fullModel string) (Provider, error)
```

These two always go together — Resolve needs ParseModel, nothing else needs ParseModel standalone.

---

## models/ — ModelMeta unified (capabilities + pricing)

`IsSubscription` moves here from Provider interface.
Pricing stays here — it's model knowledge, used by agent/session for cost tracking.

```go
// llm/models/meta.go

type ModelMeta struct {
    ID            string
    DisplayName   string
    ContextWindow int
    MaxTokens     int
    Vision        bool
    Thinking      bool
    IsSubscription bool    // flat sub or local compute (was Provider.IsSubscription())
    // Pricing ($ per 1M tokens) — from llm-registry
    InputCost      float64
    OutputCost     float64
    CacheReadCost  float64
    CacheWriteCost float64
}
```

```go
// llm/models/catalog.go
// In-memory model cache. Populated at startup by RefreshModels().

func RefreshModels()                              // fetches all connected providers
func RefreshProviderModels(provider string)       // refresh one provider
func GetModelMeta(fullModel string) *ModelMeta    // "provider/model" lookup
func AllModels() map[string]*ModelMeta
func ModelCount(provider string) int
func DetectAvailable(currentModel string) []ModelInfo
```

```go
// llm/models/registry.go
// Model capabilities resolution: remote llm-registry → hardcoded → name inference → defaults

func EnrichMeta(m ModelMeta) ModelMeta           // fills missing capabilities
func ApplyRegistryPricing(m *ModelMeta)          // fills pricing from llm-registry
func LookupModel(id string) *ModelMeta           // public lookup
func ModelSupportsThinking(fullModel string) bool // public helper
```

---

## providers/ — HTTP implementations only

Each file implements `llm.Provider`. No utilities, no catalog, no resolve.

Each provider reads `ThinkingLevel` from `req.ThinkingLevel` instead of storing it internally.
`SetThinkingLevel()` method is removed from all providers.
`IsSubscription()` method is removed from all providers — it's in `ModelMeta` now.

```
providers/
├── anthropic.go      ← Anthropic Messages API
├── claude_oauth.go   ← Claude OAuth (token refresh, keychain)
├── openai.go         ← OpenAI-compatible base (DeepSeek, Ollama, OpenCode Go)
├── ollama.go         ← Ollama local (auto-detect via ping) + FetchOllamaModels()
├── ollama_cloud.go   ← Ollama Cloud
└── opencode_go.go    ← OpenCode Go
```

Note: `OllamaAvailable()`, `FetchOllamaModels()` stay in `ollama.go` — they're provider-specific discovery, not catalog logic.

---

## What moves OUT of llm/providers/

| File | Moves to |
|---|---|
| `providers.go` (ParseModel) | `llm/resolve.go` |
| `resolve.go` | `llm/resolve.go` |
| `catalog.go` | `llm/models/catalog.go` |
| `model_registry.go` | `llm/models/registry.go` |
| `status.go` | transport layer (discussed separately) |

---

## Dependency graph after refactor

```
llm/providers/    → llm/, config/
llm/models/       → llm/, config/         (needs credentials for RefreshModels)
llm/              → llm/providers/,
                    llm/models/            (resolve.go needs both)
config/           → stdlib only
```

---

## What this enables for 01-agent.md

- Agent calls `llm.Resolve(fullModel)` to build a provider — clean single function
- Agent calls `llm.ParseModel(fullModel)` to split provider/model string
- Session reads `llm/models.GetModelMeta(fullModel)` for context window, thinking support, pricing
- Session puts `ThinkingLevel` directly in `llm.Request` — no SetThinkingLevel on provider
- Session accumulates cost using `ModelMeta.InputCost` etc. — no transport involvement
- `ModelMeta.IsSubscription` tells session/transport if cost is reference or actual spend
