package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// TestSessionContextDoesNotBlockDuringInFlightTurn is the live acceptance
// test for a follow-up bug report: GET /api/sessions/{id}/context (built on
// Session.ContextBreakdown()) is documented as a fast, read-only snapshot,
// but it used to take s.mu — the SAME mutex promptSync holds locked from
// turn_start to turn_end — just to read s.provider.Name()/s.lastInputTokens/
// s.contextWindow, none of which had a lock-free twin at all (unlike
// modelStr/thinkingStr/statsSnapshot/maxIterationsVal, already fixed for
// Meta()/Model()/Thinking()/Stats()/MaxIterations()). Same class of bug,
// same fix shape: providerNameVal/lastInputTokensVal/contextWindowVal now
// mirror the fields ContextBreakdown() reads.
//
// Mirrors TestSessionInfoDoesNotBlockDuringInFlightTurn's exact technique:
// starts a REAL turn, polls IsBusy() (robust to fast/slow models) rather
// than a fixed sleep, then races a real GET /context request against it.
// The request must return near-instantly, not wait for the turn to finish.
func TestSessionContextDoesNotBlockDuringInFlightTurn(t *testing.T) {
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	srv := NewServer(a, ServerOptions{Logger: logx.NewNilLogger()})
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	srv.mu.Lock()
	srv.sessions[sess.ID()] = newSessionProxy(sess, logx.NewNilLogger())
	srv.mu.Unlock()

	turnDone := make(chan struct{})
	sess.Subscribe(func(e types.Event) {
		if e.Type == types.EventTurnEnd {
			close(turnDone)
		}
	})
	sess.Prompt(context.Background(),
		"Write a detailed, at least 500-word essay about the history of the number zero, "+
			"covering its origins in ancient Babylon, India, and the Maya, plus its adoption "+
			"in the Islamic world and Europe. Do not call any tools. Just write the essay "+
			"directly, without stopping to ask questions.")

	deadline := time.Now().Add(10 * time.Second)
	for !sess.IsBusy() {
		if time.Now().After(deadline) {
			t.Skip("turn never observed busy within 10s — can't reliably race /context against it in this environment")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctxStart := time.Now()
	resp, err := http.Get(ts.URL + "/api/sessions/" + sess.ID() + "/context")
	ctxElapsed := time.Since(ctxStart)
	if err != nil {
		t.Fatalf("GET /context: %v", err)
	}
	defer resp.Body.Close()

	t.Logf("GET /context took %v", ctxElapsed)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /context status = %d, want 200", resp.StatusCode)
	}

	// Same ceiling as TestSessionInfoDoesNotBlockDuringInFlightTurn: 3s is
	// generous — comfortably below what the reported bug would exhibit
	// (tracking the whole turn's duration), comfortably above normal
	// request overhead.
	if ctxElapsed > 3*time.Second {
		t.Errorf("GET /context took %v while a turn was in flight — it blocked on the turn instead of returning a live snapshot (the exact regression this test guards against)", ctxElapsed)
	}

	<-turnDone // let the turn finish before the test (and its defers) tear down
}
