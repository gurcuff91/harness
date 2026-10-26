package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
	"github.com/gurcuff91/harness/types"
)

var histSeq int

// resumedTUI starts a real server over a FileStore holding one session whose
// history is seeded from layout ("m" = message, "c" = compaction checkpoint;
// message texts are "m<index>"), and returns a TUI bound to it, ready to call
// renderHistory — the resume path.
func resumedTUI(t *testing.T, layout string) *TUI {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	fs, err := store.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(gw.Close)
	histSeq++
	prov := fmt.Sprintf("tuihist-%d", histSeq) // the provider registry is process-global
	if err := agent.NewOpenAIProvider(prov, gw.URL, agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
		return []types.ModelMeta{{ID: "m", ContextWindow: 100000, MaxTokens: 4096}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	// Leaving it registered leaked a live provider into every later test of the
	// binary — one that asserts "no provider is available" then finds it.
	t.Cleanup(func() { agent.UnregisterCustomProvider(prov) })
	a := agent.New(agent.AgentOptions{Store: fs, ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := server.Start(a, "", server.ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	c := client.New(srv.Addr())
	sess, err := c.CreateSession(prov+"/m", "/p", "")
	if err != nil {
		t.Fatal(err)
	}
	h, err := store.OpenSession(fs, sess.ID)
	if err != nil || h == nil {
		t.Fatalf("open: %v", err)
	}
	for i, ch := range layout {
		if ch == 'c' {
			err = h.AddCompactionSummary(fmt.Sprintf("s%d", i))
		} else {
			err = h.AddMessage(types.NewUserTextMessage(fmt.Sprintf("m%d", i)))
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	tu := newTestTUIForEvents()
	tu.client = c
	tu.sessionID = sess.ID
	return tu
}

func historyText(t *TUI) string { return strings.Join(blockSummary(t), "\n") }

// Resuming a session compacted at least twice replays only from just AFTER the
// second-to-last checkpoint: the earlier part is hidden (with a hint saying how
// much), the last pre-compaction block and the working set are shown, and the
// replay doesn't open on a bare "Compacting" marker.
func TestRenderHistoryResumesFromSecondToLastCheckpoint(t *testing.T) {
	// idx: 0 1 2(c) 3 4 5(c) 6 7 — checkpoints at 2 and 5 → cut = 3
	tu := resumedTUI(t, "mmcmmcmm")
	tu.renderHistory()
	got := historyText(tu)

	if !strings.Contains(got, "↑ 3 earlier messages hidden") {
		t.Errorf("missing/incorrect hidden hint:\n%s", got)
	}
	for _, hidden := range []string{"m0", "m1"} {
		if strings.Contains(got, hidden) {
			t.Errorf("%s is before the cut but was rendered:\n%s", hidden, got)
		}
	}
	for _, shown := range []string{"m3", "m4", "m6", "m7"} {
		if !strings.Contains(got, shown) {
			t.Errorf("%s should be rendered:\n%s", shown, got)
		}
	}
	// Exactly one "Compacting" marker survives: the LAST checkpoint (5); the
	// second-to-last (2) is the cut and is not rendered.
	if n := strings.Count(got, "Compacting"); n != 1 {
		t.Errorf("Compacting markers = %d, want 1:\n%s", n, got)
	}
}

// A session compacted fewer than twice is replayed whole, with no hint.
func TestRenderHistoryShortSessionIsWhole(t *testing.T) {
	for _, layout := range []string{"mmm", "mmcmm"} {
		tu := resumedTUI(t, layout)
		tu.renderHistory()
		got := historyText(tu)
		if strings.Contains(got, "earlier messages hidden") {
			t.Errorf("%q: unexpected hint:\n%s", layout, got)
		}
		if !strings.Contains(got, "m0") {
			t.Errorf("%q: first message missing:\n%s", layout, got)
		}
	}
}
