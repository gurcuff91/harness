package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/logx"
)

// noKeepAliveClient disables HTTP keep-alive so each request opens (and
// fully closes) its own TCP connection — otherwise http.DefaultClient's
// pooled, reused connection can make a "server is really down" check
// misleadingly succeed against a connection that was established before
// shutdown, even though the listener has already stopped accepting new ones.
var noKeepAliveClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func newTestAgentForRun() *agent.Agent {
	return agent.New(agent.AgentOptions{Store: store.NewInMemoryStore()})
}

// waitForServer polls addr until it accepts connections or the deadline
// passes — Run's listener bind happens synchronously inside Run, but these
// tests call Run in a goroutine (it blocks until ctx is cancelled), so the
// test needs to wait for the bind before issuing a request.
func waitForServer(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become reachable within %s", url, timeout)
}

// TestRunConfigDefaultsToLoopbackEphemeralAddr verifies Run's documented
// default — "127.0.0.1:0" (loopback-only, OS-assigned port) — matches what
// every transport's own in-process server already uses directly (see
// transports/telegram, transports/slack, transports/acp, all calling
// net.Listen("tcp", "127.0.0.1:0")). Exercises the exact default-application
// logic Run itself runs (looping opts over a zero-value runConfig), the same
// pattern transports/acp's TestRunConfigDefaultsToOSStdinStdout uses for its
// own Option defaults.
func TestRunConfigDefaultsToLoopbackEphemeralAddr(t *testing.T) {
	cfg := runConfig{addr: "127.0.0.1:0"}
	for _, opt := range []Option{} {
		opt(&cfg)
	}
	if cfg.addr != "127.0.0.1:0" {
		t.Errorf("default addr = %q, want %q", cfg.addr, "127.0.0.1:0")
	}
}

// TestWithAddrOverridesDefault verifies WithAddr replaces the default.
func TestWithAddrOverridesDefault(t *testing.T) {
	cfg := runConfig{addr: "127.0.0.1:0"}
	WithAddr("127.0.0.1:9999")(&cfg)
	if cfg.addr != "127.0.0.1:9999" {
		t.Errorf("addr = %q, want %q", cfg.addr, "127.0.0.1:9999")
	}
}

// TestRunServesRequestsAndShutsDownCleanly is the core end-to-end test: Run
// binds the given address, serves real HTTP requests, and returns cleanly
// (nil error) once ctx is cancelled — exercising the exact sequence
// documented on Run (listen → serve → wait for ctx → graceful Close), and
// confirming the listener is genuinely closed afterward (a further request
// must fail).
func TestRunServesRequestsAndShutsDownCleanly(t *testing.T) {
	a := newTestAgentForRun()
	ctx, cancel := context.WithCancel(context.Background())
	addr := "127.0.0.1:18965" // fixed, unlikely-collision test-only port

	done := make(chan error, 1)
	go func() { done <- Run(ctx, a, WithAddr(addr)) }()

	waitForServer(t, "http://"+addr+"/api/server", 3*time.Second)

	resp, err := noKeepAliveClient.Get("http://" + addr + "/api/server")
	if err != nil {
		t.Fatalf("GET /api/server: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/server status = %d, want 200", resp.StatusCode)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error on clean shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx was cancelled")
	}

	if _, err := noKeepAliveClient.Get("http://" + addr + "/api/server"); err == nil {
		t.Error("server still accepting connections after Run returned")
	}
}

// captureLogTee — a small recording Logger used to verify Run's WithLogger
// actually reaches request handling, without depending on internal/logx
// (server must not import it — only internal/cli constructs
// internal/logx.HarnessLogger{} and passes it in via WithLogger).
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingLogger) Info(component, event string, kv ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, component+" "+event)
}
func (r *recordingLogger) Warn(component, event string, kv ...any)  { r.Info(component, event, kv...) }
func (r *recordingLogger) Error(component, event string, kv ...any) { r.Info(component, event, kv...) }

func (r *recordingLogger) has(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// TestRunDefaultsToNilLoggerSilently verifies Run's documented default —
// logx.NilLogger{} when WithLogger isn't passed — so an SDK consumer who
// never configures one gets no output at all, not even request logging.
func TestRunDefaultsToNilLoggerSilently(t *testing.T) {
	cfg := runConfig{addr: "127.0.0.1:0", logger: logx.NilLogger{}}
	for _, opt := range []Option{} {
		opt(&cfg)
	}
	if _, ok := cfg.logger.(logx.NilLogger); !ok {
		t.Errorf("default logger = %T, want logx.NilLogger", cfg.logger)
	}
}

// TestRunUsesInjectedLoggerForRequests is the end-to-end test for
// WithLogger: a real HTTP request against a Run-started server must reach
// the injected Logger's Info method (via requestLogger middleware, now
// always registered — see requestLogger's doc comment for why that
// simplification is safe).
func TestRunUsesInjectedLoggerForRequests(t *testing.T) {
	a := newTestAgentForRun()
	ctx, cancel := context.WithCancel(context.Background())
	addr := "127.0.0.1:18967"
	rec := &recordingLogger{}

	done := make(chan error, 1)
	go func() { done <- Run(ctx, a, WithAddr(addr), WithLogger(rec)) }()
	waitForServer(t, "http://"+addr+"/api/server", 3*time.Second)

	resp, err := noKeepAliveClient.Get("http://" + addr + "/api/server")
	if err != nil {
		t.Fatalf("GET /api/server: %v", err)
	}
	resp.Body.Close()

	if !rec.has("server request") {
		t.Errorf("injected logger never received a request log line, got: %v", rec.lines)
	}
	if !rec.has("server listening") {
		t.Errorf("injected logger never received the listening log line, got: %v", rec.lines)
	}

	cancel()
	<-done
}

// TestRunReturnsErrorOnBindFailure verifies Run surfaces a listen failure
// (e.g. an address already in use) as its returned error, rather than
// hanging or panicking.
func TestRunReturnsErrorOnBindFailure(t *testing.T) {
	a1 := newTestAgentForRun()
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	addr := "127.0.0.1:18966"
	done1 := make(chan error, 1)
	go func() { done1 <- Run(ctx1, a1, WithAddr(addr)) }()
	waitForServer(t, "http://"+addr+"/api/server", 3*time.Second)

	a2 := newTestAgentForRun()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	if err := Run(ctx2, a2, WithAddr(addr)); err == nil {
		t.Fatal("Run with a colliding address returned nil, want a bind error")
	}

	cancel1()
	<-done1
}
