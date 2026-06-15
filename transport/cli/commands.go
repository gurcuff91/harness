package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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

	data, err := c.GetProviders()
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}

	var providers []map[string]any
	json.Unmarshal(data, &providers)

	switch output {
	case "json":
		b, _ := json.MarshalIndent(providers, "", "  ")
		fmt.Println(string(b))
	default:
		for _, p := range providers {
			name, _ := p["name"].(string)
			active, _ := p["active"].(bool)
			isSub, _ := p["is_subscription"].(bool)
			activation, _ := p["activation"].(string)
			modelCount, _ := p["model_count"].(float64)
			status := "inactive"
			cred := ""
			if active {
				status = "active"
				switch {
				case isSub:
					cred = " subscription"
				case activation == "auto":
					cred = " auto"
				default:
					cred = " api_key"
				}
			}
			fmt.Printf("%-20s %-8s %s (%d models)\n", name, status, cred, int(modelCount))
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

	// Validate provider exists
	provExists := false
	isSub := false
	if data, err := c.GetProviders(); err == nil {
		var providers []map[string]any
		json.Unmarshal(data, &providers)
		for _, p := range providers {
			if n, _ := p["name"].(string); n == name {
				provExists = true
				isSub, _ = p["is_subscription"].(bool)
				break
			}
		}
	}
	if !provExists {
		return fmt.Errorf("unknown provider: %s\nRun 'harness providers' to see available providers.", name)
	}

	if isSub {
		fmt.Println("Starting OAuth authentication...")
		creds, err := ObtainOAuthCredentials(name)
		if err != nil {
			return fmt.Errorf("OAuth: %w", err)
		}
		_, err = c.ConnectProviderWithCreds(name, creds)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	} else {
		if apiKey == "" {
			fmt.Print("Enter API key: ")
			fmt.Scanln(&apiKey)
			apiKey = strings.TrimSpace(apiKey)
			if apiKey == "" {
				return fmt.Errorf("api key required")
			}
		}
		_, err = c.ConnectProvider(name, apiKey)
		if err != nil {
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
	if data, err := c.GetProviders(); err == nil {
		var providers []map[string]any
		json.Unmarshal(data, &providers)
		for _, p := range providers {
			if n, _ := p["name"].(string); n == name {
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

	var data []byte
	if all {
		data, err = c.do("GET", "/api/sessions", nil)
	} else {
		cwd, _ := os.Getwd()
		data, err = c.do("GET", "/api/sessions?cwd="+cwd, nil)
	}
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	var sessions []map[string]any
	json.Unmarshal(data, &sessions)

	switch output {
	case "json":
		b, _ := json.MarshalIndent(sessions, "", "  ")
		fmt.Println(string(b))
	default:
		for _, s := range sessions {
			id, _ := s["id"].(string)
			name, _ := s["name"].(string)
			cwd, _ := s["cwd"].(string)
			model, _ := s["model"].(string)
			if name == "" {
				name = id[:8]
			}
			shortCwd := shortenPath(cwd)
			fmt.Printf("%-12s %-20s %s  %s\n", id[:12], name, shortCwd, model)
		}
	}
	return nil
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
	if data, err := c.do("GET", "/api/sessions", nil); err == nil {
		var sessions []map[string]any
		json.Unmarshal(data, &sessions)
		for _, s := range sessions {
			if sid, _ := s["id"].(string); sid == id {
				found = true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf("session not found: %s\nRun 'harness sessions --all' to see all sessions.", id)
	}

	_, err = c.DeleteSession(id)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	fmt.Println("Session deleted:", id)
	return nil
}
