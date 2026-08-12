package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gurcuff91/harness/types"
)

// MCPServer is an alias of the public types.* shape — it lives in types/ so
// the client SDK can use it without importing this internal package. This file
// remains the domain owner (validation, persistence); the shape itself is
// defined once, in types.
type MCPServer = types.MCPServer

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

// ValidThinkingLevel reports whether level is an accepted thinking level
// (off|low|medium|high|xhigh). Exposed so callers can VALIDATE a level without
// persisting it — e.g. a session's /thinking command applies the level to the
// live session only, and must reject an invalid value first without touching
// the global default that SetThinkingLevel would write.
func ValidThinkingLevel(level string) bool {
	return thinkingLevels[level]
}

// SettingsManager is a thread-safe store for harness settings.
// Backed by ~/.harness/settings.json.
//
// Design: the manager is an AGNOSTIC typed store. It exposes methods only for
// GENERAL, known settings — core singletons (ActiveModel, ThinkingLevel) and
// keyed collections (Providers, MCP servers). It never contains logic specific
// to a concrete provider. The manager just stores and returns typed values by
// name.
type SettingsManager struct {
	mu       sync.RWMutex
	path     string
	data     settingsData
	loadedAt time.Time // mtime of the file as of the last load() — see reloadIfStale
}

// settingsData is the on-disk representation. Field names, struct tags, and the
// REST API (see server SettingsDTO) all share ONE vocabulary.
type settingsData struct {
	// Core singletons.
	ActiveModel   string `json:"active_model,omitempty"`
	ThinkingLevel string `json:"thinking_level,omitempty"`

	// Keyed collection (dynamic entries by name).
	MCP map[string]MCPServer `json:"mcp,omitempty"` // key = server name
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

// ── Cross-process freshness ───────────────────────────────────────────────
//
// Same problem, same fix as CredentialsManager.reloadIfStale (see its comment
// for the full story): m.mu only guards goroutines within THIS process, so
// multiple harness processes (TUI + Telegram + Slack + serve, or several TUI
// windows) each held their own in-memory settings.json snapshot, populated
// once at startup, and every read method served straight from it forever —
// a /model or /mcp add in one instance was invisible to every other running
// instance until it restarted. reloadIfStale is called at the top of every
// public read method; os.Stat is far cheaper than the full os.ReadFile+
// json.Unmarshal a load() does, so this barely costs anything on the common
// case (file unchanged) and only reloads when it actually has to.
func (m *SettingsManager) reloadIfStale() {
	info, err := os.Stat(m.path)
	if err != nil {
		return // no file yet — nothing to reload
	}
	m.mu.RLock()
	stale := info.ModTime().After(m.loadedAt)
	m.mu.RUnlock()
	if !stale {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if info.ModTime().After(m.loadedAt) {
		m.load()
	}
}

// ── Domain methods ───────────────────────────────────────────────────────

// ActiveModel returns the persisted active model ("provider/model").
func (m *SettingsManager) ActiveModel() string {
	m.reloadIfStale()
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data.ActiveModel
}

// SetActiveModel persists the active model. Locked cross-process — re-reads
// the latest disk state first so a concurrent write to an unrelated field is
// never discarded (see SetMCPServer's comment for the full rationale).
func (m *SettingsManager) SetActiveModel(model string) error {
	release, err := acquireFileLock(m.path)
	if err != nil {
		return err
	}
	defer release()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	m.data.ActiveModel = model
	return m.save()
}

// ThinkingLevel returns the persisted thinking level. The settings file is the
// single source of truth; per-invocation overrides use the CLI/TUI --thinking
// flag (which also validates), not an environment variable.
func (m *SettingsManager) ThinkingLevel() string {
	m.reloadIfStale()
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
	release, err := acquireFileLock(m.path)
	if err != nil {
		return err
	}
	defer release()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	m.data.ThinkingLevel = level
	return m.save()
}

// ── MCP servers collection ───────────────────────────────────────────────
// Agnostic pattern: keyed by server name, stored verbatim.

// MCPServer returns the stored config for an MCP server by name.
func (m *SettingsManager) MCPServer(name string) (MCPServer, bool) {
	m.reloadIfStale()
	m.mu.RLock()
	defer m.mu.RUnlock()
	srv, ok := m.data.MCP[name]
	return srv, ok
}

// MCPServers returns a defensive copy of the whole MCP collection.
func (m *SettingsManager) MCPServers() map[string]MCPServer {
	m.reloadIfStale()
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
// transport is inferred from which of command/url is set. Locked
// cross-process: re-reads the latest disk state first (another process may
// have changed an unrelated setting since this manager's in-memory copy was
// last synced), applies this change on top, then persists — so a concurrent
// writer's update is never silently discarded.
func (m *SettingsManager) SetMCPServer(name string, srv MCPServer) error {
	if err := validateMCPServer(srv); err != nil {
		return err
	}
	release, err := acquireFileLock(m.path)
	if err != nil {
		return err
	}
	defer release()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	if m.data.MCP == nil {
		m.data.MCP = make(map[string]MCPServer)
	}
	m.data.MCP[name] = srv
	return m.save()
}

// DeleteMCPServer removes an MCP server's config. Same lock-then-reload
// pattern as SetMCPServer.
func (m *SettingsManager) DeleteMCPServer(name string) error {
	release, err := acquireFileLock(m.path)
	if err != nil {
		return err
	}
	defer release()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	delete(m.data.MCP, name)
	return m.save()
}

// ── Internal ─────────────────────────────────────────────────────────────

// load reads settings.json from disk into m.data and records the file's
// mtime (at the moment of the read) in m.loadedAt — the baseline
// reloadIfStale compares future os.Stat calls against. Caller holds m.mu.
func (m *SettingsManager) load() {
	info, statErr := os.Stat(m.path)
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &m.data)
	if statErr == nil {
		m.loadedAt = info.ModTime()
	}
}

func (m *SettingsManager) save() error {
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.path, data, 0600); err != nil {
		return err
	}
	if info, err := os.Stat(m.path); err == nil {
		m.loadedAt = info.ModTime()
	}
	return nil
}
