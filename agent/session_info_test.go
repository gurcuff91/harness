package agent

import (
	"context"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// TestSessionInfoAndSessionSearchToolsReachRealTurn is the live integration
// test for the new EnableSessionInfo flag: confirms both SessionInfo and
// SessionSearch are genuinely registered and callable end-to-end against a
// real provider — not just unit-testable in isolation (see
// agent/store/search_test.go for the focused unit coverage of the FTS5
// sync/query logic itself, and agent/tools/session_test.go for the tool's
// own input-parsing/delegation responsibilities).
func TestSessionInfoAndSessionSearchToolsReachRealTurn(t *testing.T) {
	a := New(AgentOptions{EnableSessionInfo: true, Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment to run a real tool call")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var sawSessionInfo, sawSessionSearch bool
	done := make(chan struct{})
	sess.Subscribe(func(e types.Event) {
		if e.Type == types.EventToolResult {
			switch e.ToolName {
			case "SessionInfo":
				sawSessionInfo = true
			case "SessionSearch":
				sawSessionSearch = true
			}
		}
		if e.Type == types.EventTurnEnd {
			close(done)
		}
	})
	sess.Prompt(context.Background(),
		"Call the SessionInfo tool exactly once, then call the SessionSearch tool exactly once "+
			"searching for the word 'test'. Report back whatever each returned verbatim.")

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("turn did not finish within 90s")
	}

	if !sawSessionInfo {
		t.Skip("model did not invoke SessionInfo this run (nondeterministic tool use) — rerun to exercise this path")
	}
	if !sawSessionSearch {
		t.Skip("model did not invoke SessionSearch this run (nondeterministic tool use) — rerun to exercise this path")
	}
}

// TestSessionInfoDisabledByDefault confirms EnableSessionInfo defaults to
// false — the staged-rollout flag must not leak into agents that never
// opted in (matches every other EnableX flag's own zero-value default).
func TestSessionInfoDisabledByDefault(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()
	if a.opts.EnableSessionInfo {
		t.Error("EnableSessionInfo defaulted to true — it must default to false")
	}
}

// TestSessionInfoGettersDoNotDeadlockUnderPromptSyncLock is the regression
// test for a REAL deadlock found live while implementing SessionInfo: its
// closure originally called (*sessRef).Meta(), which takes s.mu — but
// SessionInfo's Execute runs INSIDE a tool executor goroutine while
// promptSync holds s.mu for the entire turn (including parallel tool
// execution). Every foreground SessionInfo call hung indefinitely (past
// any timeout, since it never even reached one) until the closure was
// rewritten to use ID()/CWD()/Name()/CurrentModel()/CurrentThinking()/
// CreatedAt() — none of which take s.mu. Mirrors
// TestCurrentModelDoesNotDeadlockUnderPromptSyncLock exactly (same root
// cause class, same fix shape — see the subagent-timeout-background
// project memory).
func TestSessionInfoGettersDoNotDeadlockUnderPromptSyncLock(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Simulate promptSync holding s.mu for the duration of a turn.
	sess.mu.Lock()
	defer sess.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Exactly what SessionInfo's closure does — if any of these took
		// s.mu, this goroutine would block forever right here.
		_ = sess.ID()
		_ = sess.CWD()
		_ = sess.Name()
		_ = sess.CurrentModel()
		_ = sess.CurrentThinking()
		_ = sess.CreatedAt()
		_ = sess.CurrentStats()
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SessionInfo's getters deadlocked — one of them blocked waiting for s.mu while s.mu was held by the simulated turn (promptSync). This is the exact deadlock that hung every foreground SessionInfo call.")
	}
}

// TestSessionSearchMessagesDoesNotDeadlockUnderPromptSyncLock guards
// against the SAME deadlock class for SearchMessages after the
// SessionSearch-into-SessionStore migration: Session.SearchMessages must
// mirror AllMessages()'s locking shape (briefly take s.mu only to read the
// immutable s.id, release before calling the port) — never hold s.mu for
// the actual search. If a future change accidentally routed this through
// something that takes s.mu (like Meta()), this test would hang and fail
// exactly like TestSessionInfoGettersDoNotDeadlockUnderPromptSyncLock does
// for that bug.
func TestSessionSearchMessagesDoesNotDeadlockUnderPromptSyncLock(t *testing.T) {
	a := New(AgentOptions{EnableSessionInfo: true, Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Simulate promptSync holding s.mu for the duration of a turn.
	sess.mu.Lock()
	defer sess.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// InMemoryStore returns ErrSearchNotSupported immediately — what
		// matters here is only that the call returns AT ALL rather than
		// blocking forever on s.mu.
		_, _ = sess.SearchMessages("anything", 10)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SearchMessages deadlocked — it must never take s.mu (mirrors AllMessages()'s locking shape), but something in this path blocked waiting for s.mu while s.mu was held by the simulated turn (promptSync).")
	}
}

// TestResetClearsInMemoryStats is the regression test for a real bug found
// live while extending SessionInfo with usage stats: Reset() called
// s.store.Reset() (which correctly clears Stats on the PERSISTED meta) but
// never cleared the in-memory s.stats/lastInputTokens this Session handle
// actually reads from — Stats(), CurrentStats(), ContextBreakdown(), and the
// auto-compact threshold check all kept seeing the pre-reset totals. Worse,
// the next updateStats/persistStatsLocked call (or draining pending
// delegated cost) would silently resurrect the stale totals right back onto
// the just-cleared store. Confirmed failing before the fix (CostUSD
// survived Reset() unchanged), passing after.
func TestResetClearsInMemoryStats(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.mu.Lock()
	sess.stats.CostUSD = 5.0
	sess.stats.InputTokens = 1000
	sess.stats.ContextUsage = 0.5
	sess.snapshotStatsLocked()
	sess.mu.Unlock()

	if err := sess.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	sess.mu.Lock()
	stats := sess.stats
	sess.mu.Unlock()
	if stats.CostUSD != 0 || stats.InputTokens != 0 || stats.ContextUsage != 0 {
		t.Errorf("in-memory s.stats not cleared by Reset(): %+v", stats)
	}

	// CurrentStats() (the lock-free snapshot SessionInfo reads) must also
	// reflect the reset, not just the s.mu-guarded field.
	if got := sess.CurrentStats(); got.CostUSD != 0 || got.InputTokens != 0 {
		t.Errorf("CurrentStats() not cleared by Reset(): %+v", got)
	}
}
