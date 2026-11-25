package agent

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// The decision table: every doubt resolves to "alive".
func TestOwnerAliveRules(t *testing.T) {
	world := map[int]procInfo{
		10: {exists: true, start: "A", startKnown: true},
		11: {exists: true},                               // exists, start unreadable
		12: {exists: true, start: "B", startKnown: true}, // pid reused by a later process
	}
	look := func(pid int) procInfo { return world[pid] }
	for _, c := range []struct {
		name    string
		pid     int
		started string
		want    bool
	}{
		{"no such process", 99, "A", false},
		{"bad pid", 0, "", false},
		{"same process", 10, "A", true},
		{"pid reused (markers differ)", 12, "A", false},
		{"marker unreadable: alive", 11, "A", true},
		{"old claim without marker: alive if the pid exists", 12, "", true},
		{"old claim without marker, no process", 99, "", false},
	} {
		if got := ownerAlive(c.pid, c.started, look); got != c.want {
			t.Errorf("%s: ownerAlive(%d, %q) = %v, want %v", c.name, c.pid, c.started, got, c.want)
		}
	}
}

// On the real OS: this process exists and its marker is stable; a child that
// has exited no longer exists; this pid with another marker is not this process.
func TestProcLookupOnThisOS(t *testing.T) {
	me := procLookup(os.Getpid())
	if !me.exists {
		t.Fatal("this process is reported as not existing")
	}
	supported := runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if supported && !me.startKnown {
		t.Fatalf("no start marker on %s", runtime.GOOS)
	}
	if again := procLookup(os.Getpid()); again.start != me.start {
		t.Errorf("start marker not stable: %q then %q", me.start, again.start)
	}
	if ownStart != me.start {
		t.Errorf("ownStart = %q, want %q", ownStart, me.start)
	}
	if !ownerAlive(os.Getpid(), me.start, procLookup) {
		t.Error("this process is not alive by its own marker")
	}
	if supported && ownerAlive(os.Getpid(), me.start+"x", procLookup) {
		t.Error("another marker on this pid was taken as this process (a reused pid would block forever)")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^$") // a real child that exits at once
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if procLookup(cmd.Process.Pid).exists {
		t.Errorf("exited child %d still reported as existing", cmd.Process.Pid)
	}
	if procLookup(-1).exists || procLookup(0).exists {
		t.Error("an invalid pid exists")
	}
}

// The scenario that motivated the marker: a claim left by a dead process whose
// pid now belongs to a live one (here: this very process) is taken over.
func TestAClaimWithAReusedPidIsTakenOver(t *testing.T) {
	if !procLookup(os.Getpid()).startKnown {
		t.Skip("this OS has no start marker")
	}
	ch := newFakeChannel("fake")
	a, model, _ := channelAgent(t, ch)
	sm := config.GetSettingsManager()
	_, _ = sm.ClaimChannel(config.ChannelClaim{Type: "fake", ID: "C1", Session: "before-the-deploy", PID: os.Getpid(), Started: "an-older-process"},
		func(config.ChannelClaim) bool { return true })
	s, err := a.NewSession(t.TempDir(), model, SessionWithChannels(types.InputChannel{Type: "fake", Ref: "c1"}))
	if err != nil {
		t.Fatalf("a claim with a reused pid blocked the channel: %v", err)
	}
	defer s.Close()
	cl := sm.ChannelClaims()
	if len(cl) != 1 || cl[0].Session != s.ID() || cl[0].Started != ownStart {
		t.Errorf("claims = %+v", cl)
	}
	// And the live owner's claim (same pid, same marker) still blocks.
	_, err = a.NewSession(t.TempDir(), model, SessionWithChannels(types.InputChannel{Type: "fake", Ref: "c1"}))
	if !errors.Is(err, ErrChannelTaken) {
		t.Errorf("a live owner's claim did not block: %v", err)
	}
}

// A process of another user exists: on Unix, signal 0 to it fails with EPERM,
// which must read as "exists" (else a live owner's claim would be taken). pid 1
// (init/launchd) always exists and, for a normal user, belongs to root.
func TestAnotherUsersProcessExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no pid 1 to borrow on Windows")
	}
	if !procLookup(1).exists {
		t.Error("pid 1 is reported as not existing")
	}
}
