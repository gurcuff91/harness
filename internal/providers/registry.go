package providers

import (
	"errors"
	"fmt"
	"reflect"
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

// ErrNotActive is returned (wrapped) by Resolve when the provider exists but has
// no usable credentials right now (never connected, or disconnected since).
var ErrNotActive = errors.New("provider not active")

// ErrModelNotFound is returned (wrapped) by Resolve when the provider is active
// but does not offer the requested model (retired, renamed, or the provider has
// no models at all).
var ErrModelNotFound = errors.New("model not available")

// IsUnavailable reports whether err means "this provider/model can't be used
// right now" — as opposed to a malformed request. It is the test for falling
// back to another model: the provider is gone, not connected, or no longer
// offers the model.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotActive) || errors.Is(err, ErrModelNotFound)
}

// ErrNameConflict is returned (wrapped) by ReloadCustom when a settings.json
// entry was skipped because a provider registered from code already uses its
// name. The programmatic provider wins; the rest of the reload is applied.
var ErrNameConflict = errors.New("custom provider name already used by a provider registered from code")

// ReloadCustom re-reads settings.json's "provider" collection from the active
// SettingsStore and swaps the settings-declared providers in the registry —
// live, with agents and servers running. Built-in and programmatic
// (RegisterOpenAI) providers are never touched. Same rules as at start: disabled
// entries and reserved names are skipped, in name order.
//
// One copy-on-write swap: snapshots already handed out (All) stay intact, so
// a turn in flight finishes with the provider it had; sessions re-resolve
// their provider by name at their next turn (an edited provider is picked up,
// a deleted/disabled one fails with ErrNotFound).
//
// An entry whose config is unchanged keeps its existing provider object (and
// model cache) — reloading twice without changes leaves the registry
// identical. An edited entry gets a fresh provider (empty model cache: its
// models are re-fetched on demand).
//
// Name conflicts: if a provider registered from code already uses an entry's
// name, the programmatic one WINS and the entry is skipped; the rest of the
// reload is still applied, and the returned error (wrapping ErrNameConflict)
// lists the skipped names.
func ReloadCustom() error {
	EnsureRegistry()
	fresh := buildCustomProviders(config.GetSettingsManager().CustomProviders())

	registryMu.Lock()
	defer registryMu.Unlock()

	current := map[string]customProvider{} // settings-declared, by name
	programmatic := map[string]bool{}
	var kept []Provider // built-in + programmatic, in registry order
	for _, p := range all {
		if c, ok := p.(customProvider); ok {
			if c.meta().fromSettings {
				current[c.Name()] = c
				continue
			}
			if c.meta().programmatic {
				programmatic[c.Name()] = true
			}
		}
		kept = append(kept, p)
	}

	var conflicts []string
	next := append(make([]Provider, 0, len(kept)+len(fresh)), kept...)
	for _, p := range fresh {
		c := p.(customProvider)
		if programmatic[c.Name()] {
			conflicts = append(conflicts, c.Name())
			continue
		}
		// Unchanged entry: keep the object and its model cache. settingsCfg
		// includes the entry's Type, so switching a name from one type to
		// another is a change and gets a fresh provider of the new type.
		if old, ok := current[c.Name()]; ok && reflect.DeepEqual(old.meta().settingsCfg, c.meta().settingsCfg) {
			next = append(next, old.(Provider))
			continue
		}
		next = append(next, c)
	}
	all = next // copy-on-write swap

	if len(conflicts) > 0 {
		return fmt.Errorf("%w: skipped %s (the programmatic provider wins)", ErrNameConflict, strings.Join(conflicts, ", "))
	}
	return nil
}

// IsSettingsDeclared reports whether p was declared in settings.json's
// "provider" collection (the kind ReloadCustom manages).
func IsSettingsDeclared(p Provider) bool {
	c, ok := p.(customProvider)
	return ok && c.meta().fromSettings
}

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

	// Custom providers (settings.json's "provider" collection), read at start;
	// ReloadCustom re-reads them live.
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
		// TODO(openai-responses): a third custom type, "openai-responses", is
		// still missing — it is what would let one harness instance consume the
		// codex-oauth of another through its proxy, the way "anthropic" does for
		// claude-oauth. It needs a CustomResponses provider (the Codex request
		// translation and stream parser already exist in codex_oauth.go and can
		// be reused) plus a case here, a line in validateCustomProvider, the
		// CLI's --type, the OpenAPI enum and a RegisterResponses. It was left out
		// of the "anthropic" change on purpose: it could not be tried live while
		// the only codex-oauth login at hand had an expired token. The proxy side
		// already speaks it (dialect "openai-responses"); only the consuming side
		// is absent.
		switch cfg.Type {
		case "openai":
			out = append(out, NewCustomOpenAI(name, cfg))
		case "anthropic":
			out = append(out, NewCustomAnthropic(name, cfg))
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
		customMeta:     customMeta{programmatic: true},
		name:           name,
		displayName:    display,
		baseURL:        url,
		headers:        headers,
		reasoningSplit: reasoningSplit,
		fetchModelsFn:  fetchModels,
		client:         llm.NewProviderClient(0),
		cache:          make(map[string]types.ModelMeta),
	})
	all = next // copy-on-write: snapshots already handed out stay intact
	return nil
}

// RegisterAnthropic is RegisterOpenAI for an Anthropic Messages-compatible
// endpoint (agent.NewAnthropicProvider is its public wrapper): it builds a
// CustomAnthropic and appends it to the registry. url is the API root WITHOUT
// "/v1" (…/v1/messages and …/v1/models are appended), so pointing at another
// harness's proxy is just its "/api/providers/<name>/proxy" root. Same rules as
// RegisterOpenAI: reserved and duplicate names are rejected, nothing is written
// to settings.json or credentials.json, and authentication is entirely headers.
func RegisterAnthropic(name, url, display string, headers map[string]string, fetchModels func() ([]types.ModelMeta, error)) error {
	if config.IsReservedProviderName(name) {
		return fmt.Errorf("%q is a built-in provider name and cannot be used for a custom provider", name)
	}
	EnsureRegistry()

	registryMu.Lock()
	defer registryMu.Unlock()
	for _, p := range all {
		if p.Name() == name {
			return fmt.Errorf("provider %q is already registered", name)
		}
	}
	all = append(append(make([]Provider, 0, len(all)+1), all...), &CustomAnthropic{
		customMeta:    customMeta{programmatic: true},
		name:          name,
		displayName:   display,
		baseURL:       url,
		headers:       headers,
		fetchModelsFn: fetchModels,
		client:        llm.NewProviderClient(0),
		cache:         make(map[string]types.ModelMeta),
	})
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
		if c, ok := p.(customProvider); !ok || !c.meta().programmatic {
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
		return nil, "", fmt.Errorf("%w: provider %q is not active (missing credentials)", ErrNotActive, providerName)
	}

	// 3. Lazy fetch if cache is empty
	if len(p.Models()) == 0 {
		_, _ = p.FetchModels()
	}

	// 4. Default model or validate
	if modelID == "" {
		models := p.Models()
		if len(models) == 0 {
			return nil, "", fmt.Errorf("%w: provider %q has no available models", ErrModelNotFound, providerName)
		}
		modelID = models[0].ID
	} else if p.ModelMeta(modelID) == nil {
		return nil, "", fmt.Errorf("%w: model %q not found in provider %q", ErrModelNotFound, modelID, providerName)
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
