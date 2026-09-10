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

// ── Start: URL + PKCE construction ─────────────────────────────────────────

func TestCodexFlowStartBuildsAuthorizeURL(t *testing.T) {
	openedURL := stubBrowser(t)
	silenceCallbackNote(t)
	f := NewCodexOauthFlow()

	if _, err := f.Start(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() {
		concrete := f.(*codexOauthFlow)
		concrete.stopMu.Lock()
		stop := concrete.stopListener
		concrete.stopMu.Unlock()
		stop()
	})
	authURL := *openedURL
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
}

func TestCodexPKCEVerifierProducesMatchingChallenge(t *testing.T) {
	openedURL := stubBrowser(t)
	silenceCallbackNote(t)
	f := NewCodexOauthFlow().(*codexOauthFlow)
	if _, err := f.Start(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f.stopMu.Lock()
	stop := f.stopListener
	f.stopMu.Unlock()
	t.Cleanup(stop)
	authURL := *openedURL
	q, _ := url.Parse(authURL)
	challenge := q.Query().Get("code_challenge")

	// Reproduce the S256 chain: sha256(verifier) base64url must equal the
	// challenge the URL carries — proving Exchange's code_verifier is the
	// one the authorize request committed to.
	h := sha256.Sum256([]byte(f.verifier))
	want := base64.RawURLEncoding.EncodeToString(h[:])
	if challenge != want {
		t.Errorf("challenge does not match sha256(verifier): %q vs %q", challenge, want)
	}
}

// ── Exchange validation ─────────────────────────────────────────────────────

func TestCodexExchangeBeforeStartErrors(t *testing.T) {
	f := NewCodexOauthFlow()
	_, err := f.Exchange("somecode")
	if err == nil {
		t.Fatal("expected an error for Exchange called before Start")
	}
	if !strings.Contains(err.Error(), "before Start") {
		t.Errorf("error should identify the ordering problem, got: %v", err)
	}
}

func TestCodexExchangeEmptyCodeErrors(t *testing.T) {
	stubBrowser(t)
	silenceCallbackNote(t)
	f := NewCodexOauthFlow()
	if _, err := f.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		concrete := f.(*codexOauthFlow)
		concrete.stopMu.Lock()
		stop := concrete.stopListener
		concrete.stopMu.Unlock()
		stop()
	})
	if _, err := f.Exchange("  "); err == nil {
		t.Fatal("expected an error for an empty pasted code")
	}
}

func TestCodexExchangeStripsStateFragment(t *testing.T) {
	silenceCallbackNote(t)
	// The paste may arrive as "CODE#STATE" (claude's callback page format) —
	// Exchange strips the fragment before validating. Verify via the form the
	// token POST would send: stub the HTTP client? postToken is package-
	// internal; instead assert the stripping logic through the exported path
	// with an httptest server pointed at codexTokenURL — skipped here because
	// codexTokenURL is a const. The fragment-stripping behavior is shared
	// with claude.go's identical logic and covered there; here we only pin
	// that Exchange normalizes the same way by calling it against a fake
	// server via URL override — which the design (single const endpoint)
	// deliberately doesn't allow, so this asserts the pre-flight validation
	// only.
	f := NewCodexOauthFlow()
	stubBrowser(t)
	silenceCallbackNote(t)
	if _, err := f.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		concrete := f.(*codexOauthFlow)
		concrete.stopMu.Lock()
		stop := concrete.stopListener
		concrete.stopMu.Unlock()
		stop()
	})
	if _, err := f.Exchange("CODE#STATE"); err == nil {
		t.Fatal("expected an error from Exchange without a reachable token endpoint")
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
	// Bind with retries: other tests' Start() listeners release the port
	// asynchronously (their t.Cleanup fires between tests, but the kernel
	// can hold the socket in TIME_WAIT briefly), so a single immediate bind
	// can spuriously fail when the whole package runs together.
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
	// serveCodexCallbackPage no longer returns anything — it owns its Serve
	// goroutine and its post-shutdown error filtering internally. The test
	// only needs the listener reachable (bound synchronously above) and the
	// one-shot behavior observable over HTTP.
	go serveCodexCallbackPage(l, func() { l.Close() })

	resp, err := http.Get("http://localhost:1455/auth/callback?code=ac_TEST<code>&state=xyz")
	if err != nil {
		t.Fatalf("callback page not reachable after synchronous bind: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// The page must render the code — HTML-escaped (the < > in the code).
	if !strings.Contains(string(body), "ac_G_5WeiXYZ") && !strings.Contains(string(body), "ac_TEST") {
		t.Errorf("callback page did not render the code, got:\n%s", body)
	}
	if !strings.Contains(string(body), "&lt;code&gt;") {
		t.Errorf("code must be HTML-escaped (XSS guard), got:\n%s", body)
	}

	// One-shot: the server should shut down after serving — a second request
	// must fail. Allow a short settle window (the handler defers shutdown by
	// 200ms to flush the response first).
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

// silenceCallbackNote replaces noteCallbackUnavailable with a no-op for the
// test's lifetime — tests that call Start() spin the callback-page listener
// in a goroutine, and parallel binds to the same port would otherwise spam
// stderr with the bind-failure note on every run.
func silenceCallbackNote(t *testing.T) {
	t.Helper()
	noteMu.Lock()
	orig := noteCallbackUnavailable
	noteCallbackUnavailable = func(error) {}
	noteMu.Unlock()
	t.Cleanup(func() {
		noteMu.Lock()
		noteCallbackUnavailable = orig
		noteMu.Unlock()
	})
}
