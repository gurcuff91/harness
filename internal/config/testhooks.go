package config

import "github.com/gurcuff91/harness/configstore"

// SwapStoresForTest replaces the process-global settings/credentials stores —
// and the managers built on them — with the given ones, returning a function
// that restores the previous state. A nil argument keeps that side as it is.
//
// For tests only. The managers are process singletons, so without this a test
// exercising schedules, instances or transport state would read and WRITE the
// real ~/.harness files (isolating HOME doesn't help once a manager exists).
// Typical use:
//
//	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), nil))
//
// Unlike SetSettingsStore/SetCredentialsStore, this ignores whether the
// managers are already in use — that's the point.
func SwapStoresForTest(settings configstore.SettingsStore, creds configstore.CredentialsStore) (restore func()) {
	registryMu.Lock()
	defer registryMu.Unlock()
	prevSS, prevCS, prevSM, prevCM := settingsStore, credentialsStore, settingsMgr, credsMgr
	if settings != nil {
		settingsStore = settings
		settingsMgr = NewSettingsManager(settings)
	}
	if creds != nil {
		credentialsStore = creds
		credsMgr = NewCredentialsManager(creds)
	}
	return func() {
		registryMu.Lock()
		defer registryMu.Unlock()
		settingsStore, credentialsStore, settingsMgr, credsMgr = prevSS, prevCS, prevSM, prevCM
	}
}
