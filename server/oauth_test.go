package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/logx"
)

// newOAuthTestServer stands up a real *httptest.Server wired to the actual
// harness server.handler() — the same router POST /api/oauth/{provider}
// registers on. No mock router: exercising handleOAuth through the real
// chi mux catches route-registration mistakes a direct function call would
// miss.
func newOAuthTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore()})
	t.Cleanup(func() { a.Close() })
	srv := NewServer(a, ServerOptions{Logger: logx.NewNilLogger()})
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	return ts
}

func postOAuth(t *testing.T, ts *httptest.Server, provider string, body map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	resp, err := http.Post(ts.URL+"/api/oauth/"+provider, "application/json", reader)
	if err != nil {
		t.Fatalf("POST /api/oauth/%s: %v", provider, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestHandleOAuthStartClaudeReturnsAuthURLAndVerifier(t *testing.T) {
	ts := newOAuthTestServer(t)
	resp, out := postOAuth(t, ts, "claude-oauth", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, out)
	}
	if out["auth_url"] == "" || out["auth_url"] == nil {
		t.Error("missing auth_url in response")
	}
	if out["verifier_code"] == "" || out["verifier_code"] == nil {
		t.Error("missing verifier_code in response")
	}
}

func TestHandleOAuthStartCodexBindsListenerAndSecondCallConflicts(t *testing.T) {
	ts := newOAuthTestServer(t)

	resp1, out1 := postOAuth(t, ts, "codex-oauth", nil)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first Start status = %d, body = %v", resp1.StatusCode, out1)
	}
	if out1["verifier_code"] == "" {
		t.Fatal("missing verifier_code")
	}

	// A second concurrent Start for codex-oauth must fail — the first
	// flow's listener still holds port 1455.
	resp2, out2 := postOAuth(t, ts, "codex-oauth", nil)
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("second concurrent Start status = %d, want 409; body = %v", resp2.StatusCode, out2)
	}
}

func TestHandleOAuthExchangeMissingVerifierCode(t *testing.T) {
	ts := newOAuthTestServer(t)
	resp, out := postOAuth(t, ts, "claude-oauth", map[string]any{"exchange_code": "somecode"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, out)
	}
}

func TestHandleOAuthUnknownProviderReturns404(t *testing.T) {
	ts := newOAuthTestServer(t)
	resp, out := postOAuth(t, ts, "not-a-real-provider", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, out)
	}
}

func TestHandleOAuthExchangeFailureSurfacesAs502(t *testing.T) {
	ts := newOAuthTestServer(t)
	// A syntactically-plausible but bogus code/verifier pair will genuinely
	// fail against Anthropic's real token endpoint (no local stub exists
	// for a fixed const URL) — this exercises the 502 mapping path.
	resp, out := postOAuth(t, ts, "claude-oauth", map[string]any{
		"exchange_code": "definitely-not-a-real-code",
		"verifier_code": "definitely-not-a-real-verifier",
	})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %v (expected the upstream token endpoint to reject this)", resp.StatusCode, out)
	}
}

func TestHandleOAuthCodexListenerTimeoutReleasesPort(t *testing.T) {
	// This test manipulates a package var in internal/oauthflow, which the
	// server handler transitively uses — see oauthflow's own
	// TestCodexListenerAutoShutsDownAfterTimeout for the focused unit test.
	// Here we only need to confirm the SERVER's stateless handling doesn't
	// somehow keep its own reference alive past the flow's own cleanup —
	// covered indirectly by the "second call conflicts, but only while the
	// first is genuinely still bound" test above. Skipped as redundant with
	// the lower-level test; kept as a documented placeholder in case the
	// server ever grows its own listener bookkeeping.
	t.Skip("covered by internal/oauthflow.TestCodexListenerAutoShutsDownAfterTimeout")
	_ = time.Second
}
