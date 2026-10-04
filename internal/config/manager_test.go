package config

import (
	"errors"
	"testing"

	"github.com/gurcuff91/harness/configstore"
)

// resetRegistry clears the process-global registry so each test starts from
// "nothing registered, no manager built yet", restoring it afterwards.
func resetRegistry(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	saved := struct {
		ss configstore.SettingsStore
		cs configstore.CredentialsStore
		sm *SettingsManager
		cm *CredentialsManager
	}{settingsStore, credentialsStore, settingsMgr, credsMgr}
	settingsStore, credentialsStore, settingsMgr, credsMgr = nil, nil, nil, nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		settingsStore, credentialsStore, settingsMgr, credsMgr = saved.ss, saved.cs, saved.sm, saved.cm
		registryMu.Unlock()
	})
}

// A registered store is what the global managers actually use.
func TestRegisteredStoresBackTheGlobalManagers(t *testing.T) {
	resetRegistry(t)
	settings := configstore.NewInMemoryStore()
	creds := configstore.NewInMemoryStore()
	if err := SetSettingsStore(settings); err != nil {
		t.Fatal(err)
	}
	if err := SetCredentialsStore(creds); err != nil {
		t.Fatal(err)
	}

	if err := GetSettingsManager().SetActiveModel("x/1"); err != nil {
		t.Fatal(err)
	}
	if err := GetCredentialsManager().SetCredential("minimax", ProviderCredential{Type: "api_key", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}

	if v, found, _ := settings.Get(nsCore, keyActiveModel); !found || string(v) != `"x/1"` {
		t.Errorf("settings store not used: %s found=%v", v, found)
	}
	if _, found, _ := creds.Get(nsProviders, "minimax"); !found {
		t.Error("credentials store not used")
	}
	// Separate ports really are separate: nothing leaked across.
	if _, found, _ := settings.Get(nsProviders, "minimax"); found {
		t.Error("a credential leaked into the settings store")
	}
}

// Registering after the manager is in use is rejected and changes nothing.
func TestLateRegistrationIsRejected(t *testing.T) {
	resetRegistry(t)
	first := configstore.NewInMemoryStore()
	_ = SetSettingsStore(first)
	m := GetSettingsManager()

	if err := SetSettingsStore(configstore.NewInMemoryStore()); !errors.Is(err, configstore.ErrAlreadyInitialized) {
		t.Fatalf("late SetSettingsStore = %v, want ErrAlreadyInitialized", err)
	}
	if GetSettingsManager() != m {
		t.Error("the manager changed after a rejected registration")
	}

	_ = GetCredentialsManager()
	if err := SetCredentialsStore(configstore.NewInMemoryStore()); !errors.Is(err, configstore.ErrAlreadyInitialized) {
		t.Fatalf("late SetCredentialsStore = %v, want ErrAlreadyInitialized", err)
	}
}

func TestNilStoreIsRejected(t *testing.T) {
	resetRegistry(t)
	if err := SetSettingsStore(nil); err == nil {
		t.Error("SetSettingsStore(nil) should error")
	}
	if err := SetCredentialsStore(nil); err == nil {
		t.Error("SetCredentialsStore(nil) should error")
	}
}

// With nothing registered, the managers fall back to FileStore on
// ~/.harness/{settings,credentials}.json.
func TestDefaultsToFileStoreUnderHome(t *testing.T) {
	resetRegistry(t)
	t.Setenv("HOME", t.TempDir())
	if _, ok := GetSettingsManager().store.(*configstore.FileStore); !ok {
		t.Errorf("default settings store = %T, want *configstore.FileStore", GetSettingsManager().store)
	}
	if _, ok := GetCredentialsManager().store.(*configstore.FileStore); !ok {
		t.Errorf("default credentials store = %T, want *configstore.FileStore", GetCredentialsManager().store)
	}
}
