package oauthflow

// Claude (Anthropic) OAuth PKCE flow. Obtains tokens directly — no Claude Code
// install, no keychain read. Validated end-to-end against a real account: a
// token harness obtains itself is accepted by Anthropic identically to one
// from Claude Code's keychain (harness masquerades as Claude Code via the same
// public client_id, scopes, and identity headers as
// internal/providers/claude_oauth.go's buildCCHeaders).

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// Claude Code OAuth constants (public, shared with claude_oauth.go's refresh).
const (
	claudeClientID   = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeAuthURL    = "https://claude.ai/oauth/authorize"
	claudeRedirect   = "https://platform.claude.com/oauth/code/callback"
	claudeOAuthScope = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

	// claudeDefaultExpiresIn is used when the token endpoint omits expires_in
	// (it returns 28800 = 8h in practice).
	claudeDefaultExpiresIn = 28800
)

// claudeTokenURL is the single OAuth token endpoint (same one, same reason as
// internal/providers/claude_oauth.go's refresh path). An earlier version tried
// two endpoints in series as a migration fallback, but the authorization code
// is single-use: if the first endpoint redeems it and its response is lost, the
// fallback redeems a consumed code and the login fails. Both endpoints were
// verified live and identical, so the fallback bought no resilience anyway.
const claudeTokenURL = "https://platform.claude.com/v1/oauth/token"

// claudeOauthFlow implements OauthFlow for Claude. Unexported — callers get it
// as an OauthFlow via NewClaudeOauthFlow.
type claudeOauthFlow struct {
	verifier string
	state    string
}

// NewClaudeOauthFlow returns Claude's OAuth PKCE flow. No provider argument —
// the caller already knows it wants Claude. Returns the OauthFlow interface,
// not the concrete type, so callers depend only on the contract.
func NewClaudeOauthFlow() OauthFlow {
	return &claudeOauthFlow{}
}

// Start generates PKCE, builds Claude's authorization URL, opens the browser,
// and returns the URL. See OauthFlow.Start.
func (f *claudeOauthFlow) Start() (string, error) {
	verifier, challenge := generatePKCE()
	f.verifier = verifier
	f.state = randomURLSafe(32)

	q := url.Values{}
	q.Set("client_id", claudeClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", claudeRedirect)
	q.Set("scope", claudeOAuthScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", f.state)
	authURL := claudeAuthURL + "?" + q.Encode()

	openBrowser(authURL) // best-effort; caller also prints the URL

	return authURL, nil
}

// Exchange swaps the authorization code for credentials. The code may arrive
// as "CODE" or "CODE#STATE" — Claude's callback page concatenates the state
// with a '#', stripped here. See OauthFlow.Exchange.
func (f *claudeOauthFlow) Exchange(code string) (*types.Credentials, error) {
	code = strings.TrimSpace(code)
	if idx := strings.Index(code, "#"); idx != -1 {
		code = code[:idx]
	}
	if code == "" {
		return nil, fmt.Errorf("empty authorization code")
	}
	if f.verifier == "" {
		return nil, fmt.Errorf("Exchange called before Start")
	}

	return postToken(
		claudeTokenURL,
		map[string]string{
			"grant_type":    "authorization_code",
			"client_id":     claudeClientID,
			"code":          code,
			"state":         f.state,
			"redirect_uri":  claudeRedirect,
			"code_verifier": f.verifier,
		},
		claudeDefaultExpiresIn,
		"", // subscription type not returned by this flow
	)
}
