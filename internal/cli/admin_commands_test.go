package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/store"
)

// capture runs fn and returns what it printed to stdout.
func capture(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err := fn()
	w.Close()
	os.Stdout = old
	return <-done, err
}

// These commands used to start an in-memory HTTP server and talk to it through
// a client; they now call admin directly. Each runs here with no server and no
// agent, over the process-global stores that isolate() swaps.

func TestSettingsCommandsWithoutAServer(t *testing.T) {
	isolate(t)
	ad := admin.New()
	defer ad.Close()
	out, err := capture(t, func() error { return RunSettingsSet(context.Background(), ad, "thinking", "high", "text") })
	if err != nil || out != "thinking = high\n" {
		t.Fatalf("set: %q %v", out, err)
	}
	out, err = capture(t, func() error { return RunSettings(context.Background(), ad, "text") })
	if err != nil || out != "model            (none)\nthinking         high\n" {
		t.Fatalf("show: %q %v", out, err)
	}
	if _, err := capture(t, func() error { return RunSettingsSet(context.Background(), ad, "thinking", "bogus", "text") }); err == nil || !strings.Contains(err.Error(), "set thinking:") {
		t.Fatalf("a bad level must fail: %v", err)
	}
}

func TestMCPAndCustomProviderCommandsWithoutAServer(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	ad := admin.New()
	defer ad.Close()
	if out, err := capture(t, func() error { return RunMCPAdd(ctx, ad, "wiki", MCPAddOpts{URL: "http://x/mcp"}, "text") }); err != nil || out != "MCP server added: wiki\n" {
		t.Fatalf("add: %q %v", out, err)
	}
	if out, err := capture(t, func() error { return RunMCPSetEnabled(ctx, ad, "wiki", false, "text") }); err != nil || out != "MCP server disabled: wiki\n" {
		t.Fatalf("disable: %q %v", out, err)
	}
	if srv, err := ad.MCP.Get("wiki"); err != nil || !srv.Disabled || srv.URL != "http://x/mcp" {
		t.Fatalf("stored %+v %v", srv, err)
	}
	if err := RunMCPSetEnabled(ctx, ad, "ghost", true, "text"); err == nil || err.Error() != `mcp server "ghost" not found` {
		t.Fatalf("missing: %v", err)
	}
	if _, err := capture(t, func() error { return RunMCPRemove(ctx, ad, "wiki", "text") }); err != nil {
		t.Fatal(err)
	}
	if err := RunMCPRemove(ctx, ad, "wiki", "text"); err == nil || !strings.Contains(err.Error(), "remove mcp") {
		t.Fatalf("second remove: %v", err)
	}

	if out, err := capture(t, func() error {
		return RunProviderAdd(ctx, ad, "gw", ProviderAddOpts{Type: "openai", URL: "http://gw/v1"}, "text")
	}); err != nil || out != "Custom provider added: gw\n" {
		t.Fatalf("provider add: %q %v", out, err)
	}
	out, err := capture(t, func() error { return RunProviderList(ctx, ad, "text") })
	if err != nil || !strings.HasPrefix(out, "gw ") {
		t.Fatalf("provider list: %q %v", out, err)
	}
	if err := RunProviderAdd(ctx, ad, "bad", ProviderAddOpts{Type: "nope", URL: "http://x"}, "text"); err == nil {
		t.Fatal("an invalid provider must fail")
	}
}

func TestSessionsAndDeleteWithoutAServer(t *testing.T) {
	isolate(t)
	st := store.NewInMemoryStore()
	if err := st.SaveMeta(store.SessionMeta{ID: "abcdef0123456789", CWD: "/p", Name: "mine", Model: "p/m"}); err != nil {
		t.Fatal(err)
	}
	ad := admin.New(admin.WithSessionStore(st))
	defer ad.Close()
	ctx := context.Background()
	out, err := capture(t, func() error { return RunSessions(ctx, ad, true, "text") })
	if err != nil || !strings.Contains(out, "abcdef0123456789  mine") || !strings.Contains(out, "p/m") {
		t.Fatalf("sessions: %q %v", out, err)
	}
	if err := RunDelete(ctx, ad, "ghost", "text"); err == nil || !strings.HasPrefix(err.Error(), "session not found: ghost\nRun 'harness sessions --all'") {
		t.Fatalf("delete unknown: %v", err)
	}
	if out, err := capture(t, func() error { return RunDelete(ctx, ad, "abcdef0123456789", "text") }); err != nil || out != "Session deleted: abcdef0123456789\n" {
		t.Fatalf("delete: %q %v", out, err)
	}
	if metas, _ := st.ListMetas(""); len(metas) != 0 {
		t.Fatalf("still stored: %v", metas)
	}
}

func TestProvidersConnectDisconnectWithoutAServer(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	ad := admin.New()
	defer ad.Close()
	out, err := capture(t, func() error { return RunProviders(ctx, ad, "text") })
	if err != nil || !strings.Contains(out, "anthropic") {
		t.Fatalf("providers: %q %v", out, err)
	}
	if err := RunConnect(ctx, ad, "ghost", "k", "text"); err == nil || !strings.HasPrefix(err.Error(), "unknown provider: ghost\nRun 'harness providers'") {
		t.Fatalf("connect unknown: %v", err)
	}
	if err := RunDisconnect(ctx, ad, "ghost", "text"); err == nil || !strings.HasPrefix(err.Error(), "unknown provider: ghost") {
		t.Fatalf("disconnect unknown: %v", err)
	}
	if err := RunConnect(ctx, ad, "", "k", "text"); err == nil || err.Error() != "provider name required" {
		t.Fatalf("empty: %v", err)
	}
}

func TestMemoAndSchedulesWithoutAServer(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	mem := defaultMemory()
	if mem == nil {
		t.Skip("no memory store available")
	}
	defer mem.Close()
	if _, err := mem.Write(globalCWD, "note", "remember this", true); err != nil {
		t.Fatal(err)
	}
	ad := admin.New(admin.WithMemory(mem))
	defer ad.Close()
	out, err := capture(t, func() error { return RunMemo(ctx, ad, MemoOpts{Global: true, Content: true}, "text") })
	if err != nil || !strings.Contains(out, "1 memories (showing 1):") || !strings.Contains(out, "• note") || !strings.Contains(out, "    remember this") {
		t.Fatalf("memo: %q %v", out, err)
	}
	out, err = capture(t, func() error { return RunSchedules(ctx, ad, "text") })
	if err != nil || out != "No schedules.\n" {
		t.Fatalf("schedules: %q %v", out, err)
	}
	out, err = capture(t, func() error { return RunSchedules(ctx, ad, "json") })
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("schedules json: %q %v", out, err)
	}
}
