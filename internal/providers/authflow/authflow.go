package authflow

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// ObtainOAuthCredentials gets OAuth credentials for a provider using only
// SILENT sources: the OS keychain and cached credential files. It never spawns
// an interactive login — that would corrupt the TUI's raw-mode terminal, and
// keeping CLI and TUI on the same silent path makes both transports behave
// identically. If no credentials are found, it returns an actionable error
// telling the user to run `claude auth login` themselves, then retry.
func ObtainOAuthCredentials(provName string) (*types.Credentials, error) {
	switch provName {
	case "claude-oauth":
		return obtainClaudeOAuth()
	default:
		return nil, fmt.Errorf("no OAuth flow for provider: %s", provName)
	}
}

// ── Claude OAuth ────────────────────────────────────────────────────────────────

func obtainClaudeOAuth() (*types.Credentials, error) {
	// Storage differs per OS (per Claude Code docs):
	//   - macOS:         encrypted Keychain (fallback to file for older versions)
	//   - Linux/Windows: ~/.claude/.credentials.json (mode 0600), or under
	//                    $CLAUDE_CONFIG_DIR when set.
	if runtime.GOOS == "darwin" {
		if creds := readClaudeCredentialsFromKeychain(); creds != nil {
			return creds, nil
		}
	}
	if creds := readClaudeCredentialsFromFile(); creds != nil {
		return creds, nil
	}
	// No credentials found — guide the user to authenticate manually. We do NOT
	// spawn `claude auth login`: it takes over the terminal (incompatible with
	// the TUI's raw mode), and running an interactive subprocess implicitly is
	// surprising. The user runs it once, then retries the connect.
	return nil, fmt.Errorf("no Claude credentials found — run 'claude auth login' to authenticate, then reconnect\n  (install Claude Code: npm install -g @anthropic-ai/claude-code)")
}

// readClaudeCredentialsFromKeychain reads OAuth tokens from macOS keychain.
func readClaudeCredentialsFromKeychain() *types.Credentials {
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

// claudeCredentialsFilePath returns the path to Claude Code's credentials file. It
// honors $CLAUDE_CONFIG_DIR (used on Linux/Windows per the docs); otherwise it
// defaults to ~/.claude/.credentials.json. UserHomeDir resolves correctly on
// all three OSes (incl. Windows %USERPROFILE%).
func claudeCredentialsFilePath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".credentials.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", ".credentials.json")
}

// readClaudeCredentialsFromFile reads OAuth tokens from the credentials file. This
// is the primary source on Linux and Windows, and a fallback on macOS.
func readClaudeCredentialsFromFile() *types.Credentials {
	data, err := os.ReadFile(claudeCredentialsFilePath())
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
