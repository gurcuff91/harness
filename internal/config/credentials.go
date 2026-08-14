package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrInvalidCredential is returned by SetCredential when the credential fails
// validation. Detectable with errors.Is.
var ErrInvalidCredential = errors.New("invalid credential")

// CredentialsManager is a thread-safe, typed store for provider credentials,
// backed by ~/.harness/credentials.json (0600). Each provider has ONE typed
// credential entry (not a scatter of prefixed keys). Credentials are INTERNAL:
// they are never exposed over the HTTP API or a CLI command — only connect /
// disconnect read and write them.
type CredentialsManager struct {
	mu       sync.RWMutex
	path     string
	data     credentialsData
	loadedAt time.Time // mtime of the file as of the last load() — see reloadIfStale
}

type credentialsData struct {
	Providers map[string]ProviderCredential `json:"providers,omitempty"`
}

// ProviderCredential is the complete authentication data for one provider. Only
// the fields relevant to Type are populated.
type ProviderCredential struct {
	Type             string `json:"type"` // "api_key" | "oauth"
	APIKey           string `json:"api_key,omitempty"`
	AccessToken      string `json:"access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"` // optional (oauth)
}

func newCredentialsManager() *CredentialsManager {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".harness")
	_ = os.MkdirAll(dir, 0700)
	m := &CredentialsManager{
		path: filepath.Join(dir, "credentials.json"),
	}
	m.load()
	return m
}

// ── Cross-process freshness ───────────────────────────────────────────────
//
// m.mu (sync.RWMutex) only protects goroutines WITHIN this process. Multiple
// harness processes (TUI + Telegram + Slack + serve, or several TUI windows,
// all common to run at once) each have their own, unrelated in-memory copy of
// credentials.json — none of them see a write another process makes, because
// m.data was populated once at startup and every read method used to serve
// straight from that stale in-memory snapshot forever. In practice: process A
// refreshes an expired OAuth token (or a user runs /connect there) and
// persists the new one; every OTHER running instance kept using its own
// stale copy — including the now-redeemed refresh token — and got a
// permanent invalid_grant on its own next refresh attempt, forcing a manual
// /connect in EVERY instance instead of just the one.
//
// reloadIfStale (called at the top of every public read method) closes that
// gap cheaply: os.Stat is far lighter than the os.ReadFile+json.Unmarshal a
// full load() does, so comparing mtimes on every read barely costs anything,
// and only triggers a real reload when the file has actually changed
// underneath this process.
func (m *CredentialsManager) reloadIfStale() {
	info, err := os.Stat(m.path)
	if err != nil {
		return // no file yet (never connected) — nothing to reload
	}
	m.mu.RLock()
	stale := info.ModTime().After(m.loadedAt)
	m.mu.RUnlock()
	if !stale {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-check under the write lock: another goroutine may have already
	// reloaded while we were waiting for it.
	if info.ModTime().After(m.loadedAt) {
		m.load()
	}
}

// WithLock runs fn while holding the cross-process credentials.json lock —
// use it to wrap a read-modify-write cycle that spans multiple manager calls
// (e.g. "re-read the latest refresh_token, then redeem it, then persist the
// result"), where locking only the final write would still leave the read
// racing a concurrent writer. SetCredential/DeleteCredential already lock
// internally for their own single read-modify-write; WithLock is for callers
// (like claude_oauth's token refresh) that need the lock held across several
// manager calls as one atomic unit.
//
// credentials.json is read and potentially rewritten on every LLM call whose
// OAuth token is close to expiring (see claude_oauth.go's getValidToken), so
// the read-check-refresh-write cycle races far more often than a one-shot
// write like instances.json's. And OAuth refresh tokens are SINGLE-USE: if
// two processes both read the same refresh_token before either writes the
// new one, both attempt to redeem it — only one succeeds, the other gets a
// permanent invalid_grant from the provider. This lock closes that window by
// serializing the ENTIRE read-modify-write cycle across processes, not just
// the final write.
func (m *CredentialsManager) WithLock(fn func() error) error {
	release, err := acquireFileLock(m.path)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// validateCredential enforces the required fields per credential type: an
// api_key credential needs an APIKey; an oauth credential needs access, refresh
// and expiry (subscription type is optional).
func validateCredential(c ProviderCredential) error {
	switch c.Type {
	case "api_key":
		if c.APIKey == "" {
			return fmt.Errorf("%w: api_key credential requires api_key", ErrInvalidCredential)
		}
	case "oauth":
		if c.AccessToken == "" {
			return fmt.Errorf("%w: oauth credential requires access_token", ErrInvalidCredential)
		}
		if c.RefreshToken == "" {
			return fmt.Errorf("%w: oauth credential requires refresh_token", ErrInvalidCredential)
		}
		if c.ExpiresAt == 0 {
			return fmt.Errorf("%w: oauth credential requires expires_at", ErrInvalidCredential)
		}
	default:
		return fmt.Errorf("%w: type must be \"api_key\" or \"oauth\", got %q", ErrInvalidCredential, c.Type)
	}
	return nil
}

// Credential returns the stored credential for a provider by name (any type).
func (m *CredentialsManager) Credential(provider string) (ProviderCredential, bool) {
	m.reloadIfStale()
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.data.Providers[provider]
	return c, ok
}

// APIKey returns the stored API key for a provider, or ("", false) if there is
// no credential or it is not an api_key credential.
func (m *CredentialsManager) APIKey(provider string) (string, bool) {
	m.reloadIfStale()
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.data.Providers[provider]
	if !ok || c.Type != "api_key" || c.APIKey == "" {
		return "", false
	}
	return c.APIKey, true
}

// OAuth returns the stored OAuth credential for a provider, or (zero, false) if
// there is no credential or it is not an oauth credential.
func (m *CredentialsManager) OAuth(provider string) (ProviderCredential, bool) {
	m.reloadIfStale()
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.data.Providers[provider]
	if !ok || c.Type != "oauth" || c.AccessToken == "" {
		return ProviderCredential{}, false
	}
	return c, true
}

// SetCredential validates and stores (or replaces) a provider's credential.
// Holds the cross-process file lock for the full read-modify-write cycle: it
// re-reads the latest data from disk first (another process may have changed
// an unrelated provider's credential, or refreshed this same one, since this
// manager's in-memory copy was last synced), applies this change on top, then
// persists — so a concurrent writer's update is never silently discarded.
func (m *CredentialsManager) SetCredential(provider string, cred ProviderCredential) error {
	if err := validateCredential(cred); err != nil {
		return err
	}
	release, err := acquireFileLock(m.path)
	if err != nil {
		return err
	}
	defer release()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.load() // pick up whatever the latest writer persisted before we overwrite
	if m.data.Providers == nil {
		m.data.Providers = make(map[string]ProviderCredential)
	}
	m.data.Providers[provider] = cred
	return m.save()
}

// SetCredentialLocked is SetCredential for a caller that ALREADY holds the
// cross-process file lock via WithLock. It does the exact same read-modify-write
// (validate → reload latest disk → apply → persist) but does NOT call
// acquireFileLock itself — the file lock is not re-entrant, so acquiring it a
// second time from inside a WithLock closure deadlocks until it times out
// (~2s), which caused the write to silently fail. That failure was catastrophic
// for OAuth refresh: the single-use refresh token had already been redeemed on
// the wire, but the rotated result never reached disk, so the next process read
// the now-consumed old token and got a permanent invalid_grant. Callers NOT
// already under WithLock must use SetCredential.
func (m *CredentialsManager) SetCredentialLocked(provider string, cred ProviderCredential) error {
	if err := validateCredential(cred); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	if m.data.Providers == nil {
		m.data.Providers = make(map[string]ProviderCredential)
	}
	m.data.Providers[provider] = cred
	return m.save()
}

// DeleteCredential removes a provider's credential. Same lock-then-reload
// pattern as SetCredential — see its comment.
func (m *CredentialsManager) DeleteCredential(provider string) error {
	release, err := acquireFileLock(m.path)
	if err != nil {
		return err
	}
	defer release()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.load()
	delete(m.data.Providers, provider)
	return m.save()
}

// load reads credentials.json from disk into m.data and records the file's
// mtime (at the moment of the read) in m.loadedAt — the baseline
// reloadIfStale compares future os.Stat calls against. Caller holds m.mu (at
// least for writing loadedAt/data) — see reloadIfStale, SetCredential,
// DeleteCredential, and newCredentialsManager for its call sites.
func (m *CredentialsManager) load() {
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

func (m *CredentialsManager) save() error {
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
