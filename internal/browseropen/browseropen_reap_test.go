//go:build unix

package browseropen

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func zombiesAmong(t *testing.T, pids ...int) []int {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,stat=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	mine := map[int]bool{}
	for _, p := range pids {
		mine[p] = true
	}
	me := strconv.Itoa(os.Getpid())
	var z []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[1] == me && strings.HasPrefix(f[2], "Z") {
			if pid, _ := strconv.Atoi(f[0]); mine[pid] {
				z = append(z, pid)
			}
		}
	}
	return z
}

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// The launcher (open / xdg-open) exits almost at once; nothing used to wait() on
// it, so every OAuth login left a zombie child of the harness process. With
// startAndReap it is reaped as soon as it exits.
func TestStartAndReapLeavesNoZombie(t *testing.T) {
	var pids []int
	for i := 0; i < 20; i++ {
		c := exec.Command("true")
		startAndReap(c)
		pids = append(pids, c.Process.Pid)
	}
	eventually(t, "the launchers to be reaped", 10*time.Second, func() bool {
		for _, pid := range pids {
			if syscall.Kill(pid, 0) != syscall.ESRCH {
				return false
			}
		}
		return true
	})
	if z := zombiesAmong(t, pids...); len(z) != 0 {
		t.Errorf("zombies left behind: %v", z)
	}
}

// A launcher that cannot start (no such program) leaves no child and no goroutine,
// and is silent, as before: callers always print the URL as well.
func TestStartAndReapIgnoresAStartFailure(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	startAndReap(exec.Command("/no/such/browser-launcher", "http://x"))
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines %d → %d after a failed start", before, after)
	}
}

// The launcher keeps running independently of the caller: startAndReap returns
// at once and does not wait for a slow launcher.
func TestStartAndReapDoesNotBlock(t *testing.T) {
	start := time.Now()
	c := exec.Command("sleep", "30")
	startAndReap(c)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("startAndReap blocked for %v", time.Since(start))
	}
	if err := syscall.Kill(c.Process.Pid, 0); err != nil {
		t.Errorf("the launcher should still be running: %v", err)
	}
	c.Process.Kill()
	eventually(t, "the killed launcher to be reaped", 5*time.Second, func() bool {
		return syscall.Kill(c.Process.Pid, 0) == syscall.ESRCH
	})
}
