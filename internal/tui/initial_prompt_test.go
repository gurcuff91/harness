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
		if c := tu.takeSSECancel(); c != nil {
			c()
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

// A failed --resume is a HARD error (it used to be a warning plus a fresh session with the prompt
// kept in the editor): the task was meant for THAT conversation, so nothing runs and nothing is
// created. The prompt is safe because nothing ran, not because it was parked in an editor.
func TestAFailedResumeFailsAndNeverStartsAFreshSessionForThePrompt(t *testing.T) {
	f := newFallbackFixture(t)
	log := watchPrompts(t, f.c, f.sessionID)
	before := countSessions(t, f)
	tu := f.tui("no-such-session-id")
	tu.editor = components.NewEditor(tu.tui, "ph")
	tu.SetInitialPrompt("meant for the old conversation")
	err := tu.autoConnect(context.Background())
	t.Cleanup(func() {
		if c := tu.takeSSECancel(); c != nil {
			c()
		}
	})
	if err == nil || !strings.Contains(err.Error(), "no-such-session-id") {
		t.Fatalf("autoConnect returned %v, want an error that names the session that could not be resumed", err)
	}
	if tu.sessionID != "" {
		t.Errorf("a session was opened (%q) after the resume failed: the task would run in a conversation nobody asked for", tu.sessionID)
	}
	if n := countSessions(t, f); n != before {
		t.Errorf("%d sessions after the failed resume, %d before: a fresh one was created", n, before)
	}
	time.Sleep(300 * time.Millisecond)
	if n := log.count(); n != 0 {
		t.Errorf("the original session received %q", log.all())
	}
	if got := tu.editor.Value(); got != "" {
		t.Errorf("the editor holds %q: nothing ran, so there is nothing to park there", got)
	}
}

// countSessions is how many sessions the server has persisted, active or not — the number a failed
// startup must leave unchanged.
func countSessions(t *testing.T, f *fallbackFixture) int {
	t.Helper()
	list, err := f.c.ListSessions("")
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
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
	mu           sync.Mutex
	addr         string
	holdEvents   time.Duration   // how long to delay the stream's response headers
	failCreate   bool            // answer POST /api/sessions with a 500
	subscription map[string]bool // model id -> report it as a flat-fee (subscription) model in GET /api/models
	failCommand  map[string]int  // session command name ("model", "thinking") -> HTTP status to answer with, instead of running it
	requests     []string        // "METHOD path" of every request, in order
	commands     []string        // the command names POSTed to /commands, in order
	closes       int             // POST …/close calls
	eventsOpen   time.Time       // when the stream's headers reached the client
	prompts      []wirePrompt
	createCalls  int
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
		w.mu.Lock()
		w.requests = append(w.requests, r.Method+" "+r.URL.Path)
		w.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/models" && len(w.subscription) > 0:
			// rewrite the model list: mark the chosen models as subscription ones
			up, err := http.Get("http://" + upstream + "/api/models")
			if err != nil {
				http.Error(rw, err.Error(), http.StatusBadGateway)
				return
			}
			defer up.Body.Close()
			var list []map[string]any
			json.NewDecoder(up.Body).Decode(&list) //nolint:errcheck
			w.mu.Lock()
			for _, m := range list {
				if id, _ := m["model"].(string); w.subscription[id] {
					m["is_subscription"] = true
				}
			}
			w.mu.Unlock()
			rw.Header().Set("Content-Type", "application/json")
			json.NewEncoder(rw).Encode(list) //nolint:errcheck
			return
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/commands"):
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			var body struct {
				Command string `json:"command"`
			}
			json.Unmarshal(b, &body) //nolint:errcheck
			w.mu.Lock()
			w.commands = append(w.commands, body.Command)
			status := w.failCommand[body.Command]
			w.mu.Unlock()
			if status != 0 {
				http.Error(rw, `{"error":{"message":"refused by the test: `+body.Command+`"}}`, status)
				return
			}
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/close"):
			w.mu.Lock()
			w.closes++
			w.mu.Unlock()
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
				if c := tu.takeSSECancel(); c != nil {
					c()
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
		if c := tu.takeSSECancel(); c != nil {
			c()
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

// ── explicit --model / --thinking / --resume are requirements, not hints ────

// startErr is autoConnect through a wire proxy, with the flags a command line would set, returning
// the error (what Run turns into "error: …" and exit 1) and the recorded wire traffic.
func startErr(t *testing.T, f *fallbackFixture, proxy *wireProxy, model, thinking, resume, prompt string) (*TUI, *recTerminal, error) {
	t.Helper()
	rec := newRecTerminal()
	tu := New(nil)
	tu.attachTerminal(rec)
	t2 := newTestTUIForEvents()
	tu.history, tu.spinner, tu.info, tu.footer = t2.history, t2.spinner, t2.info, t2.footer
	tu.editor = components.NewEditor(tu.tui, "ph")
	tu.client = client.New(proxy.addr)
	tu.baseCtx = context.Background()
	tu.SetFlags(model, thinking, resume)
	tu.SetInitialPrompt(prompt)
	err := tu.autoConnect(context.Background())
	t.Cleanup(func() {
		if c := tu.takeSSECancel(); c != nil {
			c()
		}
	})
	time.Sleep(150 * time.Millisecond) // anything a failed start wrongly scheduled would have been drawn by now
	return tu, rec, err
}

func (w *wireProxy) sawCreate() int { w.mu.Lock(); defer w.mu.Unlock(); return w.createCalls }
func (w *wireProxy) sawCommands() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.commands...)
}
func (w *wireProxy) sawCloses() int { w.mu.Lock(); defer w.mu.Unlock(); return w.closes }

// A model the person named that cannot be used is an error — never the default model, never a
// warning. The wording is the server's own, the one -p prints.
func TestAnUnavailableExplicitModelFailsAndNothingIsDone(t *testing.T) {
	for name, c := range map[string]struct{ model, want string }{
		"unknown provider": {"zzz/no-existe", `provider not found: "zzz"`},
		"a bare word":      {"basura", `provider not found: "basura"`},
		"unknown model":    {"{FIRST}/no-existe", `model "no-existe" not found in provider`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFallbackFixture(t)
			model := strings.ReplaceAll(c.model, "{FIRST}", f.first)
			proxy := newWireProxy(t, f.addr)
			before := countSessions(t, f)
			tu, rec, err := startErr(t, f, proxy, model, "", "", "the task")
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "create session: ") {
				t.Fatalf("autoConnect returned %v, want create session: …%s", err, c.want)
			}
			if tu.sessionID != "" {
				t.Errorf("a session was opened: %q", tu.sessionID)
			}
			if n := countSessions(t, f); n != before {
				t.Errorf("%d sessions, %d before: a session was created", n, before)
			}
			if got := proxy.promptTexts(); len(got) != 0 {
				t.Errorf("a prompt was sent: %q", got)
			}
			if tu.editor.Value() != "" {
				t.Errorf("the editor holds %q: nothing ran, nothing to park", tu.editor.Value())
			}
			// nothing at all reaches the terminal: no banner frame, no idle, no title, no clear
			for _, w := range rec.all() {
				t.Errorf("a failed start wrote to the terminal: %q", w)
			}
			if len(rec.reports()) != 0 || len(rec.titles()) != 0 {
				t.Errorf("status %v / titles %v were reported for a session that never existed", rec.reports(), rec.titles())
			}
		})
	}
}

// An explicit model with NO provider at all is the same error, not the "No active providers" banner.
func TestAnExplicitModelWithNoProvidersAtAllStillFails(t *testing.T) {
	f := newFallbackFixture(t)
	f.disconnect(f.first)
	f.disconnect(f.second)
	proxy := newWireProxy(t, f.addr)
	tu, rec, err := startErr(t, f, proxy, "zzz/x", "", "", "the task")
	if err == nil || !strings.Contains(err.Error(), "provider not found") {
		t.Fatalf("autoConnect returned %v, want the server's provider-not-found error", err)
	}
	if tu.sessionID != "" || len(rec.all()) != 0 {
		t.Errorf("session %q, writes %q", tu.sessionID, rec.all())
	}
}

func TestAValidExplicitModelIsUsedAsGiven(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	tu, _, err := startErr(t, f, proxy, f.first+"/m", "", "", "the task")
	if err != nil {
		t.Fatal(err)
	}
	if tu.sessionID == "" || tu.model != f.first+"/m" {
		t.Fatalf("session %q on model %q, want a session on %s/m (not the default %s/m)", tu.sessionID, tu.model, f.first, f.second)
	}
	sess, err := f.c.GetSession(tu.sessionID)
	if err != nil || sess.Model != f.first+"/m" {
		t.Errorf("the SESSION runs on %v (%v), want %s/m", sess, err, f.first)
	}
	waitFor(t, "the prompt", func() bool { return len(proxy.promptTexts()) == 1 })
}

// No flag, or an empty one: the settings model, then the first active — and the existing warning
// for a settings model that is gone. (The fixture's settings model is second/m.)
func TestWithoutAnExplicitModelTheFallbacksStillApply(t *testing.T) {
	for name, model := range map[string]string{"no flag": "", "an empty --model": ""} {
		t.Run(name, func(t *testing.T) {
			f := newFallbackFixture(t)
			proxy := newWireProxy(t, f.addr)
			tu, _, err := startErr(t, f, proxy, model, "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if tu.model != f.second+"/m" {
				t.Errorf("model %q, want the settings model %s/m", tu.model, f.second)
			}
		})
	}
	// a settings model that is gone: the first active model and the existing warning
	f := newFallbackFixture(t)
	f.disconnect(f.second) // the settings model's provider is gone; first/m remains
	proxy := newWireProxy(t, f.addr)
	tu, _, err := startErr(t, f, proxy, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if tu.model != f.first+"/m" || !strings.Contains(screenText(tu), "not available. Using first active model") {
		t.Errorf("model %q, screen:\n%s", tu.model, screenText(tu))
	}
}

// --resume: a failure is the server's own message and nothing else happens.
func TestAFailingResumeReturnsTheServersMessageAndCreatesNothing(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	before := countSessions(t, f)
	tu, rec, err := startErr(t, f, proxy, "", "", "missing-id", "x")
	if err == nil || !strings.Contains(err.Error(), "missing-id") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("autoConnect returned %v", err)
	}
	if proxy.sawCreate() != 0 {
		t.Errorf("CreateSession was called %d times after a failed resume", proxy.sawCreate())
	}
	if n := countSessions(t, f); n != before || tu.sessionID != "" {
		t.Errorf("sessions %d (was %d), open session %q", n, before, tu.sessionID)
	}
	if len(rec.all()) != 0 {
		t.Errorf("a failed start wrote to the terminal: %q", rec.all())
	}
}

// --resume + --model whose command FAILS: the session is closed untouched, t.model is never set to
// a model that was not applied, nothing is sent.
func TestResumeWithAFailingModelSwitchClosesTheSessionAndFails(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	proxy.failCommand = map[string]int{"model": http.StatusInternalServerError}
	tu, rec, err := startErr(t, f, proxy, f.second+"/m", "", f.sessionID, "the task")
	if err == nil || !strings.Contains(err.Error(), "switch session") || !strings.Contains(err.Error(), f.second+"/m") {
		t.Fatalf("autoConnect returned %v", err)
	}
	if proxy.sawCloses() != 1 {
		t.Errorf("the resumed session was closed %d times, want 1: it must not be left half-applied", proxy.sawCloses())
	}
	if tu.model != "" || tu.sessionID != "" {
		t.Errorf("model %q session %q: nothing may be set before the switch is confirmed", tu.model, tu.sessionID)
	}
	if got := proxy.promptTexts(); len(got) != 0 {
		t.Errorf("a prompt was sent: %q", got)
	}
	if len(rec.all()) != 0 {
		t.Errorf("wrote to the terminal: %q", rec.all())
	}
	// and the saved model is exactly what it was
	if sess, _ := f.c.GetSession(f.sessionID); sess != nil && sess.Model != f.first+"/m" {
		t.Errorf("the saved model changed to %s", sess.Model)
	}
}

// the same pair, for thinking
func TestResumeWithAFailingThinkingChangeClosesTheSessionAndFails(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	proxy.failCommand = map[string]int{"thinking": http.StatusInternalServerError}
	tu, rec, err := startErr(t, f, proxy, "", "high", f.sessionID, "the task")
	if err == nil || !strings.Contains(err.Error(), "set thinking high") {
		t.Fatalf("autoConnect returned %v", err)
	}
	if proxy.sawCloses() != 1 || tu.thinking != "" || tu.sessionID != "" {
		t.Errorf("closes=%d thinking=%q session=%q", proxy.sawCloses(), tu.thinking, tu.sessionID)
	}
	if len(proxy.promptTexts()) != 0 || len(rec.all()) != 0 {
		t.Errorf("prompts %q, writes %q", proxy.promptTexts(), rec.all())
	}
}

func TestResumeWithASucceedingOverrideAppliesItAndSendsThePrompt(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	tu, _, err := startErr(t, f, proxy, f.second+"/m", "high", f.sessionID, "the task")
	if err != nil {
		t.Fatal(err)
	}
	if tu.sessionID != f.sessionID || tu.model != f.second+"/m" || tu.thinking != "high" {
		t.Fatalf("session %q model %q thinking %q", tu.sessionID, tu.model, tu.thinking)
	}
	sess, _ := f.c.GetSession(f.sessionID)
	if sess == nil || sess.Model != f.second+"/m" || sess.Thinking != "high" {
		t.Errorf("the SESSION itself is %+v, want model %s/m, thinking high", sess, f.second)
	}
	if got := proxy.sawCommands(); strings.Join(got, ",") != "model,thinking" {
		t.Errorf("commands sent: %v", got)
	}
	waitFor(t, "the prompt", func() bool { return len(proxy.promptTexts()) == 1 })
}

// A command the resumed session already satisfies is not re-sent.
func TestResumeDoesNotReapplyWhatTheSessionAlreadyHas(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	if _, _, err := startErr(t, f, proxy, f.first+"/m", "", f.sessionID, ""); err != nil {
		t.Fatal(err)
	}
	if got := proxy.sawCommands(); len(got) != 0 {
		t.Errorf("commands %v sent for a model the session already runs on", got)
	}
}

// A fresh session with an explicit --thinking: applied and CHECKED (the new session starts on the
// settings level, so ignoring the flag would run the task at a level that was not chosen).
func TestAFreshSessionAppliesAnExplicitThinkingAndFailsIfItCannot(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	tu, _, err := startErr(t, f, proxy, "", "high", "", "")
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := f.c.GetSession(tu.sessionID)
	if sess == nil || sess.Thinking != "high" || tu.thinking != "high" {
		t.Errorf("the new SESSION has %+v, TUI says %q: --thinking high was ignored", sess, tu.thinking)
	}

	f2 := newFallbackFixture(t)
	p2 := newWireProxy(t, f2.addr)
	p2.failCommand = map[string]int{"thinking": http.StatusInternalServerError}
	before := countSessions(t, f2)
	tu2, rec, err := startErr(t, f2, p2, "", "high", "", "the task")
	if err == nil || !strings.Contains(err.Error(), "set thinking high") {
		t.Fatalf("autoConnect returned %v", err)
	}
	if p2.sawCloses() != 1 || tu2.sessionID != "" || len(p2.promptTexts()) != 0 || len(rec.all()) != 0 {
		t.Errorf("closes=%d session=%q prompts=%q writes=%q", p2.sawCloses(), tu2.sessionID, p2.promptTexts(), rec.all())
	}
	_ = before
}

// Regression guards for what does NOT change: with no explicit flag violated, the soft paths keep
// their warning and the prompt in the editor.
func TestNoFlagsAndNoProvidersKeepsTheBannerAndThePrompt(t *testing.T) {
	f := newFallbackFixture(t)
	f.disconnect(f.first)
	f.disconnect(f.second)
	proxy := newWireProxy(t, f.addr)
	tu, _, err := startErr(t, f, proxy, "", "", "", "do not lose me")
	if err != nil {
		t.Fatalf("no flag was violated, so this is not an error: %v", err)
	}
	if tu.editor.Value() != "do not lose me" || !strings.Contains(screenText(tu), "No active providers") {
		t.Errorf("editor %q, screen:\n%s", tu.editor.Value(), screenText(tu))
	}
}

func TestTheServerRefusingASessionWithoutAFlagKeepsTheWarningAndThePrompt(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	proxy.failCreate = true
	tu, _, err := startErr(t, f, proxy, "", "", "", "keep me")
	if err != nil {
		t.Fatalf("no flag was violated: %v", err)
	}
	if tu.editor.Value() != "keep me" || !strings.Contains(screenText(tu), "Failed to create session") {
		t.Errorf("editor %q, screen:\n%s", tu.editor.Value(), screenText(tu))
	}
	// ...but WITH an explicit --model the same refusal is the process failing
	f2 := newFallbackFixture(t)
	p2 := newWireProxy(t, f2.addr)
	p2.failCreate = true
	_, rec, err := startErr(t, f2, p2, f2.first+"/m", "", "", "x")
	if err == nil || !strings.Contains(err.Error(), "create session") || len(rec.all()) != 0 {
		t.Errorf("err=%v writes=%q: an explicit model whose session cannot be created must fail", err, rec.all())
	}
}

// A session whose SAVED model is gone is moved by the automatic fallback (it concerns the session's
// own model, not a flag the person passed) and says so — and an explicit --model applies on top.
func TestTheAutomaticFallbackOfASavedModelIsUnaffected(t *testing.T) {
	f := newFallbackFixture(t)
	f.disconnect(f.first)
	proxy := newWireProxy(t, f.addr)
	tu, _, err := startErr(t, f, proxy, "", "", f.sessionID, "")
	if err != nil {
		t.Fatalf("no --model passed, so the fallback applies and it is not an error: %v", err)
	}
	if tu.model != f.second+"/m" || !strings.Contains(screenText(tu), "not available") {
		t.Errorf("model %q, screen:\n%s", tu.model, screenText(tu))
	}
}

// The "(sub)" mark in the footer belongs to the model the session ACTUALLY runs on: with an explicit
// --model the flag is decided after the explicit model is chosen, not before.
func TestTheSubscriptionMarkFollowsTheExplicitModel(t *testing.T) {
	f := newFallbackFixture(t)
	proxy := newWireProxy(t, f.addr)
	proxy.subscription = map[string]bool{f.first + "/m": true} // only the NON-default model is a subscription one
	tu, _, err := startErr(t, f, proxy, f.first+"/m", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !tu.isSubscription {
		t.Errorf("--model %s/m is a subscription model but the TUI does not know (it would price it as pay-per-use)", f.first)
	}
	// and without the flag, the default model is not one
	tu2, _, err := startErr(t, f, proxy, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if tu2.isSubscription {
		t.Errorf("the default model %s/m is not a subscription model", f.second)
	}
}
