package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/components"
)

// promptRecorder wraps the real client's SendPrompt path by observing what reaches the session: it
// subscribes to the same event stream the TUI uses and records each prompt echo, in order.
type promptLog struct {
	mu      sync.Mutex
	prompts []string
}

func (p *promptLog) add(s string) { p.mu.Lock(); p.prompts = append(p.prompts, s); p.mu.Unlock() }
func (p *promptLog) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}
func (p *promptLog) count() int { return len(p.all()) }

// watchPrompts opens a SECOND stream on the session, independent of the TUI's, and records every
// prompt the session receives (received_prompt / follow_up_start). It is how the tests count what
// really reached the agent, without trusting the TUI's own screen.
func watchPrompts(t *testing.T, c *client.Client, sessionID string) *promptLog {
	t.Helper()
	// the fixture leaves the session saved, not active: bring it up so there is a stream to watch
	// (resuming is idempotent, so the TUI's own resume afterwards finds it live)
	if _, err := c.ResumeSession(sessionID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ev, err := c.StreamEvents(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	log := &promptLog{}
	go func() {
		for e := range ev {
			if (e.Type == "received_prompt" || e.Type == "follow_up_start") && e.Text != "" {
				log.add(e.Text)
			}
		}
	}()
	return log
}

// startWith builds a TUI over the fixture's session, sets an initial prompt and runs the real
// autoConnect — the path `harness "task"` takes.
func startWith(t *testing.T, f *fallbackFixture, resume, prompt string) *TUI {
	t.Helper()
	tu := f.tui(resume)
	tu.SetInitialPrompt(prompt)
	tu.autoConnect(context.Background())
	t.Cleanup(func() {
		if tu.sseCancel != nil {
			tu.sseCancel()
		}
	})
	return tu
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTheInitialPromptIsEchoedAndRunsWithoutTyping(t *testing.T) {
	f := newFallbackFixture(t)
	tu := startWith(t, f, f.sessionID, "hello from the command line")
	waitFor(t, "the echo of the initial prompt", func() bool {
		return strings.Contains(screenText(tu), "hello from the command line")
	})
	if got := screenText(tu); !strings.Contains(got, "❯ hello from the command line") && !strings.Contains(got, "hello from the command line") {
		t.Errorf("the prompt was not echoed:\n%s", got)
	}
	if tu.InitialPrompt() != "" {
		t.Errorf("the prompt was not consumed: %q", tu.InitialPrompt())
	}
}

// The prompt is sent ONCE, and it is the stream that must be subscribed first. The session is
// observed from a second client, so this counts what actually reached the agent.
func TestTheInitialPromptReachesTheAgentExactlyOnce(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID)
	startWith(t, f, f.sessionID, "only once please")
	waitFor(t, "the prompt to reach the agent", func() bool { return log.count() >= 1 })
	time.Sleep(400 * time.Millisecond) // a duplicate would arrive well within this
	if got := log.all(); len(got) != 1 || got[0] != "only once please" {
		t.Errorf("the agent received %q, want exactly [\"only once please\"]", got)
	}
}

func TestTheInitialPromptIsNeverReplayedBySwitchingSession(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID)
	tu := startWith(t, f, f.sessionID, "first and last")
	waitFor(t, "the first delivery", func() bool { return log.count() >= 1 })

	tu.resumeInPlace(f.sessionID) // /resume
	tu.newSessionInPlace()        // /new
	tu.cmdFork()                  // /fork
	time.Sleep(500 * time.Millisecond)

	if n := log.count(); n != 1 {
		t.Errorf("the agent received %d prompts %q after /resume, /new and /fork, want 1", n, log.all())
	}
	if tu.InitialPrompt() != "" {
		t.Errorf("an initial prompt is still pending: %q", tu.InitialPrompt())
	}
}

// Even if the stream is reopened (startSSE runs again for any reason), the prompt does not go twice.
func TestTheInitialPromptIsNotReplayedWhenTheStreamIsReopened(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID)
	tu := startWith(t, f, f.sessionID, "no repeats")
	waitFor(t, "the first delivery", func() bool { return log.count() >= 1 })
	for i := 0; i < 3; i++ {
		tu.stopSSE()
		tu.startSSE(context.Background())
		tu.sendInitialPrompt() // what a path that re-runs the startup tail would call
	}
	time.Sleep(400 * time.Millisecond)
	if n := log.count(); n != 1 {
		t.Errorf("%d deliveries %q, want 1", n, log.all())
	}
}

func TestNoInitialPromptMeansNothingIsSent(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID)
	tu := startWith(t, f, f.sessionID, "")
	time.Sleep(400 * time.Millisecond)
	if n := log.count(); n != 0 {
		t.Errorf("a TUI started without a prompt sent %q", log.all())
	}
	if tu.editor != nil && tu.editor.Value() != "" {
		t.Errorf("the editor was filled: %q", tu.editor.Value())
	}
	// blank is no prompt either
	tu2 := startWith(t, f, f.sessionID, " \n\t ")
	time.Sleep(300 * time.Millisecond)
	if n := log.count(); n != 0 || tu2.InitialPrompt() != "" {
		t.Errorf("a blank prompt was sent or kept: %q / %q", log.all(), tu2.InitialPrompt())
	}
}

// --resume plus a prompt: the session is resumed and the prompt is its NEXT turn.
func TestAResumedSessionReceivesThePromptAsItsNextTurn(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID)
	tu := startWith(t, f, f.sessionID, "the next question")
	if tu.sessionID != f.sessionID {
		t.Fatalf("the TUI is on %q, want the resumed %q", tu.sessionID, f.sessionID)
	}
	waitFor(t, "the prompt", func() bool { return log.count() >= 1 })
	if got := log.all(); got[0] != "the next question" {
		t.Errorf("received %q", got)
	}
}

// ── bytes and meaning ───────────────────────────────────────────────────────

// The prompt reaches the agent byte for byte: newlines, accents, emoji, CJK, tabs inside, and text
// that merely LOOKS like a command.
func TestThePromptArrivesByteIdentical(t *testing.T) {
	for name, prompt := range map[string]string{
		"newlines":          "line one\nline two\n\nline four",
		"accents":           "¿Cómo está el módulo de autenticación? ñandú",
		"emoji and CJK":     "revisa 🚀 y 日本語 y عربى",
		"inner tabs":        "a\tb\t\tc",
		"quotes":            `she said "hi" and 'bye' and ` + "`code`",
		"backslashes":       `C:\path\to\file and \n literal`,
		"a long prompt":     strings.Repeat("palabra ", 4000),
		"markdown":          "# title\n\n- a\n- b\n\n```go\nfunc main() {}\n```",
		"looks like a flag": "--model x -p y",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFallbackFixture(t)
			log := watchPrompts(t, f.c, f.sessionID)
			startWith(t, f, f.sessionID, prompt)
			waitFor(t, "delivery", func() bool { return log.count() >= 1 })
			want := strings.TrimSpace(prompt) // the ends are trimmed exactly as typed text is
			if got := log.all()[0]; got != want {
				t.Errorf("the agent received %d bytes, want %d\n got : %.80q\n want: %.80q", len(got), len(want), got, want)
			}
		})
	}
}

// A prompt that starts with "/" or "!" is TEXT for the agent, not a command: harness "/quit" must
// not exit and harness "!ls" must not run a shell command behind the agent's back.
func TestAPromptStartingWithASlashOrABangIsTextNotACommand(t *testing.T) {
	for _, prompt := range []string{"/quit", "/exit", "/new", "/reset", "/fork", "/model x", "!echo pwned", "/unknown thing"} {
		t.Run(prompt, func(t *testing.T) {
			f := newFallbackFixture(t)
			log := watchPrompts(t, f.c, f.sessionID)
			tu := startWith(t, f, f.sessionID, prompt)
			waitFor(t, "delivery", func() bool { return log.count() >= 1 })
			if got := log.all()[0]; got != prompt {
				t.Errorf("the agent received %q, want the text %q", got, prompt)
			}
			select {
			case <-tu.quitCh:
				t.Errorf("%q made the TUI quit: it was run as a command", prompt)
			default:
			}
			if tu.sessionID != f.sessionID {
				t.Errorf("%q switched the session (%q): it was run as a command", prompt, tu.sessionID)
			}
		})
	}
}

// ── the order that matters: subscribe, then send ────────────────────────────

// The echo of a prompt is an event on the session's stream, so a prompt sent before the stream is
// subscribed loses it. Hold the stream's opening back and check nothing is sent until it is open.
func TestThePromptWaitsForTheStreamToBeSubscribed(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID)
	tu := f.tui(f.sessionID)
	tu.SetInitialPrompt("after the subscription")
	tu.sessionID = f.sessionID // a session exists, but NO stream was opened for it (startSSE not run)

	// a stream that never opens: the readiness channel stays open
	never := make(chan struct{})
	tu.sseReady = never
	old := streamReadyTimeout
	streamReadyTimeout = 400 * time.Millisecond
	defer func() { streamReadyTimeout = old }()

	start := time.Now()
	done := make(chan struct{})
	go func() { tu.sendInitialPrompt(); close(done) }()
	time.Sleep(150 * time.Millisecond)
	if n := log.count(); n != 0 {
		t.Fatalf("the prompt was sent (%q) while the stream was not subscribed", log.all())
	}
	<-done
	if d := time.Since(start); d < 350*time.Millisecond {
		t.Errorf("sendInitialPrompt returned after %v: it did not wait for the stream", d)
	}
	// the timeout is only a backstop: the prompt then goes anyway (never lost)
	waitFor(t, "the prompt after the backstop", func() bool { return log.count() >= 1 })

	// and when the stream DOES open, it goes as soon as it is open
	tu2 := f.tui(f.sessionID)
	tu2.SetInitialPrompt("as soon as it opens")
	tu2.sessionID = f.sessionID
	ready := make(chan struct{})
	tu2.sseReady = ready
	before := log.count()
	go tu2.sendInitialPrompt()
	time.Sleep(150 * time.Millisecond)
	if log.count() != before {
		t.Fatalf("sent before the stream was open")
	}
	close(ready)
	waitFor(t, "the prompt once the stream is open", func() bool { return log.count() > before })
}

// ── no usable session: nothing is sent and nothing is lost ──────────────────

func TestWithNoUsableSessionThePromptIsKeptInTheEditorNotSent(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID) // observe the (resumed) session BEFORE the providers go away
	f.disconnect(f.first)
	f.disconnect(f.second) // no provider: autoConnect ends without a model

	tu := f.tui("")
	tu.editor = components.NewEditor(tu.tui, "ph")
	tu.SetInitialPrompt("do not lose me")
	tu.autoConnect(context.Background())
	if tu.sessionID != "" {
		t.Fatalf("setup: a session exists (%q)", tu.sessionID)
	}
	time.Sleep(300 * time.Millisecond)
	if n := log.count(); n != 0 {
		t.Errorf("the prompt was sent with no session: %q", log.all())
	}
	if got := tu.editor.Value(); got != "do not lose me" {
		t.Errorf("the editor holds %q, want the prompt kept for the user", got)
	}
	if tu.InitialPrompt() != "" {
		t.Errorf("the prompt is still pending after being kept: %q", tu.InitialPrompt())
	}
}

func TestAFailedResumeKeepsThePromptInsteadOfSendingItToAFreshSession(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID)
	tu := f.tui("no-such-session-id")
	tu.editor = components.NewEditor(tu.tui, "ph")
	tu.SetInitialPrompt("meant for the old conversation")
	tu.autoConnect(context.Background())
	t.Cleanup(func() {
		if tu.sseCancel != nil {
			tu.sseCancel()
		}
	})
	if !strings.Contains(screenText(tu), "Failed to resume") {
		t.Fatalf("setup: the resume did not fail:\n%s", screenText(tu))
	}
	time.Sleep(300 * time.Millisecond)
	if tu.editor.Value() != "meant for the old conversation" {
		t.Errorf("the prompt was not kept in the editor: %q", tu.editor.Value())
	}
	// and no turn started on the fresh fallback session
	if tu.sessionID != "" {
		if info, err := f.c.GetSessionInfo(tu.sessionID); err == nil && info.Busy {
			t.Errorf("a turn is running on the fallback session: the prompt was sent")
		}
	}
	if n := log.count(); n != 0 {
		t.Errorf("the original session received %q", log.all())
	}
}

// A session that cannot be created (server error) is the same case.
func TestWhenTheSessionCannotBeCreatedThePromptIsKept(t *testing.T) {
	f := newFallbackFixture(t)
	tu := f.tui("")
	tu.editor = components.NewEditor(tu.tui, "ph")
	tu.SetInitialPrompt("keep me")
	tu.client = client.New("127.0.0.1:1") // nothing listens there
	tu.autoConnect(context.Background())
	if tu.sessionID != "" {
		t.Fatalf("setup: a session exists")
	}
	if got := tu.editor.Value(); got != "keep me" {
		t.Errorf("the editor holds %q, want the prompt kept", got)
	}
}

// ── submitting goes through the normal path ─────────────────────────────────

// The initial prompt must behave like a typed one: it queues behind a running turn instead of
// being dropped, because it goes through submitPrompt.
func TestTheInitialPromptUsesTheNormalSubmitPath(t *testing.T) {
	f := newFallbackFixture(t)
	tu := f.tui(f.sessionID)
	tu.editor = components.NewEditor(tu.tui, "ph")
	tu.sessionID = f.sessionID
	tu.setSpinning(true) // a turn is "running": a typed message would be counted as queued
	tu.SetInitialPrompt("queued behind")
	tu.sendInitialPrompt()
	if tu.queueCount != 1 {
		t.Errorf("queueCount = %d, want 1: the initial prompt did not go through submitPrompt", tu.queueCount)
	}
}

// ── a wire-level view: order, timing and bodies ─────────────────────────────

// wireProxy sits between the TUI's client and the real server. It records, with timestamps, when the
// event stream's HEADERS were delivered to the client and when each prompt (and session creation)
// arrived — and it can hold the stream's headers back. That is what lets a test pin the ORDER the
// story demands (subscribe, then send) deterministically, instead of hoping a race goes the wrong way.
type wireProxy struct {
	mu          sync.Mutex
	addr        string
	holdEvents  time.Duration // how long to delay the stream's response headers
	failCreate  bool          // answer POST /api/sessions with a 500
	eventsOpen  time.Time     // when the stream's headers reached the client
	prompts     []wirePrompt
	createCalls int
}

type wirePrompt struct {
	at   time.Time
	path string
	text string
}

func (w *wireProxy) promptTexts() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, p := range w.prompts {
		out = append(out, p.text)
	}
	return out
}

func newWireProxy(t *testing.T, upstream string) *wireProxy {
	t.Helper()
	w := &wireProxy{}
	target, _ := url.Parse("http://" + upstream)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1 // stream SSE through untouched
	rp.ModifyResponse = func(r *http.Response) error {
		if strings.HasSuffix(r.Request.URL.Path, "/events") {
			w.mu.Lock()
			hold := w.holdEvents
			w.mu.Unlock()
			if hold > 0 {
				time.Sleep(hold) // the client does not get its headers (StreamEvents does not return) until now
			}
			w.mu.Lock()
			w.eventsOpen = time.Now()
			w.mu.Unlock()
		}
		return nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/sessions":
			w.mu.Lock()
			w.createCalls++
			fail := w.failCreate
			w.mu.Unlock()
			if fail {
				http.Error(rw, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
				return
			}
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/prompt"):
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			var body struct {
				Text string `json:"text"`
			}
			json.Unmarshal(b, &body) //nolint:errcheck
			w.mu.Lock()
			w.prompts = append(w.prompts, wirePrompt{at: time.Now(), path: r.URL.Path, text: body.Text})
			w.mu.Unlock()
		}
		rp.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	w.addr = strings.TrimPrefix(srv.URL, "http://")
	return w
}

// tuiThrough is a TUI whose client talks to the server through proxy.
func tuiThrough(f *fallbackFixture, proxy *wireProxy, resume, prompt string) *TUI {
	tu := f.tui(resume)
	tu.client = client.New(proxy.addr)
	tu.SetInitialPrompt(prompt)
	return tu
}

// THE ORDER THE STORY DEMANDS, pinned deterministically: the stream's headers are held back, and the
// prompt must not reach the server until they have been delivered (a prompt sent before the stream is
// subscribed loses its echo) — and must follow right after, not after a timeout.
func TestThePromptIsSentOnlyAfterTheStreamOpensAndRightAfterIt(t *testing.T) {
	for name, resume := range map[string]string{"a fresh session": "", "a resumed session": "RESUME"} {
		t.Run(name, func(t *testing.T) {
			f := newFallbackFixture(t)
			if resume == "RESUME" {
				resume = f.sessionID
			}
			proxy := newWireProxy(t, f.addr)
			proxy.holdEvents = 600 * time.Millisecond
			tu := tuiThrough(f, proxy, resume, "ordered prompt")
			tu.baseCtx = context.Background()
			done := make(chan struct{})
			go func() { tu.autoConnect(context.Background()); close(done) }()
			waitFor(t, "the prompt to reach the server", func() bool { return len(proxy.promptTexts()) >= 1 })
			<-done
			t.Cleanup(func() {
				if tu.sseCancel != nil {
					tu.sseCancel()
				}
			})
			proxy.mu.Lock()
			open, sent := proxy.eventsOpen, proxy.prompts[0].at
			proxy.mu.Unlock()
			if open.IsZero() {
				t.Fatal("the stream never opened")
			}
			if sent.Before(open) {
				t.Errorf("the prompt reached the server %v BEFORE the stream's headers were delivered: its echo can be lost", open.Sub(sent))
			}
			if gap := sent.Sub(open); gap > 1500*time.Millisecond {
				t.Errorf("the prompt was sent %v after the stream opened: it waited for a timeout instead of the stream", gap)
			}
			if got := proxy.promptTexts(); len(got) != 1 || got[0] != "ordered prompt" {
				t.Errorf("the server received %q", got)
			}
		})
	}
}

// The main path of the story: `harness "hello"` with NO --resume starts a FRESH session and sends.
func TestAFreshSessionReceivesThePromptWithoutAnyResume(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	tu := tuiThrough(f, proxy, "", "hello on a fresh session")
	tu.autoConnect(context.Background())
	t.Cleanup(func() {
		if tu.sseCancel != nil {
			tu.sseCancel()
		}
	})
	if tu.sessionID == "" || tu.sessionID == f.sessionID {
		t.Fatalf("setup: expected a NEW session, got %q (saved one is %q)", tu.sessionID, f.sessionID)
	}
	waitFor(t, "the prompt", func() bool { return len(proxy.promptTexts()) >= 1 })
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.createCalls != 1 {
		t.Errorf("%d sessions created, want 1", proxy.createCalls)
	}
	if len(proxy.prompts) != 1 || proxy.prompts[0].text != "hello on a fresh session" || !strings.Contains(proxy.prompts[0].path, tu.sessionID) {
		t.Errorf("prompts sent: %+v — want exactly one, to the new session %s", proxy.prompts, tu.sessionID)
	}
}

// The server REFUSES to create the session (not the "no providers" case: the models list fine).
func TestWhenTheServerRefusesToCreateTheSessionThePromptIsKeptAndNotSent(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	proxy.failCreate = true
	tu := tuiThrough(f, proxy, "", "keep me, the server said no")
	tu.editor = components.NewEditor(tu.tui, "ph")
	tu.autoConnect(context.Background())
	if proxy.createCalls != 1 {
		t.Fatalf("setup: CreateSession was called %d times, want 1 (so this is the create-failed branch)", proxy.createCalls)
	}
	if !strings.Contains(screenText(tu), "Failed to create session") {
		t.Fatalf("setup: not the create-failed branch:\n%s", screenText(tu))
	}
	time.Sleep(300 * time.Millisecond)
	if got := proxy.promptTexts(); len(got) != 0 {
		t.Errorf("the prompt was sent without a session: %q", got)
	}
	if got := tu.editor.Value(); got != "keep me, the server said no" {
		t.Errorf("the editor holds %q, want the prompt kept for the user", got)
	}
}

// The backstop is a real, bounded wait: not zero (it would send before the stream is open when the
// stream is slow) and not unbounded (a stuck connection would hold the first message forever).
func TestTheStreamWaitBackstopIsSane(t *testing.T) {
	if streamReadyTimeout < time.Second || streamReadyTimeout > 30*time.Second {
		t.Errorf("streamReadyTimeout = %v, want between 1s and 30s", streamReadyTimeout)
	}
}
