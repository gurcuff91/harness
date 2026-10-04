package slack

import (
	"os"
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

// TestMain runs every test in this package against fresh in-memory settings
// and credentials stores. Tests here start real servers, which register
// themselves in the colleague registry (settings store) — without this they
// would write to the real ~/.harness of whoever runs the suite.
func TestMain(m *testing.M) {
	restore := config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore())
	code := m.Run()
	restore()
	os.Exit(code)
}
