package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/types"
)

// ── part A: idle once the session is ready ──────────────────────────────────

func TestASessionThatIsReadyIsIdleBeforeAnyPrompt(t *testing.T) {
	tu, rec, f := connectedTUI(t)
	if tu.sessionID != f.sessionID {
		t.Fatal("setup")
	}
	// autoConnect resumed a session and opened its stream; nothing was typed.
	eq(t, rec.reports(), []string{"idle"})
	// the exact bytes: the name is published as the session is established, then idle
	var osc []string
	for _, w := range rec.all() {
		if strings.HasPrefix(w, "\x1b]") { // frames are other writes; each report is its own
			osc = append(osc, w)
		}
	}
	if len(osc) != 2 || !strings.HasPrefix(osc[0], "\x1b]0;") || osc[1] != "\x1b]7501;state=idle:app=harness\x1b\\" {
		t.Errorf("startup reported %q, want the title and then exactly the idle report", osc)
	}
}

func TestAFreshSessionIsIdleToo(t *testing.T) {
	// no --resume: the banner path creates a new session
	f := newFallbackFixture(t)
	rec := newRecTerminal()
	tu := New(nil)
	tu.attachTerminal(rec)
	tu.history = newTestTUIForEvents().history
	tu.spinner = newTestTUIForEvents().spinner
	tu2 := newTestTUIForEvents()
	tu.history, tu.spinner, tu.info, tu.footer = tu2.history, tu2.spinner, tu2.info, tu2.footer
	tu.client = f.c
	tu.baseCtx = context.Background()
	tu.autoConnect(context.Background())
	t.Cleanup(func() {
		if c := tu.takeSSECancel(); c != nil {
			c()
		}
	})
	if tu.sessionID == "" {
		t.Fatal("setup: no session was created")
	}
	eq(t, rec.reports(), []string{"idle"})
}

func TestNothingIsReportedWhenThereIsNoSession(t *testing.T) {
	// server unreachable / no providers / no model: no session exists, so there is no agent to
	// report on — the terminal keeps seeing the shell.
	f := newFallbackFixture(t)
	f.disconnect(f.first)
	f.disconnect(f.second)
	rec := newRecTerminal()
	tu := New(nil)
	tu.attachTerminal(rec)
	tu2 := newTestTUIForEvents()
	tu.history, tu.spinner, tu.info, tu.footer = tu2.history, tu2.spinner, tu2.info, tu2.footer
	tu.client = f.c
	tu.baseCtx = context.Background()
	tu.autoConnect(context.Background())
	if tu.sessionID != "" {
		t.Fatalf("setup: a session exists (%q) although no provider is usable", tu.sessionID)
	}
	eq(t, rec.reports(), nil)
	eq(t, rec.titles(), nil)
}

func TestTheFirstTurnGoesIdleWorkingDone(t *testing.T) {
	tu, rec, _ := connectedTUI(t)
	// the session's own stream is open; drive the events as a turn would
	tu.stopSSE() // keep the order of the log deterministic: no live stream racing the test
	eq(t, rec.reports(), []string{"idle"})
	tu.status.Set(ansi.StatusWorking)
	tu.status.Set(ansi.StatusDone)
	eq(t, rec.reports(), []string{"idle", "working", "done"})
}

func TestTheFirstTurnThroughConsumeEventsGoesIdleWorkingDone(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.status.Set(ansi.StatusIdle) // what startSSE does once the session is ready
	play2 := func(types ...string) {
		ctx, cancel := context.WithCancel(context.Background())
		ch := feed(ev(types...))
		done := make(chan struct{})
		go func() { tu.consumeEvents(ctx, ch); close(done) }()
		for len(ch) > 0 {
			sleepMs(2)
		}
		sleepMs(20)
		cancel()
		<-done
	}
	play2("turn_start", "loop_start", "text", "turn_end")
	eq(t, rec.reports(), []string{"idle", "working", "done"})
}

func TestResumeAndNewSessionLeaveTheStateIdle(t *testing.T) {
	for name, switchTo := range map[string]func(tu *TUI, f *fallbackFixture){
		"resume": func(tu *TUI, f *fallbackFixture) { tu.resumeInPlace(f.sessionID) },
		"new":    func(tu *TUI, f *fallbackFixture) { tu.newSessionInPlace() },
		"fork":   func(tu *TUI, f *fallbackFixture) { tu.cmdFork() },
	} {
		t.Run(name, func(t *testing.T) {
			tu, rec, f := connectedTUI(t)
			tu.status.Set(ansi.StatusWorking)
			tu.status.Set(ansi.StatusDone) // the result of the turn the user is leaving
			switchTo(tu, f)
			rs := rec.reports()
			if rs[len(rs)-1] != "idle" {
				t.Errorf("after a %s switch the state is %q (all: %v), want idle", name, rs[len(rs)-1], rs)
			}
		})
	}
}

func TestExitStillEndsWithClear(t *testing.T) {
	writes, _ := runTUI(t, func(tu *TUI, rec *recTerminal) {
		tu.status.Set(ansi.StatusWorking)
		tu.status.Set(ansi.StatusDone)
	})
	last := writes[len(writes)-1]
	if last != "\x1b]7501;state=clear:app=harness\x1b\\" {
		t.Errorf("the last write is %q, want the clear", last)
	}
}

// ── part B: the title ───────────────────────────────────────────────────────

func TestTheTitleIsTheSessionNameInOneWrite(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.setSessionName("Refactor auth")
	if got := rec.all(); len(got) != 1 || got[0] != "\x1b]0;Refactor auth\x07" {
		t.Fatalf("writes = %q, want exactly one: ESC ] 0 ; Refactor auth BEL", got)
	}
	if tu.sessionName != "Refactor auth" {
		t.Errorf("sessionName = %q: setSessionName must still record the name", tu.sessionName)
	}
}

func TestTheTitleFollowsEveryTriggerThroughTheRealCode(t *testing.T) {
	tu, rec, f := connectedTUI(t)
	start := rec.titles()
	if len(start) != 1 || start[0] == "" {
		t.Fatalf("after startup the titles are %q, want the session's name", start)
	}
	if start[0] != ansi.SanitizeTitle(tu.sessionName) {
		t.Errorf("title %q is not the session name %q", start[0], tu.sessionName)
	}

	// /rename
	tu.applyCommandResult("rename", []string{"Fix", "the", "build"}, nil)
	if got := rec.titles(); got[len(got)-1] != "Fix the build" {
		t.Errorf("after /rename the titles are %q", got)
	}

	// resume, new session and fork each publish the name they switch to
	for name, switchTo := range map[string]func(){
		"resume": func() { tu.resumeInPlace(f.sessionID) },
		"new":    func() { tu.newSessionInPlace() },
		"fork":   func() { tu.cmdFork() },
	} {
		tu.applyCommandResult("rename", []string{"before-" + name}, nil)
		n := len(rec.titles())
		switchTo()
		got := rec.titles()
		if len(got) == n {
			t.Errorf("%s did not publish a title", name)
			continue
		}
		if want := ansi.SanitizeTitle(tu.sessionName); got[len(got)-1] != want {
			t.Errorf("after %s the title is %q, want the new session's name %q", name, got[len(got)-1], want)
		}
	}
}

func TestIdenticalTitlesAreOneWrite(t *testing.T) {
	tu, rec := statusTUI(t)
	for i := 0; i < 5; i++ {
		tu.setSessionName("Same name")
	}
	tu.setSessionName("  Same   name\n") // sanitizes to the same title
	eq(t, rec.titles(), []string{"Same name"})
	tu.setSessionName("Other")
	tu.setSessionName("Same name") // a repeat after a different one IS written
	eq(t, rec.titles(), []string{"Same name", "Other", "Same name"})
}

func TestHostileSessionNamesNeverProduceAMalformedSequence(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"BEL would end the OSC early", "evil\x07 rm -rf", "evil rm -rf"},
		{"ESC would inject a sequence", "a\x1b]0;pwned\x07b", "a]0;pwnedb"},
		{"ESC CSI", "x\x1b[2Jy", "x[2Jy"},
		{"ST", "x\x1b\\y", "x\\y"},
		{"newline and CR", "line1\nline2\r\nline3", "line1 line2 line3"},
		{"tab", "a\tb", "a b"},
		{"NUL and DEL", "a\x00b\x7fc", "abc"},
		{"C1 controls (8-bit CSI/OSC/ST)", "a\u009b2Jb\u009dc\u009cd", "a2Jbcd"},
		{"U+0085 NEL", "a\u0085b", "ab"},
		{"invalid UTF-8", "ok\xff\xfe!", "ok!"},
		{"only controls", "\x07\x1b\x00", ""},
		{"surrounding space", "   padded   ", "padded"},
		{"multibyte kept", "Refactor ñandú 日本語 🚀", "Refactor ñandú 日本語 🚀"},
		{"zero-width joiner kept", "👨\u200d👩", "👨\u200d👩"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ansi.SanitizeTitle(c.in)
			if got != c.want {
				t.Errorf("SanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
			}
			// the invariant that matters, whatever the input: nothing that can end or alter the OSC
			for _, r := range got {
				if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
					t.Errorf("control character %U survived in %q", r, got)
				}
			}
			if !utf8.ValidString(got) {
				t.Errorf("the title %q is not valid UTF-8", got)
			}
			// ...and on the wire: exactly one ESC ] 0 ; ... BEL, with no other ESC or BEL inside it
			tu, rec := statusTUI(t)
			tu.status.SetTitle(c.in)
			ws := rec.all()
			if c.want == "" {
				if len(ws) != 0 {
					t.Errorf("a name with nothing printable was published as %q", ws)
				}
				return
			}
			if len(ws) != 1 {
				t.Fatalf("%d writes, want 1", len(ws))
			}
			w := ws[0]
			if !strings.HasPrefix(w, "\x1b]0;") || !strings.HasSuffix(w, "\x07") {
				t.Fatalf("malformed sequence %q", w)
			}
			if body := w[len("\x1b]0;") : len(w)-1]; strings.ContainsAny(body, "\x1b\x07\x9c\x9b\x9d\n\r") {
				t.Errorf("the body of %q carries a terminator or a control character", w)
			}
		})
	}
}

func TestALongTitleIsCutOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("日", 200)
	got := ansi.SanitizeTitle(long)
	if n := utf8.RuneCountInString(got); n != ansi.MaxTitleRunes {
		t.Errorf("%d runes, want exactly the cap of %d", n, ansi.MaxTitleRunes)
	}
	if !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Errorf("a cut title ends with an ellipsis and stays valid UTF-8: %q", got)
	}
	if got := ansi.SanitizeTitle(strings.Repeat("a", ansi.MaxTitleRunes)); strings.Contains(got, "…") {
		t.Errorf("a title of exactly the cap is not cut: %q", got)
	}
	// the cut must not leave a dangling space before the ellipsis
	spaced := strings.Repeat("ab ", 60)
	if got := ansi.SanitizeTitle(spaced); strings.HasSuffix(got, " …") {
		t.Errorf("space before the ellipsis: %q", got)
	}
	if got := ansi.SanitizeTitle(strings.Repeat("x\x1b", 400)); utf8.RuneCountInString(got) > ansi.MaxTitleRunes {
		t.Errorf("the cap counts what is left after sanitizing: %d runes", utf8.RuneCountInString(got))
	}
}

func TestSetTitleIsSafeAfterClearAndOnNil(t *testing.T) {
	var r *statusReporter
	r.SetTitle("x") // nil-safe
	tu, rec := statusTUI(t)
	tu.status.SetTitle("A")
	tu.status.Clear()
	tu.status.SetTitle("B") // a late rename after the user quit
	eq(t, rec.titles(), []string{"A", ""})
	if last := rec.all()[len(rec.all())-1]; last != "\x1b]7501;state=clear:app=harness\x1b\\" {
		t.Errorf("the last write is %q, want the clear: nothing may follow it", last)
	}
}

// ── exit restores the title ─────────────────────────────────────────────────

func TestExitRestoresTheTitleBeforeTheClear(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.setSessionName("Refactor auth")
	tu.status.Clear()
	ws := rec.all()
	want := []string{"\x1b]0;Refactor auth\x07", "\x1b]0;\x07", "\x1b]7501;state=clear:app=harness\x1b\\"}
	if len(ws) != len(want) {
		t.Fatalf("writes = %q, want %q", ws, want)
	}
	for i := range want {
		if ws[i] != want[i] {
			t.Errorf("write %d = %q, want %q", i, ws[i], want[i])
		}
	}
}

func TestExitLeavesTheTitleAloneWhenNoneWasPublished(t *testing.T) {
	tu, rec := statusTUI(t) // a session that never had a name to publish
	tu.status.Clear()
	eq(t, rec.titles(), nil)
	eq(t, rec.reports(), []string{"clear"})
}

func TestRunRestoresTheTitleAfterTheRendererStops(t *testing.T) {
	withAProvider(t)
	writes, _ := runTUI(t, nil)
	stopAt, setAt, restoreAt, clearAt := -1, -1, -1, -1
	for i, w := range writes {
		switch {
		case w == "<stop>":
			stopAt = i
		case strings.HasPrefix(w, "\x1b]0;") && w != "\x1b]0;\x07":
			setAt = i
		case w == "\x1b]0;\x07":
			restoreAt = i
		case strings.Contains(w, "state=clear"):
			clearAt = i
		}
	}
	if setAt < 0 {
		t.Fatalf("the real Run never published the session's name as the title: %q", writes)
	}
	if !(stopAt < restoreAt && restoreAt < clearAt && clearAt == len(writes)-1) {
		t.Errorf("stop %d, restore %d, clear %d of %d: the title is given back once the renderer has stopped, just before the clear", stopAt, restoreAt, clearAt, len(writes))
	}
}

// ── a title is never interleaved with a frame ───────────────────────────────

func TestAReportAndATitleAreEachOneWriteNeverMixedWithAFrame(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.setSessionName("A name")
	tu.status.Set(ansi.StatusWorking)
	tu.tui.RequestRender(true) // a real frame goes through the same terminal
	tu.status.Set(ansi.StatusDone)
	for _, w := range rec.all() {
		hasOSC := strings.Contains(w, "\x1b]0;") || strings.Contains(w, "\x1b]7501;")
		if !hasOSC {
			continue
		}
		if strings.Contains(w, "\x1b[?2026") || strings.Contains(w, "\x1b[2K") || strings.Contains(w, "\r\n") {
			t.Errorf("a status/title write carries frame content: %q", w)
		}
		if strings.Count(w, "\x1b]") != 1 {
			t.Errorf("one write carries %d sequences: %q", strings.Count(w, "\x1b]"), w)
		}
	}
}

func sleepMs(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }

// withAProvider registers one usable provider, so the real Run can create a session (without one it
// shows "No active providers" and — correctly — reports nothing). Run reads the process-global
// provider registry and settings, hence the isolation.
func withAProvider(t *testing.T) {
	t.Helper()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(gw.Close)
	histSeq++
	name := fmt.Sprintf("tuititle-%d", histSeq)
	models := func() ([]types.ModelMeta, error) {
		return []types.ModelMeta{{ID: "m", ContextWindow: 100000, MaxTokens: 4096}}, nil
	}
	if err := agent.NewOpenAIProvider(name, gw.URL, agent.ProviderWithFetchModels(models)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(name) })
	t.Cleanup(providers.SetForTest([]providers.Provider{providers.Find(name)}))
}

// startSSE is where idle is reported, and it is reached from paths that have just set a session.
// Called with none (a defensive guard), it must report nothing: there is no agent to describe.
func TestStartSSEWithNoSessionReportsNothing(t *testing.T) {
	tu, rec := statusTUI(t)
	tu.sessionID = ""
	tu.startSSE(context.Background())
	eq(t, rec.reports(), nil)
	if tu.takeSSECancel() != nil {
		t.Error("a stream was opened for no session")
	}
}
