package providers

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers/llm"
	"github.com/gurcuff91/harness/types"
)

// all is the provider registry. It is COPY-ON-WRITE: a writer (initRegistry,
// RegisterOpenAI, Unregister) builds a NEW slice and swaps it in under
// registryMu; readers take the current slice via All() and iterate it with
// no lock held. A slice handed out is never mutated afterwards, so iterating
// it is race-free while providers are (un)registered live — an SDK embedder
// connecting/disconnecting providers while agents and servers run.
var all = []Provider{}

var initOnce sync.Once

// registryMu guards all (the slice header); see all's doc comment.
var registryMu sync.RWMutex

// All returns a snapshot of the registry: safe to iterate concurrently with
// (un)registration, never mutated after it's returned.
func All() []Provider {
	EnsureRegistry()
	registryMu.RLock()
	defer registryMu.RUnlock()
	return all
}

// Find returns the registered provider named name (nil if none).
func Find(name string) Provider {
	for _, p := range All() {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// ErrNotFound is returned when no provider is registered under a name.
var ErrNotFound = errors.New("provider not found")

// ErrNotRemovable is returned by Unregister for a provider that wasn't
// registered programmatically (built-in, or declared in settings.json).
var ErrNotRemovable = errors.New("provider cannot be unregistered")

func initRegistry() {
	list := []Provider{}
	if oauth, err := NewClaudeOAuth(); err == nil {
		list = append(list, oauth)
	}
	if codex, err := NewCodexOAuth(); err == nil {
		list = append(list, codex)
	}
	list = append(list,
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
	list = append(list, buildCustomProviders(config.GetSettingsManager().CustomProviders())...)
	registryMu.Lock()
	all = list
	registryMu.Unlock()
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
	initOnce.Do(func() {
		initRegistry()
		// When the model catalog is refreshed in the background (it was
		// served from the disk cache, or a first fetch failed and was later
		// retried successfully), re-fetch each active provider's model list so
		// the prices/capabilities it cached are recomputed from the fresh
		// catalog — no restart needed.
		llm.OnRegistryUpdate(refreshCachedModels)
	})
}

// refreshCachedModels re-fetches the models of every active provider that
// already has a cached model list (best effort).
func refreshCachedModels() {
	for _, p := range All() {
		if p.IsActive() && len(p.Models()) > 0 {
			_, _ = p.FetchModels()
		}
	}
}

// RegisterOpenAI builds a CustomOpenAI provider and appends it to the
// global registry — the internal implementation
// agent.NewOpenAIProvider (a public, SDK-safe wrapper never exposing this
// package's Provider interface or any other internal/… type) calls this.
// Returns an error if name collides with a reserved built-in provider
// name; never touches settings.json or credentials.json. There is no
// apiKey parameter — CustomOpenAI authenticates entirely via headers
// (see its own doc comment for why), symmetric with the declarative
// settings.json path: neither carries a separate "API key" concept.
//
// fetchModels, if non-nil, becomes the provider's FetchModels — see
// CustomOpenAI.fetchModelsFn's doc comment. Passing nil uses the default
// HTTP-GET-<url>/models discovery (unfiltered, same as any
// settings.json-configured custom provider).
func RegisterOpenAI(name, url, display string, headers map[string]string, reasoningSplit bool, fetchModels func() ([]types.ModelMeta, error)) error {
	if config.IsReservedProviderName(name) {
		return fmt.Errorf("%q is a built-in provider name and cannot be used for a custom provider", name)
	}
	EnsureRegistry() // make sure the built-in/settings.json-configured providers are already registered

	registryMu.Lock()
	defer registryMu.Unlock()
	for _, p := range all {
		if p.Name() == name {
			return fmt.Errorf("provider %q is already registered", name)
		}
	}
	next := append(append(make([]Provider, 0, len(all)+1), all...), &CustomOpenAI{
		name:           name,
		displayName:    display,
		baseURL:        url,
		headers:        headers,
		reasoningSplit: reasoningSplit,
		fetchModelsFn:  fetchModels,
		client:         llm.NewProviderClient(0),
		cache:          make(map[string]types.ModelMeta),
		programmatic:   true,
	})
	all = next // copy-on-write: snapshots already handed out stay intact
	return nil
}

// Unregister removes a provider registered programmatically (RegisterOpenAI)
// so the name can be registered again (new URL/headers) in the same process.
// Built-in providers and settings.json-declared custom providers return
// ErrNotRemovable (the latter are managed through settings: they'd come back
// on the next start). An unknown name returns ErrNotFound.
func Unregister(name string) error {
	EnsureRegistry()
	registryMu.Lock()
	defer registryMu.Unlock()
	for i, p := range all {
		if p.Name() != name {
			continue
		}
		if c, ok := p.(*CustomOpenAI); !ok || !c.programmatic {
			return fmt.Errorf("%w: %q is built in or declared in settings.json", ErrNotRemovable, name)
		}
		next := make([]Provider, 0, len(all)-1)
		next = append(append(next, all[:i]...), all[i+1:]...)
		all = next // copy-on-write
		return nil
	}
	return fmt.Errorf("%w: %q", ErrNotFound, name)
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
	p := Find(providerName)
	if p == nil {
		return nil, "", fmt.Errorf("%w: %q", ErrNotFound, providerName)
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
	for _, p := range All() {
		if p.IsActive() {
			_, _ = p.FetchModels()
		}
	}
}

// RefreshProviderModels refreshes models for a single provider by name.
func RefreshProviderModels(providerName string) {
	for _, p := range All() {
		if p.Name() == providerName && p.IsActive() {
			_, _ = p.FetchModels()
			return
		}
	}
}
