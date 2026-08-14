package providers

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// isAuthError decides retry-vs-abort in getValidToken's refresh loop: a
// permanent auth failure (a consumed/invalid single-use refresh token) must NOT
// be retried, while a transient network error must. Getting this wrong either
// hammers a dead token or gives up on a recoverable blip — both were in play in
// the invalid_grant bug this suite guards.
func TestIsAuthError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"invalid_grant (the reported failure)", fmt.Errorf(`refresh HTTP 400: {"error": "invalid_grant"}`), true},
		{"bare HTTP 400", fmt.Errorf("refresh HTTP 400 (x): body"), true},
		{"HTTP 401", fmt.Errorf("refresh HTTP 401 (x): body"), true},
		{"HTTP 403", fmt.Errorf("refresh HTTP 403 (x): body"), true},
		{"token_expired", fmt.Errorf("token_expired"), true},
		{"revoked", fmt.Errorf("token revoked"), true},
		{"network timeout is NOT permanent", fmt.Errorf("refresh request (x): dial tcp: i/o timeout"), false},
		{"HTTP 500 is NOT permanent", fmt.Errorf("refresh HTTP 500 (x): server error"), false},
		{"HTTP 429 is NOT permanent", fmt.Errorf("refresh HTTP 429 (x): rate limited"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAuthError(tc.err); got != tc.want {
				t.Errorf("isAuthError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Section 1 invariants: a single, bounded refresh endpoint. The 2-endpoint
// fallback was removed because, with a single-use refresh token, a retry
// against a second endpoint after the first may have redeemed it is a
// double-redeem (permanent invalid_grant), not resilience.
func TestOAuthRefreshEndpointIsSingleAndBounded(t *testing.T) {
	if oauthTokenURL != "https://platform.claude.com/v1/oauth/token" {
		t.Errorf("oauthTokenURL = %q, want the single platform.claude.com endpoint", oauthTokenURL)
	}
	if oauthRefreshClient.Timeout <= 0 {
		t.Fatal("oauthRefreshClient must have a bounded timeout, got 0 (infinite) — an unbounded refresh can hold the cross-process lock indefinitely")
	}
	if oauthRefreshClient.Timeout > time.Minute {
		t.Errorf("oauthRefreshClient timeout = %v, want a tight bound (~30s) so a slow refresh can't outlive staleFileLockAge", oauthRefreshClient.Timeout)
	}
	// It must NOT be the default (infinite) client.
	if oauthRefreshClient == http.DefaultClient {
		t.Error("refresh must not use http.DefaultClient (Timeout: 0)")
	}
}
