package agent

import (
	"testing"

	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
)

// ── fireScheduledPrompt: no auto-resume from disk ──────────────────────────

// TestFireScheduledPromptDropsInactiveOwnerWithoutResuming is the direct
// regression test for reverting the auto-resume-from-disk behavior: a
// schedule whose owner session is NOT active in this instance must be
// dropped silently — never resurrected from disk by the engine on its own
// initiative. Only a transport that keeps a session active (e.g.
// Telegram/Slack's prewarmPumps) is responsible for that; the agent's
// scheduler engine only ever fires into sessions already live in THIS
// process's activeSessions map.
func TestFireScheduledPromptDropsInactiveOwnerWithoutResuming(t *testing.T) {
	fs, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	a := New(AgentOptions{Store: fs})
	defer a.Close()

	models := a.Models()
	if len(models) < 1 {
		t.Skip("need at least 1 active model in this environment")
	}

	// Create a session, persist a message, then close it — it exists on
	// disk (resumable) but is NOT in a.activeSessions anymore.
	sess, err := a.NewSession(t.TempDir(), models[0].Model)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ownerID := sess.ID()
	sess.Close()

	// Sanity: the session really is gone from the active set.
	if a.resolveScheduledSession(ownerID) != nil {
		t.Fatal("test setup invalid: session still active after Close()")
	}

	// Firing a scheduled prompt for that now-inactive owner must NOT
	// resurrect it — resolveScheduledSession must still report nil
	// afterward (fireScheduledPrompt has no side effect on activeSessions
	// when the owner isn't found).
	a.fireScheduledPrompt("some-slug", "do something", ownerID)

	if a.resolveScheduledSession(ownerID) != nil {
		t.Error("fireScheduledPrompt must not auto-resume an inactive owner from disk — the prompt should simply be dropped")
	}
}

// ── Schedule*/ScheduleList/ScheduleDelete tool gating ──────────────────────

// TestScheduleToolsNotRegisteredWithoutEnableScheduler is the direct
// regression test for gating the Schedule* tools on EnableScheduler: a
// session built from an agent that never opted into scheduling must not
// see Schedule/ScheduleList/ScheduleDelete in its tool set at all — even
// though the underlying schedule store is still opened (for the read-only
// `harness schedules` listing).
func TestScheduleToolsNotRegisteredWithoutEnableScheduler(t *testing.T) {
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

	defs := sess.tools.Definitions()
	for _, name := range []string{tools.ToolSchedule, tools.ToolScheduleList, tools.ToolScheduleDelete} {
		for _, d := range defs {
			if d.Name == name {
				t.Errorf("tool %q must NOT be registered when EnableScheduler is off", name)
			}
		}
	}
}

// TestScheduleToolsRegisteredWithEnableScheduler confirms the flip side:
// once EnableScheduler is set, all three Schedule* tools ARE registered.
func TestScheduleToolsRegisteredWithEnableScheduler(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore(), EnableScheduler: true})
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

	defs := sess.tools.Definitions()
	want := map[string]bool{tools.ToolSchedule: false, tools.ToolScheduleList: false, tools.ToolScheduleDelete: false}
	for _, d := range defs {
		if _, ok := want[d.Name]; ok {
			want[d.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q must be registered when EnableScheduler is on", name)
		}
	}
}

// TestScheduleAdapterNilWithoutEnableScheduler is a narrower unit test of
// scheduleAdapter() itself, independent of a live session/tool registry.
func TestScheduleAdapterNilWithoutEnableScheduler(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore()})
	defer a.Close()
	if a.scheduleAdapter() != nil {
		t.Error("scheduleAdapter() must return nil when EnableScheduler is off, even though the store itself is open")
	}
	if a.schedStore == nil {
		t.Error("the schedule store itself must still open regardless of EnableScheduler (read-only listing depends on it)")
	}
}

func TestScheduleAdapterNonNilWithEnableScheduler(t *testing.T) {
	a := New(AgentOptions{Store: store.NewInMemoryStore(), EnableScheduler: true})
	defer a.Close()
	if a.scheduleAdapter() == nil {
		t.Error("scheduleAdapter() must be non-nil when EnableScheduler is on")
	}
}
