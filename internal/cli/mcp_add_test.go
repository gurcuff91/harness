package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

// `harness mcp add --timeout/--call-timeout` must reach settings.json: the two are separate
// settings (connect vs one tool call) and the second is what lets a remote tool run for minutes.
func TestMCPAddStoresTheTimeouts(t *testing.T) {
	restore := config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore())
	defer restore()
	t.Setenv("HOME", t.TempDir())
	a := newConfigAgent()
	defer a.Close()

	if err := RunMCPAdd(context.Background(), a, "wiki", MCPAddOpts{URL: "http://127.0.0.1:8787/mcp", Timeout: 7000, CallTimeout: 600000}, "text"); err != nil {
		t.Fatal(err)
	}
	got, ok := config.GetSettingsManager().MCPServer("wiki")
	if !ok || got.Timeout != 7000 || got.CallTimeout != 600000 || got.URL == "" {
		t.Fatalf("stored %+v (found=%v), want url + timeout 7000 + call_timeout 600000", got, ok)
	}

	// Left unset they stay unset (the defaults apply at connect time, not at write time).
	if err := RunMCPAdd(context.Background(), a, "plain", MCPAddOpts{Command: "echo hi"}, "text"); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.GetSettingsManager().MCPServer("plain"); got.Timeout != 0 || got.CallTimeout != 0 {
		t.Errorf("unset timeouts were stored as %d / %d", got.Timeout, got.CallTimeout)
	}

	for _, o := range []MCPAddOpts{{URL: "http://x/mcp", Timeout: -1}, {URL: "http://x/mcp", CallTimeout: -5}} {
		err := RunMCPAdd(context.Background(), a, "neg", o, "text")
		if err == nil || !strings.Contains(err.Error(), "negative") {
			t.Errorf("%+v: err = %v, want a negative-value refusal", o, err)
		}
	}
	if _, ok := config.GetSettingsManager().MCPServer("neg"); ok {
		t.Error("a refused add stored the server anyway")
	}
}

// The same through the real command line: Main parses the flags with Kong and runs the command, so
// this is what catches a flag that is declared but never reaches RunMCPAdd.
func TestMCPAddFlagsThroughTheRealCommandLine(t *testing.T) {
	restore := config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore())
	defer restore()
	t.Setenv("HOME", t.TempDir())

	if code := Main([]string{"mcp", "add", "wiki", "--url", "http://127.0.0.1:8787/mcp", "--timeout", "7000", "--call-timeout", "600000"}); code != 0 {
		t.Fatalf("harness mcp add exited %d", code)
	}
	got, ok := config.GetSettingsManager().MCPServer("wiki")
	if !ok || got.Timeout != 7000 || got.CallTimeout != 600000 {
		t.Fatalf("stored %+v (found=%v), want timeout 7000 and call_timeout 600000", got, ok)
	}
	if code := Main([]string{"mcp", "add", "bare", "--url", "http://x/mcp"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got, _ := config.GetSettingsManager().MCPServer("bare"); got.Timeout != 0 || got.CallTimeout != 0 {
		t.Errorf("flags left off were stored as %d / %d", got.Timeout, got.CallTimeout)
	}
}
