package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
)

// `harness mcp add --timeout/--connection-timeout` must reach settings.json: the two are separate
// settings (one tool call vs connecting), and the first is what lets a remote tool run for minutes.
func TestMCPAddStoresTheTimeouts(t *testing.T) {
	restore := config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore())
	defer restore()
	t.Setenv("HOME", t.TempDir())
	a := newConfigAgent()
	defer a.Close()

	if err := RunMCPAdd(context.Background(), a, "wiki", MCPAddOpts{URL: "http://127.0.0.1:8787/mcp", Timeout: 600000, ConnectionTimeout: 7000}, "text"); err != nil {
		t.Fatal(err)
	}
	got, ok := config.GetSettingsManager().MCPServer("wiki")
	if !ok || got.Timeout != 600000 || got.ConnectionTimeout != 7000 || got.URL == "" {
		t.Fatalf("stored %+v (found=%v), want url + timeout 600000 + connection_timeout 7000", got, ok)
	}

	// Left unset they stay unset (the defaults apply at connect time, not at write time).
	if err := RunMCPAdd(context.Background(), a, "plain", MCPAddOpts{Command: "echo hi"}, "text"); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.GetSettingsManager().MCPServer("plain"); got.Timeout != 0 || got.ConnectionTimeout != 0 {
		t.Errorf("unset timeouts were stored as %d / %d", got.Timeout, got.ConnectionTimeout)
	}

	for _, o := range []MCPAddOpts{{URL: "http://x/mcp", Timeout: -1}, {URL: "http://x/mcp", ConnectionTimeout: -5}} {
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

	if code := Main([]string{"mcp", "add", "wiki", "--url", "http://127.0.0.1:8787/mcp", "--timeout", "600000", "--connection-timeout", "7000"}); code != 0 {
		t.Fatalf("harness mcp add exited %d", code)
	}
	got, ok := config.GetSettingsManager().MCPServer("wiki")
	if !ok || got.Timeout != 600000 || got.ConnectionTimeout != 7000 {
		t.Fatalf("stored %+v (found=%v), want timeout 600000 and connection_timeout 7000", got, ok)
	}
	if code := Main([]string{"mcp", "add", "bare", "--url", "http://x/mcp"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got, _ := config.GetSettingsManager().MCPServer("bare"); got.Timeout != 0 || got.ConnectionTimeout != 0 {
		t.Errorf("flags left off were stored as %d / %d", got.Timeout, got.ConnectionTimeout)
	}
}

// The old flag names are gone: "call_timeout" was the per-call timeout until 0.101.3, and the old
// "--timeout" meant connecting. Both were replaced, so the old ones must not parse (Kong would
// reject an unknown flag) — a silent acceptance would store a value under a name nobody reads.
func TestTheOldTimeoutFlagNamesAreRejected(t *testing.T) {
	restore := config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore())
	defer restore()
	t.Setenv("HOME", t.TempDir())

	// The grammar itself is what must refuse them: parse the same arguments the command line
	// would, and expect an error (Main would print the usage and end the process, which a test
	// cannot survive).
	parser, err := newParser()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"mcp", "add", "old", "--url", "http://x/mcp", "--call-timeout", "600000"},
		{"mcp", "add", "old2", "--url", "http://x/mcp", "--connect-timeout", "7000"},
	} {
		if _, err := parser.Parse(args); err == nil {
			t.Errorf("%v: parsed with no error, want the unknown flag refused", args)
		} else if !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%v: err = %v, want an unknown-flag error", args, err)
		}
	}
	// and the new names DO parse
	for _, args := range [][]string{
		{"mcp", "add", "new", "--url", "http://x/mcp", "--timeout", "600000", "--connection-timeout", "7000"},
	} {
		if _, err := parser.Parse(args); err != nil {
			t.Errorf("%v: err = %v, want it to parse", args, err)
		}
	}
	if _, ok := config.GetSettingsManager().MCPServer("old"); ok {
		t.Error("a refused add stored the server anyway")
	}
}
