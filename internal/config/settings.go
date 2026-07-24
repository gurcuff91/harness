package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrInvalidMCPServer is returned by SetMCPServer when the server config fails
// validation. Callers (e.g. the HTTP API) can detect it with errors.Is to map
// it to a 422 Unprocessable Entity.
var ErrInvalidMCPServer = errors.New("invalid mcp server")

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
}

// SettingsManager is a thread-safe store for harness settings.
// Backed by ~/.harness/settings.json.
//
// Design: the manager is an AGNOSTIC typed store. It exposes methods only for
// GENERAL, known settings — core singletons (ActiveModel, ThinkingLevel) and
// keyed collections (Providers, MCP servers). It never contains logic specific
// to a concrete provider (e.g. "ollama"). Interpreting a ProviderConfig —
// applying env-var cascades, defaults, etc. — is the responsibility of the
// provider itself. The manager just stores and returns typed values by name.
type SettingsManager struct {
	mu   sync.RWMutex
	path string
	data settingsData
}

// settingsData is the on-disk representation. Field names, struct tags, and the
// REST API (see server SettingsDTO) all share ONE vocabulary.
type settingsData struct {
	// Core singletons.
	ActiveModel   string `json:"active_model,omitempty"`
	ThinkingLevel string `json:"thinking_level,omitempty"`

	// Keyed collections (dynamic entries by name).
	Providers map[string]ProviderConfig `json:"providers,omitempty"` // key = provider name
	MCP       map[string]MCPServer      `json:"mcp,omitempty"`       // key = server name
}

// ProviderConfig is the generic, per-provider configuration the manager stores
// verbatim. Fields are optional; a zero value means "not configured" and the
// owning provider should fall back to its own default. Grows on demand — only
// add a field when a provider actually consumes it.
type ProviderConfig struct {
	URL string `json:"url,omitempty"`
}

// MCPServer is the configuration of one MCP (Model Context Protocol) server.
// The transport is INFERRED, not declared: a server with a Command is local
// (spawns a process); a server with a URL is remote (dials HTTP). Declaring
// both is invalid. Servers are enabled by default — set Disabled to turn one
// off without deleting it.
//
// On-disk shape (settings.json):
//
//	local:  { "command": "npx", "args": ["-y", "@mcp/fs"], "env": {...} }
//	remote: { "url": "https://…/mcp", "headers": {...} }
//	off:    add "disabled": true to either
type MCPServer struct {
	Command  string            `json:"command,omitempty"`  // local: executable
	Args     []string          `json:"args,omitempty"`     // local: arguments
	URL      string            `json:"url,omitempty"`      // remote: server URL
	Env      map[string]string `json:"env,omitempty"`      // local: process env vars
	Headers  map[string]string `json:"headers,omitempty"`  // remote: custom HTTP headers
	Cwd      string            `json:"cwd,omitempty"`      // local: working directory (optional)
	Timeout  int               `json:"timeout,omitempty"`  // ms for connect (initialize+tools/list); 0 = default 5000
	Disabled bool              `json:"disabled,omitempty"` // enabled by default; set true to skip
}

// IsRemote reports whether the server is a remote (HTTP) transport. A server is
// remote when it has a URL; otherwise it is local (stdio). Validation
// guarantees exactly one of Command/URL is set before this is consulted.
func (s MCPServer) IsRemote() bool { return s.URL != "" }

// Argv returns the full local command line (executable + args) for the stdio
// transport. Empty when Command is unset.
func (s MCPServer) Argv() []string {
	if s.Command == "" {
		return nil
	}
	return append([]string{s.Command}, s.Args...)
}

func newSettingsManager() *SettingsManager {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".harness")
	_ = os.MkdirAll(dir, 0700)
	m := &SettingsManager{
		path: filepath.Join(dir, "settings.json"),
	}
	m.load()
	return m
}

// ── Domain methods ───────────────────────────────────────────────────────

// ActiveModel returns the persisted active model ("provider/model").
func (m *SettingsManager) ActiveModel() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data.ActiveModel
}

// SetActiveModel persists the active model.
func (m *SettingsManager) SetActiveModel(model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.ActiveModel = model
	return m.save()
}

// ThinkingLevel returns the persisted thinking level. The settings file is the
// single source of truth; per-invocation overrides use the CLI/TUI --thinking
// flag (which also validates), not an environment variable.
func (m *SettingsManager) ThinkingLevel() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data.ThinkingLevel
}

// SetThinkingLevel validates and persists the thinking level. Accepted values:
// off | low | medium | high | xhigh. Validating here means every caller (HTTP
// PATCH, session command, ...) gets the same guarantee.
func (m *SettingsManager) SetThinkingLevel(level string) error {
	if !thinkingLevels[level] {
		return fmt.Errorf("%w: %q (want off|low|medium|high|xhigh)", ErrInvalidThinkingLevel, level)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.ThinkingLevel = level
	return m.save()
}

// ── Providers collection ─────────────────────────────────────────────────
// The manager stores ProviderConfig verbatim by name. It applies NO cascade,
// env logic, or defaults — that is the owning provider's job.

// Provider returns the stored config for a provider by name.
func (m *SettingsManager) Provider(name string) (ProviderConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.data.Providers[name]
	return cfg, ok
}

// Providers returns a defensive copy of the whole providers collection.
func (m *SettingsManager) Providers() map[string]ProviderConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]ProviderConfig, len(m.data.Providers))
	for k, v := range m.data.Providers {
		out[k] = v
	}
	return out
}

// SetProvider stores (or replaces) a provider's config.
func (m *SettingsManager) SetProvider(name string, cfg ProviderConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.Providers == nil {
		m.data.Providers = make(map[string]ProviderConfig)
	}
	m.data.Providers[name] = cfg
	return m.save()
}

// DeleteProvider removes a provider's config.
func (m *SettingsManager) DeleteProvider(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data.Providers, name)
	return m.save()
}

// ── MCP servers collection ───────────────────────────────────────────────
// Same agnostic pattern as Providers: keyed by server name, stored verbatim.

// MCPServer returns the stored config for an MCP server by name.
func (m *SettingsManager) MCPServer(name string) (MCPServer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	srv, ok := m.data.MCP[name]
	return srv, ok
}

// MCPServers returns a defensive copy of the whole MCP collection.
func (m *SettingsManager) MCPServers() map[string]MCPServer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]MCPServer, len(m.data.MCP))
	for k, v := range m.data.MCP {
		out[k] = v
	}
	return out
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.MCP == nil {
		m.data.MCP = make(map[string]MCPServer)
	}
	m.data.MCP[name] = srv
	return m.save()
}

// DeleteMCPServer removes an MCP server's config.
func (m *SettingsManager) DeleteMCPServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data.MCP, name)
	return m.save()
}

// ── Internal ─────────────────────────────────────────────────────────────

func (m *SettingsManager) load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &m.data)
}

func (m *SettingsManager) save() error {
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0600)
}
