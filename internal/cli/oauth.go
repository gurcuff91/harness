package cli

import (
	"fmt"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/internal/browseropen"
	"github.com/gurcuff91/harness/types"
)

// RunOAuth drives a provider's native OAuth PKCE flow for the CLI through
// admin.Providers: OAuthStart gives the auth URL and PKCE verifier, the browser
// is opened (this CLI's own job — admin never does), the URL is printed as a
// fallback, and it blocks waiting for the user to paste back the authorization
// code, then OAuthExchange (with the SAME verifier) returns credentials. This
// is the blocking counterpart to the TUI's event-driven path, which drives the
// same two steps over the server's POST /api/oauth/{provider}.
//
// Requires an interactive TTY for the code paste; on a pipe/CI it returns an
// actionable error rather than blocking on input that will never arrive.
func RunOAuth(ad *admin.Admin, provName string) (*types.Credentials, error) {
	authURL, verifierCode, err := ad.Providers.OAuthStart(provName)
	if err != nil {
		return nil, err
	}

	fmt.Printf("\n🌐  Opening your browser to authenticate...\n")
	fmt.Printf("    If it doesn't open, paste this URL manually:\n\n    %s\n\n", authURL)
	fmt.Printf("    After logging in, the page shows an authorization code\n")
	fmt.Printf("    (the '?code=...' value). Copy and paste it here.\n\n")
	browseropen.Open(authURL)

	code, err := PromptLine("    Code: ")
	if err == ErrNoTTY {
		return nil, fmt.Errorf("OAuth needs an interactive terminal to paste the code — run 'harness connect %s' in a terminal", provName)
	}
	if err != nil {
		return nil, fmt.Errorf("reading code: %w", err)
	}

	creds, err := ad.Providers.OAuthExchange(provName, code, verifierCode)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	return creds, nil
}
