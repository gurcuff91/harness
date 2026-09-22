package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// TestSessionInfoDoesNotBlockDuringInFlightTurn is the live acceptance test
// for a reported regression: GET /api/sessions/{id}/info (and, sharing the
// same code path, GET /api/sessions/{id}) is documented and intended as a
// fast, read-only snapshot endpoint, but it used to block for the entire
// duration of a running turn — built from Session.Meta(), which takes s.mu,
// the SAME mutex promptSync holds locked from turn_start to turn_end.
// Confirmed live before the fix: a real request against a busy session
// blocked 9.9s-60s+ depending on the turn, resolving the instant turn_end
// fired.
//
// This test starts a REAL turn (not a simulated lock, unlike
// agent/session_info_test.go's TestMetaDoesNotBlockUnderPromptSyncLock
// — that one guards the mechanism, this one proves the actual HTTP endpoint
// behaves correctly end-to-end) and concurrently hits GET /info from a
// second HTTP request while the turn is confirmed still in flight (polls
// IsBusy() rather than a fixed sleep, so this is robust to fast and slow
// models alike). The /info request must return near-instantly, not wait
// for the turn to finish.
func TestSessionInfoDoesNotBlockDuringInFlightTurn(t *testing.T) {
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

	// Register the session in the server's active map directly — same shape
	// handleCreateSession/handleResumeSession leave it in, without needing an
	// extra HTTP round trip.
	srv.mu.Lock()
	srv.sessions[sess.ID()] = newSessionProxy(sess, logx.NewNilLogger())
	srv.mu.Unlock()

	turnDone := make(chan struct{})
	sess.Subscribe(func(e types.Event) {
		if e.Type == types.EventTurnEnd {
			close(turnDone)
		}
	})
	// A long-enough generation task that a real model can't finish
	// instantly, so there's a real window to race /info against. Some
	// models may still be fast enough to finish before the poll below ever
	// observes IsBusy() — handled by skipping in that (rare) case rather
	// than risking a false pass, see the poll loop's own comment.
	sess.Prompt(context.Background(),
		"Write a detailed, at least 500-word essay about the history of the number zero, "+
			"covering its origins in ancient Babylon, India, and the Maya, plus its adoption "+
			"in the Islamic world and Europe. Do not call any tools. Just write the essay "+
			"directly, without stopping to ask questions.")

	// Poll for the turn to actually become busy (promptSync holding s.mu)
	// before racing /info against it — a fixed sleep would be flaky across
	// environments/models of very different speeds.
	deadline := time.Now().Add(10 * time.Second)
	for !sess.IsBusy() {
		if time.Now().After(deadline) {
			t.Skip("turn never observed busy within 10s — can't reliably race /info against it in this environment")
		}
		time.Sleep(5 * time.Millisecond)
	}

	infoStart := time.Now()
	resp, err := http.Get(ts.URL + "/api/sessions/" + sess.ID() + "/info")
	infoElapsed := time.Since(infoStart)
	if err != nil {
		t.Fatalf("GET /info: %v", err)
	}
	defer resp.Body.Close()

	t.Logf("GET /info took %v", infoElapsed)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /info status = %d, want 200", resp.StatusCode)
	}
	var info sessionInfoDTO
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode /info response: %v", err)
	}

	// The core assertion: /info must return in roughly the same order of
	// magnitude as any other non-blocking read (a couple hundred ms at
	// most for HTTP round trip + JSON), NOT wait anywhere close to how long
	// a real multi-second turn takes. 3s is a generous ceiling — comfortably
	// below what the reported bug exhibited (9.9s-60s+), comfortably above
	// normal request overhead.
	if infoElapsed > 3*time.Second {
		t.Errorf("GET /info took %v while a turn was in flight — it blocked on the turn instead of returning a live snapshot (the exact regression this test guards against)", infoElapsed)
	}

	<-turnDone // let the turn finish before the test (and its defers) tear down
}
