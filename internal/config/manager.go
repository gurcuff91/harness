// Package config provides persistent storage for harness credentials and settings.
// Entry points: GetCredentialsManager() and GetSettingsManager().
package config

import (
	"sync"
)

var (
	credsMgr     *CredentialsManager
	credsMgrOnce sync.Once

	settingsMgr     *SettingsManager
	settingsMgrOnce sync.Once
)

// GetCredentialsManager returns the singleton CredentialsManager.
// Backed by ~/.harness/credentials.json — key-value store for provider credentials.
func GetCredentialsManager() *CredentialsManager {
	credsMgrOnce.Do(func() {
		credsMgr = newCredentialsManager()
	})
	return credsMgr
}

// GetSettingsManager returns the singleton SettingsManager.
// Backed by ~/.harness/settings.json — model, thinking level, and provider-specific KV.
func GetSettingsManager() *SettingsManager {
	settingsMgrOnce.Do(func() {
		settingsMgr = newSettingsManager()
	})
	return settingsMgr
}
