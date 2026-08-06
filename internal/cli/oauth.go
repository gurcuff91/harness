package cli

import (
	"fmt"

	"github.com/gurcuff91/harness/internal/oauthflow"
	"github.com/gurcuff91/harness/types"
)

// RunOAuth drives a provider's native OAuth PKCE flow for the CLI: it resolves
// the provider's flow (oauthflow.For), opens the browser, prints the URL as a
// fallback, blocks waiting for the user to paste back the authorization code,
// then exchanges it for credentials. This is the blocking counterpart to the
// TUI's event-driven value-capture path — both resolve the flow via
// oauthflow.For and drive the same oauthflow.OauthFlow, so the OAuth logic and
// the provider→flow mapping each live in exactly one place.
//
// Requires an interactive TTY for the code paste; on a pipe/CI it returns an
// actionable error rather than blocking on input that will never arrive.
func RunOAuth(provName string) (*types.Credentials, error) {
	flow, err := oauthflow.For(provName)
	if err != nil {
		return nil, err
	}

	authURL, err := flow.Start()
	if err != nil {
		return nil, err
	}

	fmt.Printf("\n🌐  Opening your browser to authenticate...\n")
	fmt.Printf("    If it doesn't open, paste this URL manually:\n\n    %s\n\n", authURL)
	fmt.Printf("    After logging in, the page shows an authorization code\n")
	fmt.Printf("    (the '?code=...' value). Copy and paste it here.\n\n")

	code, err := PromptLine("    Code: ")
	if err == ErrNoTTY {
		return nil, fmt.Errorf("OAuth needs an interactive terminal to paste the code — run 'harness connect %s' in a terminal", provName)
	}
	if err != nil {
		return nil, fmt.Errorf("reading code: %w", err)
	}

	creds, err := flow.Exchange(code)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	return creds, nil
}
