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
// as an OauthFlow via NewCodexOauthFlow. Unlike claudeOauthFlow, it DOES
// carry per-instance state: not PKCE state (that's stateless too — Start
// returns the verifier instead of storing it), but the local callback
// listener's shutdown hook, which must live somewhere for as long as the
// listener itself is bound.
type codexOauthFlow struct {
	// stopMu/stopListener guard the callback listener Start() binds — kept
	// per-flow (not a package global) because two flows (or a flow plus a
	// test) binding the same port would otherwise stomp each other's
	// shutdown hook, leaking a listener nobody can close. Three things can
	// call stopListener: the one-shot callback handler (success path), the
	// codexListenerTimeout timer (abandoned-login path), and a test's
	// cleanup — shutdownOnce inside serveCodexCallbackPage makes the actual
	// listener.Close() idempotent regardless of which fires first.
	stopMu       sync.Mutex
	stopListener func()
}

// codexListenerTimeout bounds how long the local callback listener stays
// bound if the browser never redirects back (abandoned login, closed tab,
// network hiccup). Without this, a forgotten login would hold port 1455
// open for the lifetime of the parent server process. A package var (not a
// const) so tests can shrink it instead of waiting 5 real minutes.
var codexListenerTimeout = 5 * time.Minute

// NewCodexOauthFlow returns Codex's OAuth PKCE flow. No provider argument —
// the caller already knows it wants Codex. Returns the OauthFlow interface,
// not the concrete type, so callers depend only on the contract.
func NewCodexOauthFlow() OauthFlow {
	return &codexOauthFlow{}
}

// Start generates PKCE, builds Codex's authorization URL, and binds the
// local callback listener. Does not open a browser — see OauthFlow.Start's
// doc comment. Returns the URL and the verifier the caller must pass back
// to Exchange.
//
// Unlike the pre-HTTP-API version of this flow, a port-1455 bind failure is
// now a HARD error (not a silent degrade-to-"copy from the address bar"):
// stateless callers only get the auth_url back, with nowhere else to
// display the code if the listener never starts, so failing loudly here —
// "a login is already in progress" — is more honest than returning a URL
// whose promised callback page silently never renders.
func (f *codexOauthFlow) Start() (authURL, verifierCode string, err error) {
	verifier, challenge := generatePKCE()
	state := randomURLSafe(32)

	q := url.Values{}
	q.Set("client_id", codexClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", codexRedirect)
	q.Set("scope", codexOAuthScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", "codex_cli_rs")
	authURL = codexAuthURL + "?" + q.Encode()

	// Bind the callback listener SYNCHRONOUSLY — ordering matters and a
	// goroutine loses the race: returning from a ChatGPT login with an
	// ACTIVE session redirects to localhost:1455 in milliseconds, faster
	// than a freshly spawned goroutine's net.Listen can win it, and the
	// browser then shows an unfriendly "connection refused" (observed
	// live). net.Listen is instant; only Serve() blocks, so the bind
	// happens here and Serve moves to a goroutine.
	l, err := net.Listen("tcp", "localhost:1455")
	if err != nil {
		return "", "", fmt.Errorf("port 1455 already in use — a login is already in progress: %w", err)
	}

	f.stopMu.Lock()
	f.stopListener = func() { l.Close() }
	stop := f.stopListener
	f.stopMu.Unlock()

	// Auto-shutdown safety net: if the callback never arrives (abandoned
	// login), don't hold the port forever. serveCodexCallbackPage's
	// shutdownOnce makes this safe to race against the real callback
	// firing first — whichever happens first wins, the other is a no-op.
	timer := time.AfterFunc(codexListenerTimeout, stop)

	// serveCodexCallbackPage launches its own Serve goroutine and handles
	// its own post-shutdown error filtering (post-success noise is
	// silent); nothing to wait on — Start returns immediately with the
	// port already listening. It also stops the timer once the real
	// callback arrives, so the timer doesn't fire pointlessly later.
	go serveCodexCallbackPage(l, stop, timer)

	return authURL, verifier, nil
}

// serveCodexCallbackPage serves the one-shot callback page on an ALREADY-
// BOUND listener (Start binds it synchronously — see the comment there for
// why the bind can't live inside this goroutine). It renders a small page
// displaying the authorization code the browser arrived with — the same
// "here is your code, copy it" experience Anthropic's hosted callback page
// gives claude-oauth users. One-shot: the first request served shuts the
// server down (and cancels the abandoned-login timer), so the process
// never keeps a listener open beyond the moment the browser lands or the
// timeout elapses. Blocks its own goroutine until the callback arrives (or
// the timer fires) — harmless either way: the process exits whenever the
// harness session does regardless.
func serveCodexCallbackPage(l net.Listener, stopListener func(), timeoutTimer *time.Timer) error {
	mux := http.NewServeMux()
	var shutdownOnce sync.Once
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, codexCallbackPageHTML, html.EscapeString(code))
		// One-shot: the FIRST request stops the server and cancels the
		// abandoned-login timer (the real callback arrived, so the timer's
		// job is moot). Closing the listener immediately after writing the
		// response is safe — the bytes are buffered to the kernel and the
		// response is complete; there's nothing to flush asynchronously.
		// shutdownOnce guards against a second racing request
		// double-closing (a double Close would just error, but the once
		// makes intent explicit).
		shutdownOnce.Do(func() {
			timeoutTimer.Stop()
			go stopListener()
		})
	})

	srv := &http.Server{Handler: mux}
	// Serve() errors are SILENT here, unconditionally: the BIND already
	// succeeded synchronously in Start, so Serve's only possible failures
	// are post-bind noise — an in-flight Accept racing this listener's
	// Close (from the one-shot handler, the timeout timer, OR a test's
	// cleanup, all legitimate) surfaces as wrapped "use of closed network
	// connection", never worth a scary note for a listener that either did
	// its job or was deliberately stopped.
	go func() {
		_ = srv.Serve(l)
	}()
	return nil
}

// codexCallbackPageHTML is the single small page the one-shot listener
// serves. Deliberately cloned from Anthropic's own hosted callback page —
// pulled by inspecting its live computed styles (light background
// rgb(252,252,251), text rgb(11,11,11), 22px/500-weight heading, 14px
// secondary text in rgb(82,81,78), a bordered/rounded code box, and a
// borderless "Copy code" button with a clipboard glyph) so the two
// providers' login UX reads as the same product rather than two
// implementations bolted together. The %s placeholder receives the
// (HTML-escaped) code.
const codexCallbackPageHTML = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Authentication code — harness</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;background:rgb(252,252,251);color:rgb(11,11,11);display:flex;flex-direction:column;align-items:center;justify-content:center;min-height:100vh;margin:0;text-align:center">
  <div style="max-width:896px;padding:0 16px">
    <h1 style="font-weight:500;font-size:22px;margin:0 0 16px">Authentication code</h1>
    <p style="color:rgb(82,81,78);font-size:14px;margin:0 0 16px">Paste this into harness:</p>
    <pre id="authcode" style="background:rgb(252,252,251);border:1px solid rgba(11,11,11,0.1);border-radius:12px;padding:12px;font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,'Liberation Mono','Courier New',monospace;font-size:14px;color:rgb(82,81,78);word-break:break-all;white-space:pre-wrap;user-select:all;margin:0 0 16px;cursor:pointer">%s</pre>
    <button onclick="navigator.clipboard.writeText(document.getElementById('authcode').textContent).then(()=>{document.getElementById('copylabel').textContent='Copied'; setTimeout(()=>{document.getElementById('copylabel').textContent='Copy code'},1500)})" style="background:none;border:0;border-radius:8px;color:rgb(11,11,11);cursor:pointer;font-size:14px;display:inline-flex;align-items:center;gap:6px;padding:0 12px;height:32px">
      <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="8" y="2" width="8" height="4" rx="1" ry="1"></rect><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"></path></svg>
      <span id="copylabel">Copy code</span>
    </button>
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
func (f *codexOauthFlow) Exchange(code, verifierCode string) (*types.Credentials, error) {
	code = normalizePastedCode(code)
	if code == "" {
		return nil, fmt.Errorf("empty authorization code")
	}
	if verifierCode == "" {
		return nil, fmt.Errorf("verifierCode is required (pass the value Start returned)")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", codexClientID)
	form.Set("code", code)
	form.Set("redirect_uri", codexRedirect)
	form.Set("code_verifier", verifierCode)

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


