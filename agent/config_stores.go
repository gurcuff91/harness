package agent

import (
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

// SetSettingsStore registers the configstore.SettingsStore harness uses for
// its non-sensitive, process-global settings (active model, thinking level,
// MCP servers, custom providers). Without it, harness uses
// configstore.FileStore over ~/.harness/settings.json.
//
// Process-global, like NewOpenAIProvider: settings are shared by every Agent
// in the process (they're read by the provider registry, the MCP manager and
// the HTTP API, none of which belong to a single Agent). Call it once, early —
// typically in main(), before constructing any Agent. Registering after
// harness has started using its settings returns
// configstore.ErrAlreadyInitialized and changes nothing.
func SetSettingsStore(s configstore.SettingsStore) error {
	return config.SetSettingsStore(s)
}

// SetCredentialsStore registers the configstore.CredentialsStore harness uses
// for secrets (provider API keys and OAuth tokens). Without it, harness uses
// configstore.FileStore over ~/.harness/credentials.json (0600).
//
// Kept separate from SetSettingsStore so secrets can live in a different
// backend than ordinary settings — e.g. a cloud secret manager for
// credentials alongside plain files for settings. Same process-global,
// register-early contract as SetSettingsStore.
func SetCredentialsStore(s configstore.CredentialsStore) error {
	return config.SetCredentialsStore(s)
}
