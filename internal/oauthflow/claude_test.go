package oauthflow

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"
)

// TestClaudeStartBuildsCorrectAuthURL verifies Start's authorization URL
// carries every required OAuth/PKCE parameter with the exact values Anthropic
// expects (validated against a real account — see the native-oauth-flow memo),
// opens the browser with that same URL, and returns it.
func TestClaudeStartBuildsCorrectAuthURL(t *testing.T) {
	opened := stubBrowser(t)

	f := &claudeOauthFlow{}
	authURL, err := f.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if *opened != authURL {
		t.Errorf("browser opened %q but Start returned %q", *opened, authURL)
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth URL does not parse: %v", err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != claudeAuthURL {
		t.Errorf("auth endpoint = %q, want %q", got, claudeAuthURL)
	}

	q := u.Query()
	checks := map[string]string{
		"client_id":             claudeClientID,
		"response_type":         "code",
		"redirect_uri":          claudeRedirect,
		"scope":                 claudeOAuthScope,
		"code_challenge_method": "S256",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("auth URL %s = %q, want %q", k, got, want)
		}
	}
	if q.Get("code_challenge") == "" {
		t.Error("auth URL missing code_challenge")
	}
	if q.Get("state") == "" {
		t.Error("auth URL missing state")
	}

	// The challenge in the URL must be S256 of the flow's stored verifier —
	// otherwise the exchange would fail with a PKCE mismatch.
	sum := sha256.Sum256([]byte(f.verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); q.Get("code_challenge") != want {
		t.Error("auth URL code_challenge does not match the flow's stored verifier")
	}
}

// TestNewClaudeOauthFlowReturnsInterface verifies the constructor hands back
// the OauthFlow interface (not the concrete type), the idiomatic shape callers
// depend on.
func TestNewClaudeOauthFlowReturnsInterface(t *testing.T) {
	var _ OauthFlow = NewClaudeOauthFlow()
}

// TestClaudeExchangeStripsStateFragment is the regression test for the
// callback's "CODE#STATE" shape: Exchange must strip everything from '#'
// onward. Verified via the fast-fail path — a value that is only a #fragment
// yields an empty code, rejected before any network call.
func TestClaudeExchangeStripsStateFragment(t *testing.T) {
	f := &claudeOauthFlow{verifier: "v", state: "s"}

	if _, err := f.Exchange("#somestate"); err == nil {
		t.Error("expected empty-code rejection when the value is only a #fragment")
	}
	if _, err := f.Exchange("   "); err == nil {
		t.Error("expected empty-code rejection for whitespace-only input")
	}
}

// TestClaudeExchangeEmptyCode verifies a blank code is rejected before any
// network call.
func TestClaudeExchangeEmptyCode(t *testing.T) {
	f := &claudeOauthFlow{verifier: "v", state: "s"}
	if _, err := f.Exchange(""); err == nil {
		t.Error("expected an error for an empty authorization code")
	}
}

// TestClaudeExchangeBeforeStart verifies Exchange fails cleanly if called
// before Start (no verifier yet) — a programming error, caught before it
// becomes a confusing PKCE mismatch from the server.
func TestClaudeExchangeBeforeStart(t *testing.T) {
	f := &claudeOauthFlow{} // no Start called → empty verifier
	if _, err := f.Exchange("somecode"); err == nil {
		t.Error("expected an error when Exchange is called before Start")
	}
}
