package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/gurcuff91/harness/agent"
)

// RunProviders lists all registered providers.
func RunProviders(ctx context.Context, a *agent.Agent, output string) error {
	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	providers, err := c.GetProviders()
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
func RunConnect(ctx context.Context, a *agent.Agent, name, apiKey, output string) error {
	if name == "" {
		return fmt.Errorf("provider name required")
	}

	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	// Validate provider exists and read its authoritative credential type.
	provExists := false
	credType := ""
	if providers, err := c.GetProviders(); err == nil {
		for _, p := range providers {
			if p.Name == name {
				provExists = true
				credType = p.CredentialType
				break
			}
		}
	}
	if !provExists {
		return fmt.Errorf("unknown provider: %s\nRun 'harness providers' to see available providers.", name)
	}

	// Branch on the credential type the provider actually needs.
	switch credType {
	case "oauth":
		// Silent-only: read tokens from the keychain / credentials file. If none
		// exist, ObtainOAuthCredentials returns an actionable "run claude auth
		// login" error (we don't spawn the interactive login — same as the TUI).
		creds, err := ObtainOAuthCredentials(name)
		if err != nil {
			return fmt.Errorf("OAuth: %w", err)
		}
		if _, err = c.ConnectProviderWithCreds(name, creds); err != nil {
			return fmt.Errorf("connect: %w", err)
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
		if _, err = c.ConnectProvider(name, apiKey); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	default: // "none" — auto-detected (e.g. ollama via ping); no credential needed.
		if _, err = c.ConnectProvider(name, ""); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	}

	fmt.Printf("Connected: %s\n", name)
	return nil
}

// RunDisconnect disconnects a provider.
func RunDisconnect(ctx context.Context, a *agent.Agent, name, output string) error {
	if name == "" {
		return fmt.Errorf("provider name required")
	}

	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	// Validate provider exists
	provExists := false
	if providers, err := c.GetProviders(); err == nil {
		for _, p := range providers {
			if p.Name == name {
				provExists = true
				break
			}
		}
	}
	if !provExists {
		return fmt.Errorf("unknown provider: %s\nRun 'harness providers' to see available providers.", name)
	}

	_, err = c.DisconnectProvider(name)
	if err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}

	fmt.Printf("Disconnected: %s\n", name)
	return nil
}

// RunSessions lists sessions, optionally all.
func RunSessions(ctx context.Context, a *agent.Agent, all bool, output string) error {
	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	cwd := ""
	if !all {
		cwd, _ = os.Getwd()
	}
	sessions, err := c.ListSessions(cwd)
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
func RunDelete(ctx context.Context, a *agent.Agent, id, output string) error {
	if id == "" {
		return fmt.Errorf("session ID required")
	}

	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	// Validate session exists by checking all CWDs
	found := false
	if sessions, err := c.ListSessions(""); err == nil {
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

	if err := c.DeleteSession(id); err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	fmt.Println("Session deleted:", id)
	return nil
}
