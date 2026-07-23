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
// Mirrors the shape used by other agents (e.g. OpenCode): a "local" server runs
// a command with an Env map; a "remote" server dials a URL with custom Headers.
type MCPServer struct {
	Type    string            `json:"type"`              // "local" | "remote"
	Command []string          `json:"command,omitempty"` // local: command + args
	URL     string            `json:"url,omitempty"`     // remote: server URL
	Env     map[string]string `json:"env,omitempty"`     // local: process env vars
	Headers map[string]string `json:"headers,omitempty"` // remote: custom HTTP headers
	Cwd     string            `json:"cwd,omitempty"`     // local: working directory (optional)
	Timeout int               `json:"timeout,omitempty"` // ms for connect (initialize+tools/list); 0 = default 5000
	Enabled bool              `json:"enabled"`
}

// mcpServerRaw mirrors MCPServer but with `command` as json.RawMessage, so the
// custom UnmarshalJSON can detect whether the user wrote it as a string
// (Claude Desktop / OpenCode style: "command": "uvx", "args": [...]) or as a
// flat array (the canonical shape MCPServer.Command expects).
type mcpServerRaw struct {
	Type    string            `json:"type"`
	Command json.RawMessage   `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
	Enabled bool              `json:"enabled"`
}

// UnmarshalJSON accepts both the canonical `command: ["uvx", "arg1"]` shape
// AND the Claude Desktop / OpenCode `command: "uvx", "args: ["arg1"]` shape,
// merging the latter into the former. Without this, users copying a config
// from Claude Desktop would see "mcp stdio: empty command" with no visible
// error because the malformed fields would be silently dropped.
func (s *MCPServer) UnmarshalJSON(data []byte) error {
	var raw mcpServerRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.Type = raw.Type
	s.URL = raw.URL
	s.Env = raw.Env
	s.Headers = raw.Headers
	s.Cwd = raw.Cwd
	s.Timeout = raw.Timeout
	s.Enabled = raw.Enabled
	s.Command = decodeMCPCommand(raw.Command, raw.Args)
	return nil
}

// decodeMCPCommand merges the two accepted command shapes:
//   - canonical: `command: ["uvx", "arg1", "arg2"]`           → ["uvx", "arg1", "arg2"]
//   - Claude / OpenCode: `command: "uvx", args: ["arg1"]`     → ["uvx", "arg1"]
//   - mixed: `command: ["uvx"], args: ["arg1"]`                → ["uvx", "arg1"]
//   - empty: command absent + args absent                       → nil
func decodeMCPCommand(cmd json.RawMessage, args []string) []string {
	if len(cmd) > 0 {
		// Try array form first.
		var arr []string
		if err := json.Unmarshal(cmd, &arr); err == nil {
			if len(args) > 0 {
				return append(arr, args...)
			}
			return arr
		}
		// Fall back to single-string form.
		var str string
		if err := json.Unmarshal(cmd, &str); err == nil && str != "" {
			if len(args) > 0 {
				out := make([]string, 0, 1+len(args))
				out = append(out, str)
				out = append(out, args...)
				return out
			}
			return []string{str}
		}
	}
	if len(args) > 0 {
		return args
	}
	return nil
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

// canonicalMCPType maps friendly transport aliases to the canonical values the
// rest of the code expects: "http"/"sse" → "remote", "stdio" → "local". This
// lets users write the more intuitive "http" in settings.json.
func canonicalMCPType(t string) string {
	switch t {
	case "http", "sse", "streamable-http":
		return "remote"
	case "stdio":
		return "local"
	default:
		return t
	}
}

// validateMCPServer enforces the minimal shape of an MCP server: type must be
// "local" or "remote"; a local server needs a command; a remote server needs a
// URL. Living here (not in the API) means EVERY caller gets the same guarantee.
func validateMCPServer(srv MCPServer) error {
	switch srv.Type {
	case "local":
		if len(srv.Command) == 0 {
			return fmt.Errorf("%w: local server requires a command", ErrInvalidMCPServer)
		}
	case "remote":
		if srv.URL == "" {
			return fmt.Errorf("%w: remote server requires a url", ErrInvalidMCPServer)
		}
	default:
		return fmt.Errorf("%w: type must be \"local\" or \"remote\", got %q", ErrInvalidMCPServer, srv.Type)
	}
	return nil
}

// SetMCPServer validates and stores (or replaces) an MCP server's config. The
// transport type is canonicalized (http→remote, stdio→local) before validation.
func (m *SettingsManager) SetMCPServer(name string, srv MCPServer) error {
	srv.Type = canonicalMCPType(srv.Type)
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
	// Canonicalize MCP transport aliases from hand-edited files (http→remote,
	// stdio→local) so the manager always sees canonical types.
	for name, srv := range m.data.MCP {
		if c := canonicalMCPType(srv.Type); c != srv.Type {
			srv.Type = c
			m.data.MCP[name] = srv
		}
	}
}

func (m *SettingsManager) save() error {
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0600)
}
