package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// ── In-memory registry cache (no disk, fetched once per session) ───────────

var (
	remoteRegistry     map[string]ModelMeta
	remoteRegistryOnce sync.Once
)

// hardcodedRegistry covers models NOT in the public llm-registry.
// All data sourced from official provider documentation.
// DeepSeek V4 removed (now in llm-registry).
var hardcodedRegistry = map[string]ModelMeta{
	// GLM (Z.AI) — docs.z.ai — 202752 ctx (~200K), thinking=on, no vision
	"glm-5":   {ID: "glm-5", ContextWindow: 202752, MaxTokens: 32000, Thinking: true},
	"glm-5.1": {ID: "glm-5.1", ContextWindow: 202752, MaxTokens: 128000, Thinking: true},
	"glm-4.7": {ID: "glm-4.7", ContextWindow: 202752, MaxTokens: 32000, Thinking: true},
	"glm-4.6": {ID: "glm-4.6", ContextWindow: 128000, MaxTokens: 32000, Thinking: true},
	// Kimi (Moonshot) — platform.kimi.ai — 256K ctx, vision=true, thinking=true
	"kimi-k2.5":        {ID: "kimi-k2.5", ContextWindow: 256000, MaxTokens: 32000, Vision: true, Thinking: true},
	"kimi-k2.6":        {ID: "kimi-k2.6", ContextWindow: 256000, MaxTokens: 32000, Vision: true, Thinking: true},
	"kimi-k2:1t":       {ID: "kimi-k2:1t", ContextWindow: 256000, MaxTokens: 32000, Vision: true, Thinking: true},
	"kimi-k2-thinking": {ID: "kimi-k2-thinking", ContextWindow: 256000, MaxTokens: 32000, Vision: true, Thinking: true},
	// MiniMax — platform.minimax.io — 204800 ctx, text-only in M2.x
	"minimax-m2.5": {ID: "minimax-m2.5", ContextWindow: 204800, MaxTokens: 131072, Thinking: true},
	"minimax-m2.7": {ID: "minimax-m2.7", ContextWindow: 204800, MaxTokens: 131072, Thinking: true},
	// MiMo (Xiaomi) — huggingface.co/XiaomiMiMo — 1M ctx, vision=true (V2.5/omni)
	"mimo-v2.5":     {ID: "mimo-v2.5", ContextWindow: 1000000, MaxTokens: 32000, Vision: true, Thinking: true},
	"mimo-v2.5-pro": {ID: "mimo-v2.5-pro", ContextWindow: 1000000, MaxTokens: 32000, Vision: true, Thinking: true},
	"mimo-v2-pro":   {ID: "mimo-v2-pro", ContextWindow: 256000, MaxTokens: 32000, Thinking: true},
	"mimo-v2-omni":  {ID: "mimo-v2-omni", ContextWindow: 256000, MaxTokens: 32000, Vision: true, Thinking: true},
	// Qwen Plus (Alibaba) — qwen.ai — 1M ctx, vision=true, thinking=true
	"qwen3.5-plus": {ID: "qwen3.5-plus", ContextWindow: 1000000, MaxTokens: 32000, Vision: true, Thinking: true},
	"qwen3.6-plus": {ID: "qwen3.6-plus", ContextWindow: 1000000, MaxTokens: 32000, Vision: true, Thinking: true},
}

// defaultMeta is used when no other source has info.
var defaultMeta = ModelMeta{
	ContextWindow: 128000,
	MaxTokens:     32000,
	Vision:        false,
}

// enrichMeta fills missing capabilities for models from providers that
// don't expose capability endpoints (OpenAI, OpenCode Go).
// Priority: remote registry → hardcoded → infer by name → generic defaults.
// Providers that return real capabilities (Anthropic, Ollama, OllamaCloud)
// should NOT call enrichMeta — their data is already authoritative.
func enrichMeta(m ModelMeta) ModelMeta {
	// 1. Try remote llm-registry (in-memory, fetched once)
	if r := lookupRemote(m.ID); r != nil {
		if m.ContextWindow <= 0 { m.ContextWindow = r.ContextWindow }
		if m.MaxTokens <= 0    { m.MaxTokens = r.MaxTokens }
		if !m.Vision          { m.Vision = r.Vision }
		if !m.Thinking        { m.Thinking = r.Thinking }
		if m.DisplayName == "" { m.DisplayName = r.DisplayName }
		return m
	}

	// 2. Hardcoded known models
	if r, ok := hardcodedRegistry[m.ID]; ok {
		if m.ContextWindow <= 0 { m.ContextWindow = r.ContextWindow }
		if m.MaxTokens <= 0    { m.MaxTokens = r.MaxTokens }
		if !m.Vision           { m.Vision = r.Vision }
		if !m.Thinking         { m.Thinking = r.Thinking }
		return m
	}

	// 3. Infer from model name
	if m.ContextWindow <= 0 { m.ContextWindow = inferContextWindow(m.ID) }
	if m.MaxTokens <= 0    { m.MaxTokens = 32000 }
	if !m.Vision           { m.Vision = inferVision(m.ID) }

	// 4. Generic defaults for anything still missing
	if m.ContextWindow <= 0 { m.ContextWindow = defaultMeta.ContextWindow }
	if m.MaxTokens <= 0    { m.MaxTokens = defaultMeta.MaxTokens }

	return m
}

// LookupModel is the public API — returns enriched metadata for a model ID.
func LookupModel(id string) *ModelMeta {
	if r := lookupRemote(id); r != nil {
		return r
	}
	if r, ok := hardcodedRegistry[id]; ok {
		return &r
	}
	return nil
}

func lookupRemote(id string) *ModelMeta {
	reg := getRemoteRegistry()
	if m, ok := reg[id]; ok {
		return &m
	}
	// Try trimming common suffixes (e.g. "deepseek-v4-pro" matches "deepseek-v4-pro")
	// Some registries use different naming — try lowercase
	lower := strings.ToLower(id)
	for k, v := range reg {
		if strings.ToLower(k) == lower {
			return &v
		}
	}
	return nil
}

// getRemoteRegistry fetches the llm-registry once per session (no disk cache).
func getRemoteRegistry() map[string]ModelMeta {
	remoteRegistryOnce.Do(func() {
		remoteRegistry = fetchRemoteRegistry()
		if remoteRegistry == nil {
			remoteRegistry = make(map[string]ModelMeta)
		}
	})
	return remoteRegistry
}

func fetchRemoteRegistry() map[string]ModelMeta {
	const url = "https://raw.githubusercontent.com/yamanahlawat/llm-registry/main/src/llm_registry/data/models.json"
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil { resp.Body.Close() }
		return nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	return parseRegistry(data)
}

func parseRegistry(data []byte) map[string]ModelMeta {
	var raw struct {
		Models map[string]struct {
			TokenCosts struct {
				ContextWindow int `json:"context_window"`
				MaxTokens     int `json:"max_output_tokens"`
			} `json:"token_costs"`
			Features struct {
				Vision   bool `json:"vision"`
				Thinking bool `json:"reasoning"`
			} `json:"features"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	result := make(map[string]ModelMeta, len(raw.Models))
	for id, m := range raw.Models {
		mt := m.TokenCosts.MaxTokens
		if mt <= 0 { mt = 32000 }
		result[id] = ModelMeta{
			ID:            id,
			ContextWindow: m.TokenCosts.ContextWindow,
			MaxTokens:     mt,
			Vision:        m.Features.Vision,
			Thinking:      m.Features.Thinking,
		}
	}
	return result
}

// modelSupportsThinking checks if a model has thinking capability.
// Checks: in-memory model cache → hardcoded registry → llm-registry → false.
func modelSupportsThinking(provider, model string) bool {
	// Check in-memory cache first (most accurate — populated from /api/show etc.)
	modelCacheMu.RLock()
	if meta, ok := modelCache[provider+"/"+model]; ok {
		modelCacheMu.RUnlock()
		return meta.Thinking
	}
	modelCacheMu.RUnlock()

	// Check hardcoded registry
	if meta, ok := hardcodedRegistry[model]; ok {
		return meta.Thinking
	}

	// Check remote llm-registry
	if r := lookupRemote(model); r != nil {
		return r.Thinking
	}

	return false
}

// NewThinkingProviderForOpenAI is exported for use from registry.
func NewThinkingProviderForOpenAI(p *OpenAI, provider, model string) *OpenAI {
	return newThinkingProvider(p, provider, model)
}

func newThinkingProvider(p *OpenAI, provider, model string) *OpenAI {
	p.thinking = modelSupportsThinking(provider, model)
	if p.thinking {
		p.thinkingLevel = GetThinking()
	}
	return p
}
