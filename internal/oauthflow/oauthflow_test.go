package oauthflow

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

// stubBrowser replaces the package-level openBrowser with a no-op that records
// the URL it was handed, restoring the real one when the test ends. This keeps
// `go test` from actually launching a browser every run, and lets a test
// assert the exact URL a flow tried to open. Returns a pointer to the captured
// URL. Shared by every flow's tests.
func stubBrowser(t *testing.T) *string {
	t.Helper()
	var opened string
	orig := openBrowser
	openBrowser = func(u string) { opened = u }
	t.Cleanup(func() { openBrowser = orig })
	return &opened
}

// TestGeneratePKCEProducesValidS256Pair verifies the PKCE pair satisfies
// RFC 7636: challenge == base64url(sha256(verifier)), verifier within
// [43,128], url-safe alphabet only, and unique per call.
func TestGeneratePKCEProducesValidS256Pair(t *testing.T) {
	verifier, challenge := generatePKCE()

	if len(verifier) < 43 || len(verifier) > 128 {
		t.Errorf("verifier length %d out of RFC 7636 range [43,128]", len(verifier))
	}

	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Errorf("challenge is not base64url(sha256(verifier))\n got:  %s\n want: %s", challenge, want)
	}

	for _, s := range []string{verifier, challenge} {
		if strings.ContainsAny(s, "+/=") {
			t.Errorf("%q contains non-url-safe base64 characters", s)
		}
	}

	v2, c2 := generatePKCE()
	if verifier == v2 || challenge == c2 {
		t.Error("two generatePKCE calls produced identical output — not random")
	}
}

// TestRandomURLSafeLengthAndAlphabet verifies the state generator returns the
// requested length and stays url-safe.
func TestRandomURLSafeLengthAndAlphabet(t *testing.T) {
	s := randomURLSafe(32)
	if len(s) != 32 {
		t.Errorf("randomURLSafe(32) length = %d, want 32", len(s))
	}
	if strings.ContainsAny(s, "+/=") {
		t.Errorf("state %q contains non-url-safe characters", s)
	}
	if randomURLSafe(32) == s {
		t.Error("two randomURLSafe calls collided — not random")
	}
}

// TestForResolvesKnownProvider verifies For returns a working flow for a known
// provider and a clear error for an unknown one — this is the single
// provider→flow dispatch point both the CLI and TUI rely on, so a wrong or
// silent mapping here would misroute a login.
func TestForResolvesKnownProvider(t *testing.T) {
	flow, err := For("claude-oauth")
	if err != nil {
		t.Fatalf("For(claude-oauth) errored: %v", err)
	}
	if flow == nil {
		t.Fatal("For(claude-oauth) returned a nil flow")
	}
	// It must be a usable OauthFlow (interface satisfied).
	var _ OauthFlow = flow

	if _, err := For("openai"); err == nil {
		t.Error("For(openai) should error — no OAuth flow registered for it")
	}
	if _, err := For(""); err == nil {
		t.Error("For(\"\") should error")
	}
}
