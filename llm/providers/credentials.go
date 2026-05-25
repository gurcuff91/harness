package providers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ProviderCreds struct {
	APIKey           string `json:"api_key,omitempty"`
	AccessToken      string `json:"access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
	SubscriptionType string `json:"subscription_type,omitempty"`
}

type CredsFile map[string]*ProviderCreds

func credsPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".harness")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "credentials.json")
}

func LoadCreds() CredsFile {
	data, err := os.ReadFile(credsPath())
	if err != nil {
		return make(CredsFile)
	}
	var f CredsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return make(CredsFile)
	}
	return f
}

func SaveCreds(f CredsFile) {
	data, _ := json.MarshalIndent(f, "", "  ")
	os.WriteFile(credsPath(), data, 0600)
}

func GetCreds(provider string) *ProviderCreds {
	f := LoadCreds()
	return f[provider]
}

func SetCreds(provider string, creds *ProviderCreds) {
	f := LoadCreds()
	f[provider] = creds
	SaveCreds(f)
}

func HasAPIKey(provider string) bool {
	if envKey := apiKeyFromEnv(provider); envKey != "" {
		return true
	}
	c := GetCreds(provider)
	return c != nil && c.APIKey != ""
}

func GetAPIKey(provider string) string {
	if envKey := apiKeyFromEnv(provider); envKey != "" {
		return envKey
	}
	c := GetCreds(provider)
	if c != nil {
		return c.APIKey
	}
	return ""
}

func apiKeyFromEnv(provider string) string {
	switch provider {
	case "anthropic":
		return os.Getenv("ANTHROPIC_API_KEY")
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	case "ollama-cloud":
		return os.Getenv("OLLAMA_API_KEY")
	case "opencode-go":
		return os.Getenv("OPENCODE_GO_API_KEY")
	}
	return ""
}

func HasOAuth(provider string) bool {
	c := GetCreds(provider)
	return c != nil && c.AccessToken != ""
}

func ConnectAPIKey(provider string) error {
	fmt.Printf("\n  Enter %s API key: ", provider)
	var key []byte
	buf := make([]byte, 1)
	for {
		os.Stdin.Read(buf)
		if buf[0] == 13 || buf[0] == 10 {
			break
		}
		if buf[0] == 3 {
			fmt.Println()
			return fmt.Errorf("cancelled")
		}
		if buf[0] == 127 || buf[0] == 8 {
			if len(key) > 0 {
				key = key[:len(key)-1]
				fmt.Print("\b \b")
			}
			continue
		}
		key = append(key, buf[0])
		fmt.Print("*")
	}
	fmt.Println()
	apiKey := string(key)
	if apiKey == "" {
		return fmt.Errorf("no key provided")
	}
	SetCreds(provider, &ProviderCreds{APIKey: apiKey})
	return nil
}
