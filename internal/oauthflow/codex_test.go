package oauthflow

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stopFlow closes the flow's listener (if any) so subsequent tests can
// rebind port 1455. Safe to call even if Start failed (stopListener will be
// nil in that case — guarded).
func stopFlow(t *testing.T, f OauthFlow) {
	t.Helper()
	concrete, ok := f.(*codexOauthFlow)
	if !ok {
		return
	}
	t.Cleanup(func() {
		concrete.stopMu.Lock()
		stop := concrete.stopListener
		concrete.stopMu.Unlock()
		if stop != nil {
			stop()
		}
	})
}

// ── Start: URL + PKCE construction ─────────────────────────────────────────

func TestCodexFlowStartBuildsAuthorizeURL(t *testing.T) {
	f := NewCodexOauthFlow()
	authURL, verifierCode, err := f.Start()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stopFlow(t, f)

	if verifierCode == "" {
		t.Fatal("Start returned an empty verifierCode")
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth URL not parseable: %v", err)
	}
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/oauth/authorize" {
		t.Errorf("authorize endpoint = %s, want https://auth.openai.com/oauth/authorize", authURL)
	}
	q := u.Query()
	if q.Get("client_id") != codexClientID {
		t.Errorf("client_id = %q, want the Codex CLI's public PKCE client", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != codexRedirect {
		t.Errorf("redirect_uri = %q, want the CLI's registered localhost callback", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256 (PKCE)", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Error("missing code_challenge")
	}
	if q.Get("originator") != "codex_cli_rs" {
		t.Errorf("originator = %q, want codex_cli_rs (the backend allowlist's first-party value)", q.Get("originator"))
	}
	if q.Get("codex_cli_simplified_flow") != "true" {
		t.Error("missing codex_cli_simplified_flow=true")
	}
	if q.Get("state") == "" {
		t.Error("missing state (CSRF guard)")
	}
	// Scope must carry the api.connectors additions (Zed-verified).
	if !strings.Contains(q.Get("scope"), "api.connectors.read") || !strings.Contains(q.Get("scope"), "api.connectors.invoke") {
		t.Errorf("scope = %q, want it to include api.connectors.read/invoke", q.Get("scope"))
	}

	// The challenge in the URL must be S256 of the RETURNED verifierCode.
	h := sha256.Sum256([]byte(verifierCode))
	want := base64.RawURLEncoding.EncodeToString(h[:])
	if q.Get("code_challenge") != want {
		t.Errorf("challenge does not match sha256(verifierCode): %q vs %q", q.Get("code_challenge"), want)
	}
}

// TestCodexStartFailsLoudlyWhenPortBusy is the regression test for the
// hard-error behavior a stateless HTTP handler needs: a second Start while
// a listener is already bound must fail immediately, not silently degrade
// to "no callback page" (which was fine when Start/Exchange lived in one
// process's memory, but leaves a caller with nowhere to learn the code was
// never displayed once Start only returns auth_url/verifier_code).
func TestCodexStartFailsLoudlyWhenPortBusy(t *testing.T) {
	first := NewCodexOauthFlow()
	if _, _, err := first.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	stopFlow(t, first)

	second := NewCodexOauthFlow()
	_, _, err := second.Start()
	if err == nil {
		t.Fatal("expected the second Start to fail while the first flow's listener is still bound")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("error should explain a login is already in progress, got: %v", err)
	}
}

// TestCodexListenerAutoShutsDownAfterTimeout verifies the abandoned-login
// safety net: if the callback never arrives, the listener releases the
// port on its own after codexListenerTimeout, rather than holding it for
// the lifetime of the parent process.
func TestCodexListenerAutoShutsDownAfterTimeout(t *testing.T) {
	orig := codexListenerTimeout
	codexListenerTimeout = 50 * time.Millisecond
	t.Cleanup(func() { codexListenerTimeout = orig })

	f := NewCodexOauthFlow()
	if _, _, err := f.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the timer time to fire and release the port; then a fresh Start
	// must succeed (no "already in progress").
	deadline := time.Now().Add(2 * time.Second)
	var released bool
	for time.Now().Before(deadline) {
		second := NewCodexOauthFlow()
		_, _, err := second.Start()
		if err == nil {
			stopFlow(t, second)
			released = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !released {
		t.Error("port 1455 was never released after codexListenerTimeout elapsed")
	}
}

// ── Exchange validation ─────────────────────────────────────────────────────

func TestCodexExchangeEmptyVerifierErrors(t *testing.T) {
	f := NewCodexOauthFlow()
	_, err := f.Exchange("somecode", "")
	if err == nil {
		t.Fatal("expected an error when verifierCode is empty")
	}
	if !strings.Contains(err.Error(), "verifierCode") {
		t.Errorf("error should name the missing verifierCode, got: %v", err)
	}
}

func TestCodexExchangeEmptyCodeErrors(t *testing.T) {
	f := NewCodexOauthFlow()
	if _, _, err := f.Start(); err != nil {
		t.Fatal(err)
	}
	stopFlow(t, f)
	if _, err := f.Exchange("  ", "v"); err == nil {
		t.Fatal("expected an error for an empty pasted code")
	}
}

func TestCodexExchangeStripsStateFragment(t *testing.T) {
	f := NewCodexOauthFlow()
	if _, _, err := f.Start(); err != nil {
		t.Fatal(err)
	}
	stopFlow(t, f)
	// "CODE#STATE" strips to "CODE", which then genuinely fails against the
	// real token endpoint (no network stub available for a fixed const
	// URL) — this only pins that fragment-stripping happens before the
	// network call, not the network outcome itself.
	if _, err := f.Exchange("CODE#STATE", "v"); err == nil {
		t.Fatal("expected an error from Exchange without a reachable/valid token endpoint")
	}
}

// ── JWT account-id extraction ────────────────────────────────────────────────

func TestExtractChatGPTAccountIDParsesRealShape(t *testing.T) {
	// A realistic id_token payload matching what auth.openai.com returns
	// (verified against a real one: namespaced auth claim block).
	claims := map[string]any{
		"iss": "https://auth.openai.com",
		"aud": []string{codexClientID},
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "6b42759c-9507-4fbe-a344-0bb46fd59c45",
			"chatgpt_plan_type":  "free",
			"chatgpt_user_id":    "user-1",
		},
		"email": "user@example.com",
	}
	payload, _ := json.Marshal(claims)
	idToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	if got := ExtractChatGPTAccountID(idToken); got != "6b42759c-9507-4fbe-a344-0bb46fd59c45" {
		t.Errorf("got %q, want the namespaced claim's account id", got)
	}
}

// ── Pasted-code normalization ───────────────────────────────────────────────

func TestNormalizePastedCode(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"raw code", "ac_G_5WeiXYZ", "ac_G_5WeiXYZ"},
		{"code with whitespace", "  ac_G_5WeiXYZ  ", "ac_G_5WeiXYZ"},
		{"claude-style CODE#STATE", "ac_G_5WeiXYZ#csrfstate", "ac_G_5WeiXYZ"},
		{"full callback URL", "http://localhost:1455/auth/callback?code=ac_G_5WeiXYZ&state=abc", "ac_G_5WeiXYZ"},
		{"full URL with extra params", "http://localhost:1455/auth/callback?code=ac_G_5WeiXYZ&state=xyz&other=1", "ac_G_5WeiXYZ"},
		{"URL without code param", "http://localhost:1455/auth/callback?error=denied", ""},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}
	for _, tc := range tests {
		if got := normalizePastedCode(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ── One-shot callback page listener ──────────────────────────────────────────

func TestServeCodexCallbackPageOneShot(t *testing.T) {
	// Serve the page, hit it once, verify the code shows up HTML-escaped in
	// the response body, then confirm the listener shut itself down (the
	// next request fails). The listener is bound synchronously FIRST —
	// mirroring Start()'s ordering — so the first request can't race the
	// bind the way a real browser would.
	var l net.Listener
	var bindErr error
	for i := 0; i < 50; i++ {
		l, bindErr = net.Listen("tcp", "localhost:1455")
		if bindErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if bindErr != nil {
		t.Fatalf("bind (after retries): %v", bindErr)
	}
	timer := time.AfterFunc(time.Minute, func() {}) // long enough to never fire in this test
	t.Cleanup(func() { timer.Stop() })
	go serveCodexCallbackPage(l, func() { l.Close() }, timer)

	resp, err := http.Get("http://localhost:1455/auth/callback?code=ac_TEST<code>&state=xyz")
	if err != nil {
		t.Fatalf("callback page not reachable after synchronous bind: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), "ac_TEST") {
		t.Errorf("callback page did not render the code, got:\n%s", body)
	}
	if !strings.Contains(string(body), "&lt;code&gt;") {
		t.Errorf("code must be HTML-escaped (XSS guard), got:\n%s", body)
	}

	// One-shot: the server should shut down after serving — a second request
	// must fail. Allow a short settle window.
	dead := false
	for i := 0; i < 50; i++ {
		time.Sleep(30 * time.Millisecond)
		r, err := http.Get("http://localhost:1455/auth/callback?code=second")
		if err != nil {
			dead = true
			break
		}
		r.Body.Close()
	}
	if !dead {
		t.Error("callback server is still serving after the first request — the one-shot shutdown did not fire")
	}
}
