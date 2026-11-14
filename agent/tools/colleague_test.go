package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeColleagueServer is a minimal, in-memory stand-in for a real harness
// server — just enough of the session lifecycle (create, resume, close,
// ask, settings) for askColleague's persistence contract to be exercised
// end-to-end without spinning up a real *agent.Agent/*server.Server. Session
// state (whether a session is "active"/resumable, and the running message
// count used to assert history accumulates) lives in-memory, keyed by id.
type fakeColleagueServer struct {
	mu       sync.Mutex
	sessions map[string]*fakeColleagueSession
	nextID   int
	// askLog records every prompt text this server received, in order.
	askLog []string
	// asks records each ask in full (text, origin, no_queue) so a test can assert
	// HOW a colleague was prompted, not just what.
	asks []fakeAsk
	// resumeCalls counts POST …/resume: talking to an ACTIVE session must never
	// need one.
	resumeCalls int
	// createdWith records "<cwd>|<name>" of every session created over the API.
	createdWith []string
	// closeCalls counts POST …/close per session id.
	closeCalls map[string]int
	// openElsewhereOnResume, when set, makes the NEXT resume of that id find the
	// session already active — somebody opened it between the tool's check and its
	// resume — so the response says resumed:false.
	openElsewhereOnResume map[string]bool
	// persisted are sessions that exist on disk but are not active (closed).
}

type fakeAsk struct {
	SessionID string
	Text      string
	Origin    string
	NoQueue   bool
}

type fakeColleagueSession struct {
	id     string
	name   string
	cwd    string
	closed bool // true after /close: no longer ACTIVE (still on disk)
	busy   bool // a turn is in flight: a no_queue ask is refused with 409
	turns  int
}

func newFakeColleagueServer() *fakeColleagueServer {
	return &fakeColleagueServer{sessions: map[string]*fakeColleagueSession{}, closeCalls: map[string]int{}, openElsewhereOnResume: map[string]bool{}}
}

// addActive registers an already-open session (what a TUI or a transport holds).
func (f *fakeColleagueServer) addActive(id, name, cwd string, busy bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[id] = &fakeColleagueSession{id: id, name: name, cwd: cwd, busy: busy}
}

func (f *fakeColleagueServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/server", func(w http.ResponseWriter, r *http.Request) {
		// askColleague's liveness probe (GetServerInfo) hits this first, on
		// every call — must respond for any of the other endpoints below to
		// ever be reached.
		json.NewEncoder(w).Encode(map[string]string{"name": "harness", "version": "test", "cwd": "/colleague/cwd"})
	})
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"active_model": "fake/model-1", "thinking_level": "off"})
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{{"provider": "fake", "model": "fake/model-1"}})
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("active") == "true":
			f.mu.Lock()
			out := []map[string]any{}
			for _, sess := range f.sessions {
				if sess.closed {
					continue
				}
				out = append(out, map[string]any{"id": sess.id, "name": sess.name, "cwd": sess.cwd, "model": "fake/model-1", "busy": sess.busy})
			}
			f.mu.Unlock()
			json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost:
			var body struct {
				CWD  string `json:"cwd"`
				Name string `json:"name"`
			}
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			f.mu.Lock()
			f.nextID++
			id := fmt.Sprintf("sess-%d", f.nextID)
			f.sessions[id] = &fakeColleagueSession{id: id, name: "New Session", cwd: body.CWD}
			f.createdWith = append(f.createdWith, body.CWD+"|"+body.Name)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"id": id})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	mux.HandleFunc("/api/sessions/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		parts := strings.Split(rest, "/")
		id := parts[0]

		f.mu.Lock()
		sess, ok := f.sessions[id]
		f.mu.Unlock()

		switch {
		case len(parts) == 2 && parts[1] == "resume" && r.Method == http.MethodPost:
			f.mu.Lock()
			f.resumeCalls++
			f.mu.Unlock()
			if !ok {
				http.Error(w, `{"error":{"message":"session `+id+` not found"}}`, http.StatusNotFound)
				return
			}
			f.mu.Lock()
			// Resume is idempotent: resumed is true only if THIS call reopened it.
			opened := sess.closed && !f.openElsewhereOnResume[id]
			sess.closed = false
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": id, "resumed": opened})
		case len(parts) == 2 && parts[1] == "close" && r.Method == http.MethodPost:
			if !ok {
				http.Error(w, `{"error":{"message":"session not active"}}`, http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			sess.closed = true
			f.closeCalls[id]++
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"status": map[string]string{"code": "closed"}})
		case len(parts) == 2 && parts[1] == "ask" && r.Method == http.MethodPost:
			if !ok || sess.closed {
				http.Error(w, `{"error":{"message":"session not found"}}`, http.StatusNotFound)
				return
			}
			var body struct {
				Text    string `json:"text"`
				Origin  string `json:"origin"`
				NoQueue bool   `json:"no_queue"`
			}
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			f.mu.Lock()
			if sess.busy && body.NoQueue {
				f.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":{"message":"session is busy"}}`))
				return
			}
			sess.turns++
			f.askLog = append(f.askLog, body.Text)
			f.asks = append(f.asks, fakeAsk{SessionID: id, Text: body.Text, Origin: body.Origin, NoQueue: body.NoQueue})
			turns := sess.turns
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"text": fmt.Sprintf("reply #%d to: %s", turns, body.Text)})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	return mux
}

func (f *fakeColleagueServer) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

// TestAskColleagueFreshSessionIsClosedNotDeleted confirms a call with no session
// creates one, asks it, and closes (never deletes) it, so the conversation is on
// the colleague's disk.
func TestAskColleagueFreshSessionIsClosedNotDeleted(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	text, err := askColleague(context.Background(), ts.URL, "hello", "", nil, 5*time.Second)
	if err != nil {
		t.Fatalf("askColleague: %v", err)
	}
	if !strings.Contains(text, "reply #1 to: hello") {
		t.Errorf("response = %q, want it to contain the colleague's reply", text)
	}
	if srv.sessionCount() != 1 {
		t.Fatalf("server has %d sessions, want 1 (created, never deleted)", srv.sessionCount())
	}
	for _, sess := range srv.sessions {
		if !sess.closed {
			t.Error("session was never closed — defer must call CloseSession")
		}
	}
}

// A NEW conversation (no session): created on the colleague in ITS working
// directory with the default name, asked, and closed — and the reply carries the
// session id and says the conversation is closed but can be continued with it.
func TestAskColleagueNewConversationReturnsItsIDAndIsClosed(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	text, err := askColleague(context.Background(), ts.URL, "hello", "", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "reply #1 to: hello") {
		t.Errorf("reply = %q", text)
	}
	id := extractSessionID(t, text)
	if !strings.Contains(text, "closed now that the colleague has answered") || !strings.Contains(text, "pass this id as `session` to continue") {
		t.Errorf("the footer must say it is closed AND how to continue it: %q", text)
	}
	if strings.Contains(text, "cannot be continued") || strings.Contains(text, "New conversation —") {
		t.Errorf("the old 'cannot be continued' note is false now: %q", text)
	}
	if srv.sessionCount() != 1 || !srv.sessions[id].closed || srv.closeCalls[id] != 1 {
		t.Errorf("sessions=%d closed=%v closeCalls=%d: the new session must be created and closed exactly once", srv.sessionCount(), srv.sessions[id].closed, srv.closeCalls[id])
	}
	// created in the colleague's own working directory, with the default name
	if len(srv.createdWith) != 1 || srv.createdWith[0] != "/colleague/cwd|" {
		t.Errorf("created with %v, want the colleague's cwd and NO name (so harness picks its default)", srv.createdWith)
	}
}

// With `session`, the colleague's ACTIVE session is prompted directly: no resume,
// no new session, the session is left open and the prompt is tagged as coming from
// a colleague and sent without queueing.
func TestAskColleagueActiveSessionIsPromptedDirectly(t *testing.T) {
	srv := newFakeColleagueServer()
	srv.addActive("tui-session", "Fix the bug", "/proj/a", false)
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	for i, msg := range []string{"first", "second"} {
		text, err := askColleague(context.Background(), ts.URL, msg, "tui-session", nil, 5*time.Second)
		if err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
		if want := fmt.Sprintf("reply #%d to: %s", i+1, msg); !strings.HasPrefix(text, want) {
			t.Errorf("reply %d = %q, want it to start with the colleague's text %q", i, text, want)
		}
		if extractSessionID(t, text) != "tui-session" || !strings.Contains(text, "active on the colleague") {
			t.Errorf("reply %d footer = %q, want the id and 'active on the colleague'", i, text)
		}
	}
	if srv.resumeCalls != 0 {
		t.Errorf("resume was called %d times: an active session needs none", srv.resumeCalls)
	}
	if srv.sessionCount() != 1 {
		t.Errorf("sessions = %d, want 1: no new session may be created", srv.sessionCount())
	}
	if sess := srv.sessions["tui-session"]; sess.closed {
		t.Error("the colleague's session was closed: it is somebody else's, ask must leave it open")
	}
	for _, a := range srv.asks {
		if a.Origin != "colleague" || !a.NoQueue || a.SessionID != "tui-session" {
			t.Errorf("ask = %+v, want origin=colleague, no_queue=true, on tui-session", a)
		}
	}
}

// A busy session refuses at once and the tool says so, as an error: the message
// was not sent and nothing was created.
func TestAskColleagueBusySessionIsAnErrorAndNothingIsSent(t *testing.T) {
	srv := newFakeColleagueServer()
	srv.addActive("busy-one", "Long task", "/proj/a", true)
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	start := time.Now()
	text, err := askColleague(context.Background(), ts.URL, "are you free?", "busy-one", nil, 30*time.Second)
	if err == nil {
		t.Fatal("a busy session must be an error")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v: a busy session must be refused at once, not waited on", time.Since(start))
	}
	for _, want := range []string{"busy", "NOT sent", "never queued"} {
		if !strings.Contains(text, want) {
			t.Errorf("message %q lacks %q", text, want)
		}
	}
	if len(srv.asks) != 0 || len(srv.askLog) != 0 {
		t.Errorf("the colleague received %v: nothing may reach a busy session", srv.askLog)
	}
	if srv.sessionCount() != 1 {
		t.Errorf("sessions = %d, want 1: a busy refusal must not create a fallback session", srv.sessionCount())
	}
}

// A session that is NOT active is resumed — a closed one from an earlier call, or
// any persisted one — answered, and closed again because THIS call opened it. The
// history is the same session's (no fresh one is created).
func TestAskColleagueInactiveSessionIsResumedAnsweredAndClosedAgain(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	first, err := askColleague(context.Background(), ts.URL, "first question", "", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	id := extractSessionID(t, first)
	if !srv.sessions[id].closed {
		t.Fatal("setup: the first call must leave the session closed")
	}

	second, err := askColleague(context.Background(), ts.URL, "follow-up", id, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("continuing a closed session: %v", err)
	}
	if !strings.Contains(second, "reply #2 to: follow-up") {
		t.Errorf("reply = %q, want turn 2 of the SAME conversation (it has context)", second)
	}
	if extractSessionID(t, second) != id {
		t.Errorf("the footer must keep the same id, got %q", second)
	}
	if srv.resumeCalls != 1 {
		t.Errorf("resume called %d times, want 1", srv.resumeCalls)
	}
	if srv.sessionCount() != 1 {
		t.Errorf("sessions = %d: continuing must not create a new one", srv.sessionCount())
	}
	if !srv.sessions[id].closed || srv.closeCalls[id] != 2 {
		t.Errorf("closed=%v closeCalls=%d: the resumed session must be closed again (once per call = 2 in total)", srv.sessions[id].closed, srv.closeCalls[id])
	}
	if len(srv.askLog) != 2 || srv.askLog[0] != "first question" || srv.askLog[1] != "follow-up" {
		t.Errorf("askLog = %v: both turns must land in the same session", srv.askLog)
	}
	if !strings.Contains(second, "closed now that the colleague has answered") {
		t.Errorf("footer = %q", second)
	}
}

// A session id that exists nowhere is "not found" — no fresh session in its place,
// nothing created, nothing asked.
func TestAskColleagueUnknownSessionIsNotFoundAndCreatesNothing(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	text, err := askColleague(context.Background(), ts.URL, "hello", "does-not-exist", nil, 5*time.Second)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(text, "not found") || !strings.Contains(text, "does-not-exist") {
		t.Errorf("message = %q, want a clear not-found", text)
	}
	if srv.sessionCount() != 0 || len(srv.createdWith) != 0 {
		t.Errorf("sessions=%d created=%v: a missing session must NOT be replaced by a fresh one", srv.sessionCount(), srv.createdWith)
	}
	if len(srv.asks) != 0 {
		t.Errorf("asks = %v, want none", srv.asks)
	}
}

// THE SAFEGUARD. Resume is idempotent, so a session somebody opened between the
// tool's "is it active?" check and its resume comes back as already active
// (resumed:false). It is theirs: the tool asks it and must NOT close it — closing
// would end a person's live conversation (their next prompt would be 'not found').
func TestAskColleagueNeverClosesASessionItDidNotOpen(t *testing.T) {
	srv := newFakeColleagueServer()
	srv.addActive("race", "Opened by a user meanwhile", "/p", false)
	srv.sessions["race"].closed = true       // looked inactive when the tool checked…
	srv.openElsewhereOnResume["race"] = true // …but somebody had opened it by the time of the resume
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	text, err := askColleague(context.Background(), ts.URL, "are you there?", "race", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "reply #1 to: are you there?") {
		t.Errorf("reply = %q", text)
	}
	if srv.closeCalls["race"] != 0 || srv.sessions["race"].closed {
		t.Errorf("closeCalls=%d closed=%v: the tool closed a session it did not open", srv.closeCalls["race"], srv.sessions["race"].closed)
	}
	if !strings.Contains(text, "active on the colleague") || strings.Contains(text, "closed now that") {
		t.Errorf("the footer must say the session is still ACTIVE: %q", text)
	}
}

// A new conversation is tagged as coming from a colleague too (its TUI, if any,
// shows it with the colleague icon) and sent without queueing.
func TestAskColleagueNewConversationIsTaggedAsFromAColleague(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	if _, err := askColleague(context.Background(), ts.URL, "hi", "", nil, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(srv.asks) != 1 || srv.asks[0].Origin != "colleague" || !srv.asks[0].NoQueue {
		t.Errorf("asks = %+v", srv.asks)
	}
}

// TestAskColleagueBackgroundPersistsSessionToo confirms background mode
// follows the identical create-or-resume/close-never-delete contract as
// the synchronous path.
func TestAskColleagueBackgroundPersistsSessionToo(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	msg, err := askColleagueBackground(ts.URL, "wonder-woman", "hello", "", nil)
	if err != nil {
		t.Fatalf("askColleagueBackground: %v", err)
	}
	path := extractResultPath(t, msg)

	deadline := time.Now().Add(3 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		b, readErr := os.ReadFile(path)
		if readErr == nil && len(b) > 0 {
			content = string(b)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(content, "reply #1 to: hello") || !strings.Contains(content, "[colleague session: ") || !strings.Contains(content, "closed now that the colleague has answered") {
		t.Fatalf("background result = %q, want the colleague's reply and the closed note", content)
	}
	if srv.sessionCount() != 1 {
		t.Errorf("server has %d sessions, want 1", srv.sessionCount())
	}
}

// TestAskColleagueLivenessProbeFailsFastAgainstUnresponsiveColleague
// reproduces the real incident: a colleague whose process is suspended
// (SIGTSTP/Ctrl+Z) keeps its TCP listener open — the connection succeeds —
// but nothing ever answers. Before the liveness probe existed, this hung
// until Ask's own (120s-scale) timeout; the probe must fail in well under
// a second here since nothing is actually listening on this port at all
// (a stronger, easier-to-simulate case than a genuinely stuck TCP accept
// queue, but it exercises the exact same "the request never returns"
// code path askColleague's liveness probe exists to catch).
func TestAskColleagueLivenessProbeFailsFastAgainstUnresponsiveColleague(t *testing.T) {
	// A real listener that never accepts any connection — closest
	// same-process approximation of "TCP handshake succeeds, then nothing
	// ever responds" without actually suspending a process in the test
	// suite. Using a closed/unused port (connection refused) still proves
	// the probe fails fast and askColleague never proceeds to create a
	// session; it just fails at the dial step instead of after a partial
	// handshake, which is a strictly EASIER failure for the probe to catch
	// (both are "the request never gets a response in time or at all").
	unreachable := "http://127.0.0.1:1" // reserved/unassigned — connection refused, fast

	start := time.Now()
	text, err := askColleague(context.Background(), unreachable, "hello", "", nil, 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error against an unreachable colleague, got nil")
	}
	if elapsed > colleagueLivenessTimeout {
		t.Errorf("askColleague took %v, want it to fail within the liveness probe's own %v bound", elapsed, colleagueLivenessTimeout)
	}
	if !strings.Contains(text, "not responding") {
		t.Errorf("response = %q, want a clear not-responding message", text)
	}
}

// TestAskColleagueRespectsCallerCancellation confirms the whole point of
// wiring the turn's ctx through client.WithContext: cancelling ctx (what
// Session.Stop()/Esc does) interrupts an in-flight askColleague call
// promptly, rather than leaving it hung until some internal timeout
// eventually fires on its own.
func TestAskColleagueRespectsCallerCancellation(t *testing.T) {
	// A server that accepts the connection but never responds to
	// /api/server — simulates the suspended-process scenario more
	// faithfully than the unreachable-port case above (a real TCP
	// handshake succeeds; the liveness probe genuinely hangs waiting for a
	// response, exactly like the live incident).
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never responds until the test tears down
	}))
	// Order matters: defers run LIFO. block must be closed (unblocking the
	// handler goroutine above) BEFORE ts.Close() runs — ts.Close() itself
	// waits for all active connections to finish, which would deadlock the
	// test teardown against a handler still stuck on <-block.
	defer ts.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel() // simulates Session.Stop()
	}()

	start := time.Now()
	_, err := askColleague(ctx, ts.URL, "hello", "", nil, 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the caller's ctx is cancelled mid-call, got nil")
	}
	// Must return promptly after cancellation (well under the liveness
	// probe's own 5s timeout, let alone Ask's much longer default) — proves
	// the cancellation actually reached the in-flight HTTP request instead
	// of being ignored until an unrelated timeout fired.
	if elapsed > 2*time.Second {
		t.Errorf("askColleague took %v after ctx cancellation, want it to return promptly (well under colleagueLivenessTimeout=%v)", elapsed, colleagueLivenessTimeout)
	}
}

func extractResultPath(t *testing.T, msg string) string {
	t.Helper()
	const marker = "Result will be written to: "
	i := strings.Index(msg, marker)
	if i < 0 {
		t.Fatalf("no result path found in %q", msg)
	}
	rest := msg[i+len(marker):]
	end := strings.IndexByte(rest, '\n')
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func extractSessionID(t *testing.T, text string) string {
	t.Helper()
	const marker = "[colleague session: "
	i := strings.Index(text, marker)
	if i < 0 {
		t.Fatalf("no session footer in %q", text)
	}
	rest := text[i+len(marker):]
	end := strings.IndexAny(rest, " ]")
	if end < 0 {
		t.Fatalf("malformed session footer in %q", text)
	}
	return rest[:end]
}
