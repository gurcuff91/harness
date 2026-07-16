package types

// ── Credentials ──────────────────────────────────────────────────────────

// CredentialType identifies what kind of credentials a provider expects.
type CredentialType string

const (
	CredTypeNone   CredentialType = "none"    // auto-detected (e.g. ollama via ping)
	CredTypeAPIKey CredentialType = "api_key" // single API key (anthropic, openai, etc.)
	CredTypeOAuth  CredentialType = "oauth"   // OAuth2 tokens (claude-oauth)
)

// Credentials holds provider authentication data.
// Only the fields relevant to Type are populated — others are empty.
type Credentials struct {
	Type CredentialType `json:"type"`

	// CredTypeAPIKey
	APIKey string `json:"api_key,omitempty"`

	// CredTypeOAuth
	AccessToken      string `json:"access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"` // Unix ms
	SubscriptionType string `json:"subscription_type,omitempty"`
}

// ── SDK read models ────────────────────────────────────────────────────────

// ProviderInfo is a read-only snapshot of a provider's identity and state,
// returned by the SDK. Provider administration (connect/disconnect) is done via
// the `harness` CLI, not the SDK — so this carries no credentials.
type ProviderInfo struct {
	Name           string         `json:"name"`            // slug, e.g. "anthropic"
	DisplayName    string         `json:"display_name"`    // human-friendly name
	Description    string         `json:"description"`     // live blurb (e.g. "12 models")
	Active         bool           `json:"active"`          // has valid credentials + reachable
	CredentialType CredentialType `json:"credential_type"` // none | api_key | oauth
	ModelCount     int            `json:"model_count"`     // number of available models
}

// ModelListing pairs a model's metadata with its owning provider, as returned by
// the SDK's Models() listing. Model is the fully-qualified "provider/model" id.
type ModelListing struct {
	Provider string `json:"provider"`
	Model    string `json:"model"` // "provider/model" — pass to NewSession
	ModelMeta
}

// ── Constructors ──────────────────────────────────────────────────────────

func APIKeyCredentials(key string) Credentials {
	return Credentials{Type: CredTypeAPIKey, APIKey: key}
}

func OAuthCredentials(access, refresh string, expiresAt int64, subType string) Credentials {
	return Credentials{
		Type:             CredTypeOAuth,
		AccessToken:      access,
		RefreshToken:     refresh,
		ExpiresAt:        expiresAt,
		SubscriptionType: subType,
	}
}
