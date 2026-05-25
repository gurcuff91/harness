package providers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── In-memory model cache ──────────────────────────────

type ModelMeta struct {
	ID            string  `json:"id"`
	DisplayName   string  `json:"display_name,omitempty"`
	ContextWindow int     `json:"context_window"`
	MaxTokens     int     `json:"max_tokens"`
	Vision        bool    `json:"vision"`
	Thinking      bool    `json:"thinking"`
	// Pricing ($ per 1M tokens) — sourced from llm-registry for all providers
	InputCost      float64 `json:"input_cost,omitempty"`
	OutputCost     float64 `json:"output_cost,omitempty"`
	CacheReadCost  float64 `json:"cache_read_cost,omitempty"`
	CacheWriteCost float64 `json:"cache_write_cost,omitempty"`
}

type ModelInfo struct {
	Name     string
	Provider string
	Active   bool
}
var (
	modelCache   = make(map[string]*ModelMeta)
	modelCacheMu sync.RWMutex
)

func ParseModelKey(full string) (provider, model string) { return ParseModel(full) }

func RefreshModels() {
	var wg sync.WaitGroup
	if HasOAuth("claude-oauth") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tm, _ := NewTokenManager(); tm != nil {
				if tok, err := tm.GetValidToken(); err == nil {
					for _, m := range fetchAnthropicModels(tok) {
						modelCacheMu.Lock()
						modelCache["claude-oauth/"+m.ID] = &m
						modelCacheMu.Unlock()
					}
				}
			}
		}()
	}
	if HasAPIKey("anthropic") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, m := range fetchAnthropicModels(GetAPIKey("anthropic")) {
				modelCacheMu.Lock()
				modelCache["anthropic/"+m.ID] = &m
				modelCacheMu.Unlock()
			}
		}()
	}
	if HasAPIKey("openai") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, m := range openAIModels() {
				modelCacheMu.Lock()
				modelCache["openai/"+m.ID] = &m
				modelCacheMu.Unlock()
			}
		}()
	}
	if HasAPIKey("opencode-go") {
		for _, m := range fetchOpenCodeGoModels() {
			modelCacheMu.Lock()
			modelCache["opencode-go/"+m.ID] = &m
			modelCacheMu.Unlock()
		}
	}
	if HasAPIKey("ollama-cloud") {
		for _, m := range fetchOllamaCloudModels(GetAPIKey("ollama-cloud")) {
			modelCacheMu.Lock()
			modelCache["ollama-cloud/"+m.ID] = &m
			modelCacheMu.Unlock()
		}
	}
	if OllamaAvailable() {
		modelCacheMu.Lock()
		for _, m := range FetchOllamaModels() {
			modelCache["ollama/"+m.ID] = &m
		}
		modelCacheMu.Unlock()
	}
	wg.Wait()
}

func RefreshProviderModels(provider string) {
	switch provider {
	case "claude-oauth":
		if tm, _ := NewTokenManager(); tm != nil {
			if tok, err := tm.GetValidToken(); err == nil {
				for _, m := range fetchAnthropicModels(tok) {
					modelCacheMu.Lock()
					modelCache["claude-oauth/"+m.ID] = &m
					modelCacheMu.Unlock()
				}
			}
		}
	case "anthropic":
		if HasAPIKey("anthropic") {
			for _, m := range fetchAnthropicModels(GetAPIKey("anthropic")) {
				modelCacheMu.Lock()
				modelCache["anthropic/"+m.ID] = &m
				modelCacheMu.Unlock()
			}
		}
	case "openai":
		for _, m := range openAIModels() {
			modelCacheMu.Lock()
			modelCache["openai/"+m.ID] = &m
			modelCacheMu.Unlock()
		}
	case "ollama":
		for _, m := range FetchOllamaModels() {
			modelCacheMu.Lock()
			modelCache["ollama/"+m.ID] = &m
			modelCacheMu.Unlock()
		}
	case "ollama-cloud":
		if HasAPIKey("ollama-cloud") {
			for _, m := range fetchOllamaCloudModels(GetAPIKey("ollama-cloud")) {
				modelCacheMu.Lock()
				modelCache["ollama-cloud/"+m.ID] = &m
				modelCacheMu.Unlock()
			}
		}
	case "opencode-go":
		if HasAPIKey("opencode-go") {
			for _, m := range fetchOpenCodeGoModels() {
				modelCacheMu.Lock()
				modelCache["opencode-go/"+m.ID] = &m
				modelCacheMu.Unlock()
			}
		}
	}
}

func DetectAvailable(currentModel string) []ModelInfo {
	modelCacheMu.RLock()
	defer modelCacheMu.RUnlock()
	seen := map[string]bool{}
	var result []ModelInfo
	for fullName := range modelCache {
		provider, name := ParseModel(fullName)
		if seen[fullName] {
			continue
		}
		seen[fullName] = true
		result = append(result, ModelInfo{Name: name, Provider: provider, Active: fullName == currentModel})
	}
	return result
}

func GetModelMeta(fullModel string) *ModelMeta {
	modelCacheMu.RLock()
	defer modelCacheMu.RUnlock()
	return modelCache[fullModel]
}

func ModelCount(provider string) int {
	modelCacheMu.RLock()
	defer modelCacheMu.RUnlock()
	n := 0
	for k := range modelCache {
		if p, _ := ParseModel(k); p == provider {
			n++
		}
	}
	return n
}

func AllModels() map[string]*ModelMeta {
	modelCacheMu.RLock()
	defer modelCacheMu.RUnlock()
	out := make(map[string]*ModelMeta, len(modelCache))
	for k, v := range modelCache {
		out[k] = v
	}
	return out
}

// ── API fetching ───────────────────────────────

func fetchAnthropicModels(tokenOrKey string) []ModelMeta {
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
	req.Header.Set("x-api-key", tokenOrKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer "+tokenOrKey)
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		req.Header.Set("x-app", "cli")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return nil
		}
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}

	var result struct {
		Data []struct {
			ID             string `json:"id"`
			DisplayName    string `json:"display_name"`
			MaxInputTokens int    `json:"max_input_tokens"`
			MaxOutputTokens int   `json:"max_tokens"`
			Capabilities   struct {
				Vision   bool `json:"vision"`
				Thinking bool `json:"extended_thinking"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	var models []ModelMeta
	for _, m := range result.Data {
		if len(m.ID) == 0 || m.ID[0] != 'c' {
			continue
		}
		cw := m.MaxInputTokens
		if cw <= 0 {
			cw = 200000
		}
		mt := m.MaxOutputTokens
		if mt <= 0 {
			mt = 64000
		}
		meta := ModelMeta{ // Anthropic API is authoritative for caps
			ID: m.ID, DisplayName: m.DisplayName,
			ContextWindow: cw, MaxTokens: mt,
			Vision: m.Capabilities.Vision,
			Thinking: m.Capabilities.Thinking,
		}
		ApplyRegistryPricing(&meta) // pricing always from registry
		models = append(models, meta)
	}
	return models
}

func openAIModels() []ModelMeta {
	ids := []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "o1", "o3", "o3-mini", "o4-mini"}
	var models []ModelMeta
	for _, id := range ids {
		if m := LookupModel(id); m != nil {
			models = append(models, *m)
		} else {
			models = append(models, ModelMeta{ID: id})
		}
	}
	return models
}

func fetchOllamaCloudModels(apiKey string) []ModelMeta {
	// Get model list
	req, _ := http.NewRequest("GET", "https://ollama.com/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil { resp.Body.Close() }
		return nil
	}
	defer resp.Body.Close()

	var list struct {
		Data []struct{ ID string `json:"id"` } `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil
	}

	// Enrich each model from ollama.com/api/show
	var models []ModelMeta
	for _, item := range list.Data {
		meta := ModelMeta{
			ID:            item.ID,
			ContextWindow: inferContextWindow(item.ID),
			MaxTokens:     32000,
			Vision:        inferVision(item.ID),
		}
		if info := fetchOllamaModelInfo(item.ID); info != nil {
			meta = *info  // /api/show is authoritative — no enrich
		}
		models = append(models, meta)
	}
	return models
}

// fetchOllamaModelInfo gets real capabilities from ollama.com/api/show
func fetchOllamaModelInfo(name string) *ModelMeta {
	body, _ := json.Marshal(map[string]string{"name": name})
	req, _ := http.NewRequest("POST", "https://ollama.com/api/show", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil { resp.Body.Close() }
		return nil
	}
	defer resp.Body.Close()

	var info struct {
		ModelInfo    map[string]any `json:"model_info"`
		Capabilities []string       `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil
	}

	meta := &ModelMeta{ID: name, MaxTokens: 32000}
	for k, v := range info.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if f, ok := v.(float64); ok {
				meta.ContextWindow = int(f)
			}
		}
	}
	if meta.ContextWindow == 0 {
		meta.ContextWindow = inferContextWindow(name)
	}
	for _, cap := range info.Capabilities {
		switch cap {
		case "vision":
			meta.Vision = true
		case "thinking":
			meta.Thinking = true
		}
	}
	ApplyRegistryPricing(meta) // pricing always from registry
	return meta
}

// inferVision returns true if the model name implies vision support.
func inferVision(id string) bool {
	visionMarkers := []string{"vl", "vision", "gemma3", "gemma4", "llava", "bakllava", "minicpm-v", "qwen2.5vl"}
	for _, m := range visionMarkers {
		if containsInsensitive(id, m) {
			return true
		}
	}
	return false
}

// inferContextWindow returns a reasonable context window based on model name.
func inferContextWindow(id string) int {
	// 1M context models
	million := []string{"deepseek-v4", "minimax-m2", "kimi-k2", "glm-5"}
	for _, m := range million {
		if containsInsensitive(id, m) {
			return 1000000
		}
	}
	// Large context models (128k)
	large := []string{"deepseek", "qwen", "kimi", "minimax", "gemini", "gpt-oss", "glm"}
	for _, m := range large {
		if containsInsensitive(id, m) {
			return 128000
		}
	}
	return 32000
}

func containsInsensitive(s, sub string) bool {
	sl := strings.ToLower(s)
	return strings.Contains(sl, strings.ToLower(sub))
}
