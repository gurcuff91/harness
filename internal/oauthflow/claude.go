package oauthflow

// Claude (Anthropic) OAuth PKCE flow. Obtains tokens directly — no Claude Code
// install, no keychain read. Validated end-to-end against a real account: a
// token harness obtains itself is accepted by Anthropic identically to one
// from Claude Code's keychain (harness masquerades as Claude Code via the same
// public client_id, scopes, and identity headers as
// internal/providers/claude_oauth.go's buildCCHeaders).
//
// ─────────────────────────────────────────────────────────────────────────
// LEGACY (disabled): the pre-native path read tokens from Claude Code's macOS
// keychain / credentials file instead of running the OAuth flow itself. Kept
// here, commented out, as a revert-fast fallback during the native flow's
// trial phase. Delete once the native flow is proven in the field (along with
// the imports it needed: encoding/json, os, os/exec, path/filepath, runtime,
// strings).
//
/*
func obtainClaudeOAuthFromDisk() (*types.Credentials, error) {
	// macOS: encrypted Keychain (fallback to file); Linux/Windows:
	// ~/.claude/.credentials.json (or $CLAUDE_CONFIG_DIR).
	if runtime.GOOS == "darwin" {
		if creds := readClaudeCredentialsFromKeychain(); creds != nil {
			return creds, nil
		}
	}
	if creds := readClaudeCredentialsFromFile(); creds != nil {
		return creds, nil
	}
	return nil, fmt.Errorf("no Claude credentials found — run 'claude auth login' to authenticate, then reconnect\n  (install Claude Code: npm install -g @anthropic-ai/claude-code)")
}

func readClaudeCredentialsFromKeychain() *types.Credentials {
	if t := readKeychainItem("Claude Code-credentials"); t != nil {
		return t
	}
	return readKeychainItem("claude-code")
}

func readKeychainItem(service string) *types.Credentials {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &raw); err != nil {
		return nil
	}
	data := raw
	if nested, ok := raw["claudeAiOauth"].(map[string]any); ok {
		data = nested
	}
	at, _ := data["accessToken"].(string)
	rt, _ := data["refreshToken"].(string)
	ea, _ := data["expiresAt"].(float64)
	st, _ := data["subscriptionType"].(string)
	if at == "" || rt == "" {
		return nil
	}
	return &types.Credentials{
		Type: types.CredTypeOAuth, AccessToken: at, RefreshToken: rt,
		ExpiresAt: int64(ea), SubscriptionType: st,
	}
}

func claudeCredentialsFilePath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".credentials.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", ".credentials.json")
}

func readClaudeCredentialsFromFile() *types.Credentials {
	data, err := os.ReadFile(claudeCredentialsFilePath())
	if err != nil {
		return nil
	}
	var creds struct {
		OAuthTokens []struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
			SubType      string `json:"subscriptionType"`
		} `json:"oauthTokens"`
	}
	if err := json.Unmarshal(data, &creds); err != nil || len(creds.OAuthTokens) == 0 {
		return nil
	}
	t := creds.OAuthTokens[0]
	return &types.Credentials{
		Type: types.CredTypeOAuth, AccessToken: t.AccessToken,
		RefreshToken: t.RefreshToken, ExpiresAt: t.ExpiresAt, SubscriptionType: t.SubType,
	}
}
*/
// ─────────────────────────────────────────────────────────────────────────

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

// claudeTokenURLs are the token endpoints, newest first — Anthropic migrated
// the endpoint over time, so postToken tries them in order (same list, same
// reason as internal/providers/claude_oauth.go's refresh path).
var claudeTokenURLs = []string{
	"https://platform.claude.com/v1/oauth/token",
	"https://console.anthropic.com/v1/oauth/token",
}

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
		claudeTokenURLs,
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
