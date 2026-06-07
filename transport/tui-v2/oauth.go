package tuiv2

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// runOAuthLogin executes `claude auth login`, reads tokens from keychain,
// and returns credentials ready to pass to provider.Connect().
func runOAuthLogin() (*types.Credentials, error) {
	fmt.Println("\n  🔑 Starting authentication via Claude Code...")
	cmd := exec.Command("claude", "auth", "login")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		resetTerminal()
		return nil, fmt.Errorf("claude auth login failed: %w\n  Install: npm install -g @anthropic-ai/claude-code", err)
	}
	resetTerminal()

	token := readClaudeFromKeychain()
	if token == nil {
		token = readClaudeCredentialsFile()
	}
	if token == nil {
		return nil, fmt.Errorf("login completed but couldn't import tokens — try again")
	}
	return token, nil
}

func resetTerminal() {
	exec.Command("stty", "sane").Run()
	fmt.Print("\033[?25h\033[0m")
}

// readClaudeFromKeychain reads OAuth tokens from macOS keychain.
func readClaudeFromKeychain() *types.Credentials {
	if t := readKeychainItem("Claude Code-credentials"); t != nil {
		return t
	}
	return readKeychainItem("claude-code")
}

func readKeychainItem(service string) *types.Credentials {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &raw); err != nil {
		return nil
	}
	data := raw
	if nested, ok := raw["claudeAiOauth"].(map[string]any); ok {
		data = nested
	}
	at, _ := data["accessToken"].(string)
	rt, _ := data["refreshToken"].(string)
	ea, _ := data["expiresAt"].(float64)
	st, _ := data["subscriptionType"].(string)
	if at == "" || rt == "" {
		return nil
	}
	return &types.Credentials{
		Type: types.CredTypeOAuth, AccessToken: at, RefreshToken: rt,
		ExpiresAt: int64(ea), SubscriptionType: st,
	}
}

func readClaudeCredentialsFile() *types.Credentials {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		return nil
	}
	var creds struct {
		OAuthTokens []struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
			SubType      string `json:"subscriptionType"`
		} `json:"oauthTokens"`
	}
	if err := json.Unmarshal(data, &creds); err != nil || len(creds.OAuthTokens) == 0 {
		return nil
	}
	t := creds.OAuthTokens[0]
	return &types.Credentials{
		Type: types.CredTypeOAuth, AccessToken: t.AccessToken,
		RefreshToken: t.RefreshToken, ExpiresAt: t.ExpiresAt, SubscriptionType: t.SubType,
	}
}
