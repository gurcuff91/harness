package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/internal/tui/components"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
	"github.com/gurcuff91/harness/types"
)

// fallbackFixture is a real server with two providers, a session saved on the
// first, and the default model on the second. disconnect(name) removes a
// provider from the registry — "the provider got disconnected".
type fallbackFixture struct {
	c             *client.Client
	sessionID     string
	first, second string
	disconnect    func(name string)
}

// newFallbackFixture isolates the process-global provider registry to EXACTLY the
// two providers of the test, so "nothing is left to fall back to" means nothing —
// not "nothing except whatever an earlier test of the binary left registered".
func newFallbackFixture(t *testing.T) *fallbackFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), nil))
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(gw.Close)
	histSeq++
	f := &fallbackFixture{first: fmt.Sprintf("tuifb-a-%d", histSeq), second: fmt.Sprintf("tuifb-b-%d", histSeq)}
	models := func() ([]types.ModelMeta, error) {
		return []types.ModelMeta{{ID: "m", ContextWindow: 100000, MaxTokens: 4096}}, nil
	}
	for _, n := range []string{f.first, f.second} {
		if err := agent.NewOpenAIProvider(n, gw.URL, agent.ProviderWithFetchModels(models)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(f.first); agent.UnregisterCustomProvider(f.second) })
	t.Cleanup(providers.SetForTest([]providers.Provider{providers.Find(f.first), providers.Find(f.second)}))
	f.disconnect = func(name string) {
		var keep []providers.Provider
		for _, p := range providers.All() {
			if p.Name() != name {
				keep = append(keep, p)
			}
		}
		providers.SetForTest(keep) // restored by the Cleanup above, which holds the original
	}

	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := server.Start(a, "", server.ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	config.GetSettingsManager().SetActiveModel(f.second + "/m")

	f.c = client.New(srv.Addr())
	sess, err := f.c.CreateSession(f.first+"/m", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	f.sessionID = sess.ID
	return f
}

// screenText is the FULL text of every raw block on screen (blockSummary
// truncates each to 60 characters, which cuts a model name in half).
func screenText(t *TUI) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for _, blk := range t.history.Blocks() {
		if r, ok := blk.(*components.RawBlock); ok {
			b.WriteString(r.Text())
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (f *fallbackFixture) tui(resumeID string) *TUI {
	tu := newTestTUIForEvents()
	tu.client = f.c
	tu.baseCtx = context.Background()
	tu.resumeID = resumeID
	return tu
}

// Startup with --resume on a session whose provider is gone: the TUI used to
// print "Failed to resume" and drop to a fresh session. It must resume, on the
// default model, and tell the user.
func TestAutoConnectResumesThroughAModelFallback(t *testing.T) {
	f := newFallbackFixture(t)
	f.disconnect(f.first)
	tu := f.tui(f.sessionID)

	tu.autoConnect(context.Background())

	got := screenText(tu)
	if strings.Contains(got, "Failed to resume") {
		t.Fatalf("the resume was refused although a fallback model exists:\n%s", got)
	}
	if tu.sessionID != f.sessionID {
		t.Errorf("the TUI is on session %q, want the resumed %q", tu.sessionID, f.sessionID)
	}
	if tu.model != f.second+"/m" {
		t.Errorf("model = %q, want the default %q", tu.model, f.second+"/m")
	}
	for _, want := range []string{"resumed:", f.first + "/m", "not available", f.second + "/m"} {
		if !strings.Contains(got, want) {
			t.Errorf("the screen lacks %q:\n%s", want, got)
		}
	}
}

// A normal resume shows no warning.
func TestAutoConnectResumeWithoutFallbackIsQuiet(t *testing.T) {
	f := newFallbackFixture(t)
	tu := f.tui(f.sessionID)
	tu.autoConnect(context.Background())
	got := screenText(tu)
	if tu.sessionID != f.sessionID || tu.model != f.first+"/m" || strings.Contains(got, "not available") {
		t.Errorf("session=%q model=%q\n%s", tu.sessionID, tu.model, got)
	}
}

// The in-app /resume (switching sessions inside a running TUI) follows the same rule.
func TestResumeInPlaceThroughAModelFallback(t *testing.T) {
	f := newFallbackFixture(t)
	f.disconnect(f.first)
	tu := f.tui("")
	tu.resumeInPlace(f.sessionID)

	got := screenText(tu)
	if tu.sessionID != f.sessionID || tu.model != f.second+"/m" {
		t.Fatalf("session=%q model=%q\n%s", tu.sessionID, tu.model, got)
	}
	if !strings.Contains(got, f.first+"/m") || !strings.Contains(got, "not available") {
		t.Errorf("no fallback notice:\n%s", got)
	}
}

// With nothing to fall back to the TUI reports the refusal — it does not resume a
// half-session — and the message says what to do.
func TestResumeInPlaceRefusalIsReported(t *testing.T) {
	f := newFallbackFixture(t)
	f.disconnect(f.first)
	f.disconnect(f.second)
	tu := f.tui("")
	tu.resumeInPlace(f.sessionID)

	got := screenText(tu)
	if tu.sessionID == f.sessionID {
		t.Errorf("the TUI claims to have resumed %q with no usable model", f.sessionID)
	}
	for _, want := range []string{"Failed to resume", f.first + "/m", "connect a provider"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal on screen lacks %q:\n%s", want, got)
		}
	}
}

// The same refusal at startup (--resume): reported as a warning, never a crash and
// never a resumed half-session.
func TestAutoConnectResumeRefusalFallsThroughToAFreshSession(t *testing.T) {
	f := newFallbackFixture(t)
	f.disconnect(f.first)
	f.disconnect(f.second)
	tu := f.tui(f.sessionID)
	tu.autoConnect(context.Background())
	if tu.sessionID == f.sessionID {
		t.Errorf("resumed %q with nothing to run it on", f.sessionID)
	}
}
