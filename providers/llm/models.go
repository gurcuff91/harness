package llm

import (
	"encoding/json"
	"github.com/gurcuff91/harness/types"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ── Model metadata lookup ──────────────────────────────────────────────────

// FindMeta searches all registries (provider caches + hardcoded + remote) for a model ID.
// Returns nil if the model is not found anywhere.
func FindMeta(full string) *types.ModelMeta {
	_, modelID := parseModel(full)
	if meta := LookupModel(modelID); meta != nil {
		return meta
	}
	enriched := EnrichMeta(types.ModelMeta{ID: modelID})
	if enriched.ID != "" {
		return &enriched
	}
	return nil
}

// ── In-memory registry cache (no disk, fetched once per session) ───────────

var (
	remoteRegistry     map[string]types.ModelMeta
	remoteRegistryOnce sync.Once
)

// hardcodedRegistry is the LAST-RESORT fallback for models that OpenRouter
// doesn't list under an ID we can match (mostly exact aggregator IDs that
// OpenRouter only carries with version/date suffixes, e.g. "deepseek-v4" vs
// OpenRouter's "deepseek-v4-pro", or "qwen3.5-plus" vs "qwen3.5-plus-20260420").
// Keys MUST be normalized (lowercase, "."→"-") to match normalizeModelID.
// All data sourced from official provider documentation.
var hardcodedRegistry = map[string]types.ModelMeta{
	// DeepSeek V4 (aggregator IDs without suffix) — 1M ctx, thinking
	"deepseek-v4": {ID: "deepseek-v4", ContextWindow: 1000000, MaxTokens: 32000, Thinking: true},
	// MiMo (Xiaomi) — huggingface.co/XiaomiMiMo — not on OpenRouter
	"mimo-v2-5":     {ID: "mimo-v2.5", ContextWindow: 1000000, MaxTokens: 32000, Vision: true, Thinking: true},
	"mimo-v2-5-pro": {ID: "mimo-v2.5-pro", ContextWindow: 1000000, MaxTokens: 32000, Vision: true, Thinking: true},
	"mimo-v2-pro":   {ID: "mimo-v2-pro", ContextWindow: 256000, MaxTokens: 32000, Thinking: true},
	"mimo-v2-omni":  {ID: "mimo-v2-omni", ContextWindow: 256000, MaxTokens: 32000, Vision: true, Thinking: true},
	// Qwen Plus (aggregator IDs without date suffix) — 1M ctx, vision, thinking
	"qwen3-5-plus": {ID: "qwen3.5-plus", ContextWindow: 1000000, MaxTokens: 32000, Vision: true, Thinking: true},
	"qwen3-6-plus": {ID: "qwen3.6-plus", ContextWindow: 1000000, MaxTokens: 32000, Vision: true, Thinking: true},
}

// defaultMeta is used when no other source has info.
var defaultMeta = types.ModelMeta{
	ContextWindow: 128000,
	MaxTokens:     32000,
	Vision:        false,
}

// EnrichMeta completes a model's metadata through a STRICT, IMMUTABLE priority
// cascade. Each tier only fills fields its predecessor left empty — it never
// overwrites a value already present. Tiers, in order:
//
//  0. Provider (the data already in `m`)   — authoritative, never touched
//  1. OpenRouter catalog                    — fills gaps the provider left
//  2. Hardcoded registry                    — fills gaps OpenRouter left
//  3. Name inference                        — fills gaps the registries left
//  4. Generic defaults                      — final backstop
//
// CRITICAL: the cascade is ADDITIVE. There are no early returns that would skip
// a later tier — if OpenRouter supplies context but not thinking, the hardcoded
// tier still gets a chance to supply thinking, and so on. A field is "missing"
// when it is zero/empty/false. Pricing is treated as a group: filled from a
// source only if ALL price fields are still zero (so we never mix a provider's
// price with a registry's).
//
// Providers that return real capabilities (Anthropic, Ollama, OllamaCloud)
// should NOT call EnrichMeta — their data is already authoritative; they call
// ApplyRegistryPricing instead to fill only missing prices.
func EnrichMeta(m types.ModelMeta) types.ModelMeta {
	// Tier 1: OpenRouter.
	if r := lookupRemote(m.ID); r != nil {
		fillMeta(&m, r)
	}
	// Tier 2: hardcoded registry.
	if r, ok := hardcodedRegistry[normalizeModelID(m.ID)]; ok {
		fillMeta(&m, &r)
	}
	// Tier 3: name inference (no struct source — computed).
	if m.ContextWindow <= 0 {
		m.ContextWindow = InferContextWindow(m.ID)
	}
	if !m.Vision {
		m.Vision = InferVision(m.ID)
	}
	// Tier 4: generic defaults.
	if m.ContextWindow <= 0 {
		m.ContextWindow = defaultMeta.ContextWindow
	}
	if m.MaxTokens <= 0 {
		m.MaxTokens = defaultMeta.MaxTokens
	}
	if m.DisplayName == "" {
		m.DisplayName = m.ID
	}
	return m
}

// fillMeta copies fields from src into dst ONLY where dst's field is still
// empty (zero/false/""). It never overwrites existing data — the heart of the
// priority cascade. Pricing is a group: filled only if dst has no price at all.
func fillMeta(dst *types.ModelMeta, src *types.ModelMeta) {
	if dst.ContextWindow <= 0 {
		dst.ContextWindow = src.ContextWindow
	}
	if dst.MaxTokens <= 0 {
		dst.MaxTokens = src.MaxTokens
	}
	if !dst.Vision {
		dst.Vision = src.Vision
	}
	if !dst.Thinking {
		dst.Thinking = src.Thinking
	}
	if !dst.ThinkingAdaptive {
		dst.ThinkingAdaptive = src.ThinkingAdaptive
	}
	if !dst.ThinkingLegacy {
		dst.ThinkingLegacy = src.ThinkingLegacy
	}
	if dst.DisplayName == "" {
		dst.DisplayName = src.DisplayName
	}
	// Pricing as a group: only fill when dst has none, so we never blend a
	// provider's prices with a registry's.
	if dst.InputPrice == 0 && dst.OutputPrice == 0 && dst.CacheRead == 0 && dst.CacheWrite == 0 {
		dst.InputPrice = src.InputPrice
		dst.OutputPrice = src.OutputPrice
		dst.CacheRead = src.CacheRead
		dst.CacheWrite = src.CacheWrite
	}
}

// LookupModel is the public API — returns enriched metadata for a model ID.
func LookupModel(id string) *types.ModelMeta {
	if r := lookupRemote(id); r != nil {
		return r
	}
	if r, ok := hardcodedRegistry[normalizeModelID(id)]; ok {
		return &r
	}
	return nil
}

func lookupRemote(id string) *types.ModelMeta {
	reg := getRemoteRegistry()
	if m, ok := reg[normalizeModelID(id)]; ok {
		return &m
	}
	return nil
}

// normalizeModelID canonicalizes a model ID for cross-source matching:
// lowercased, provider prefix dropped, OpenRouter "~" alias prefix stripped,
// Ollama ":tag" suffix dropped (e.g. "minimax-m2.5:cloud" → "minimax-m2-5"),
// and "."→"-" so "claude-opus-4.6" (OpenRouter) matches "claude-opus-4-6" (ours).
func normalizeModelID(id string) string {
	s := strings.ToLower(id)
	s = strings.TrimPrefix(s, "~")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	// Drop Ollama-style ":tag" suffix (e.g. ":cloud", ":latest", ":7b").
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, ".", "-")
	return s
}

// getRemoteRegistry fetches the OpenRouter model catalog once per session (no
// disk cache). OpenRouter is the single remote source of model metadata — it
// covers our providers better than llm-registry (89% vs 63%) and is kept fresh.
func getRemoteRegistry() map[string]types.ModelMeta {
	remoteRegistryOnce.Do(func() {
		remoteRegistry = fetchRemoteRegistry()
		if remoteRegistry == nil {
			remoteRegistry = make(map[string]types.ModelMeta)
		}
	})
	return remoteRegistry
}

func fetchRemoteRegistry() map[string]types.ModelMeta {
	const url = "https://openrouter.ai/api/v1/models"
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	return parseRegistry(data)
}

// parseRegistry decodes the OpenRouter /api/v1/models response into our
// ModelMeta map, keyed by normalized model ID. Pricing in OpenRouter is USD
// per token (e.g. "0.0000003"); we store per-million, so multiply by 1e6.
// Capabilities are inferred from architecture.input_modalities (vision) and
// supported_parameters (reasoning).
func parseRegistry(data []byte) map[string]types.ModelMeta {
	var raw struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			Architecture  struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt      string `json:"prompt"`
				Completion  string `json:"completion"`
				InputCacheR string `json:"input_cache_read"`
				InputCacheW string `json:"input_cache_write"`
			} `json:"pricing"`
			TopProvider struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	perMillion := func(s string) float64 {
		if s == "" {
			return 0
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return v * 1e6
	}
	result := make(map[string]types.ModelMeta, len(raw.Data))
	for _, m := range raw.Data {
		vision := false
		for _, mod := range m.Architecture.InputModalities {
			if mod == "image" || mod == "video" {
				vision = true
				break
			}
		}
		thinking := false
		for _, p := range m.SupportedParameters {
			if p == "reasoning" || p == "include_reasoning" {
				thinking = true
				break
			}
		}
		mt := m.TopProvider.MaxCompletionTokens
		if mt <= 0 {
			mt = 32000
		}
		result[normalizeModelID(m.ID)] = types.ModelMeta{
			ID:            m.ID,
			DisplayName:   m.Name,
			ContextWindow: m.ContextLength,
			MaxTokens:     mt,
			Vision:        vision,
			Thinking:      thinking,
			InputPrice:    perMillion(m.Pricing.Prompt),
			OutputPrice:   perMillion(m.Pricing.Completion),
			CacheRead:     perMillion(m.Pricing.InputCacheR),
			CacheWrite:    perMillion(m.Pricing.InputCacheW),
		}
	}
	return result
}

// ApplyRegistryPricing fills pricing fields on a types.ModelMeta from the
// OpenRouter catalog. Called after provider APIs populate caps (context,
// vision, thinking) for providers whose API gives no prices (Anthropic,
// Ollama). It only FILLS the gap: if the provider already supplied any price,
// its numbers are respected and OpenRouter is not consulted.
func ApplyRegistryPricing(m *types.ModelMeta) {
	if m == nil {
		return
	}
	// Respect provider-supplied pricing — only fill when all prices are zero.
	if m.InputPrice != 0 || m.OutputPrice != 0 || m.CacheRead != 0 || m.CacheWrite != 0 {
		return
	}
	reg := getRemoteRegistry()
	if reg == nil {
		return
	}
	// Try normalized match first, then strip date suffix (e.g.
	// claude-sonnet-4-20250514 → claude-sonnet-4).
	entry, ok := reg[normalizeModelID(m.ID)]
	if !ok {
		stripped := stripDateSuffix(m.ID)
		if stripped != m.ID {
			entry, ok = reg[normalizeModelID(stripped)]
		}
	}
	if !ok {
		return
	}
	m.InputPrice = entry.InputPrice
	m.OutputPrice = entry.OutputPrice
	m.CacheRead = entry.CacheRead
	m.CacheWrite = entry.CacheWrite
}

// stripDateSuffix removes trailing -YYYYMMDD or -YYYYMM from model IDs.
func stripDateSuffix(id string) string {
	// Match -YYYYMMDD (8 digits) or -YYYYMM (6 digits) at end
	n := len(id)
	if n > 9 && id[n-9] == '-' {
		allDigits := true
		for _, c := range id[n-8:] {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return id[:n-9]
		}
	}
	if n > 7 && id[n-7] == '-' {
		allDigits := true
		for _, c := range id[n-6:] {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return id[:n-7]
		}
	}
	return id
}

// modelSupportsThinking checks if a model has thinking capability.
// Checks: in-memory model cache → OpenRouter catalog → hardcoded registry → false.
// ModelSupportsThinking is the public API — accepts "provider/model" or bare model ID.
// ModelSupportsThinking checks if a model supports extended thinking.
// Uses a provider lookup function to check the authoritative provider cache first.
// Falls back to the OpenRouter catalog and hardcoded registry.
func ModelSupportsThinking(fullModel string) bool {
	_, model := parseModel(fullModel)
	return modelSupportsThinking(model)
}

// ModelSupportsThinkingWithLookup checks thinking support using a provider cache lookup.
// Use this when a provider instance is available for authoritative data.
func ModelSupportsThinkingWithLookup(fullModel string, lookup func(modelID string) *types.ModelMeta) bool {
	_, modelID := parseModel(fullModel)
	if lookup != nil {
		if meta := lookup(modelID); meta != nil {
			return meta.Thinking
		}
	}
	return modelSupportsThinking(modelID)
}

func modelSupportsThinking(model string) bool {
	// 1. Remote OpenRouter catalog — broad coverage, kept fresh
	if r := lookupRemote(model); r != nil {
		return r.Thinking
	}
	// 2. Hardcoded registry — last-resort static data for models not in OpenRouter
	if meta, ok := hardcodedRegistry[normalizeModelID(model)]; ok {
		return meta.Thinking
	}
	// 3. Infer from model name — last resort for very new models
	lower := strings.ToLower(model)
	if strings.Contains(lower, "claude") && (strings.Contains(lower, "opus") || strings.Contains(lower, "sonnet")) {
		return true
	}
	return false
}

// ── Inference helpers ─────────────────────────────────────────────────────

// inferVision returns true if the model name implies vision support.
func InferVision(id string) bool {
	visionMarkers := []string{"vl", "vision", "gemma3", "gemma4", "llava", "bakllava", "minicpm-v", "qwen2.5vl"}
	for _, m := range visionMarkers {
		if containsInsensitive(id, m) {
			return true
		}
	}
	return false
}

// inferContextWindow returns a reasonable context window based on model name.
func InferContextWindow(id string) int {
	million := []string{"deepseek-v4", "minimax-m2", "kimi-k2", "glm-5"}
	for _, m := range million {
		if containsInsensitive(id, m) {
			return 1000000
		}
	}
	large := []string{"deepseek", "qwen", "kimi", "minimax", "gemini", "gpt-oss", "glm"}
	for _, m := range large {
		if containsInsensitive(id, m) {
			return 128000
		}
	}
	return 32000
}

func containsInsensitive(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// ParseModel splits "provider/model" — local copy to avoid circular imports.
func parseModel(full string) (provider, model string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	if strings.HasPrefix(full, "claude-") {
		return "claude-oauth", full
	}
	if strings.HasPrefix(full, "gpt-") || strings.HasPrefix(full, "o1-") ||
		strings.HasPrefix(full, "o3-") || strings.HasPrefix(full, "o4-") {
		return "openai", full
	}
	return "claude-oauth", full
}
