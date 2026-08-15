package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ── resolvePath ────────────────────────────────────────────────────────────

func TestResolvePath(t *testing.T) {
	cwd := "/some/session/cwd"
	if got := resolvePath(cwd, "relative/file.txt"); got != filepath.Join(cwd, "relative/file.txt") {
		t.Errorf("relative path: got %q", got)
	}
	abs := "/tmp/absolute/file.txt"
	if got := resolvePath(cwd, abs); got != abs {
		t.Errorf("absolute path must be returned as-is: got %q, want %q", got, abs)
	}
}

// ── Read/Write/Edit: relative paths resolve against the session's cwd, NOT
// the real OS process cwd — the exact bug the third-party report described. ──

func TestWriteAndReadFile_RelativePathResolvesAgainstSessionCWD(t *testing.T) {
	sessionCWD := t.TempDir() // deliberately NOT the process's real cwd

	w := WriteFile(sessionCWD)
	out, err := w.Execute(context.Background(), json.RawMessage(`{"path":"note.txt","content":"hello from session"}`))
	if err != nil {
		t.Fatalf("Write: %v (out=%q)", err, out)
	}

	// The file must land INSIDE sessionCWD, not the process's real cwd.
	if _, err := os.Stat(filepath.Join(sessionCWD, "note.txt")); err != nil {
		t.Fatalf("expected note.txt inside session cwd %s: %v", sessionCWD, err)
	}
	if procCwd, _ := os.Getwd(); procCwd != sessionCWD {
		if _, err := os.Stat(filepath.Join(procCwd, "note.txt")); err == nil {
			os.Remove(filepath.Join(procCwd, "note.txt")) // clean up before failing
			t.Fatal("note.txt leaked into the process's real cwd — relative path was NOT resolved against the session cwd")
		}
	}

	r := ReadFile(sessionCWD)
	text, _, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"note.txt"}`))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(text, "hello from session") {
		t.Errorf("Read returned %q, want content written above", text)
	}
}

func TestEdit_RelativePathResolvesAgainstSessionCWD(t *testing.T) {
	sessionCWD := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionCWD, "code.txt"), []byte("foo bar"), 0644); err != nil {
		t.Fatal(err)
	}

	e := Edit(sessionCWD)
	out, err := e.Execute(context.Background(), json.RawMessage(`{"path":"code.txt","old_text":"foo","new_text":"baz"}`))
	if err != nil {
		t.Fatalf("Edit: %v (out=%q)", err, out)
	}
	data, err := os.ReadFile(filepath.Join(sessionCWD, "code.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "baz bar" {
		t.Errorf("file content = %q, want %q", data, "baz bar")
	}
}

// Absolute paths are unaffected regardless of the session cwd passed in — no
// regression on today's behavior, no sandboxing introduced.
func TestReadWriteEdit_AbsolutePathIgnoresSessionCWD(t *testing.T) {
	realDir := t.TempDir()
	absPath := filepath.Join(realDir, "abs.txt")
	// A DIFFERENT, unrelated cwd — must have no bearing on an absolute path.
	unrelatedCWD := t.TempDir()

	w := WriteFile(unrelatedCWD)
	if out, err := w.Execute(context.Background(), json.RawMessage(`{"path":"`+jsonEsc(absPath)+`","content":"x"}`)); err != nil {
		t.Fatalf("Write: %v (out=%q)", err, out)
	}
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("expected file at the absolute path %s: %v", absPath, err)
	}

	r := ReadFile(unrelatedCWD)
	text, _, err := r.ExecuteRich(context.Background(), json.RawMessage(`{"path":"`+jsonEsc(absPath)+`"}`))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(text, "x") {
		t.Errorf("Read returned %q", text)
	}
}

func jsonEsc(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1]) // strip the surrounding quotes json.Marshal adds
}

// ── The exact regression the third-party report described: two "sessions"
// (two tool sets built with different cwds) in one process must never
// cross-contaminate on a relative path. ──────────────────────────────────────

func TestTwoSessionsWithDifferentCWDsDoNotCrossContaminate(t *testing.T) {
	cwdA := t.TempDir()
	cwdB := t.TempDir()

	wA, rA := WriteFile(cwdA), ReadFile(cwdA)
	wB, rB := WriteFile(cwdB), ReadFile(cwdB)

	if _, err := wA.Execute(context.Background(), json.RawMessage(`{"path":"shared.txt","content":"from A"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := wB.Execute(context.Background(), json.RawMessage(`{"path":"shared.txt","content":"from B"}`)); err != nil {
		t.Fatal(err)
	}

	textA, _, err := rA.ExecuteRich(context.Background(), json.RawMessage(`{"path":"shared.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	textB, _, err := rB.ExecuteRich(context.Background(), json.RawMessage(`{"path":"shared.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textA, "from A") {
		t.Errorf("session A read %q, want its own write", textA)
	}
	if !strings.Contains(textB, "from B") {
		t.Errorf("session B read %q, want its own write", textB)
	}
}

// ── Bash: cmd.Dir must be the session's cwd, in BOTH the foreground and
// background code paths — a relative reference inside the command resolves
// there, not against the hosting OS process's real cwd. ─────────────────────

func TestBash_ForegroundRunsInSessionCWD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses bash -c, unix-only path in these tests")
	}
	sessionCWD := t.TempDir()
	b := Bash(sessionCWD)
	out, err := b.Execute(context.Background(), json.RawMessage(`{"command":"pwd"}`))
	if err != nil {
		t.Fatalf("Bash: %v (out=%q)", err, out)
	}
	// resolve symlinks (macOS /tmp is a symlink to /private/tmp) before comparing
	wantDir, _ := filepath.EvalSymlinks(sessionCWD)
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(out))
	if gotDir != wantDir {
		t.Errorf("pwd printed %q, want the session cwd %q", out, sessionCWD)
	}
}

func TestBash_BackgroundRunsInSessionCWD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses bash -c, unix-only path in these tests")
	}
	sessionCWD := t.TempDir()
	b := Bash(sessionCWD)
	// Background writes its pwd to a file inside the session cwd (a relative
	// path) — proving BOTH that cmd.Dir was applied AND that the write landed
	// in the right place.
	out, err := b.Execute(context.Background(), json.RawMessage(`{"command":"pwd > where.txt","background":true}`))
	if err != nil {
		t.Fatalf("Bash background: %v (out=%q)", err, out)
	}
	// Poll briefly for the detached process to finish (it's near-instant).
	path := filepath.Join(sessionCWD, "where.txt")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			wantDir, _ := filepath.EvalSymlinks(sessionCWD)
			gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
			if gotDir != wantDir {
				t.Errorf("background pwd = %q, want session cwd %q", data, sessionCWD)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background command never wrote where.txt inside the session cwd")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
