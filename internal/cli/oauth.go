package cli

import (
	"fmt"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/browseropen"
	"github.com/gurcuff91/harness/types"
)

// RunOAuth drives a provider's native OAuth PKCE flow for the CLI entirely
// through the server's stateless POST /api/oauth/{provider} endpoint: it
// calls c.StartOAuth to get the auth URL and PKCE verifier, opens the
// browser (this CLI's own job — the server never does), prints the URL as
// a fallback, blocks waiting for the user to paste back the authorization
// code, then calls c.ExchangeOAuth (passing the SAME verifier StartOAuth
// returned) for credentials. This is the blocking counterpart to the TUI's
// event-driven value-capture path — both drive the exact same two
// client.Client methods, so the OAuth logic itself lives in exactly one
// place (internal/oauthflow, behind the server) and neither client
// re-implements or imports it.
//
// Requires an interactive TTY for the code paste; on a pipe/CI it returns an
// actionable error rather than blocking on input that will never arrive.
func RunOAuth(c *client.Client, provName string) (*types.Credentials, error) {
	authURL, verifierCode, err := c.StartOAuth(provName)
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

	creds, err := c.ExchangeOAuth(provName, code, verifierCode)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	return creds, nil
}
