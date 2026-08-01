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
	mu   sync.RWMutex
	path string
	data credentialsData
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

// ── Cross-process file lock ───────────────────────────────────────────────
//
// m.mu (sync.RWMutex) only protects goroutines WITHIN this process. Multiple
// harness processes (TUI + Telegram + Slack + serve, all common to run at
// once) each have their own, unrelated m.mu — none of them see each other.
// credentials.json is read and potentially rewritten on every LLM call whose
// OAuth token is close to expiring (see claude_oauth.go's getValidToken), so
// the read-check-refresh-write cycle races far more often than a one-shot
// write like instances.json's. And OAuth refresh tokens are SINGLE-USE: if
// two processes both read the same refresh_token before either writes the
// new one, both attempt to redeem it — only one succeeds, the other gets a
// permanent invalid_grant from the provider. This lock closes that window by
// serializing the ENTIRE read-modify-write cycle across processes, not just
// the final write.
//
// Same primitive as agent/... — wait, this lives in internal/config, so it
// has its own copy: os.OpenFile(O_CREATE|O_EXCL) is atomic across processes
// on every OS Go supports, no per-platform syscalls needed. A lock older than
// staleCredentialsLockAge is treated as abandoned (a crashed holder) and
// reclaimed automatically — never wedges every future credential operation.
const (
	credentialsLockRetryDelay  = 20 * time.Millisecond
	credentialsLockMaxAttempts = 100
	staleCredentialsLockAge    = 5 * time.Second
)

func credentialsLockPath(credsPath string) string {
	return credsPath + ".lock"
}

// acquireCredentialsLock creates credentialsLockPath() exclusively, retrying
// with backoff if another process holds it. Returns a release function the
// caller must call (removing the lock file) once done.
func acquireCredentialsLock(credsPath string) (release func(), err error) {
	path := credentialsLockPath(credsPath)
	for attempt := 0; attempt < credentialsLockMaxAttempts; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("credentials: create lock: %w", err)
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > staleCredentialsLockAge {
			_ = os.Remove(path)
			continue
		}
		time.Sleep(credentialsLockRetryDelay)
	}
	return nil, fmt.Errorf("credentials: timed out waiting for credentials.json lock")
}

// WithLock runs fn while holding the cross-process credentials.json lock —
// use it to wrap a read-modify-write cycle that spans multiple manager calls
// (e.g. "re-read the latest refresh_token, then redeem it, then persist the
// result"), where locking only the final write would still leave the read
// racing a concurrent writer. SetCredential/DeleteCredential already lock
// internally for their own single read-modify-write; WithLock is for callers
// (like claude_oauth's token refresh) that need the lock held across several
// manager calls as one atomic unit.
func (m *CredentialsManager) WithLock(fn func() error) error {
	release, err := acquireCredentialsLock(m.path)
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.data.Providers[provider]
	return c, ok
}

// APIKey returns the stored API key for a provider, or ("", false) if there is
// no credential or it is not an api_key credential.
func (m *CredentialsManager) APIKey(provider string) (string, bool) {
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
	release, err := acquireCredentialsLock(m.path)
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

// DeleteCredential removes a provider's credential. Same lock-then-reload
// pattern as SetCredential — see its comment.
func (m *CredentialsManager) DeleteCredential(provider string) error {
	release, err := acquireCredentialsLock(m.path)
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

func (m *CredentialsManager) load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &m.data)
}

func (m *CredentialsManager) save() error {
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0600)
}
