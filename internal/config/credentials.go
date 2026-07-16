package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
func (m *CredentialsManager) SetCredential(provider string, cred ProviderCredential) error {
	if err := validateCredential(cred); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.Providers == nil {
		m.data.Providers = make(map[string]ProviderCredential)
	}
	m.data.Providers[provider] = cred
	return m.save()
}

// DeleteCredential removes a provider's credential.
func (m *CredentialsManager) DeleteCredential(provider string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
