package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/types"
)

// RunProviders lists all registered providers.
func RunProviders(ctx context.Context, ad *admin.Admin, output string) error {
	providers, err := ad.Providers.List(ctx)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}

	switch output {
	case "json":
		b, _ := json.MarshalIndent(providers, "", "  ")
		fmt.Println(string(b))
	default:
		for _, p := range providers {
			status := "inactive"
			cred := ""
			if p.Active {
				status = "active"
				switch {
				case p.IsSubscription:
					cred = " subscription"
				case p.Activation == "auto":
					cred = " auto"
				default:
					cred = " api_key"
				}
			}
			fmt.Printf("%-20s %-8s %s (%d models)\n", p.Name, status, cred, p.ModelCount)
		}
	}
	return nil
}

// RunConnect connects a provider.
func RunConnect(ctx context.Context, ad *admin.Admin, name, apiKey, output string) error {
	if name == "" {
		return fmt.Errorf("provider name required")
	}

	// Validate provider exists and read its authoritative credential type.
	credType := ""
	ct, err := ad.Providers.CredentialType(name)
	provExists := err == nil
	if provExists {
		credType = string(ct)
	}
	if !provExists {
		return fmt.Errorf("unknown provider: %s\nRun 'harness providers' to see available providers.", name)
	}

	// Branch on the credential type the provider actually needs.
	switch credType {
	case "oauth":
		// Native OAuth PKCE flow (admin.Providers.OAuthStart/OAuthExchange):
		// open the browser, let the user log in, and exchange the pasted code
		// for tokens — no Claude Code install or keychain read required.
		// RunOAuth stays provider-agnostic — a new OAuth provider needs no
		// change here.
		creds, err := RunOAuth(ad, name)
		if err != nil {
			return fmt.Errorf("OAuth: %w", err)
		}
		if _, err = ad.Providers.Connect(ctx, name, admin.ConnectCredentials{
			AccessToken:      creds.AccessToken,
			RefreshToken:     creds.RefreshToken,
			ExpiresAt:        creds.ExpiresAt,
			SubscriptionType: creds.SubscriptionType,
			AccountID:        creds.AccountID,
		}); err != nil {
			return fmt.Errorf("connect: %w", apiErr(err))
		}
	case "api_key":
		if apiKey == "" {
			secret, err := PromptSecret("Enter API key: ")
			if err != nil {
				return fmt.Errorf("api key required: pass it as an argument (harness connect %s <api_key>) or run in a terminal", name)
			}
			apiKey = secret
		}
		if apiKey == "" {
			return fmt.Errorf("api key required")
		}
		if _, err = ad.Providers.Connect(ctx, name, admin.ConnectCredentials{APIKey: apiKey}); err != nil {
			return fmt.Errorf("connect: %w", apiErr(err))
		}
	default: // "none" — auto-detected (e.g. ollama via ping); no credential needed.
		if _, err = ad.Providers.Connect(ctx, name, admin.ConnectCredentials{}); err != nil {
			return fmt.Errorf("connect: %w", apiErr(err))
		}
	}

	fmt.Printf("Connected: %s\n", name)
	return nil
}

// RunDisconnect disconnects a provider.
func RunDisconnect(ctx context.Context, ad *admin.Admin, name, output string) error {
	if name == "" {
		return fmt.Errorf("provider name required")
	}

	// Validate provider exists
	if _, err := ad.Providers.CredentialType(name); err != nil {
		return fmt.Errorf("unknown provider: %s\nRun 'harness providers' to see available providers.", name)
	}

	if err := ad.Providers.Disconnect(name); err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}

	fmt.Printf("Disconnected: %s\n", name)
	return nil
}

// RunSessions lists sessions, optionally all.
func RunSessions(ctx context.Context, ad *admin.Admin, all bool, output string) error {
	cwd := ""
	if !all {
		cwd, _ = os.Getwd()
	}
	sessions, err := ad.Sessions.List(cwd)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	// Sort by LastActiveAt descending — most recently active first.
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActiveAt.After(sessions[j].LastActiveAt)
	})

	switch output {
	case "json":
		b, _ := json.MarshalIndent(sessions, "", "  ")
		fmt.Println(string(b))
	default:
		// Resolve display names and measure the longest one so all columns align.
		type row struct {
			id, name, ago, cwd, model string
		}
		rows := make([]row, len(sessions))
		maxName := 0
		for i, s := range sessions {
			name := s.Name
			if name == "" {
				name = s.ID[:8]
			}
			if len(name) > maxName {
				maxName = len(name)
			}
			rows[i] = row{
				id:    s.ID,
				name:  name,
				ago:   relTime(s.LastActiveAt.UnixMilli()),
				cwd:   shortenPath(s.CWD),
				model: s.Model,
			}
		}
		nameFmt := fmt.Sprintf("%%-%ds", maxName)
		for _, r := range rows {
			fmt.Printf("%s  "+nameFmt+"  %-12s  %s\n",
				r.id, r.name, r.ago, r.cwd)
			fmt.Printf("%s  %s\n\n", spaces(len(r.id)), r.model)
		}
	}
	return nil
}

// spaces returns a string of n spaces — used to align the model line under the
// session line without repeating the ID.
func spaces(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

// RunDelete deletes a session.
func RunDelete(ctx context.Context, ad *admin.Admin, id, output string) error {
	if id == "" {
		return fmt.Errorf("session ID required")
	}

	// Validate session exists by checking all CWDs
	found := false
	if sessions, err := ad.Sessions.List(""); err == nil {
		for _, s := range sessions {
			if s.ID == id {
				found = true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf("session not found: %s\nRun 'harness sessions --all' to see all sessions.", id)
	}

	if err := ad.Sessions.Delete(id); err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	fmt.Println("Session deleted:", id)
	return nil
}

// apiErr renders an admin error the way the API did before the CLI stopped
// going through the server: a provider's structured error keeps its own
// message plus its details.
func apiErr(err error) error {
	var pe *types.ProviderAPIError
	if errors.As(err, &pe) {
		if len(pe.Details) > 0 {
			if d, jerr := json.Marshal(pe.Details); jerr == nil {
				return errors.New(pe.Message + ": " + string(d))
			}
		}
		return errors.New(pe.Message)
	}
	return err
}
