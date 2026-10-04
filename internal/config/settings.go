package config

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/types"
)

// MCPServer is an alias of the public types.* shape — it lives in types/ so
// the client SDK can use it without importing this internal package. This file
// remains the domain owner (validation, persistence); the shape itself is
// defined once, in types.
type MCPServer = types.MCPServer

// CustomProvider mirrors MCPServer's own alias pattern — the shape lives
// once in types, this package owns validation/persistence.
type CustomProvider = types.CustomProvider

// ErrInvalidMCPServer is returned by SetMCPServer when the server config fails
// validation. Callers (e.g. the HTTP API) can detect it with errors.Is to map
// it to a 422 Unprocessable Entity.
var ErrInvalidMCPServer = errors.New("invalid mcp server")

// ErrInvalidCustomProvider is returned by SetCustomProvider when the
// provider config fails validation. Same errors.Is → 422 mapping as
// ErrInvalidMCPServer.
var ErrInvalidCustomProvider = errors.New("invalid custom provider")

// reservedProviderNames are the built-in provider Name() values
// (internal/providers/*.go) a custom provider must never collide with —
// kept here (not in internal/providers) because that package already
// imports internal/config (for credential/settings access), so the
// dependency can only point this direction without creating an import
// cycle. internal/providers/registry.go's initRegistry re-checks this same
// set defensively before constructing a CustomOpenAI (belt and suspenders:
// this validation at write time is the primary guard, that one guards
// against a config file hand-edited to bypass it).
var reservedProviderNames = map[string]bool{
	"anthropic":    true,
	"claude-oauth": true,
	"codex-oauth":  true,
	"minimax":      true,
	"ollama-cloud": true,
	"ollama":       true,
	"openai":       true,
	"opencode-go":  true,
}

// IsReservedProviderName reports whether name collides with a built-in
// provider's Name() — exported so internal/providers/registry.go's
// initRegistry can defensively re-check it without duplicating the set.
func IsReservedProviderName(name string) bool { return reservedProviderNames[name] }

// ErrInvalidThinkingLevel is returned by SetThinkingLevel for an unknown level.
// Detectable with errors.Is for a 422 mapping.
var ErrInvalidThinkingLevel = errors.New("invalid thinking level")

// thinkingLevels is the canonical set of accepted thinking levels. Internal to
// config — the source of truth for what SetThinkingLevel will store.
var thinkingLevels = map[string]bool{
	"off":    true,
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
	"max":    true,
}

// ValidThinkingLevel reports whether level is an accepted thinking level
// (off|low|medium|high|xhigh|max). Exposed so callers can VALIDATE a level
// without persisting it — e.g. a session's /thinking command applies the
// level to the live session only, and must reject an invalid value first
// without touching the global default that SetThinkingLevel would write.
func ValidThinkingLevel(level string) bool {
	return thinkingLevels[level]
}

// SettingsManager is harness's typed, validated view of its non-sensitive,
// process-global settings. It owns every domain rule — accepted thinking
// levels, MCP server shape, custom-provider naming — and delegates ALL
// persistence to a configstore.SettingsStore underneath (the same split as
// agent/store's *Session over SessionStore). Storage concerns — files,
// locking, cross-process freshness — belong entirely to the store.
//
// Store layout (namespace → key → JSON value):
//
//	core     → active_model, thinking_level   (JSON strings)
//	mcp      → <server name>                  (types.MCPServer)
//	provider → <provider name>                (types.CustomProvider)
//
// Every value is its own key, so writes to different settings never
// contend: changing the active model can't clobber an MCP server another
// process just added.
//
// Read methods return the zero value when the store fails, preserving their
// existing signatures (a missing or unreadable setting falls back to the
// default, exactly as an absent settings.json always did); write methods
// return the store's error.
type SettingsManager struct {
	store configstore.SettingsStore
}

// Namespaces and keys of the settings layout — the single source of truth
// for where each setting lives in the store.
const (
	nsCore           = "core"
	nsMCP            = "mcp"
	nsProvider       = "provider"
	keyActiveModel   = "active_model"
	keyThinkingLevel = "thinking_level"
)

// NewSettingsManager returns a manager backed by store.
func NewSettingsManager(store configstore.SettingsStore) *SettingsManager {
	return &SettingsManager{store: store}
}

// ── Core settings ────────────────────────────────────────────────────────

// ActiveModel returns the persisted active model ("provider/model"), or ""
// when unset.
func (m *SettingsManager) ActiveModel() string {
	var model string
	m.getJSON(nsCore, keyActiveModel, &model)
	return model
}

// SetActiveModel persists the active model.
func (m *SettingsManager) SetActiveModel(model string) error {
	return m.setJSON(nsCore, keyActiveModel, model)
}

// ThinkingLevel returns the persisted thinking level, or "" when unset. The
// store is the single source of truth; per-invocation overrides use the
// CLI/TUI --thinking flag (which also validates), not an environment
// variable.
func (m *SettingsManager) ThinkingLevel() string {
	var level string
	m.getJSON(nsCore, keyThinkingLevel, &level)
	return level
}

// SetThinkingLevel validates and persists the thinking level. Accepted values:
// off | low | medium | high | xhigh | max. Validating here means every
// caller (HTTP PATCH, session command, ...) gets the same guarantee.
func (m *SettingsManager) SetThinkingLevel(level string) error {
	if !thinkingLevels[level] {
		return fmt.Errorf("%w: %q (want off|low|medium|high|xhigh|max)", ErrInvalidThinkingLevel, level)
	}
	return m.setJSON(nsCore, keyThinkingLevel, level)
}

// ── MCP servers collection ───────────────────────────────────────────────
// Agnostic pattern: keyed by server name, stored verbatim.

// MCPServer returns the stored config for an MCP server by name.
func (m *SettingsManager) MCPServer(name string) (MCPServer, bool) {
	var srv MCPServer
	return srv, m.getJSON(nsMCP, name, &srv)
}

// MCPServers returns the whole MCP collection (a fresh map the caller owns).
func (m *SettingsManager) MCPServers() map[string]MCPServer {
	return listJSON[MCPServer](m.store, nsMCP)
}

// validateMCPServer enforces the inferred-transport rule: EXACTLY one of
// Command (local) or URL (remote) must be set. Declaring both is ambiguous;
// declaring neither is empty. Living here (not in the API) means EVERY caller
// gets the same guarantee.
func validateMCPServer(srv MCPServer) error {
	hasCmd := srv.Command != ""
	hasURL := srv.URL != ""
	switch {
	case hasCmd && hasURL:
		return fmt.Errorf("%w: set either \"command\" (local) or \"url\" (remote), not both", ErrInvalidMCPServer)
	case !hasCmd && !hasURL:
		return fmt.Errorf("%w: requires \"command\" (local) or \"url\" (remote)", ErrInvalidMCPServer)
	}
	return nil
}

// SetMCPServer validates and stores (or replaces) an MCP server's config. The
// transport is inferred from which of command/url is set.
func (m *SettingsManager) SetMCPServer(name string, srv MCPServer) error {
	if err := validateMCPServer(srv); err != nil {
		return err
	}
	return m.setJSON(nsMCP, name, srv)
}

// DeleteMCPServer removes an MCP server's config.
func (m *SettingsManager) DeleteMCPServer(name string) error {
	return m.store.Delete(nsMCP, name)
}

// ── Custom providers collection ─────────────────────────────────────────
// Exact same agnostic pattern as MCP servers above: keyed by provider name,
// stored verbatim. internal/providers/registry.go's initRegistry reads this
// collection once at process start (no hot reload — same trade-off MCP
// servers already accept) to construct a CustomOpenAI per enabled entry.

// CustomProvider returns the stored config for a custom provider by name.
func (m *SettingsManager) CustomProvider(name string) (CustomProvider, bool) {
	var p CustomProvider
	return p, m.getJSON(nsProvider, name, &p)
}

// CustomProviders returns the whole custom-provider collection (a fresh map
// the caller owns).
func (m *SettingsManager) CustomProviders() map[string]CustomProvider {
	return listJSON[CustomProvider](m.store, nsProvider)
}

// validateCustomProvider enforces: Type must be the one currently supported
// dialect ("openai"); URL must be set; and name must not collide with a
// RESERVED built-in provider name (see reservedProviderNames) — a custom
// provider shadowing e.g. "anthropic" would be genuinely ambiguous (which
// one does "anthropic/claude-..." resolve to?) and is rejected outright
// rather than deciding an implicit precedence order.
func validateCustomProvider(name string, p CustomProvider) error {
	if p.Type != "openai" {
		return fmt.Errorf("%w: unsupported type %q (only \"openai\" is supported for now)", ErrInvalidCustomProvider, p.Type)
	}
	if p.URL == "" {
		return fmt.Errorf("%w: \"url\" is required", ErrInvalidCustomProvider)
	}
	if IsReservedProviderName(name) {
		return fmt.Errorf("%w: %q is a built-in provider name and cannot be used for a custom provider", ErrInvalidCustomProvider, name)
	}
	return nil
}

// SetCustomProvider validates and stores (or replaces) a custom provider's
// config.
func (m *SettingsManager) SetCustomProvider(name string, p CustomProvider) error {
	if err := validateCustomProvider(name, p); err != nil {
		return err
	}
	return m.setJSON(nsProvider, name, p)
}

// DeleteCustomProvider removes a custom provider's config.
func (m *SettingsManager) DeleteCustomProvider(name string) error {
	return m.store.Delete(nsProvider, name)
}

// ── Internal ─────────────────────────────────────────────────────────────

// getJSON decodes (namespace, key) into out, reporting whether a value was
// found and decoded. A store error or an undecodable value counts as "not
// found" — see the SettingsManager doc comment for why reads don't surface
// errors.
func (m *SettingsManager) getJSON(namespace, key string, out any) bool {
	return getJSON(m.store, namespace, key, out)
}

// setJSON encodes v and stores it under (namespace, key).
func (m *SettingsManager) setJSON(namespace, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("config: encode %s/%s: %w", namespace, key, err)
	}
	return m.store.Set(namespace, key, raw)
}
