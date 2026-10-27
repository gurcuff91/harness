//go:build unix

package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// zombieChildrenOfThisProcess lists the <defunct> children of the test process.
// It reads `ps` (present on Linux and macOS alike) instead of /proc, which macOS
// does not have. Tests here look only at the pids THEY launched (see
// zombiesAmong): with the bug present, zombies from one test would otherwise make
// every later test fail for a reason that is not its own.
func zombieChildrenOfThisProcess(t *testing.T) []int {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,stat=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	me := strconv.Itoa(os.Getpid())
	var zombies []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[1] == me && strings.HasPrefix(f[2], "Z") {
			pid, _ := strconv.Atoi(f[0])
			zombies = append(zombies, pid)
		}
	}
	return zombies
}

// zombiesAmong returns which of pids are currently <defunct> children of this process.
func zombiesAmong(t *testing.T, pids ...int) []int {
	t.Helper()
	mine := map[int]bool{}
	for _, p := range pids {
		mine[p] = true
	}
	var out []int
	for _, z := range zombieChildrenOfThisProcess(t) {
		if mine[z] {
			out = append(out, z)
		}
	}
	return out
}

func pidFrom(t *testing.T, out string) int {
	t.Helper()
	m := regexp.MustCompile(`PID: (\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no PID in %q", out)
	}
	pid, _ := strconv.Atoi(m[1])
	return pid
}

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A background command that finishes must not linger as a zombie: once it has
// exited, its process-table entry is gone (kill -0 says "no such process").
func TestBackgroundFinishedCommandIsReaped(t *testing.T) {
	out, err := runBashBackground(t.TempDir(), "sleep 0.3")
	if err != nil {
		t.Fatal(err)
	}
	pid := pidFrom(t, out)
	eventually(t, "the finished background command to be reaped", 5*time.Second, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	})
	if z := zombiesAmong(t, pid); len(z) != 0 {
		t.Errorf("pid %d is still a zombie", pid)
	}
}

// The reported scenario: a burst of short background commands leaves NO zombie
// children behind.
func TestManyShortBackgroundsLeaveNoZombies(t *testing.T) {
	const n = 50
	dir := t.TempDir()
	pids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out, err := runBashBackground(dir, "true")
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pidFrom(t, out))
	}
	eventually(t, "all the short backgrounds to be reaped", 10*time.Second, func() bool {
		return len(zombiesAmong(t, pids...)) == 0
	})
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Errorf("pid %d still exists (kill -0 → %v)", pid, err)
		}
	}
}

// Reaping must not touch a command that is still running: the tool call returns
// at once, the command stays alive, and kill <pid> (what the tool tells the
// agent to do) still stops it — and the stopped process is reaped too.
func TestLongBackgroundStaysAliveAndCanBeKilled(t *testing.T) {
	start := time.Now()
	out, err := runBashBackground(t.TempDir(), "sleep 60")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the tool call took %v: it must return immediately, not wait for the child", time.Since(start))
	}
	pid := pidFrom(t, out)
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the long-running background died (kill -0 → %v)", err)
	}
	if z := zombiesAmong(t, pid); len(z) != 0 {
		t.Fatalf("a RUNNING child must not be a zombie: %v", z)
	}
	// still its own session leader (no Setsid regression)
	if sid, err := syscall.Getsid(pid); err != nil || sid != pid {
		t.Errorf("session id of the background = %d (err %v), want its own pid %d — Setsid was lost", sid, err, pid)
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the killed background to be reaped", 5*time.Second, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	})
}

// The command's stdout AND stderr still reach the log file.
func TestBackgroundLogStillReceivesStdoutAndStderr(t *testing.T) {
	out, err := runBashBackground(t.TempDir(), "echo to-stdout; echo to-stderr 1>&2")
	if err != nil {
		t.Fatal(err)
	}
	logPath := regexp.MustCompile(`Log: (\S+)`).FindStringSubmatch(out)[1]
	defer os.Remove(logPath)
	eventually(t, "the log to receive the output", 5*time.Second, func() bool {
		b, _ := os.ReadFile(logPath)
		return strings.Contains(string(b), "to-stdout") && strings.Contains(string(b), "to-stderr")
	})
}

// The tool's reply is unchanged: PID, log path and the kill hint.
func TestBackgroundReplyIsUnchanged(t *testing.T) {
	out, err := runBashBackground(t.TempDir(), "true")
	if err != nil {
		t.Fatal(err)
	}
	pid := pidFrom(t, out)
	for _, want := range []string{"Started in background:", "Log: ", "Stop with: kill " + strconv.Itoa(pid), "Check progress by reading the log."} {
		if !strings.Contains(out, want) {
			t.Errorf("reply %q lacks %q", out, want)
		}
	}
}

// Through the real tool entry point (background:true), not just the helper.
func TestBashToolBackgroundReapsToo(t *testing.T) {
	bash := Bash(t.TempDir())
	input, _ := json.Marshal(map[string]any{"command": "sleep 0.2", "background": true})
	out, err := bash.Execute(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	pid := pidFrom(t, out)
	eventually(t, "the tool-launched background to be reaped", 5*time.Second, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	})
}

// Each reaper is a goroutine that lives exactly as long as its child: while the
// children run there is one per child (so the count visibly rises — this is what
// proves the test sees the reapers at all), and once they exit every goroutine is
// gone again (no leak).
func TestReaperGoroutinesLiveExactlyAsLongAsTheirChild(t *testing.T) {
	const n = 20
	stop := t.TempDir() + "/stop"
	runtime.GC()
	before := runtime.NumGoroutine()
	var pids []int
	for i := 0; i < n; i++ {
		// each child blocks until the file appears — a controllable lifetime
		out, err := runBashBackground(t.TempDir(), "while [ ! -e "+stop+" ]; do sleep 0.05; done")
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pidFrom(t, out))
	}
	time.Sleep(200 * time.Millisecond)
	if during := runtime.NumGoroutine(); during < before+n {
		t.Errorf("goroutines while %d children run = %d (baseline %d): expected a reaper per child", n, during, before)
	}
	if err := os.WriteFile(stop, nil, 0o644); err != nil { // let every child finish
		t.Fatal(err)
	}
	eventually(t, "every child to be reaped", 10*time.Second, func() bool {
		for _, pid := range pids {
			if syscall.Kill(pid, 0) != syscall.ESRCH {
				return false
			}
		}
		return true
	})
	eventually(t, "the goroutine count to return to its baseline", 10*time.Second, func() bool {
		return runtime.NumGoroutine() <= before
	})
}

// A command that fails to start leaves nothing to reap and no goroutine behind.
func TestBackgroundStartFailureLeavesNothing(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	if _, err := runBashBackground("/no/such/dir/at/all", "true"); err == nil {
		t.Fatal("expected a start error for a missing working directory")
	}
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines %d → %d after a failed start", before, after)
	}
}
