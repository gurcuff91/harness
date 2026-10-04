package config

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gurcuff91/harness/configstore"
)

// ErrInvalidCredential is returned by SetCredential when the credential fails
// validation. Detectable with errors.Is.
var ErrInvalidCredential = errors.New("invalid credential")

// CredentialsManager is harness's typed, validated view of provider
// credentials. It owns the domain rules (one typed credential per provider,
// per-type required fields) and delegates ALL persistence to a
// configstore.CredentialsStore underneath — files, locking, cross-process
// freshness, and the atomicity UpdateCredential relies on are the store's
// job, never this manager's. Credentials are INTERNAL: they are never
// exposed over the HTTP API or a CLI command — only connect / disconnect
// read and write them.
//
// Store layout: namespace "providers", key = provider name, value = a
// ProviderCredential JSON document.
type CredentialsManager struct {
	store configstore.CredentialsStore
}

// nsProviders is the credentials-store namespace holding one
// ProviderCredential per provider name.
const nsProviders = "providers"

// ProviderCredential is the complete authentication data for one provider. Only
// the fields relevant to Type are populated.
type ProviderCredential struct {
	Type             string `json:"type"` // "api_key" | "oauth"
	APIKey           string `json:"api_key,omitempty"`
	AccessToken      string `json:"access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"` // optional (oauth)
	AccountID        string `json:"account_id,omitempty"`        // optional (codex-oauth) — per-request header account id
}

// NewCredentialsManager returns a manager backed by store.
func NewCredentialsManager(store configstore.CredentialsStore) *CredentialsManager {
	return &CredentialsManager{store: store}
}

// UpdateCredential performs an atomic read-modify-write of one provider's
// credential: fn receives the freshest stored credential (and whether one
// exists) and returns:
//   - next:  the value to persist (ignored if write is false)
//   - write: whether to persist next at all — return false to make this a
//     pure read-then-decide with no write (e.g. "another process already
//     refreshed for us, nothing to do")
//   - err:   fn's own error (e.g. a refresh call failed); propagated to the
//     caller, no write happens
//
// The whole read → fn → write cycle runs inside ONE
// CredentialsStore.SwapValue call, so its atomicity — including across
// several harness processes — is the store's guarantee. fn must not call
// back into this manager (it runs while the store holds whatever guarantees
// that atomicity; for the file store, a non-re-entrant cross-process lock).
//
// Use case (see claude_oauth.go's getValidToken): re-check whether the token
// is still valid — another process may have refreshed while THIS one was
// waiting or sleeping through a retry backoff — and only refresh (and
// persist) if it's genuinely still expired. OAuth refresh tokens are
// SINGLE-USE: without this atomicity, two processes could both read the same
// refresh_token and both redeem it — only one succeeds, the other gets a
// permanent invalid_grant.
func (m *CredentialsManager) UpdateCredential(
	provider string,
	fn func(current ProviderCredential, ok bool) (next ProviderCredential, write bool, err error),
) error {
	return m.store.SwapValue(nsProviders, provider, func(raw []byte, found bool) ([]byte, bool, error) {
		var current ProviderCredential
		ok := found && json.Unmarshal(raw, &current) == nil
		next, write, err := fn(current, ok)
		if err != nil || !write {
			return nil, false, err
		}
		if err := validateCredential(next); err != nil {
			return nil, false, err
		}
		encoded, err := json.Marshal(next)
		if err != nil {
			return nil, false, fmt.Errorf("config: encode credential %q: %w", provider, err)
		}
		return encoded, true, nil
	})
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
	var c ProviderCredential
	return c, getJSON(m.store, nsProviders, provider, &c)
}

// APIKey returns the stored API key for a provider, or ("", false) if there is
// no credential or it is not an api_key credential.
func (m *CredentialsManager) APIKey(provider string) (string, bool) {
	c, ok := m.Credential(provider)
	if !ok || c.Type != "api_key" || c.APIKey == "" {
		return "", false
	}
	return c.APIKey, true
}

// OAuth returns the stored OAuth credential for a provider, or (zero, false) if
// there is no credential or it is not an oauth credential.
func (m *CredentialsManager) OAuth(provider string) (ProviderCredential, bool) {
	c, ok := m.Credential(provider)
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
	raw, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("config: encode credential %q: %w", provider, err)
	}
	return m.store.Set(nsProviders, provider, raw)
}

// DeleteCredential removes a provider's credential.
func (m *CredentialsManager) DeleteCredential(provider string) error {
	return m.store.Delete(nsProviders, provider)
}
