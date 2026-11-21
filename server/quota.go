package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// Quota cache rules (GET /api/providers/{name}/quota). Variables so tests can
// shorten them.
var (
	// quotaTTL is how long a good reading is served without asking again.
	quotaTTL = 60 * time.Second
	// quotaMinInterval is the least time between two calls to a provider's
	// usage endpoint, ?refresh=true included: usage endpoints rate-limit
	// (claude-oauth answers 429 when polled, ollama allows 10 a minute).
	quotaMinInterval = 5 * time.Second
	// quotaBackoff429 is the pause after a 429 that carried no Retry-After.
	quotaBackoff429 = 60 * time.Second
)

// quotaEntry is one provider's cached reading. mu serializes the calls to the
// provider (a burst of requests makes one call, the rest wait for it).
type quotaEntry struct {
	mu          sync.Mutex
	good        *types.Quota // last good reading, nil before the first
	goodAt      time.Time
	lastAttempt time.Time
	lastErr     error     // error of the last attempt, nil if it succeeded
	retryAfter  time.Time // no call before this (429 / Retry-After)
}

// quotaCache is process-global, like the provider registry it caches: two
// servers in one process share one account, hence one rate limit.
var quotaCache = struct {
	mu      sync.Mutex
	entries map[string]*quotaEntry
}{entries: map[string]*quotaEntry{}}

func quotaEntryFor(name string) *quotaEntry {
	quotaCache.mu.Lock()
	defer quotaCache.mu.Unlock()
	e := quotaCache.entries[name]
	if e == nil {
		e = &quotaEntry{}
		quotaCache.entries[name] = e
	}
	return e
}

// forgetQuota drops a provider's cached reading — on connect/disconnect, so a
// reading of the previous account is never served for the new one.
func forgetQuota(name string) {
	quotaCache.mu.Lock()
	delete(quotaCache.entries, name)
	quotaCache.mu.Unlock()
}

// read returns the reading to serve: fresh from the cache, a new one, or —
// when the provider cannot be read — the last good one marked stale. err is
// returned only when there is no reading at all; retry is the Retry-After to
// advertise with it.
func (e *quotaEntry) read(ctx context.Context, r providers.QuotaReporter, refresh bool, now time.Time) (q *types.Quota, retry time.Duration, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	fresh := e.good != nil && now.Sub(e.goodAt) < quotaTTL
	throttled := !e.lastAttempt.IsZero() && now.Sub(e.lastAttempt) < quotaMinInterval
	blocked := now.Before(e.retryAfter)
	if fresh && (!refresh || throttled || blocked) {
		return e.good, 0, nil
	}
	if throttled || blocked {
		return e.fallback(now)
	}

	e.lastAttempt = now
	got, err := r.Quota(ctx)
	if err == nil {
		e.good, e.goodAt, e.lastErr, e.retryAfter = got, now, nil, time.Time{}
		return got, 0, nil
	}
	if errors.Is(err, providers.ErrNoUpstream) || errors.Is(err, providers.ErrNoQuota) {
		// Not a failure to remember: the account changed state. Nothing stale
		// may be served for it.
		e.good, e.lastErr, e.lastAttempt = nil, nil, time.Time{}
		return nil, 0, err
	}
	e.lastErr = err
	var he *providers.QuotaHTTPError
	if errors.As(err, &he) {
		switch {
		case he.RetryAfter > 0:
			e.retryAfter = now.Add(he.RetryAfter)
		case he.Status == http.StatusTooManyRequests:
			e.retryAfter = now.Add(quotaBackoff429)
		}
	}
	return e.fallback(now)
}

// fallback serves the last good reading as stale, or the last error.
func (e *quotaEntry) fallback(now time.Time) (*types.Quota, time.Duration, error) {
	var retry time.Duration
	if now.Before(e.retryAfter) {
		retry = e.retryAfter.Sub(now)
	}
	if e.good != nil {
		stale := *e.good
		stale.Stale = true
		return &stale, retry, nil
	}
	if e.lastErr == nil {
		return nil, retry, fmt.Errorf("usage endpoint not called yet")
	}
	return nil, retry, e.lastErr
}

// handleProviderQuota serves GET /api/providers/{name}/quota[?refresh=true]:
// the provider's subscription usage in one format (types.Quota), cached for
// quotaTTL. 404 unknown provider or one without quota, 409 not connected, 502
// the provider could not be read and there is no earlier reading.
func (s *Server) handleProviderQuota(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p := providers.Find(name)
	if p == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q not found", name), nil)
		return
	}
	reporter, ok := p.(providers.QuotaReporter)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q does not report a quota", name), nil)
		return
	}
	refresh, _ := strconv.ParseBool(r.URL.Query().Get("refresh"))
	q, retry, err := quotaEntryFor(name).read(r.Context(), reporter, refresh, time.Now())
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((retry+time.Second-1)/time.Second)))
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, q)
	case errors.Is(err, providers.ErrNoUpstream):
		writeError(w, http.StatusConflict, fmt.Sprintf("provider %q is not connected (or has no valid credentials): connect it first", name), nil)
	case errors.Is(err, providers.ErrNoQuota):
		writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q has no quota for this account", name), nil)
	default:
		writeError(w, http.StatusBadGateway, err.Error(), nil)
	}
}
