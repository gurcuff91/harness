// Package config is harness's typed, validated access to its process-global
// configuration: settings (SettingsManager) and provider credentials
// (CredentialsManager). Persistence is delegated to the configstore ports —
// FileStore over ~/.harness/{settings,credentials}.json unless an SDK
// embedder registered its own store first (see SetSettingsStore /
// SetCredentialsStore).
//
// Entry points: GetSettingsManager() and GetCredentialsManager().
package config

import (
	"fmt"
	"os"
	"sync"

	"github.com/gurcuff91/harness/configstore"
)

// Process-global registry. A store must be registered BEFORE the matching
// manager is first used: once a manager exists, swapping its backend would
// silently split this process's configuration across two stores, so a late
// registration is rejected instead.
var (
	registryMu sync.Mutex

	settingsStore    configstore.SettingsStore
	credentialsStore configstore.CredentialsStore

	settingsMgr *SettingsManager
	credsMgr    *CredentialsManager
)

// SetSettingsStore registers the SettingsStore every SettingsManager in this
// process uses. Call once, early (typically in main(), before constructing
// any Agent). Returns configstore.ErrAlreadyInitialized if the settings
// manager is already in use.
func SetSettingsStore(s configstore.SettingsStore) error {
	if s == nil {
		return fmt.Errorf("config: SetSettingsStore: nil store")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if settingsMgr != nil {
		return configstore.ErrAlreadyInitialized
	}
	settingsStore = s
	return nil
}

// SetCredentialsStore registers the CredentialsStore every CredentialsManager
// in this process uses. Call once, early (typically in main(), before
// constructing any Agent). Returns configstore.ErrAlreadyInitialized if the
// credentials manager is already in use.
func SetCredentialsStore(s configstore.CredentialsStore) error {
	if s == nil {
		return fmt.Errorf("config: SetCredentialsStore: nil store")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if credsMgr != nil {
		return configstore.ErrAlreadyInitialized
	}
	credentialsStore = s
	return nil
}

// GetSettingsManager returns the process-wide SettingsManager, built on first
// use over the registered SettingsStore — or a FileStore on
// ~/.harness/settings.json when none was registered.
func GetSettingsManager() *SettingsManager {
	registryMu.Lock()
	defer registryMu.Unlock()
	if settingsMgr == nil {
		if settingsStore == nil {
			settingsStore = defaultFileStore(configstore.DefaultSettingsPath, 0600)
		}
		settingsMgr = NewSettingsManager(settingsStore)
	}
	return settingsMgr
}

// GetCredentialsManager returns the process-wide CredentialsManager, built on
// first use over the registered CredentialsStore — or a FileStore on
// ~/.harness/credentials.json (0600) when none was registered.
func GetCredentialsManager() *CredentialsManager {
	registryMu.Lock()
	defer registryMu.Unlock()
	if credsMgr == nil {
		if credentialsStore == nil {
			credentialsStore = defaultFileStore(configstore.DefaultCredentialsPath, 0600)
		}
		credsMgr = NewCredentialsManager(credentialsStore)
	}
	return credsMgr
}

// defaultStore is the union of both ports — FileStore and InMemoryStore
// each implement it, so one default can back either manager.
type defaultStore interface {
	configstore.SettingsStore
	configstore.CredentialsStore
}

// defaultFileStore opens the default file-backed store. A malformed file is
// not an error at all (FileStore treats it as empty and the next write
// replaces it). The only remaining failures are environmental — no home
// directory, or a file that exists but can't be read — and the getters have
// no error return to report them through, nor anywhere safe to print (the
// TUI owns the terminal). So this process silently runs on an in-memory
// store instead: harness still starts, it just can't persist changes.
func defaultFileStore(path func() (string, error), perm os.FileMode) defaultStore {
	if p, err := path(); err == nil {
		if fs, err := configstore.NewFileStore(p, perm); err == nil {
			return fs
		}
	}
	return configstore.NewInMemoryStore()
}
