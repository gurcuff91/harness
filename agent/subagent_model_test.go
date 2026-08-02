package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent/store"
)

// TestCurrentModelReflectsSwitchModel is the regression test for the bug
// where the Subagent tool kept spawning sub-agents against a session's
// ORIGINAL model even after /model (or ACP's session/set_config_option)
// switched it to a different one — e.g. a session created with a
// rate-limited provider, switched to a working one, but every subsequent
// Subagent call still hit the rate-limited model.
//
// Root cause: buildSessionTools' Subagent executor closure used to capture
// the "model" parameter — a plain string, frozen at the moment the
// session's tools were built (once, at NewSession/ResumeSession/
// ForkSession time) — and never looked at it again. SwitchModel updates
// Session.modelID/provider (and the persisted store), but never touched
// that already-built tool closure.
//
// The fix threads a **Session (sessRef) into buildSessionTools instead of a
// model string, so the closure can call (*sessRef).CurrentModel() at
// EXECUTION time. This test can't invoke the Subagent tool end-to-end
// without a live provider call (see TestSubagentMaxIterationsIsCapped's
// comment for the same limitation), but it exercises the exact mechanism
// the fix relies on: CurrentModel() must reflect a SwitchModel call made
// after the session — and therefore its tools, including Subagent's
// closure — were already built.
func TestCurrentModelReflectsSwitchModel(t *testing.T) {
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

	if got := sess.CurrentModel(); got != models[0].Model {
		t.Fatalf("CurrentModel() = %q right after creation, want %q", got, models[0].Model)
	}

	if err := sess.SwitchModel(t.Context(), models[1].Model); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}

	if got := sess.CurrentModel(); got != models[1].Model {
		t.Errorf("CurrentModel() = %q after SwitchModel(%q), want %q — this is exactly what a Subagent call made AFTER a /model switch must see, not the original model",
			got, models[1].Model, models[1].Model)
	}
}

// TestCurrentModelDoesNotDeadlockUnderPromptSyncLock is the regression test
// for the deadlock introduced when CurrentModel() was first added (v0.74.7):
// it took s.mu.Lock(), but promptSync — the function that runs every turn —
// ALSO holds s.mu.Lock() for the ENTIRE turn (including the parallel tool
// execution phase, which is exactly when the Subagent tool's executor calls
// CurrentModel()). Result: a circular wait — the tool goroutine blocked
// waiting for s.mu while promptSync's wg.Wait() blocked waiting for the tool
// goroutine to finish. The hung process's stack trace showed exactly that:
//
//	goroutine N [sync.Mutex.Lock, 3 minutes]:
//	  Session.CurrentModel       ← waiting for s.mu
//	  Subagent executor
//	  runStream.func2            ← tool execution goroutine
//	goroutine M [sync.Mutex.Lock, 3 minutes]:
//	  WaitGroup.Wait             ← waiting for tools to finish
//	  runStream
//	  promptSync                 ← already holds s.mu
//
// This test reproduces the exact condition: it takes s.mu (simulating
// promptSync's hold during a turn) and then calls CurrentModel() from
// another goroutine (simulating the Subagent executor). With the old
// s.mu.Lock()-based CurrentModel(), this deadlocks and the test times out.
// With the atomic.Value-based fix, CurrentModel() returns immediately
// without needing s.mu, so the test completes instantly.
func TestCurrentModelDoesNotDeadlockUnderPromptSyncLock(t *testing.T) {
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

	// Simulate promptSync holding s.mu for the duration of a turn — this is
	// the exact condition that caused the deadlock: the Subagent executor
	// runs inside a turn, and the turn holds s.mu the whole time.
	sess.mu.Lock()
	defer sess.mu.Unlock()

	done := make(chan string, 1)
	go func() {
		// This is what the Subagent tool's executor does — if CurrentModel()
		// tries to take s.mu, it blocks forever here.
		done <- sess.CurrentModel()
	}()

	select {
	case got := <-done:
		if got == "" {
			t.Fatal("CurrentModel() returned empty string")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CurrentModel() deadlocked — it blocked waiting for s.mu while s.mu was held by the simulated turn (promptSync). This is the exact deadlock that hung every foreground Subagent call.")
	}
}

// TestCurrentModelConcurrentReadsWhileSwitchModelWrites verifies the
// atomic.Value-based CurrentModel() is safe under concurrent access —
// multiple readers (Subagent executors running in parallel tool execution)
// while a SwitchModel write is in flight. -race catches any data race that
// would arise from a non-atomic implementation.
func TestCurrentModelConcurrentReadsWhileSwitchModelWrites(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()

	models := a.Models()
	if len(models) < 2 {
		t.Skip("need at least 2 active models to test concurrent reads + writes")
	}

	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers — simulate parallel Subagent executors calling CurrentModel()
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = sess.CurrentModel()
				}
			}
		}()
	}

	// Writer — simulate SwitchModel calls
	for i := 0; i < 100; i++ {
		idx := i % 2
		_ = sess.SwitchModel(t.Context(), models[idx].Model)
	}

	close(stop)
	wg.Wait()
}
