package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/internal/providers/llm"
)

// DefaultProxyMaxBodyBytes caps a proxied request body (ServerOptions.
// ProxyMaxBodyBytes overrides it). Generous on purpose: a base64 image or a
// large diff must fit.
const DefaultProxyMaxBodyBytes = 32 << 20

// proxyIdleTimeout aborts a proxied response that delivers no bytes for this
// long — the same watchdog a session's own stream has. A var so tests can
// lower it.
var proxyIdleTimeout = llm.StreamIdleTimeout

// maxRewrittenResponseBytes bounds a non-streaming response held in memory to
// be rewritten (only claude-oauth rewrites responses).
const maxRewrittenResponseBytes = 64 << 20

// handleProviderProxy is a TRANSPARENT proxy to a provider's native API:
//
//	ANY /api/providers/{name}/proxy/<path>  →  <provider native base URL>/<path>
//
// A client speaks the provider's own dialect (Anthropic Messages, OpenAI Chat
// Completions, Codex Responses…) — an official vendor SDK works by pointing its
// base URL at /api/providers/{name}/proxy — and harness supplies what the
// client must not hold: the credentials (OAuth refresh included) and identity
// headers. The request body and the response (SSE streams included, flushed as
// they arrive) pass through untouched, with one exception: claude-oauth, whose
// backend only accepts Claude Code-shaped requests, gets its body adapted (see
// providers.Upstream.Rewrite).
//
// Nothing is created: no session, no agent, nothing persisted. Whatever the
// caller omits (tools, a system prompt, max_tokens) is simply absent; the
// provider answers with its own error where its API requires it.
//
// Harness-originated failures use harness's error shape: 404 unknown provider,
// 409 provider not usable (not connected), 403 path/method outside the
// provider's allowlist, 400 unusable body, 413 oversize body, 502 upstream
// unreachable. Everything the upstream answers — 4xx/5xx included — is passed
// through as-is.
func (s *Server) handleProviderProxy(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p := providers.Find(name)
	if p == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q not found", name), nil)
		return
	}

	rel := strings.TrimPrefix(r.URL.EscapedPath(), "/api/providers/"+url.PathEscape(name)+"/proxy")
	path := providers.CleanProxyPath(rel)
	if path == "" {
		writeError(w, http.StatusBadRequest, "invalid proxy path", nil)
		return
	}

	up, err := p.Upstream()
	if err != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("provider %q is not connected (or has no valid credentials): connect it first", name), nil)
		return
	}
	if up.Allowed == nil || !up.Allowed(r.Method, path) {
		writeError(w, http.StatusForbidden, fmt.Sprintf("%s %s is not allowed through the %q proxy", r.Method, path, name), nil)
		return
	}

	// Body: read whole (bounded) — needed to size-check deterministically and,
	// for claude-oauth, to rewrite.
	var body []byte
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		limit := s.proxyMaxBody
		if limit <= 0 {
			limit = DefaultProxyMaxBodyBytes
		}
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds the %d byte proxy limit", limit), nil)
			} else {
				writeError(w, http.StatusBadRequest, "reading request body: "+err.Error(), nil)
			}
			return
		}
	}

	identity := make(map[string]string, len(up.Headers)+2)
	for k, v := range up.Headers {
		identity[k] = v
	}
	if up.Rewrite != nil && len(body) > 0 {
		body, err = up.Rewrite(path, body, identity)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
			return
		}
	}

	// Upstream request bound to the caller's context (client gone → upstream
	// cancelled) plus the idle watchdog's cancel.
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	target := up.BaseURL + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	out, err := http.NewRequestWithContext(ctx, r.Method, target, bodyReader)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid proxy request", nil)
		return
	}
	forwardRequestHeaders(out.Header, r.Header, identity, up.Defaults)

	client := up.Client
	if client == nil {
		client = llm.NewProviderClient(0)
	}
	resp, err := client.Do(out)
	if err != nil {
		if r.Context().Err() != nil {
			return // the caller left
		}
		// err mentions the target URL and transport failure, never a header.
		s.logger.Warn("server", "proxy_upstream_error", "provider", name, "path", path, "err", err.Error())
		writeError(w, http.StatusBadGateway, fmt.Sprintf("provider %q unreachable", name), nil)
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header, up.RewriteResponse != nil)
	w.WriteHeader(resp.StatusCode)

	idle := newIdleWatchdog(proxyIdleTimeout, func() { cancel(errProxyIdle) })
	defer idle.Stop()
	var streamErr error
	switch ct := resp.Header.Get("Content-Type"); {
	case up.RewriteResponse != nil && strings.HasPrefix(ct, "text/event-stream"):
		streamErr = copySSERewritten(w, resp.Body, idle, up.RewriteResponse)
	case up.RewriteResponse != nil && strings.HasPrefix(ct, "application/json"):
		streamErr = copyJSONRewritten(w, resp.Body, idle, up.RewriteResponse)
	default:
		streamErr = copyFlushing(w, resp.Body, idle)
	}
	if streamErr != nil && r.Context().Err() == nil {
		s.logger.Warn("server", "proxy_stream_error", "provider", name, "path", path, "err", streamErr.Error())
		// Headers are gone, so the status can't change. Abort the connection
		// instead of ending cleanly: a client must see a broken stream, not a
		// truncated one that looks complete.
		panic(http.ErrAbortHandler)
	}
}

var errProxyIdle = errors.New("upstream stream idle")

// ── headers ──────────────────────────────────────────────────────────────

var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

// dropFromRequest are the caller's headers never forwarded: credentials (the
// provider's own are applied instead), the connection's own framing, and the
// browser/proxy provenance a vendor API may refuse or log.
var dropFromRequest = map[string]bool{
	"Host": true, "Content-Length": true, "Accept-Encoding": true,
	"Authorization": true, "X-Api-Key": true, "Api-Key": true, "Cookie": true,
	"Chatgpt-Account-Id": true,
	"Origin":             true, "Referer": true,
	"Forwarded": true, "X-Forwarded-For": true, "X-Forwarded-Host": true,
	"X-Forwarded-Proto": true, "X-Real-Ip": true,
}

// forwardRequestHeaders builds the upstream request's headers: the caller's,
// minus credentials and framing; then Defaults for what the caller left unset;
// then the provider's identity headers, which always win. anthropic-beta is
// MERGED (union of tokens): the caller opts into betas, the provider requires
// its own.
func forwardRequestHeaders(dst, src http.Header, identity, defaults map[string]string) {
	conn := connectionTokens(src)
	for k, vs := range src {
		ck := http.CanonicalHeaderKey(k)
		if hopByHop[ck] || dropFromRequest[ck] || conn[ck] || strings.HasPrefix(ck, "Sec-") {
			continue
		}
		for _, v := range vs {
			dst.Add(ck, v)
		}
	}
	for k, v := range defaults {
		if dst.Get(k) == "" {
			dst.Set(k, v)
		}
	}
	for k, v := range identity {
		if http.CanonicalHeaderKey(k) == "Anthropic-Beta" {
			if merged := mergeBetas(dst.Get(k), v); merged != "" {
				dst.Set(k, merged)
			}
			continue
		}
		dst.Set(k, v)
	}
}

func mergeBetas(a, b string) string {
	seen := map[string]bool{}
	var out []string
	for _, list := range []string{a, b} {
		for _, t := range strings.Split(list, ",") {
			if t = strings.TrimSpace(t); t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return strings.Join(out, ",")
}

func connectionTokens(h http.Header) map[string]bool {
	m := map[string]bool{}
	for _, v := range h.Values("Connection") {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				m[http.CanonicalHeaderKey(t)] = true
			}
		}
	}
	return m
}

// copyResponseHeaders copies the upstream's headers (rate limits, request ids
// stay useful to the caller), minus framing, cookies and CORS (harness's own
// CORS middleware speaks for this server). The body length is dropped when the
// body will be rewritten.
func copyResponseHeaders(dst, src http.Header, rewriting bool) {
	conn := connectionTokens(src)
	for k, vs := range src {
		ck := http.CanonicalHeaderKey(k)
		if hopByHop[ck] || conn[ck] || ck == "Set-Cookie" || strings.HasPrefix(ck, "Access-Control-") {
			continue
		}
		if rewriting && (ck == "Content-Length" || ck == "Content-Encoding") {
			continue
		}
		for _, v := range vs {
			dst.Add(ck, v)
		}
	}
	// A stream must not be buffered by an intermediary.
	if strings.HasPrefix(src.Get("Content-Type"), "text/event-stream") {
		dst.Set("Cache-Control", "no-cache")
	}
}

// ── body copying ─────────────────────────────────────────────────────────

// idleWatchdog fires fire() when nothing was read for d; touch() re-arms it.
type idleWatchdog struct {
	mu sync.Mutex
	t  *time.Timer
	d  time.Duration
}

func newIdleWatchdog(d time.Duration, fire func()) *idleWatchdog {
	return &idleWatchdog{d: d, t: time.AfterFunc(d, fire)}
}
func (w *idleWatchdog) touch() { w.mu.Lock(); w.t.Reset(w.d); w.mu.Unlock() }
func (w *idleWatchdog) Stop()  { w.mu.Lock(); w.t.Stop(); w.mu.Unlock() }

// copyFlushing streams src to w, flushing after every chunk so SSE events
// reach the caller as they arrive. A write error means the caller left.
func copyFlushing(w http.ResponseWriter, src io.Reader, idle *idleWatchdog) error {
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			idle.touch()
			if _, werr := w.Write(buf[:n]); werr != nil {
				return nil
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// copySSERewritten streams an event stream line by line, passing the payload
// of every "data:" line through rewrite. Lines are flushed as they complete.
func copySSERewritten(w http.ResponseWriter, src io.Reader, idle *idleWatchdog, rewrite func([]byte) []byte) error {
	fl, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(src, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			idle.touch()
			if payload, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				trimmed := bytes.TrimRight(payload, "\r\n")
				lead := trimmed[:len(trimmed)-len(bytes.TrimLeft(trimmed, " "))]
				body := bytes.TrimLeft(trimmed, " ")
				if len(body) > 0 && body[0] == '{' {
					nl := append(append(append([]byte("data:"), lead...), rewrite(body)...), payload[len(trimmed):]...)
					line = nl
				}
			}
			if _, werr := w.Write(line); werr != nil {
				return nil
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// copyJSONRewritten buffers a (bounded) JSON response and passes it through
// rewrite.
func copyJSONRewritten(w http.ResponseWriter, src io.Reader, idle *idleWatchdog, rewrite func([]byte) []byte) error {
	data, err := io.ReadAll(io.LimitReader(src, maxRewrittenResponseBytes))
	if err != nil {
		return err
	}
	idle.touch()
	_, _ = w.Write(rewrite(data))
	return nil
}
