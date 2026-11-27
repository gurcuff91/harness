package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

// /export writes the session to ~/.harness/exports and says where; /import
// <path> (with ~ expanded) opens it as a NEW session and switches the TUI to
// it, history shown — the session it ran from stays as it was.
func TestTUIExportThenImport(t *testing.T) {
	f := newFallbackFixture(t) // HOME is a temp dir here
	for _, m := range []types.Message{
		types.NewUserTextMessage("a question worth keeping"),
		types.NewAssistantToolCallMessage("an answer worth keeping", "", "", nil),
	} {
		if err := f.store.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	tu := f.tui(f.sessionID)
	if err := tu.autoConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := tu.sessionID

	tu.runCommand("export", nil)
	// The command answers with the path; the file is there once it shows.
	got := waitScreen(t, tu, ".export.jsonl (2 messages)")
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".harness", "exports")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".export.jsonl") || !strings.Contains(got, entries[0].Name()) {
		t.Fatalf("exports in %s: %v\n%s", dir, entries, got)
	}
	path := filepath.Join(dir, entries[0].Name())

	tu.runCommand("import", []string{"~/.harness/exports/" + filepath.Base(path)})
	// Once the notice shows, the switch is done (it is printed after it).
	got = waitScreen(t, tu, "imported from")
	tu.mu.Lock()
	now := tu.sessionID
	tu.mu.Unlock()
	if now == before || now == "" {
		t.Fatalf("the TUI did not switch to the imported session (still %s)", now)
	}
	if !strings.Contains(got, "a question worth keeping") {
		t.Errorf("the imported history lacks the question:\n%s", got)
	}
	if msgs, err := f.c.GetMessages(now); err != nil || len(msgs) != 2 || msgs[1].Parts[0].Text != "an answer worth keeping" {
		t.Errorf("the imported session's messages = %+v, %v", msgs, err)
	}
	if msgs, _ := f.c.GetMessages(before); len(msgs) != 2 {
		t.Errorf("the original session changed: %d messages", len(msgs))
	}
}

func waitScreen(t *testing.T, tu *TUI, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if got = stripANSI(screenText(tu)); strings.Contains(got, want) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the screen never showed %q:\n%s", want, got)
	return got
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	for in, want := range map[string]string{
		"~/x.export.jsonl":      filepath.Join(home, "x.export.jsonl"),
		"~":                     home,
		"rel/a.export.jsonl":    filepath.Join(cwd, "rel", "a.export.jsonl"),
		"/abs/./b.export.jsonl": "/abs/b.export.jsonl",
	} {
		if got := expandPath(in); got != want {
			t.Errorf("expandPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The session state the footer shows is written by session switches and
// command results (their own goroutines) while updateInfo reads it on
// another: all of it goes through viewMu. Run under -race.
func TestSessionStateIsSafeAcrossGoroutines(t *testing.T) {
	tu := newTestTUIForEvents()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 300 {
			tu.switchView(fmt.Sprintf("s%d", i), "p/m", "high", 10+i, nil, nil)
			tu.setSessionName(fmt.Sprintf("name %d", i))
			tu.withView(func() { t2 := tu; t2.queueCount++; t2.currTurn = i; t2.mcpConnected = i; t2.scheduleJobs = i })
			tu.mu.Lock()
			tu.stats.input = i
			tu.mu.Unlock()
		}
	}()
	for range 300 {
		tu.updateInfo()
		_ = tu.queued()
	}
	<-done
	if v := tu.view(); v.sessionID != "s299" || v.sessionName != "name 299" || v.maxIterations != 309 {
		t.Errorf("view = %+v", v)
	}
}
