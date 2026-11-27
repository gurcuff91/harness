package memory_test

import (
	"path/filepath"
	"testing"

	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/memory/storetest"
)

func TestSQLiteStorePassesTheContract(t *testing.T) {
	storetest.RunContract(t, func(t *testing.T) storetest.Store {
		s, err := memory.OpenSQLite(filepath.Join(t.TempDir(), "mem.db"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}
