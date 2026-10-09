package cli

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	io.Copy(&buf, r)
	if runErr != nil {
		t.Fatalf("command failed: %v", runErr)
	}
	return buf.String()
}

// `harness telegram pair/unpair/list` keep their exact output after moving
// from the removed telegram.Pair/Unpair/ListPaired to the silent API.
func TestTelegramPairingCLIOutputUnchanged(t *testing.T) {
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))

	steps := []struct {
		run  func() error
		want string
	}{
		{(&telegramListCmd{}).Run, "No paired chats. Run 'harness telegram pair <chat_id>'.\n"},
		{(&telegramPairCmd{ChatID: 123}).Run, "Paired chat 123.\n"},
		{(&telegramPairCmd{ChatID: 123}).Run, "Chat 123 was already paired.\n"},
		{(&telegramListCmd{}).Run, "Paired chats:\n  123\n"},
		{(&telegramUnpairCmd{ChatID: 123}).Run, "Unpaired chat 123.\n"},
		{(&telegramUnpairCmd{ChatID: 123}).Run, "Chat 123 was not paired.\n"},
	}
	for i, s := range steps {
		if got := captureStdout(t, s.run); got != s.want {
			t.Errorf("step %d: output = %q, want %q", i, got, s.want)
		}
	}
}
