package providers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// All is the fixed registry of provider instances.
var All = []Provider{}

var initOnce sync.Once

// registryMu guards WRITES to All — initRegistry's own construction and
// RegisterOpenAI's append. Reads throughout the codebase (agent.go,
// server.go, Resolve below, …) remain unprotected by design: the
// documented usage pattern is "call RegisterOpenAI in main(), before
// constructing any Agent" (see agent.NewOpenAIProvider's doc comment) —
// registration finishes before any reader goroutine exists, so this mutex
// only needs to protect concurrent registration calls against EACH OTHER
// and against initRegistry's own first-run construction, not against the
// wider read traffic that starts once Agents are up and running.
var registryMu sync.Mutex

func initRegistry() {
	All = []Provider{}
	if oauth, err := NewClaudeOAuth(); err == nil {
		All = append(All, oauth)
	}
	if codex, err := NewCodexOAuth(); err == nil {
		All = append(All, codex)
	}
	All = append(All,
		NewAnthropic(),
		NewOpenAI(),
		NewOpenCodeGo(),
		NewMiniMax(),
		NewOllamaCloud(),
		NewOllama(),
	)

	// Custom providers (settings.json's "provider" collection) — same
	// no-hot-reload contract as MCP servers: read once here, take effect
	// on the next process start.
	All = append(All, buildCustomProviders(config.GetSettingsManager().CustomProviders())...)
}

// buildCustomProviders constructs a Provider for each ENABLED, non-reserved
// entry in custom, in deterministic (sorted-by-name) order — same
// reasoning as mcp.Manager.Start()'s own sorted iteration. Pulled out of
// initRegistry as a pure function (takes the map instead of reading the
// config.GetSettingsManager() singleton itself) specifically so it's
// testable without touching the real ~/.harness/settings.json.
//
// A Disabled entry is skipped entirely — never constructed, never appears
// in the result, identical to how mcp.Manager.Start() treats a disabled
// MCP server. Names colliding with a reserved built-in are also skipped —
// defense in depth: SetCustomProvider already rejects that at write time,
// this only guards a hand-edited settings.json bypassing that check.
func buildCustomProviders(custom map[string]config.CustomProvider) []Provider {
	names := make([]string, 0, len(custom))
	for name := range custom {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []Provider
	for _, name := range names {
		cfg := custom[name]
		if cfg.Disabled || config.IsReservedProviderName(name) {
			continue
		}
		switch cfg.Type {
		case "openai":
			out = append(out, NewCustomOpenAI(name, cfg))
		}
	}
	return out
}

func EnsureRegistry() {
	initOnce.Do(initRegistry)
}

// RegisterOpenAI builds a CustomOpenAI provider and appends it to the
// global registry — the internal implementation
// agent.NewOpenAIProvider (a public, SDK-safe wrapper never exposing this
// package's Provider interface or any other internal/… type) calls this.
// Returns an error if name collides with a reserved built-in provider
// name; never touches settings.json or credentials.json — apiKey is used
// directly and only held in memory, exactly like a built-in api-key
// provider activated via its environment variable rather than `harness
// connect`.
//
// fetchModels, if non-nil, becomes the provider's FetchModels — see
// CustomOpenAI.fetchModelsFn's doc comment. Passing nil uses the default
// HTTP-GET-<url>/models discovery (unfiltered, same as any
// settings.json-configured custom provider).
func RegisterOpenAI(name, url, apiKey, display string, headers map[string]string, fetchModels func(apiKey string) ([]types.ModelMeta, error)) error {
	if config.IsReservedProviderName(name) {
		return fmt.Errorf("%q is a built-in provider name and cannot be used for a custom provider", name)
	}
	EnsureRegistry() // make sure the built-in/settings.json-configured providers are already in All

	registryMu.Lock()
	defer registryMu.Unlock()
	for _, p := range All {
		if p.Name() == name {
			return fmt.Errorf("provider %q is already registered", name)
		}
	}
	All = append(All, &CustomOpenAI{
		name:          name,
		displayName:   display,
		baseURL:       url,
		headers:       headers,
		apiKey:        apiKey,
		fetchModelsFn: fetchModels,
		client:        &http.Client{},
		cache:         make(map[string]types.ModelMeta),
	})
	return nil
}

// Resolve returns the provider and bare model ID for a "provider/model" string.
// It:
//  1. Splits "provider/model"
//  2. Finds the provider and checks credentials
//  3. Lazy-fetches models if cache is empty
//  4. Validates the model exists in that provider
//
// If only "provider" is given (no model), the first available model is used.
func Resolve(fullModel string) (Provider, string, error) {
	EnsureRegistry()

	// 1. Split "provider/model" — inline, no external dependency
	parts := strings.SplitN(fullModel, "/", 2)
	providerName := parts[0]
	modelID := ""
	if len(parts) == 2 {
		modelID = parts[1]
	}

	// 2. Find provider + check credentials
	var p Provider
	for _, candidate := range All {
		if candidate.Name() == providerName {
			p = candidate
			break
		}
	}
	if p == nil {
		return nil, "", fmt.Errorf("provider %q not found", providerName)
	}
	if !p.IsActive() {
		return nil, "", fmt.Errorf("provider %q is not active (missing credentials)", providerName)
	}

	// 3. Lazy fetch if cache is empty
	if len(p.Models()) == 0 {
		_, _ = p.FetchModels()
	}

	// 4. Default model or validate
	if modelID == "" {
		models := p.Models()
		if len(models) == 0 {
			return nil, "", fmt.Errorf("provider %q has no available models", providerName)
		}
		modelID = models[0].ID
	} else if p.ModelMeta(modelID) == nil {
		return nil, "", fmt.Errorf("model %q not found in provider %q", modelID, providerName)
	}

	return p, modelID, nil
}

// RefreshModels fetches models from all active providers.
// Used by the CLI on startup — not needed in SDK usage (Resolve handles lazy fetch).
func RefreshModels() {
	EnsureRegistry()
	for _, p := range All {
		if p.IsActive() {
			_, _ = p.FetchModels()
		}
	}
}

// RefreshProviderModels refreshes models for a single provider by name.
func RefreshProviderModels(providerName string) {
	EnsureRegistry()
	for _, p := range All {
		if p.Name() == providerName && p.IsActive() {
			_, _ = p.FetchModels()
			return
		}
	}
}
