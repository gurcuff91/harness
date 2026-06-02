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
