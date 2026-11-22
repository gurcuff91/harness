package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gurcuff91/harness/types"
)

// The quota cache: process-global and per provider, shared by every reader in
// the process (GET /api/providers/{name}/quota, a channel's /info through
// agent.SessionView) — they share one account, hence one rate limit.

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

// ForgetQuota drops a provider's cached reading — on connect/disconnect, so a
// reading of the previous account is never served for the new one.
func ForgetQuota(name string) {
	quotaCache.mu.Lock()
	delete(quotaCache.entries, name)
	quotaCache.mu.Unlock()
}

// read returns the reading to serve: fresh from the cache, a new one, or —
// when the provider cannot be read — the last good one marked stale. err is
// returned only when there is no reading at all; retry is the Retry-After to
// advertise with it.
func (e *quotaEntry) read(ctx context.Context, r QuotaReporter, refresh bool, now time.Time) (q *types.Quota, retry time.Duration, err error) {
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
	if errors.Is(err, ErrNoUpstream) || errors.Is(err, ErrNoQuota) {
		// Not a failure to remember: the account changed state. Nothing stale
		// may be served for it.
		e.good, e.lastErr, e.lastAttempt = nil, nil, time.Time{}
		return nil, 0, err
	}
	e.lastErr = err
	var he *QuotaHTTPError
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

// CachedQuota returns provider name's quota through the cache: a fresh reading,
// a new one, or the last good one marked stale. retry is the Retry-After to
// advertise. Errors: ErrProviderNotFound (wrapped) for an unknown provider,
// ErrNoQuota for a provider that does not report one (or an account without
// one), ErrNoUpstream when not connected, else the provider's failure.
func CachedQuota(ctx context.Context, name string, refresh bool) (q *types.Quota, retry time.Duration, err error) {
	p := Find(name)
	if p == nil {
		return nil, 0, fmt.Errorf("%w: %q", ErrProviderNotFound, name)
	}
	r, ok := p.(QuotaReporter)
	if !ok {
		return nil, 0, ErrNoQuota
	}
	return quotaEntryFor(name).read(ctx, r, refresh, time.Now())
}

// ErrProviderNotFound: no provider by that name.
var ErrProviderNotFound = errors.New("provider not found")

// SetQuotaRulesForTest shortens the cache rules; returns the restore func.
func SetQuotaRulesForTest(ttl, minInterval, backoff429 time.Duration) (restore func()) {
	pt, pm, pb := quotaTTL, quotaMinInterval, quotaBackoff429
	quotaTTL, quotaMinInterval, quotaBackoff429 = ttl, minInterval, backoff429
	return func() { quotaTTL, quotaMinInterval, quotaBackoff429 = pt, pm, pb }
}
