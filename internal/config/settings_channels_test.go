package config

import (
	"sync"
	"testing"

	"github.com/gurcuff91/harness/configstore"
)

func channelsManager(t *testing.T) *SettingsManager {
	t.Helper()
	return NewSettingsManager(configstore.NewInMemoryStore())
}

func alwaysAlive(ChannelClaim) bool { return true }
func neverAlive(ChannelClaim) bool  { return false }

func TestClaimChannelIsExclusiveWhileTheOwnerLives(t *testing.T) {
	m := channelsManager(t)
	a := ChannelClaim{Type: "slack", ID: "C1", Session: "s-a", PID: 10, CWD: "/a"}
	if owner, err := m.ClaimChannel(a, alwaysAlive); err != nil || owner != nil {
		t.Fatalf("first claim: owner=%v err=%v", owner, err)
	}
	b := ChannelClaim{Type: "slack", ID: "C1", Session: "s-b", PID: 20}
	owner, err := m.ClaimChannel(b, alwaysAlive)
	if err != nil || owner == nil || owner.Session != "s-a" || owner.PID != 10 || owner.CWD != "/a" {
		t.Fatalf("second claim must report the owner: owner=%+v err=%v", owner, err)
	}
	// The same session in another process is another owner.
	if owner, _ := m.ClaimChannel(ChannelClaim{Type: "slack", ID: "C1", Session: "s-a", PID: 11}, alwaysAlive); owner == nil {
		t.Error("same session id from another pid must not take the claim")
	}
	// Re-claiming by the owner itself is a no-op success.
	if owner, err := m.ClaimChannel(a, alwaysAlive); err != nil || owner != nil {
		t.Errorf("owner re-claim: owner=%v err=%v", owner, err)
	}
	// Another conversation, or the same id of another type, is free.
	if owner, _ := m.ClaimChannel(ChannelClaim{Type: "slack", ID: "C2", Session: "s-b", PID: 20}, alwaysAlive); owner != nil {
		t.Error("another conversation must be free")
	}
	if owner, _ := m.ClaimChannel(ChannelClaim{Type: "other", ID: "C1", Session: "s-b", PID: 20}, alwaysAlive); owner != nil {
		t.Error("the same id under another type must be free")
	}
}

func TestClaimOfADeadProcessIsTakenOver(t *testing.T) {
	m := channelsManager(t)
	_, _ = m.ClaimChannel(ChannelClaim{Type: "slack", ID: "C1", Session: "old", PID: 99}, alwaysAlive)
	owner, err := m.ClaimChannel(ChannelClaim{Type: "slack", ID: "C1", Session: "new", PID: 7}, neverAlive)
	if err != nil || owner != nil {
		t.Fatalf("dead owner must be taken over: owner=%v err=%v", owner, err)
	}
	cl := m.ChannelClaims()
	if len(cl) != 1 || cl[0].Session != "new" || cl[0].PID != 7 || cl[0].ClaimedAt == "" {
		t.Fatalf("claims = %+v", cl)
	}
}

func TestReleaseOnlyRemovesOurOwnClaim(t *testing.T) {
	m := channelsManager(t)
	_, _ = m.ClaimChannel(ChannelClaim{Type: "slack", ID: "C1", Session: "mine", PID: 1}, alwaysAlive)
	// A stranger's release (other session, or same session other pid) is a no-op.
	_ = m.ReleaseChannel("slack", "C1", "other", 1)
	_ = m.ReleaseChannel("slack", "C1", "mine", 2)
	if len(m.ChannelClaims()) != 1 {
		t.Fatal("a release by someone else removed the claim")
	}
	if err := m.ReleaseChannel("slack", "C1", "mine", 1); err != nil {
		t.Fatal(err)
	}
	if len(m.ChannelClaims()) != 0 {
		t.Fatal("own release did not remove the claim")
	}
	// Releasing what is not there is fine.
	if err := m.ReleaseChannel("slack", "C1", "mine", 1); err != nil {
		t.Fatal(err)
	}
}

func TestClaimRequiresIdentity(t *testing.T) {
	m := channelsManager(t)
	for _, c := range []ChannelClaim{{ID: "C1", Session: "s"}, {Type: "slack", Session: "s"}, {Type: "slack", ID: "C1"}} {
		if _, err := m.ClaimChannel(c, alwaysAlive); err == nil {
			t.Errorf("claim %+v accepted", c)
		}
	}
}

// Many sessions racing for one conversation: exactly one wins.
func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	m := channelsManager(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner, err := m.ClaimChannel(ChannelClaim{Type: "slack", ID: "C1", Session: string(rune('a'+i%26)) + string(rune('0'+i/26)), PID: 1000 + i}, alwaysAlive)
			if err == nil && owner == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d winners, want 1", wins)
	}
}
