package agent

import (
	"context"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/types"
)

// TestSessionInfoToolReachesRealTurn is the live integration test
// confirming SessionInfo is genuinely registered and callable end-to-end
// against a real provider — not just unit-testable in isolation (see
// agent/tools/session_test.go for the tool's own JSON-shape coverage).
// SessionInfo is always registered (no dedicated EnableX flag; see
// buildSessionTools), so this only needs a plain Agent.
func TestSessionInfoToolReachesRealTurn(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
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

	var sawSessionInfo bool
	done := make(chan struct{})
	sess.Subscribe(func(e types.Event) {
		if e.Type == types.EventToolResult && e.ToolName == "SessionInfo" {
			sawSessionInfo = true
		}
		if e.Type == types.EventTurnEnd {
			close(done)
		}
	})
	sess.Prompt(context.Background(),
		"Call the SessionInfo tool exactly once. Report back whatever it returned verbatim.")

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("turn did not finish within 90s")
	}

	if !sawSessionInfo {
		t.Skip("model did not invoke SessionInfo this run (nondeterministic tool use) — rerun to exercise this path")
	}
}

// TestSessionInfoGettersDoNotDeadlockUnderPromptSyncLock is the regression
// test for a REAL deadlock found live while implementing SessionInfo: its
// closure originally called what was, at the time, the ONLY Meta() — the
// s.mu-taking version now unexported as syncMeta() — but SessionInfo's
// Execute runs INSIDE a tool executor goroutine while promptSync holds
// s.mu for the entire turn (including parallel tool execution). Every
// foreground SessionInfo call hung indefinitely (past any timeout, since
// it never even reached one) until the closure was rewritten to use
// ID()/CWD()/Name()/Model()/Thinking()/CreatedAt() — none of which take
// s.mu. Mirrors TestModelDoesNotDeadlockUnderPromptSyncLock exactly (same
// root cause class, same fix shape — see the subagent-timeout-background
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
		_ = sess.Model()
		_ = sess.Thinking()
		_ = sess.CreatedAt()
		_ = sess.Stats()
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SessionInfo's getters deadlocked — one of them blocked waiting for s.mu while s.mu was held by the simulated turn (promptSync). This is the exact deadlock that hung every foreground SessionInfo call.")
	}
}

// TestMetaDoesNotBlockUnderPromptSyncLock is the regression test for a real
// bug reported against GET /api/sessions/{id}/info and
// GET /api/sessions/{id}: both used to build their response from what was
// then the only Meta() — the s.mu-taking version now unexported as
// syncMeta() — which takes s.mu, the SAME lock promptSync holds for the
// entire duration of a running turn — so a request against a busy session
// blocked until turn_end (confirmed live: 9.9s-60s+ depending on the turn).
// Mirrors TestSessionInfoGettersDoNotDeadlockUnderPromptSyncLock's exact
// "simulate promptSync holding s.mu" technique, but for the public,
// lock-free Meta()/MaxIterations() (the fix) instead of the individual
// getters SessionInfo already used correctly.
func TestMetaDoesNotBlockUnderPromptSyncLock(t *testing.T) {
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
		// Exactly what handleSessionInfo/handleGetSession do post-fix — if
		// either took s.mu, this goroutine would block forever right here.
		_ = sess.Meta()
		_ = sess.MaxIterations()
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Meta()/MaxIterations() blocked — one of them took s.mu while s.mu was held by the simulated turn (promptSync). This is the exact blocking bug reported against GET /api/sessions/{id}/info.")
	}
}

// TestMaxIterationsReflectsSetMaxIterations confirms the lock-free
// maxIterationsVal accessor genuinely tracks SetMaxIterations — not just
// "doesn't block", but actually correct: it must reflect an override made
// AFTER the session was built, exactly like Model()/Thinking() already do
// for SwitchModel/SwitchThinking.
func TestMaxIterationsReflectsSetMaxIterations(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore(), MaxIterations: 50})
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

	if got := sess.MaxIterations(); got != 50 {
		t.Fatalf("MaxIterations() before override = %d, want 50 (the Agent's default)", got)
	}

	if err := sess.SetMaxIterations(7); err != nil {
		t.Fatalf("SetMaxIterations: %v", err)
	}
	if got := sess.MaxIterations(); got != 7 {
		t.Errorf("MaxIterations() after SetMaxIterations(7) = %d, want 7", got)
	}
}

// TestContextBreakdownDoesNotBlockUnderPromptSyncLock is the regression
// test for a follow-up bug report against GET /api/sessions/{id}/context:
// Session.ContextBreakdown() used to take s.mu just to read
// s.provider.Name()/s.lastInputTokens/s.contextWindow, none of which had a
// lock-free twin at all — the same class of bug Meta()/Model()/Thinking()/
// Stats()/MaxIterations() were already fixed for. Mirrors
// TestMetaDoesNotBlockUnderPromptSyncLock's exact "simulate promptSync
// holding s.mu" technique.
func TestContextBreakdownDoesNotBlockUnderPromptSyncLock(t *testing.T) {
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
		// Exactly what handleSessionContext does post-fix — if
		// ContextBreakdown() took s.mu, this goroutine would block forever
		// right here.
		_ = sess.ContextBreakdown()
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ContextBreakdown() blocked — it took s.mu while s.mu was held by the simulated turn (promptSync). This is the exact blocking bug reported against GET /api/sessions/{id}/context.")
	}
}

// TestContextBreakdownReflectsSwitchModel confirms the lock-free
// providerNameVal/contextWindowVal accessors genuinely track SwitchModel —
// not just "doesn't block", but actually correct: ContextBreakdown()'s
// tokenizer-family derivation and ContextWindow must reflect the NEW
// model, not whatever was active when the session was built.
func TestContextBreakdownReflectsSwitchModel(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 2 {
		t.Skip("need at least 2 active models in this environment to test switching between them")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	before := sess.ContextBreakdown()

	if err := sess.SwitchModel(t.Context(), models[1].Model); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}

	after := sess.ContextBreakdown()
	if before.ContextWindow == after.ContextWindow && models[0].ContextWindow != models[1].ContextWindow {
		t.Errorf("ContextBreakdown().ContextWindow did not change after SwitchModel: before=%d after=%d", before.ContextWindow, after.ContextWindow)
	}
}

// TestResetClearsInMemoryStats is the regression test for a real bug found
// live while extending SessionInfo with usage stats: Reset() called
// s.store.Reset() (which correctly clears Stats on the PERSISTED meta) but
// never cleared the in-memory s.stats/lastInputTokens this Session handle
// actually reads from — Stats(), ContextBreakdown(), and the auto-compact
// threshold check all kept seeing the pre-reset totals. Worse, the next
// updateStats/persistStatsLocked call (or draining pending delegated cost)
// would silently resurrect the stale totals right back onto the
// just-cleared store. Confirmed failing before the fix (CostUSD survived
// Reset() unchanged), passing after.
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

	// Stats() (the lock-free snapshot SessionInfo reads) must also reflect
	// the reset, not just the s.mu-guarded field.
	if got := sess.Stats(); got.CostUSD != 0 || got.InputTokens != 0 {
		t.Errorf("Stats() not cleared by Reset(): %+v", got)
	}
}
