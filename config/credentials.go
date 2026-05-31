package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// credentials.go — neutral key-value store for provider credentials.
// No knowledge of specific providers, env vars, or credential types.

type credsFile map[string]string

func credsPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".harness")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "credentials.json")
}

func loadCredsFile() credsFile {
	data, err := os.ReadFile(credsPath())
	if err != nil {
		return make(credsFile)
	}
	var f credsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return make(credsFile)
	}
	return f
}

func saveCredsFile(f credsFile) {
	data, _ := json.MarshalIndent(f, "", "  ")
	os.WriteFile(credsPath(), data, 0600)
}

// StoreCred persists a credential value by key.
func StoreCred(key, value string) error {
	f := loadCredsFile()
	f[key] = value
	saveCredsFile(f)
	return nil
}

// LoadCred retrieves a credential value by key.
// Returns ("", false) if the key does not exist.
func LoadCred(key string) (string, bool) {
	f := loadCredsFile()
	v, ok := f[key]
	return v, ok
}

// DeleteCred removes a credential by key.
func DeleteCred(key string) error {
	f := loadCredsFile()
	delete(f, key)
	saveCredsFile(f)
	return nil
}

// DeletePrefix removes all credentials whose keys start with prefix.
func DeletePrefix(prefix string) error {
	f := loadCredsFile()
	for k := range f {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(f, k)
		}
	}
	saveCredsFile(f)
	return nil
}
