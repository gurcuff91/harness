package oauthflow

// Codex (ChatGPT subscription) OAuth PKCE flow. Obtains tokens directly — no
// Codex CLI install, no ~/.codex/auth.json read. Mirrors claude.go: harness
// masquerades as the Codex CLI via the same PUBLIC PKCE client_id the CLI
// itself uses (app_EMoamEEZ73f0CkXaXp7hrann — public by OAuth design, the
// security rests on PKCE, not on the id being secret), the same localhost
// redirect allowlist entry, and the same originator identity
// (internal/providers/codex_oauth.go's request headers). Zed ships the same
// feature and is publicly sanctioned by OpenAI for it; there is no documented
// third-party contract (openai/codex#36886 asks for one, unanswered) — this
// flow carries the same private-endpoint risk profile claude-oauth already
// accepted.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/types"
)

// Codex (OpenAI/ChatGPT) OAuth constants — the redirect host/port pair and
// the scope are what the Codex CLI itself registers; deviations from the CLI's
// redirect allowlist make auth.openai.com fail the authorize request outright
// (observed: the login page never renders for an unregistered redirect_uri).
const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexAuthURL  = "https://auth.openai.com/oauth/authorize"
	codexTokenURL = "https://auth.openai.com/oauth/token"
	// The Codex CLI's registered localhost callback. The browser shows the
	// authorization code on this page for the user to copy — the same
	// copy/paste UX claude-oauth uses; no local HTTP listener is spawned.
	codexRedirect = "http://localhost:1455/auth/callback"
	// Zed-verified scope list. api.connectors.* is what Codex CLI's own flow
	// requests beyond the openid basics.
	codexOAuthScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"

	// codexDefaultExpiresIn is used when the token endpoint omits expires_in
	// (~1h in practice; the endpoint has returned it so far).
	codexDefaultExpiresIn = 3600
)

// codexOauthFlow implements OauthFlow for Codex. Unexported — callers get it
// as an OauthFlow via NewCodexOauthFlow.
type codexOauthFlow struct {
	verifier string
	state    string

	// stopListener is this flow's OWN kill switch for the callback listener
	// Start() bound — per-flow state, not a package global, because two
	// flows (or a flow plus a test) binding the same port would otherwise
	// overwrite each other's shutdown hook, leaving stale listeners holding
	// the port with nobody able to close them. Guarded by stopMu: the
	// assignment happens on Start's goroutine, the one-shot handler calls
	// it from the HTTP server's, and tests' t.Cleanup may race both.
	stopMu       sync.Mutex
	stopListener func()
}

// NewCodexOauthFlow returns Codex's OAuth PKCE flow. No provider argument —
// the caller already knows it wants Codex. Returns the OauthFlow interface,
// not the concrete type, so callers depend only on the contract.
func NewCodexOauthFlow() OauthFlow {
	return &codexOauthFlow{}
}

// Start generates PKCE, builds Codex's authorization URL, opens the browser,
// and returns the URL. See OauthFlow.Start.
func (f *codexOauthFlow) Start() (string, error) {
	verifier, challenge := generatePKCE()
	f.verifier = verifier
	f.state = randomURLSafe(32)

	q := url.Values{}
	q.Set("client_id", codexClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", codexRedirect)
	q.Set("scope", codexOAuthScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", f.state)
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", "codex_cli_rs")
	authURL := codexAuthURL + "?" + q.Encode()

	// Bind the callback listener SYNCHRONOUSLY BEFORE opening the browser —
	// ordering matters and a goroutine loses the race: returning from a
	// ChatGPT login with an ACTIVE session redirects to localhost:1455 in
	// milliseconds, faster than a freshly spawned goroutine's net.Listen can
	// win it, and the browser then shows an unfriendly "connection refused"
	// (observed live). net.Listen is instant; only Serve() blocks, so the
	// bind happens here and Serve moves to a goroutine.
	l, err := net.Listen("tcp", "localhost:1455")
	if err != nil {
		// Port already in use (another harness instance's listener, or a
		// stale one) — NOT fatal: Exchange accepts the raw code pasted from
		// the browser's address bar, and claude-style copy/paste is the
		// flow's single funnel anyway.
		callNoteCallbackUnavailable(err)
		f.stopListener = func() {} // nothing of OURS to stop
	} else {
		f.stopMu.Lock()
		f.stopListener = func() { l.Close() }
		stop := f.stopListener
		f.stopMu.Unlock()
		// serveCodexCallbackPage launches its own Serve goroutine and handles
		// its own post-shutdown error filtering (post-success noise is
		// silent); nothing to wait on — Start returns immediately with the
		// port already listening.
		go serveCodexCallbackPage(l, stop)
	}

	openBrowser(authURL) // best-effort; caller also prints the URL

	return authURL, nil
}

// serveCodexCallbackPage serves the one-shot callback page on an ALREADY-
// BOUND listener (Start binds it synchronously before opening the browser —
// see the comment there for why the bind can't live inside this goroutine).
// It renders a small page displaying the authorization code the browser
// arrived with — the same "here is your code, copy it" experience Anthropic's
// hosted callback page gives claude-oauth users. One-shot: the first request
// served shuts the server down, so the process never keeps a listener open
// beyond the moment the browser lands. Blocks its own goroutine until that
// first request arrives (or forever, if the user abandons the login —
// harmless: the process exits whenever the harness session does).
func serveCodexCallbackPage(l net.Listener, stopListener func()) error {
	mux := http.NewServeMux()
	var shutdownOnce sync.Once
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, codexCallbackPageHTML, html.EscapeString(code))
		// One-shot: the FIRST request stops the server. Closing the listener
		// immediately after writing the response is safe — the bytes are
		// buffered to the kernel and the response is complete; there's
		// nothing to flush asynchronously. shutdownOnce guards against a
		// second racing request double-closing (a double Close would just
		// error, but the once makes intent explicit).
		shutdownOnce.Do(func() {
			go stopListener()
		})
	})

	srv := &http.Server{Handler: mux}
	// Serve() errors are SILENT here, unconditionally: the BIND already
	// succeeded synchronously in Start (and any bind failure got its note
	// there), so Serve's only possible failures are post-bind noise — an
	// in-flight Accept racing this listener's Close (from the one-shot
	// handler OR from a test's cleanup, both legitimate) surfaces as wrapped
	// "use of closed network connection", never worth a scary note for a
	// listener that either did its job or was deliberately stopped. The
	// copy-from-URL fallback always exists regardless.
	go func() {
		_ = srv.Serve(l)
	}()
	return nil
}

// codexCallbackPageHTML is the single small page the one-shot listener
// serves. Minimal, dark-mode-agnostic styling: shows the code in a
// selectable block plus a Copy button, mirroring Anthropic's own callback
// page's "copy this code" shape so the two providers' login UX feels the
// same. The %s placeholder receives the (HTML-escaped) code.
const codexCallbackPageHTML = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>harness — Codex login</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#1a1a2e;color:#e0e0e0;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0">
  <div style="text-align:center;max-width:560px;padding:32px">
    <h2 style="font-weight:600;margin:0 0 8px">Login successful</h2>
    <p style="color:#9a9a9a;margin:0 0 24px">Copy this authorization code and paste it back into harness:</p>
    <div style="display:flex;gap:8px;justify-content:center;align-items:center;background:#24243a;border-radius:10px;padding:16px 20px">
      <code id="authcode" style="font-size:1.05em;word-break:break-all;user-select:all">%s</code>
      <button onclick="navigator.clipboard.writeText(document.getElementById('authcode').textContent).then(()=>{this.textContent='Copied';setTimeout(()=>{this.textContent='Copy'},1500)})" style="background:#4a4a6a;color:#fff;border:0;border-radius:8px;padding:8px 14px;cursor:pointer;font-size:0.9em">Copy</button>
    </div>
    <p style="color:#6a6a7a;font-size:0.85em;margin-top:24px">You can close this tab after copying.</p>
  </div>
</body>
</html>`

// Exchange swaps the authorization code for credentials. Accepts the raw
// code, "CODE#STATE", or the FULL callback URL the browser lands on (e.g.
// "http://localhost:1455/auth/callback?code=...&state=...") — the last one
// is the friendliest fallback when the one-shot callback page is no longer
// being served (or the user simply copied from the address bar).
//
// The token POST is form-urlencoded (OAuth standard; auth.openai.com rejects
// JSON bodies — unlike Anthropic's token endpoint, which postToken serves).
// The response's id_token JWT payload carries the ChatGPT account id this
// provider must echo as a per-request header; it's base64url-decoded WITHOUT
// signature verification — legitimate because the token arrives directly
// from auth.openai.com over TLS, the same trust every OAuth client places in
// the token endpoint (the id_token's signature is for downstream RPs that
// received it from an untrusted channel, which is not this path).
func (f *codexOauthFlow) Exchange(code string) (*types.Credentials, error) {
	code = normalizePastedCode(code)
	if code == "" {
		return nil, fmt.Errorf("empty authorization code")
	}
	if f.verifier == "" {
		return nil, fmt.Errorf("Exchange called before Start")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", codexClientID)
	form.Set("code", code)
	form.Set("redirect_uri", codexRedirect)
	form.Set("code_verifier", f.verifier)

	req, err := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request (%s): %w", codexTokenURL, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange HTTP %d (%s): %s", resp.StatusCode, codexTokenURL, string(data))
	}

	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		IDToken      string `json:"id_token"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, fmt.Errorf("parse token response (%s): %w", codexTokenURL, err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("%s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token (%s): %s", codexTokenURL, string(data))
	}

	expiresIn := tr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = codexDefaultExpiresIn
	}

	accountID := ExtractChatGPTAccountID(tr.IDToken)
	creds := types.OAuthCredentialsWithAccount(
		tr.AccessToken,
		tr.RefreshToken,
		time.Now().Add(time.Duration(expiresIn)*time.Second).UnixMilli(),
		"", // no subscription type surfaced by this flow
		accountID,
	)
	return &creds, nil
}

// ExtractChatGPTAccountID pulls the ChatGPT account id out of an id_token
// JWT payload, with Zed's three-level fallback chain: a top-level
// chatgpt_account_id claim, then the namespaced auth claim block
// (https://api.openai.com/auth.chatgpt_account_id), then the first
// organization id. "" when none is present — callers that REQUIRE an account
// id treat empty as fatal; the provider does, since the backend rejects
// requests without ChatGPT-Account-ID. Exported (capitalized) because the
// provider side (internal/providers/codex_oauth.go) re-extracts it from each
// refresh response's fresh id_token — the login flow and the token refresh
// both need the same fallback chain, and duplicating it would let the two
// drift apart.
func ExtractChatGPTAccountID(idToken string) string {
	if idToken == "" || strings.Count(idToken, ".") < 2 {
		return ""
	}
	payloadB64 := strings.SplitN(idToken, ".", 3)[1]
	// Go's base64 RawURLEncoding rejects padding; JWTs are unpadded by spec.
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if v, ok := claims["chatgpt_account_id"].(string); ok && v != "" {
		return v
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if v, ok := auth["chatgpt_account_id"].(string); ok && v != "" {
			return v
		}
	}
	if orgs, ok := claims["organizations"].([]any); ok && len(orgs) > 0 {
		if org, ok := orgs[0].(map[string]any); ok {
			if v, ok := org["id"].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

// ── Pasted-code normalization & callback-page plumbing ──────────────────────

// normalizePastedCode accepts any of the three forms the user may paste back
// and returns just the authorization code:
//   - the raw code ("ac_G_...") — already normalized
//   - "CODE#STATE" (claude-style paste)
//   - the full callback URL the browser landed on
//     ("http://localhost:1455/auth/callback?code=...&state=...") — the
//     friendliest fallback when the one-shot callback page is gone
func normalizePastedCode(input string) string {
	s := strings.TrimSpace(input)
	if s == "" {
		return ""
	}
	// Full callback URL — extract ?code=; a URL without one (e.g. an error
	// redirect) is NOT a code, so it normalizes to empty rather than being
	// returned verbatim.
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(u.Query().Get("code"))
	}
	// Raw code possibly with a #STATE suffix.
	if idx := strings.Index(s, "#"); idx != -1 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// noteCallbackUnavailable prints the "callback page not served" note. A
// package var + mu purely so tests can silence it: Start() launches the
// listener from several tests' goroutines — which may outlive the test that
// spawned them (an abandoned login's listener binds fine and sits waiting) —
// so the stub's restore and the goroutine's call can race without a lock.
// Same stubbing pattern as openBrowser, with locking for the goroutine
// lifetime case.
var (
	noteMu                  sync.Mutex
	noteCallbackUnavailable = func(err error) {
		fmt.Fprintf(os.Stderr, "harness: note: could not serve local callback page (%v) — copy the code from the browser URL instead\n", err)
	}
)

func callNoteCallbackUnavailable(err error) {
	noteMu.Lock()
	defer noteMu.Unlock()
	noteCallbackUnavailable(err)
}
