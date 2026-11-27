package configstore_test

import (
	"path/filepath"
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/configstore/storetest"
)

// Every implementation is held to the exact same contract (see storetest).
func TestContract_FileStore(t *testing.T) {
	storetest.RunContract(t, func(t *testing.T) storetest.Store {
		fs, err := configstore.NewFileStore(filepath.Join(t.TempDir(), "store.json"), 0600)
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		return fs
	})
}

func TestContract_InMemoryStore(t *testing.T) {
	storetest.RunContract(t, func(*testing.T) storetest.Store { return configstore.NewInMemoryStore() })
}
