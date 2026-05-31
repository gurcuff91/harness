package providers

import (
	"fmt"
	"os"

	"github.com/gurcuff91/harness/config"
	"github.com/gurcuff91/harness/types"
)

// resolveAPIKey implements the credential chain for API key providers:
//  1. cache (in-memory, passed as pointer)
//  2. env var
//  3. credentials.json via CredentialsManager
func resolveAPIKey(cache *string, envVar, credKey string) (types.Credentials, error) {
	// 1. cache
	if *cache != "" {
		return types.APIKeyCredentials(*cache), nil
	}
	// 2. env
	if v := os.Getenv(envVar); v != "" {
		*cache = v
		return types.APIKeyCredentials(v), nil
	}
	// 3. file
	if v, ok := config.GetCredentialsManager().Load(credKey); ok && v != "" {
		*cache = v
		return types.APIKeyCredentials(v), nil
	}
	return types.Credentials{}, fmt.Errorf("no credentials found")
}

// saveAPIKey persists an API key and updates the cache pointer.
func saveAPIKey(cache *string, credKey, value string) error {
	if value == "" {
		return fmt.Errorf("api_key cannot be empty")
	}
	*cache = value
	return config.GetCredentialsManager().Store(credKey, value)
}

// clearAPIKey removes the API key from file and clears the cache pointer.
func clearAPIKey(cache *string, credKey string) error {
	*cache = ""
	return config.GetCredentialsManager().Delete(credKey)
}
