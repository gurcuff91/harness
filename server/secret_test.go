package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
)

func TestGenerateServerSec(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		s := GenerateServerSec()
		if !strings.HasPrefix(s, "hs_") || len(s) != 3+64 || strings.Trim(s[3:], "0123456789abcdef") != "" {
			t.Fatalf("secret = %q, want hs_ + 64 hex", s)
		}
		if seen[s] {
			t.Fatalf("a secret repeated: %s", s)
		}
		seen[s] = true
	}
}

// secureServer is a server with a secret, over a fake provider with a session.
func secureServer(t *testing.T, secret string) (*Server, string) {
	t.Helper()
	p := newToggle("sec", "m")
	_, _, _ = fallbackServer(t, p) // isolates HOME, stores and the provider registry
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, "http://" + srv.Addr()
}

func status(t *testing.T, method, url string, hdr map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestTheSecretIsRequiredEverywhereButTheOpenPaths(t *testing.T) {
	const sec = "hs_test-secret"
	_, base := secureServer(t, sec)

	// Open: who the server is, its docs, and a CORS preflight.
	for _, p := range []string{"/api/server", "/api/docs", "/api/docs/openapi.json"} {
		if s := status(t, "GET", base+p, nil); s != 200 {
			t.Errorf("GET %s without a secret = %d, want 200", p, s)
		}
	}
	if s := status(t, "OPTIONS", base+"/api/sessions", nil); s != 204 {
		t.Errorf("preflight = %d, want 204", s)
	}
	// Everything else: 401 without it, with a wrong one, and with the secret as a prefix of the guess.
	for _, p := range []string{"/api/sessions", "/api/settings", "/api/providers", "/api/models", "/api/memories", "/api/schedules", "/api/providers/sec/proxy/models", "/api/sessions/x/events"} {
		for name, hdr := range map[string]map[string]string{
			"none":         nil,
			"wrong header": {"X-Harness-Sec": "nope"},
			"wrong bearer": {"Authorization": "Bearer nope"},
			"longer guess": {"X-Harness-Sec": sec + "x"},
			"prefix guess": {"X-Harness-Sec": sec[:len(sec)-1]},
			"basic auth":   {"Authorization": "Basic " + sec},
			"bearer lower": {"Authorization": "bearer " + sec},
		} {
			if s := status(t, "GET", base+p, hdr); s != http.StatusUnauthorized {
				t.Errorf("GET %s with %s = %d, want 401", p, name, s)
			}
		}
	}
	// POST routes too (a body-less POST must not get past the middleware).
	for _, p := range []string{"/api/sessions", "/api/sessions/import", "/api/providers/sec/connect", "/api/oauth/claude-oauth"} {
		if s := status(t, "POST", base+p, nil); s != http.StatusUnauthorized {
			t.Errorf("POST %s without a secret = %d, want 401", p, s)
		}
	}
	// With the secret, either way: not a 401.
	for name, hdr := range map[string]map[string]string{
		"header": {"X-Harness-Sec": sec},
		"bearer": {"Authorization": "Bearer " + sec},
	} {
		if s := status(t, "GET", base+"/api/sessions", hdr); s != 200 {
			t.Errorf("GET /api/sessions with the secret as %s = %d, want 200", name, s)
		}
	}
}

// The 401 carries the standard error shape and a Bearer challenge, and never
// says WHICH part was wrong.
func TestTheUnauthorizedAnswer(t *testing.T) {
	_, base := secureServer(t, "hs_x")
	resp, err := http.Get(base + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 401 || !strings.Contains(string(body), "missing or invalid secret") || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("status %d body %s challenge %q", resp.StatusCode, body, resp.Header.Get("WWW-Authenticate"))
	}
}

// A server without a secret asks for nothing (the in-process servers of the
// TUI and acp).
func TestNoSecretMeansNoAuthentication(t *testing.T) {
	_, base := secureServer(t, "")
	if s := status(t, "GET", base+"/api/sessions", nil); s != 200 {
		t.Errorf("no secret configured: GET /api/sessions = %d", s)
	}
}

// The client sends the secret on EVERY kind of request: JSON calls, the SSE
// stream, the provider proxy, and the export/import streams.
func TestTheClientSendsTheSecretOnEveryRequest(t *testing.T) {
	const sec = "hs_client"
	srv, base := secureServer(t, sec)
	good := client.New(srv.Addr(), client.WithSecret(sec))
	bad := client.New(srv.Addr())
	wrong := client.New(srv.Addr(), client.WithSecret("hs_other"))

	if _, err := bad.GetSettings(); statusOf(err) != 401 {
		t.Fatalf("no secret: %v", err)
	}
	if _, err := wrong.GetSettings(); statusOf(err) != 401 {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := good.GetSettings(); err != nil {
		t.Fatalf("right secret: %v", err)
	}

	sess, err := good.CreateSession("sec/m", t.TempDir(), "Secured")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.StreamEvents(context.Background(), sess.ID); err == nil || statusOf(err) != 401 {
		t.Errorf("SSE without the secret: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := good.StreamEvents(ctx, sess.ID); err != nil {
		t.Errorf("SSE with the secret: %v", err)
	}
	if _, err := good.Ask(sess.ID, "hello", 0); err != nil {
		t.Errorf("Ask: %v", err)
	}

	var buf bytes.Buffer
	if _, _, err := bad.ExportSession(sess.ID, &buf); statusOf(err) != 401 {
		t.Errorf("export without the secret: %v", err)
	}
	if _, _, err := good.ExportSession(sess.ID, &buf); err != nil {
		t.Fatalf("export with the secret: %v", err)
	}
	if _, err := bad.ImportSession(bytes.NewReader(buf.Bytes()), ""); statusOf(err) != 401 {
		t.Errorf("import without the secret: %v", err)
	}
	if _, err := good.ImportSession(bytes.NewReader(buf.Bytes()), ""); err != nil {
		t.Errorf("import with the secret: %v", err)
	}

	resp, err := bad.ProviderDo(context.Background(), "sec", "GET", "/models", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("provider proxy without the secret = %d", resp.StatusCode)
	}
	resp, err = good.ProviderDo(context.Background(), "sec", "GET", "/models", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 401 {
		t.Error("the provider proxy refused the client's secret")
	}
	_ = base
}

// server.Run (what harness serve uses) takes the secret through WithSecret.
func TestRunWithSecret(t *testing.T) {
	p := newToggle("runsec", "m")
	_, _, _ = fallbackServer(t, p)
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// Run picks its own port: read it from the logger-free server by dialing
	// the address it logs — here a fixed loopback port is simpler.
	const addr = "127.0.0.1:18795"
	go func() { done <- Run(ctx, a, WithAddr(addr), WithSecret("hs_run")) }()
	deadline := time.Now().Add(5 * time.Second)
	var code int
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + addr + "/api/sessions"); err == nil {
			code = resp.StatusCode
			resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != http.StatusUnauthorized {
		t.Errorf("Run with a secret answered %d without it, want 401", code)
	}
	if s := status(t, "GET", "http://"+addr+"/api/sessions", map[string]string{"X-Harness-Sec": "hs_run"}); s != 200 {
		t.Errorf("Run with a secret answered %d with it, want 200", s)
	}
	cancel()
	<-done
}

// A CORS preflight (a browser sends it without credentials) is answered, and
// announces the secret headers a browser client may send.
func TestPreflightAnnouncesTheSecretHeaders(t *testing.T) {
	_, base := secureServer(t, "hs_cors")
	req, _ := http.NewRequest("OPTIONS", base+"/api/sessions", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	allow := resp.Header.Get("Access-Control-Allow-Headers")
	if resp.StatusCode != 204 || !strings.Contains(allow, "X-Harness-Sec") || !strings.Contains(allow, "Authorization") {
		t.Errorf("preflight = %d, allowed headers %q", resp.StatusCode, allow)
	}
}

// Only the exact open paths are open: a sibling that merely starts with one is not.
func TestOpenPathsAreExact(t *testing.T) {
	_, base := secureServer(t, "hs_exact")
	for _, p := range []string{"/api/server/anything", "/api/docs/other", "/api/server2", "/api/docs/openapi.json/x"} {
		if s := status(t, "GET", base+p, nil); s != http.StatusUnauthorized {
			t.Errorf("GET %s without a secret = %d, want 401 (only the exact open paths are open)", p, s)
		}
	}
}
