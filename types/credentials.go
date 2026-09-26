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
	// AccountID is the ChatGPT account id some OAuth providers require as a
	// per-request header (codex-oauth: `ChatGPT-Account-ID`, extracted from
	// the login flow's id_token JWT). Empty for providers that don't need one
	// (claude-oauth).
	AccountID string `json:"account_id,omitempty"`
}

// ── SDK read models ────────────────────────────────────────────────────────

// ModelListing pairs a model's metadata with its owning provider, as returned by
// the SDK's Models() listing. Model is the fully-qualified "provider/model" id.
//
// Whether a model is billed as a flat-fee subscription lives on the embedded
// ModelMeta.IsSubscription — set per-provider in each provider's FetchModels
// (see internal/providers/{claude_oauth,codex_oauth,minimax,opencode_go}.go):
// unconditionally true for OAuth-only providers (claude-oauth, codex-oauth)
// and OpenCode Go (always a flat subscription despite an api_key credential),
// and heuristically for MiniMax (its "sk-cp-" Token Plan Subscription Key
// prefix — MiniMax exposes no authoritative endpoint to distinguish it from
// a regular pay-as-you-go api_key, so this is a best-effort, accepted
// trade-off, not a guarantee). This is deliberately distinct from
// Provider.IsSubscription (client/types.go) — the provider-level flag used
// to branch the /connect UX (OAuth flow vs. API-key prompt) — which stays
// exactly as-is, keyed off CredentialType == OAuth only, and must NOT be
// touched by this per-model logic.
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
	return OAuthCredentialsWithAccount(access, refresh, expiresAt, subType, "")
}

// OAuthCredentialsWithAccount is OAuthCredentials plus the optional per-
// request account id (codex-oauth's ChatGPT-Account-ID). The short
// OAuthCredentials form stays for providers without an account id
// (claude-oauth) — call sites read a little cleaner when the empty value
// isn't being threaded through.
func OAuthCredentialsWithAccount(access, refresh string, expiresAt int64, subType, accountID string) Credentials {
	return Credentials{
		Type:             CredTypeOAuth,
		AccessToken:      access,
		RefreshToken:     refresh,
		ExpiresAt:        expiresAt,
		SubscriptionType: subType,
		AccountID:        accountID,
	}
}
