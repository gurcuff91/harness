package config

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/configstore"
)

func memSettings() *SettingsManager { return NewSettingsManager(configstore.NewInMemoryStore()) }
func memCreds() *CredentialsManager { return NewCredentialsManager(configstore.NewInMemoryStore()) }

// ── entryKey ─────────────────────────────────────────────────────────────

func TestEntryKey(t *testing.T) {
	if entryKey("owner", "slug") != entryKey("owner", "slug") {
		t.Error("entryKey must be deterministic")
	}
	// Part boundaries are unambiguous — no separator collisions.
	if entryKey("a", "bc") == entryKey("ab", "c") {
		t.Error(`entryKey("a","bc") collided with entryKey("ab","c")`)
	}
	if entryKey("a|b", "c") == entryKey("a", "b|c") {
		t.Error("a separator-like character inside a part must not cause collisions")
	}
	// Types are part of the identity: chat 123 (number) ≠ "123" (string).
	if entryKey("cwd", 123) == entryKey("cwd", "123") {
		t.Error("entryKey must distinguish a number from its string form")
	}
	if got := entryKey("x"); len(got) != 32 {
		t.Errorf("entryKey length = %d, want 32 hex chars", len(got))
	}
	// Pinned value: changing the derivation would silently orphan every
	// stored entry, so it must never change by accident.
	if got, want := entryKey("sess-1", "daily"), "7b19eab085bc829ace763e15c1d77eb0"; got != want {
		t.Errorf("entryKey derivation changed: got %s, want %s", got, want)
	}
}

// ── Schedules ────────────────────────────────────────────────────────────

func TestSchedulesRoundTripAndIdentity(t *testing.T) {
	m := memSettings()
	write := func(owner, slug, prompt string) {
		t.Helper()
		err := m.SwapSchedule(owner, slug, func(ScheduleEntry, bool) (ScheduleEntry, bool, error) {
			// A wrong Owner/Slug in the returned value must be overridden.
			return ScheduleEntry{Owner: "IGNORED", Slug: "IGNORED", Cron: "@daily", Prompt: prompt}, true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	write("sess-A", "daily", "A")
	write("sess-B", "daily", "B") // same slug, different owner: no collision

	all := m.Schedules()
	if len(all) != 2 {
		t.Fatalf("schedules = %+v, want 2", all)
	}
	got, ok := m.Schedule("sess-A", "daily")
	if !ok || got.Prompt != "A" || got.Owner != "sess-A" || got.Slug != "daily" {
		t.Errorf("Schedule(sess-A, daily) = %+v, %v", got, ok)
	}

	existed, err := m.DeleteSchedule("sess-A", "daily")
	if err != nil || !existed {
		t.Fatalf("DeleteSchedule = %v, %v; want true, nil", existed, err)
	}
	if existed, _ := m.DeleteSchedule("sess-A", "daily"); existed {
		t.Error("deleting twice should report not existed")
	}
	if _, ok := m.Schedule("sess-B", "daily"); !ok {
		t.Error("deleting sess-A's schedule removed sess-B's")
	}
}

func TestSwapScheduleWriteFalseAndErrorDoNotWrite(t *testing.T) {
	m := memSettings()
	_ = m.SwapSchedule("o", "s", func(ScheduleEntry, bool) (ScheduleEntry, bool, error) {
		return ScheduleEntry{Cron: "@daily", Prompt: "x"}, false, nil
	})
	sentinel := errors.New("boom")
	if err := m.SwapSchedule("o", "s", func(ScheduleEntry, bool) (ScheduleEntry, bool, error) {
		return ScheduleEntry{Cron: "@daily", Prompt: "x"}, true, sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback's error", err)
	}
	if _, ok := m.Schedule("o", "s"); ok {
		t.Error("write=false / callback error must not persist anything")
	}
}

// RecordRun-style increments from two "processes" must never be lost.
func TestSwapScheduleConcurrentIncrementsAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	open := func() *SettingsManager {
		fs, err := configstore.NewFileStore(path, 0600)
		if err != nil {
			t.Fatal(err)
		}
		return NewSettingsManager(fs)
	}
	a, b := open(), open()
	_ = a.SwapSchedule("o", "s", func(ScheduleEntry, bool) (ScheduleEntry, bool, error) {
		return ScheduleEntry{Cron: "@daily", Prompt: "x"}, true, nil
	})
	var wg sync.WaitGroup
	for i := range 20 {
		m := a
		if i%2 == 1 {
			m = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.SwapSchedule("o", "s", func(cur ScheduleEntry, ok bool) (ScheduleEntry, bool, error) {
				cur.Runs++
				return cur, ok, nil
			})
		}()
	}
	wg.Wait()
	if got, _ := a.Schedule("o", "s"); got.Runs != 20 {
		t.Errorf("Runs = %d, want 20 — concurrent increments were lost", got.Runs)
	}
}

// ── Instances ────────────────────────────────────────────────────────────

func TestReserveInstanceOnlyClaimsFreeNames(t *testing.T) {
	m := memSettings()
	if ok, err := m.ReserveInstance("n", InstanceEntry{PID: 1}); err != nil || !ok {
		t.Fatalf("first reservation = %v, %v; want true", ok, err)
	}
	if ok, _ := m.ReserveInstance("n", InstanceEntry{PID: 2}); ok {
		t.Error("a taken name must not be reserved again")
	}
	if got := m.Instances()["n"]; got.PID != 1 {
		t.Errorf("holder = %+v, want the first reservation (PID 1)", got)
	}
}

func TestDeleteInstanceIfOnlyRemovesTheProbedHolder(t *testing.T) {
	m := memSettings()
	m.ReserveInstance("n", InstanceEntry{PID: 1})

	if err := m.DeleteInstanceIf("n", 999); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Instances()["n"]; !ok {
		t.Fatal("a PID mismatch must leave the entry alone")
	}

	if err := m.DeleteInstanceIf("n", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Instances()["n"]; ok {
		t.Fatal("the matching PID must remove the entry")
	}
	// The vacated name is free again.
	if ok, _ := m.ReserveInstance("n", InstanceEntry{PID: 2}); !ok {
		t.Error("a vacated name must be reservable")
	}
}

func TestDeleteInstanceIsIdempotent(t *testing.T) {
	m := memSettings()
	m.ReserveInstance("n", InstanceEntry{PID: 1})
	if err := m.DeleteInstance("n"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteInstance("n"); err != nil {
		t.Errorf("deleting a missing instance must not error, got %v", err)
	}
	if len(m.Instances()) != 0 {
		t.Error("instance still listed after delete")
	}
}

// ── Telegram ─────────────────────────────────────────────────────────────

func TestTelegramAllowlist(t *testing.T) {
	m := memSettings()
	if added, _ := m.PairTelegram(-100123); !added {
		t.Error("first pair should report added")
	}
	if added, _ := m.PairTelegram(-100123); added {
		t.Error("second pair should report already present")
	}
	m.PairTelegram(42)
	if got := m.TelegramAllowlist(); len(got) != 2 || got[0] != -100123 || got[1] != 42 {
		t.Errorf("allowlist = %v, want [-100123 42] (sorted, negatives kept)", got)
	}
	if !m.TelegramAllowed(42) || m.TelegramAllowed(7) {
		t.Error("TelegramAllowed wrong")
	}
}

func TestTelegramSessionsScopedByCWDAndUnpairDropsAll(t *testing.T) {
	m := memSettings()
	m.PairTelegram(42)
	m.BindTelegram("/proj/a", 42, "sess-a")
	m.BindTelegram("/proj/b", 42, "sess-b")
	m.BindTelegram("/proj/a", 7, "sess-other")

	if id, ok := m.TelegramSession("/proj/a", 42); !ok || id != "sess-a" {
		t.Errorf("session in /proj/a = %q, %v", id, ok)
	}
	if got := m.TelegramSessions("/proj/a"); len(got) != 2 || got[42] != "sess-a" || got[7] != "sess-other" {
		t.Errorf("sessions in /proj/a = %v", got)
	}

	if err := m.UnbindTelegram("/proj/a", 42); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.TelegramSession("/proj/a", 42); ok {
		t.Error("unbind left the binding in place")
	}
	if _, ok := m.TelegramSession("/proj/b", 42); !ok {
		t.Error("unbind in /proj/a removed the binding in /proj/b")
	}

	if removed, _ := m.UnpairTelegram(42); !removed {
		t.Error("unpair should report removed")
	}
	if m.TelegramAllowed(42) {
		t.Error("chat still allowed after unpair")
	}
	if _, ok := m.TelegramSession("/proj/b", 42); ok {
		t.Error("unpair must drop the chat's bindings in every project")
	}
	if _, ok := m.TelegramSession("/proj/a", 7); !ok {
		t.Error("unpair of chat 42 removed another chat's binding")
	}
}

// ── Slack ────────────────────────────────────────────────────────────────

func TestSlackConfigAdminsAndSessions(t *testing.T) {
	m := memSettings()
	if _, ok := m.SlackConfig(); ok {
		t.Error("no config should be reported before one is saved")
	}
	m.SetSlackConfig(SlackConfig{Workspace: "https://x.slack.com", UserID: "U1", Team: "T1"})
	if c, ok := m.SlackConfig(); !ok || c.Workspace != "https://x.slack.com" || c.Team != "T1" {
		t.Errorf("config = %+v, %v", c, ok)
	}

	if m.SlackIsAdmin("U1") {
		t.Error("nobody is admin before any is added (fail-closed)")
	}
	m.AddSlackAdmin("U2")
	m.AddSlackAdmin("U1")
	m.AddSlackAdmin("U1")
	if got := m.SlackAdmins(); len(got) != 2 || got[0] != "U1" || got[1] != "U2" {
		t.Errorf("admins = %v, want [U1 U2]", got)
	}
	m.RemoveSlackAdmin("U1")
	if m.SlackIsAdmin("U1") || !m.SlackIsAdmin("U2") {
		t.Error("RemoveSlackAdmin wrong")
	}

	m.BindSlack("/proj/a", "C1", "sess-1")
	m.BindSlack("/proj/b", "C1", "sess-2")
	if id, ok := m.SlackSessionFor("/proj/a", "C1"); !ok || id != "sess-1" {
		t.Errorf("slack session in /proj/a = %q, %v", id, ok)
	}
	if got := m.SlackSessions("/proj/b"); len(got) != 1 || got["C1"] != "sess-2" {
		t.Errorf("slack sessions in /proj/b = %v", got)
	}
	m.UnbindSlack("/proj/a", "C1")
	if _, ok := m.SlackSessionFor("/proj/a", "C1"); ok {
		t.Error("UnbindSlack left the binding")
	}
}

// ── Transport secrets ────────────────────────────────────────────────────

func TestTransportSecretsLiveInCredentials(t *testing.T) {
	c := memCreds()
	if c.TelegramToken() != "" {
		t.Error("no token should be reported before one is saved")
	}
	if err := c.SetTelegramToken(""); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("empty token = %v, want ErrInvalidCredential", err)
	}
	c.SetTelegramToken("123:abc")
	if c.TelegramToken() != "123:abc" {
		t.Errorf("token = %q", c.TelegramToken())
	}

	if _, ok := c.SlackSession(); ok {
		t.Error("no slack session should be reported before one is saved")
	}
	if err := c.SetSlackSession(SlackSession{XoxC: "xoxc-1"}); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("incomplete slack session = %v, want ErrInvalidCredential", err)
	}
	c.SetSlackSession(SlackSession{XoxC: "xoxc-1", XoxD: "xoxd-1"})
	if s, ok := c.SlackSession(); !ok || s.XoxC != "xoxc-1" || s.XoxD != "xoxd-1" {
		t.Errorf("slack session = %+v, %v", s, ok)
	}
}

// DeleteInstanceIf deletes for real — no vacant marker is left behind.
func TestDeleteInstanceIfLeavesNoMarker(t *testing.T) {
	m := memSettings()
	m.ReserveInstance("n", InstanceEntry{PID: 1})
	if err := m.DeleteInstanceIf("n", 1); err != nil {
		t.Fatal(err)
	}
	raw, _ := m.store.List(nsInstances)
	if _, ok := raw["n"]; ok {
		t.Fatalf("raw registry still holds %q = %s; want the key removed", "n", raw["n"])
	}
}

// Legacy null markers (written by 0.81–0.84) are swept; live entries stay.
func TestDeleteVacantInstancesSweepsLegacyMarkers(t *testing.T) {
	m := memSettings()
	m.ReserveInstance("live", InstanceEntry{PID: 1})
	for _, n := range []string{"old-a", "old-b"} {
		if err := m.store.Set(nsInstances, n, []byte(vacantInstance)); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.DeleteVacantInstances(); got != 2 {
		t.Errorf("removed = %d, want 2", got)
	}
	raw, _ := m.store.List(nsInstances)
	if len(raw) != 1 || raw["live"] == nil {
		t.Fatalf("registry after sweep = %v, want only the live entry", raw)
	}
	if got := m.DeleteVacantInstances(); got != 0 {
		t.Errorf("second sweep removed %d, want 0", got)
	}
}
