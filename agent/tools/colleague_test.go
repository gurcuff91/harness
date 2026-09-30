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
	// askLog records every prompt text this server received, in order —
	// used to assert a resumed conversation actually carries prior turns'
	// awareness (a real server would; this fake just proves the SAME
	// session id is reused across calls, which is what askColleague's
	// contract depends on).
	askLog []string
}

type fakeColleagueSession struct {
	id     string
	closed bool // true after /close; ResumeSession-equivalent still works (disk-backed)
	turns  int
}

func newFakeColleagueServer() *fakeColleagueServer {
	return &fakeColleagueServer{sessions: map[string]*fakeColleagueSession{}}
}

func (f *fakeColleagueServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/server", func(w http.ResponseWriter, r *http.Request) {
		// askColleague's liveness probe (GetServerInfo) hits this first, on
		// every call — must respond for any of the other endpoints below to
		// ever be reached.
		json.NewEncoder(w).Encode(map[string]string{"name": "harness", "version": "test"})
	})
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"active_model": "fake/model-1", "thinking_level": "off"})
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{{"provider": "fake", "model": "fake/model-1"}})
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.nextID++
		id := fmt.Sprintf("sess-%d", f.nextID)
		f.sessions[id] = &fakeColleagueSession{id: id}
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": id})
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
			if !ok {
				http.Error(w, `{"error":{"message":"session `+id+` not found"}}`, http.StatusNotFound)
				return
			}
			f.mu.Lock()
			sess.closed = false
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"id": id})
		case len(parts) == 2 && parts[1] == "close" && r.Method == http.MethodPost:
			if !ok {
				http.Error(w, `{"error":{"message":"session not active"}}`, http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			sess.closed = true
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"status": map[string]string{"code": "closed"}})
		case len(parts) == 2 && parts[1] == "ask" && r.Method == http.MethodPost:
			if !ok {
				http.Error(w, `{"error":{"message":"session not found"}}`, http.StatusNotFound)
				return
			}
			var body struct {
				Text string `json:"text"`
			}
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
			f.mu.Lock()
			sess.turns++
			f.askLog = append(f.askLog, body.Text)
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

// TestAskColleagueFreshSessionIsClosedNotDeleted confirms a first call
// (session == "") creates a session, asks it, and closes (never deletes)
// it — the response must also carry the session id so the caller can
// continue the conversation.
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
	if !strings.Contains(text, "[colleague session:") {
		t.Errorf("response = %q, want a trailing [colleague session: ...] note", text)
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

// TestAskColleagueResumeContinuesSameSession confirms passing back the
// session id from a first call reuses that EXACT session (no new one
// created) and skips model resolution (GetSettings/ListModels never hit).
func TestAskColleagueResumeContinuesSameSession(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	first, err := askColleague(context.Background(), ts.URL, "first message", "", nil, 5*time.Second)
	if err != nil {
		t.Fatalf("first askColleague: %v", err)
	}
	sessID := extractSessionID(t, first)

	second, err := askColleague(context.Background(), ts.URL, "second message", sessID, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("second askColleague: %v", err)
	}
	if !strings.Contains(second, "reply #2 to: second message") {
		t.Errorf("response = %q, want turn 2 on the SAME session (proves resume, not a fresh create)", second)
	}
	if srv.sessionCount() != 1 {
		t.Errorf("server has %d sessions, want 1 (resumed, not a second one created)", srv.sessionCount())
	}
	if len(srv.askLog) != 2 || srv.askLog[0] != "first message" || srv.askLog[1] != "second message" {
		t.Errorf("askLog = %v, want both turns recorded against the same session", srv.askLog)
	}
}

// TestAskColleagueUnknownSessionFailsClearlyNoFallback confirms an invalid/
// unknown session id surfaces an explicit error — never a silent fallback
// to creating a fresh session, which would misleadingly look like the
// conversation continued when it didn't.
func TestAskColleagueUnknownSessionFailsClearlyNoFallback(t *testing.T) {
	srv := newFakeColleagueServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	text, err := askColleague(context.Background(), ts.URL, "hello", "does-not-exist", nil, 5*time.Second)
	if err == nil {
		t.Fatal("expected an error for an unknown session id, got nil")
	}
	if !strings.Contains(text, "not found") {
		t.Errorf("response = %q, want a clear not-found message", text)
	}
	if srv.sessionCount() != 0 {
		t.Errorf("server has %d sessions, want 0 (must NOT silently create a fresh one)", srv.sessionCount())
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
	if !strings.Contains(content, "[colleague session:") {
		t.Fatalf("background result = %q, want a trailing session note", content)
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

func extractSessionID(t *testing.T, text string) string {
	t.Helper()
	const marker = "[colleague session: "
	i := strings.Index(text, marker)
	if i < 0 {
		t.Fatalf("no session marker found in %q", text)
	}
	rest := text[i+len(marker):]
	end := strings.IndexAny(rest, " ]")
	if end < 0 {
		t.Fatalf("malformed session marker in %q", text)
	}
	return rest[:end]
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
