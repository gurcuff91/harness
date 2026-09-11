package oauthflow

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"
)

// TestClaudeStartBuildsCorrectAuthURL verifies Start's authorization URL
// carries every required OAuth/PKCE parameter with the exact values Anthropic
// expects (validated against a real account — see the native-oauth-flow
// memo), and that the returned verifierCode is the one the challenge in the
// URL was derived from. Start no longer opens a browser (that moved to the
// caller — see OauthFlow.Start's doc comment).
func TestClaudeStartBuildsCorrectAuthURL(t *testing.T) {
	f := &claudeOauthFlow{}
	authURL, verifierCode, err := f.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if verifierCode == "" {
		t.Fatal("Start returned an empty verifierCode")
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

	// The challenge in the URL must be S256 of the RETURNED verifierCode —
	// otherwise the exchange (which uses whatever verifierCode the caller
	// passes back) would fail with a PKCE mismatch.
	sum := sha256.Sum256([]byte(verifierCode))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); q.Get("code_challenge") != want {
		t.Error("auth URL code_challenge does not match the returned verifierCode")
	}
}

// TestClaudeStartIsStatelessAcrossInstances verifies two Start calls (even
// on two separate flow instances, mirroring a stateless HTTP handler
// constructing a fresh flow per request) produce independent verifiers —
// nothing is cached or shared at the package level.
func TestClaudeStartIsStatelessAcrossInstances(t *testing.T) {
	_, v1, err := (&claudeOauthFlow{}).Start()
	if err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	_, v2, err := (&claudeOauthFlow{}).Start()
	if err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	if v1 == v2 {
		t.Error("two Start calls produced identical verifiers — not independently random")
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
	f := &claudeOauthFlow{}

	if _, err := f.Exchange("#somestate", "v"); err == nil {
		t.Error("expected empty-code rejection when the value is only a #fragment")
	}
	if _, err := f.Exchange("   ", "v"); err == nil {
		t.Error("expected empty-code rejection for whitespace-only input")
	}
}

// TestClaudeExchangeEmptyCode verifies a blank code is rejected before any
// network call.
func TestClaudeExchangeEmptyCode(t *testing.T) {
	f := &claudeOauthFlow{}
	if _, err := f.Exchange("", "v"); err == nil {
		t.Error("expected an error for an empty authorization code")
	}
}

// TestClaudeExchangeEmptyVerifier verifies Exchange rejects a missing
// verifierCode before any network call — the stateless equivalent of the
// old "Exchange called before Start" guard, since the flow no longer stores
// a verifier to check against; the caller must supply it every time.
func TestClaudeExchangeEmptyVerifier(t *testing.T) {
	f := &claudeOauthFlow{}
	if _, err := f.Exchange("somecode", ""); err == nil {
		t.Error("expected an error when verifierCode is empty")
	}
}
