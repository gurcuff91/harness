package filelock

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func lockTarget(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "credentials.json")
}

// shortTimeout shrinks lockTimeout for a test.
func shortTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := lockTimeout
	lockTimeout = d
	t.Cleanup(func() { lockTimeout = prev })
}

// The helper process: when FILELOCK_HELPER is set, the test binary acquires
// the lock on that path, prints "locked", and sleeps until killed.
func TestMain(m *testing.M) {
	if p := os.Getenv("FILELOCK_HELPER"); p != "" {
		if _, err := Acquire(p); err != nil {
			os.Stdout.WriteString("error " + err.Error() + "\n")
			os.Exit(2)
		}
		os.Stdout.WriteString("locked\n")
		time.Sleep(time.Hour)
	}
	os.Exit(m.Run())
}

// holdInChild starts a child process that holds the lock on path, returning
// it once the lock is held.
func holdInChild(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "FILELOCK_HELPER="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, _ := bufio.NewReader(out).ReadString('\n')
	if strings.TrimSpace(line) != "locked" {
		t.Fatalf("child did not lock: %q", line)
	}
	return cmd
}

func TestReleaseRemovesTheLegacyLockAndKeepsTheKernelLockFile(t *testing.T) {
	path := lockTarget(t)
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("the legacy lock must exist while held (old binaries read it): %v", err)
	}
	release()
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Errorf("legacy lock should be gone after release, stat err = %v", err)
	}
	if _, err := os.Stat(path + ".flock"); err != nil {
		t.Errorf("the kernel lock file is never deleted: %v", err)
	}
	// And it can be taken again at once.
	r2, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	r2()
}

// THE property: a holder killed with SIGKILL (no defer, no cleanup) leaves
// nothing that blocks the next writer. The old O_EXCL lock blocked every
// write for 45 s here.
func TestAKilledHolderReleasesTheLockAtOnce(t *testing.T) {
	path := lockTarget(t)
	child := holdInChild(t, path)
	shortTimeout(t, 300*time.Millisecond)
	if _, err := Acquire(path); err == nil {
		t.Fatal("the lock must be held while the child lives")
	}
	if err := child.Process.Kill(); err != nil { // SIGKILL on Unix, TerminateProcess on Windows
		t.Fatal(err)
	}
	_ = child.Wait()
	shortTimeout(t, 5*time.Second)
	start := time.Now()
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("after the holder died: %v", err)
	}
	release()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v after the holder died; the kernel releases at once", d)
	}
}

// A waiter blocks while another PROCESS holds the lock, and gets it as soon
// as that process releases it (here: dies).
func TestAWaiterGetsTheLockWhenTheOtherProcessLetsGo(t *testing.T) {
	path := lockTarget(t)
	child := holdInChild(t, path)
	got := make(chan time.Time, 1)
	go func() {
		r, err := Acquire(path)
		if err != nil {
			t.Error(err)
			got <- time.Time{}
			return
		}
		got <- time.Now()
		r()
	}()
	time.Sleep(300 * time.Millisecond)
	select {
	case <-got:
		t.Fatal("acquired while another process held the lock")
	default:
	}
	killedAt := time.Now()
	_ = child.Process.Kill()
	_ = child.Wait()
	select {
	case at := <-got:
		if at.IsZero() || at.Sub(killedAt) > 2*time.Second {
			t.Fatalf("waiter got it %v after the release", at.Sub(killedAt))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiter never got the lock")
	}
}

// A timed-out waiter must not leave the lock held: its blocked goroutine
// releases it when it finally gets it.
func TestATimedOutWaiterDoesNotLeakTheLock(t *testing.T) {
	path := lockTarget(t)
	child := holdInChild(t, path)
	shortTimeout(t, 200*time.Millisecond)
	_, err := Acquire(path)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout, got %v", err)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	shortTimeout(t, 5*time.Second)
	for i := range 3 {
		r, err := Acquire(path)
		if err != nil {
			t.Fatalf("acquire %d after the timed-out waiter: %v", i, err)
		}
		r()
	}
}

// A ".lock" left by a NEW process killed mid-write is an orphan we can prove:
// whoever holds the kernel lock knows no new process is inside. Removed at
// once, not after 45 s.
func TestAnOrphanedNewLegacyLockIsRemovedAtOnce(t *testing.T) {
	if !kernelLocks {
		t.Skip("no kernel lock on this platform")
	}
	path := lockTarget(t)
	if err := os.WriteFile(path+".lock", []byte(newTokenPrefix+"dead-process"), 0600); err != nil {
		t.Fatal(err)
	}
	shortTimeout(t, time.Second)
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("a new-format orphan must not block: %v", err)
	}
	release()
}

// An OLD binary's lock (no prefix) is respected while fresh — that process
// may be inside — and reclaimed once stale, as old binaries do.
func TestAnOldBinarysLockIsRespectedUntilStale(t *testing.T) {
	path := lockTarget(t)
	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, []byte("old-binary-uuid"), 0600); err != nil {
		t.Fatal(err)
	}
	shortTimeout(t, 200*time.Millisecond)
	_, err := Acquire(path)
	if err == nil || !strings.Contains(err.Error(), "older harness") {
		t.Fatalf("a fresh old lock must be waited for, got %v", err)
	}
	// Our kernel lock must not stay held after that failure.
	if f, err := os.OpenFile(path+".flock", os.O_RDWR, 0600); err == nil {
		ok, _ := tryLockFile(f)
		if !ok {
			t.Error("a failed Acquire left the kernel lock held")
		}
		_ = unlockFile(f)
		f.Close()
	}
	old := time.Now().Add(-2 * staleAge)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("a stale old lock must be reclaimed: %v", err)
	}
	release()
	// And while the old binary holds it briefly, we wait and then get it.
	if err := os.WriteFile(lockPath, []byte("old-binary-2"), 0600); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(100 * time.Millisecond); _ = os.Remove(lockPath) }()
	shortTimeout(t, 5*time.Second)
	release, err = Acquire(path)
	if err != nil {
		t.Fatalf("must get the lock once the old binary releases: %v", err)
	}
	release()
}

func TestReleaseDoesNotStompSomeoneElsesLegacyLock(t *testing.T) {
	path := lockTarget(t)
	lockPath := path + ".lock"
	releaseA, err := Acquire(path)
	if err != nil {
		t.Fatalf("A acquire: %v", err)
	}
	bToken := "B-owns-this-now"
	if err := os.WriteFile(lockPath, []byte(bToken), 0600); err != nil {
		t.Fatalf("simulate B reclaim: %v", err)
	}
	releaseA()
	cur, err := os.ReadFile(lockPath)
	if err != nil || string(cur) != bToken {
		t.Errorf("A's release stomped B's lock: %q, %v", cur, err)
	}
}

func TestMutualExclusionUnderContention(t *testing.T) {
	path := lockTarget(t)
	var inside, maxSeen atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 15 {
				release, err := Acquire(path)
				if err != nil {
					t.Error(err)
					return
				}
				n := inside.Add(1)
				if n > maxSeen.Load() {
					maxSeen.Store(n)
				}
				time.Sleep(time.Millisecond)
				inside.Add(-1)
				release()
			}
		}()
	}
	wg.Wait()
	if maxSeen.Load() > 1 {
		t.Errorf("max concurrent holders = %d, want 1", maxSeen.Load())
	}
}

// The bug jade-kaiban hit: a writer holding the lock in a continuous burst
// made a polling waiter time out (1 wait in 5 with 5 ms holds). A blocked
// waiter now always gets in.
func TestABurstWriterDoesNotStarveAWaiter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("timing-sensitive; mutual exclusion is covered elsewhere")
	}
	path := lockTarget(t)
	shortTimeout(t, 3*time.Second)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if r, err := Acquire(path); err == nil {
				time.Sleep(5 * time.Millisecond)
				r()
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	time.Sleep(30 * time.Millisecond)
	for i := range 10 {
		r, err := Acquire(path)
		if err != nil {
			t.Fatalf("wait %d starved: %v", i, err)
		}
		r()
	}
}
