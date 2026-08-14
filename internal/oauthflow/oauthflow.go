// Package oauthflow implements provider OAuth PKCE login flows — harness
// obtains OAuth tokens ITSELF (browser + code exchange), without depending on
// a provider's own CLI being installed.
//
// The package is organized around one interface, OauthFlow, split into the two
// phases every transport drives separately:
//
//	Start()          → generate PKCE, open the browser, return the auth URL
//	Exchange(code)   → swap the pasted authorization code for credentials
//
// The CLI blocks on stdin between the two phases; the TUI drops into its
// value-capture and completes on the next submit — neither spawns a subprocess
// or leaves raw mode. This file holds the interface plus the pieces every
// provider's flow reuses (PKCE, browser, the generic token POST); each
// provider's specifics live in its own file (claude.go, and a future codex.go
// etc.), so adding a provider is one new file implementing OauthFlow — no
// switch to touch, no shared code to fork.
package oauthflow

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/gurcuff91/harness/types"
)

// For returns the OAuth flow for a provider by name. It is the ONE place that
// maps a provider to its flow, so callers that only know the provider name at
// runtime (the CLI's `case "oauth"` connect branch, the TUI's cmdConnect) get
// the right flow without their own switch — and adding a provider is one new
// case here plus its file, nothing at the call sites. An unknown provider is
// an error rather than a silent wrong-provider login.
func For(provider string) (OauthFlow, error) {
	switch provider {
	case "claude-oauth":
		return NewClaudeOauthFlow(), nil
	default:
		return nil, fmt.Errorf("no OAuth flow for provider: %s", provider)
	}
}

// OauthFlow is one provider's OAuth PKCE login flow. A single instance is
// single-use: call Start once, then Exchange once with the code the user
// pastes back. Obtain one via For(provider) (the runtime-dispatch entry point)
// or a concrete constructor (e.g. NewClaudeOauthFlow) when the provider is
// known at compile time.
type OauthFlow interface {
	// Start generates the PKCE verifier/challenge, opens the user's browser at
	// the provider's authorization URL, and returns that URL (so the caller
	// can also print it for a manual open). Non-blocking; touches no terminal
	// state, so it is safe to call from the TUI's raw mode.
	Start() (authURL string, err error)

	// Exchange swaps the authorization code the user pasted (after logging in)
	// for OAuth credentials ready to persist. Must be called after Start on
	// the same instance.
	Exchange(code string) (*types.Credentials, error)
}

// ── Shared PKCE (RFC 7636) ──────────────────────────────────────────────────

// generatePKCE returns a high-entropy verifier and its S256 challenge. The
// verifier is base64url(64 random bytes) capped at the RFC's 128-char maximum;
// the challenge is base64url(sha256(verifier)).
func generatePKCE() (verifier, challenge string) {
	b := make([]byte, 64)
	rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	if len(verifier) > 128 {
		verifier = verifier[:128]
	}
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return
}

// randomURLSafe returns an n-char URL-safe random string for the OAuth state
// parameter (CSRF guard).
func randomURLSafe(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	s := base64.RawURLEncoding.EncodeToString(b)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ── Shared browser open ─────────────────────────────────────────────────────

// openBrowser opens u in the user's default browser (best-effort; errors are
// ignored — callers always print the URL so a headless/failed open still lets
// the user copy it manually).
//
// It's a package var, not a plain func, purely so tests can stub it: exercising
// a flow's URL/PKCE construction must NOT actually launch a browser on every
// `go test` run.
var openBrowser = func(u string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{u}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", u}
	default:
		cmd, args = "xdg-open", []string{u}
	}
	_ = exec.Command(cmd, args...).Start()
}

// ── Shared token POST ───────────────────────────────────────────────────────

// tokenResponse is the standard OAuth token endpoint response shape, shared by
// every provider's exchange/refresh.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// postToken POSTs a JSON OAuth token request to a single endpoint and returns
// the resulting credentials. No multi-endpoint fallback: the authorization code
// (and a refresh token) is single-use, so retrying against a second endpoint
// after the first may have already redeemed it is a double-redeem, not
// resilience. defaultExpiresIn is used when the endpoint omits expires_in.
// subType is passed through onto the credentials (providers that don't have one
// pass "").
func postToken(endpoint string, body map[string]string, defaultExpiresIn int, subType string) (*types.Credentials, error) {
	payload, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request (%s): %w", endpoint, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange HTTP %d (%s): %s", resp.StatusCode, endpoint, string(data))
	}

	var tr tokenResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, fmt.Errorf("parse token response (%s): %w", endpoint, err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("%s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token (%s): %s", endpoint, string(data))
	}

	expiresIn := tr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = defaultExpiresIn
	}
	creds := types.OAuthCredentials(
		tr.AccessToken,
		tr.RefreshToken,
		time.Now().Add(time.Duration(expiresIn)*time.Second).UnixMilli(),
		subType,
	)
	return &creds, nil
}
