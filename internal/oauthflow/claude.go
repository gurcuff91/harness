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
// claudeOauthFlow is stateless — Start returns everything Exchange needs
// (auth URL + verifier) instead of storing it on the struct. Kept as a
// struct (rather than a plain function) only to satisfy the OauthFlow
// interface uniformly with codexOauthFlow, which DOES need per-instance
// state (its local listener's shutdown hook).
type claudeOauthFlow struct{}

// NewClaudeOauthFlow returns Claude's OAuth PKCE flow. No provider argument —
// the caller already knows it wants Claude. Returns the OauthFlow interface,
// not the concrete type, so callers depend only on the contract.
func NewClaudeOauthFlow() OauthFlow {
	return &claudeOauthFlow{}
}

// Start generates PKCE and builds Claude's authorization URL. Does not open
// a browser — see OauthFlow.Start's doc comment for why that moved to the
// caller. Returns the URL and the verifier the caller must pass back to
// Exchange.
//
// The URL's "state" parameter is a plain CSRF-guard random value, generated
// fresh per Start call. It is NOT persisted here — this flow instance may
// not even be the one whose Exchange gets called, in the stateless HTTP
// handler behind this package (see the package doc comment). Anthropic's
// callback page hands the state back to the user concatenated onto the
// code as "CODE#STATE", so it survives the round trip without harness
// needing to remember it: Exchange below extracts it from that suffix
// and echoes it in the token request — confirmed LIVE (2026-09-11) that
// Anthropic's token endpoint hard-requires state on the POST body: a
// request identical in every other field, missing only state, is
// rejected with 400 "invalid_request_error: Invalid request format";
// adding it back is the only change needed to get 200. This matches a
// third-party OAuth gateway's own documented fix for the exact same
// symptom (see the reference commit this comment is based on).
func (f *claudeOauthFlow) Start() (authURL, verifierCode string, err error) {
	verifier, challenge := generatePKCE()
	state := randomURLSafe(32)

	q := url.Values{}
	q.Set("client_id", claudeClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", claudeRedirect)
	q.Set("scope", claudeOAuthScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	authURL = claudeAuthURL + "?" + q.Encode()

	return authURL, verifier, nil
}

// Exchange swaps the authorization code for credentials. The pasted value
// arrives as "CODE" or "CODE#STATE" — Claude's callback page concatenates
// the state onto the code with a '#'. The state half, when present, MUST be
// echoed back in the token request body — see Start's doc comment for the
// live-confirmed 400 this endpoint returns without it. verifierCode must be
// the SAME value Start returned. See OauthFlow.Exchange.
func (f *claudeOauthFlow) Exchange(code, verifierCode string) (*types.Credentials, error) {
	code = strings.TrimSpace(code)
	var state string
	if idx := strings.Index(code, "#"); idx != -1 {
		state = code[idx+1:]
		code = code[:idx]
	}
	if code == "" {
		return nil, fmt.Errorf("empty authorization code")
	}
	if verifierCode == "" {
		return nil, fmt.Errorf("verifierCode is required (pass the value Start returned)")
	}

	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     claudeClientID,
		"code":          code,
		"redirect_uri":  claudeRedirect,
		"code_verifier": verifierCode,
	}
	if state != "" {
		body["state"] = state
	}

	return postToken(claudeTokenURL, body, claudeDefaultExpiresIn, "")
}
