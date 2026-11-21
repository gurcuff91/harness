package server

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// quotaProvider is a provider whose Quota answers what the test sets.
type quotaProvider struct {
	*toggleProvider
	mu    sync.Mutex
	calls atomic.Int32
	next  func() (*types.Quota, error)
}

func (p *quotaProvider) Quota(context.Context) (*types.Quota, error) {
	p.calls.Add(1)
	p.mu.Lock()
	f := p.next
	p.mu.Unlock()
	return f()
}

func (p *quotaProvider) answer(f func() (*types.Quota, error)) {
	p.mu.Lock()
	p.next = f
	p.mu.Unlock()
}

func okQuota(used float64) func() (*types.Quota, error) {
	return func() (*types.Quota, error) {
		return &types.Quota{Provider: "q", FetchedAt: time.Now().UTC(), Windows: []types.QuotaWindow{{Name: "weekly", Label: "Weekly", UsedPercent: used}}}, nil
	}
}

func quotaServer(t *testing.T, ps ...providers.Provider) *client.Client {
	c, _ := quotaServerURL(t, ps...)
	return c
}

func quotaServerURL(t *testing.T, ps ...providers.Provider) (*client.Client, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	t.Cleanup(providers.SetForTest(ps))
	for _, p := range ps {
		forgetQuota(p.Name())
		name := p.Name()
		t.Cleanup(func() { forgetQuota(name) })
	}
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return client.New("http://" + srv.Addr()), "http://" + srv.Addr()
}

// shortQuotaRules shrinks the cache rules for a test.
func shortQuotaRules(t *testing.T, ttl, minInterval, backoff time.Duration) {
	t.Helper()
	pt, pm, pb := quotaTTL, quotaMinInterval, quotaBackoff429
	quotaTTL, quotaMinInterval, quotaBackoff429 = ttl, minInterval, backoff
	t.Cleanup(func() { quotaTTL, quotaMinInterval, quotaBackoff429 = pt, pm, pb })
}

func TestQuotaEndpointStatusCodes(t *testing.T) {
	q := &quotaProvider{toggleProvider: newToggle("q")}
	plain := newToggle("plain")
	c := quotaServer(t, q, plain)

	if _, err := c.ProviderQuota("nope", false); statusOf(err) != http.StatusNotFound {
		t.Fatal("unknown provider must be 404")
	}
	if _, err := c.ProviderQuota("plain", false); statusOf(err) != http.StatusNotFound {
		t.Fatal("a provider without QuotaReporter must be 404")
	}
	q.answer(func() (*types.Quota, error) { return nil, providers.ErrNoUpstream })
	if _, err := c.ProviderQuota("q", false); statusOf(err) != http.StatusConflict {
		t.Fatal("not connected must be 409")
	}
	q.answer(func() (*types.Quota, error) { return nil, providers.ErrNoQuota })
	if _, err := c.ProviderQuota("q", false); statusOf(err) != http.StatusNotFound {
		t.Fatal("an account without quota must be 404")
	}
	q.answer(func() (*types.Quota, error) { return nil, &providers.QuotaHTTPError{Provider: "q", Status: 500} })
	if _, err := c.ProviderQuota("q", false); statusOf(err) != http.StatusBadGateway {
		t.Fatal("a failure with no earlier reading must be 502")
	}
}

func TestQuotaEndpointCachesAndRefreshes(t *testing.T) {
	shortQuotaRules(t, time.Hour, 100*time.Millisecond, time.Hour)
	q := &quotaProvider{toggleProvider: newToggle("q")}
	q.answer(okQuota(10))
	c := quotaServer(t, q)

	got, err := c.ProviderQuota("q", false)
	if err != nil || got.Windows[0].UsedPercent != 10 || got.Stale {
		t.Fatalf("got %+v, %v", got, err)
	}
	q.answer(okQuota(20))
	got, _ = c.ProviderQuota("q", false)
	if got.Windows[0].UsedPercent != 10 || q.calls.Load() != 1 {
		t.Fatalf("within the TTL the cached reading is served: %+v, calls=%d", got, q.calls.Load())
	}
	// refresh within the minimum interval: still the cache, no call.
	got, _ = c.ProviderQuota("q", true)
	if got.Windows[0].UsedPercent != 10 || got.Stale || q.calls.Load() != 1 {
		t.Fatalf("refresh inside the 5 s floor must not call, and a fresh reading is not stale: %+v, calls=%d", got, q.calls.Load())
	}
	time.Sleep(120 * time.Millisecond)
	got, _ = c.ProviderQuota("q", true)
	if got.Windows[0].UsedPercent != 20 || q.calls.Load() != 2 {
		t.Fatalf("refresh after the floor must call: %+v, calls=%d", got, q.calls.Load())
	}
}

func TestQuotaEndpointServesStaleOnFailureAndHonorsRetryAfter(t *testing.T) {
	shortQuotaRules(t, 50*time.Millisecond, time.Millisecond, time.Hour)
	q := &quotaProvider{toggleProvider: newToggle("q")}
	q.answer(okQuota(30))
	c := quotaServer(t, q)
	if _, err := c.ProviderQuota("q", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond) // past the TTL
	q.answer(func() (*types.Quota, error) {
		return nil, &providers.QuotaHTTPError{Provider: "q", Status: 429, RetryAfter: 30 * time.Second}
	})
	got, err := c.ProviderQuota("q", false)
	if err != nil || !got.Stale || got.Windows[0].UsedPercent != 30 {
		t.Fatalf("a 429 with an earlier reading must serve it stale: %+v, %v", got, err)
	}
	calls := q.calls.Load()
	time.Sleep(5 * time.Millisecond)
	got, err = c.ProviderQuota("q", true)
	if err != nil || !got.Stale || q.calls.Load() != calls {
		t.Fatalf("inside Retry-After nothing is called, even with refresh: %+v, %v, calls %d→%d", got, err, calls, q.calls.Load())
	}
}

// A 429 with no reading at all is a 502 carrying the Retry-After header.
func TestQuotaEndpointRetryAfterHeaderWithoutReading(t *testing.T) {
	shortQuotaRules(t, time.Minute, time.Millisecond, 90*time.Second)
	q := &quotaProvider{toggleProvider: newToggle("q")}
	q.answer(func() (*types.Quota, error) { return nil, &providers.QuotaHTTPError{Provider: "q", Status: 429} })
	c, base := quotaServerURL(t, q)
	resp, err := http.Get(base + "/api/providers/q/quota")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("Retry-After") != "90" {
		t.Fatalf("status %d Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	// The next call inside the backoff does not reach the provider.
	time.Sleep(5 * time.Millisecond)
	if _, err := c.ProviderQuota("q", true); statusOf(err) != http.StatusBadGateway || q.calls.Load() != 1 {
		t.Fatalf("calls = %d", q.calls.Load())
	}
}

// Disconnecting (ErrNoUpstream) drops the earlier reading: nothing of a
// previous account is ever served stale.
func TestQuotaEndpointNeverServesStaleAfterDisconnect(t *testing.T) {
	shortQuotaRules(t, time.Millisecond, time.Millisecond, time.Hour)
	q := &quotaProvider{toggleProvider: newToggle("q")}
	q.answer(okQuota(40))
	c := quotaServer(t, q)
	if _, err := c.ProviderQuota("q", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	q.answer(func() (*types.Quota, error) { return nil, providers.ErrNoUpstream })
	if _, err := c.ProviderQuota("q", false); statusOf(err) != http.StatusConflict {
		t.Fatal("want 409")
	}
	time.Sleep(5 * time.Millisecond)
	q.answer(func() (*types.Quota, error) { return nil, &providers.QuotaHTTPError{Provider: "q", Status: 500} })
	if _, err := c.ProviderQuota("q", false); statusOf(err) != http.StatusBadGateway {
		t.Fatal("the reading before the disconnect must not come back as stale")
	}
}

// A burst of concurrent requests makes ONE provider call.
func TestQuotaEndpointCoalescesConcurrentRequests(t *testing.T) {
	q := &quotaProvider{toggleProvider: newToggle("q")}
	q.answer(func() (*types.Quota, error) {
		time.Sleep(50 * time.Millisecond)
		return okQuota(5)()
	})
	c := quotaServer(t, q)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ProviderQuota("q", false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := q.calls.Load(); n != 1 {
		t.Fatalf("provider calls = %d, want 1", n)
	}
}

// The 5 s floor holds on its own, also when there is no fresh reading: a
// failure is not retried at every request (no Retry-After involved).
func TestQuotaEndpointMinIntervalAppliesToFailures(t *testing.T) {
	shortQuotaRules(t, time.Hour, time.Hour, time.Millisecond)
	q := &quotaProvider{toggleProvider: newToggle("q")}
	q.answer(func() (*types.Quota, error) { return nil, &providers.QuotaHTTPError{Provider: "q", Status: 500} })
	c := quotaServer(t, q)
	for range 3 {
		if _, err := c.ProviderQuota("q", true); statusOf(err) != http.StatusBadGateway {
			t.Fatal("want 502")
		}
	}
	if n := q.calls.Load(); n != 1 {
		t.Fatalf("provider calls = %d, want 1 inside the floor", n)
	}
}

// Retry-After holds on its own, past the 5 s floor, and also over a fresh
// reading that refresh=true asks to replace.
func TestQuotaEndpointRetryAfterBlocksRefreshOfAFreshReading(t *testing.T) {
	shortQuotaRules(t, time.Hour, time.Millisecond, time.Hour)
	q := &quotaProvider{toggleProvider: newToggle("q")}
	q.answer(okQuota(10))
	c := quotaServer(t, q)
	if _, err := c.ProviderQuota("q", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	q.answer(func() (*types.Quota, error) { return nil, &providers.QuotaHTTPError{Provider: "q", Status: 429} })
	if got, err := c.ProviderQuota("q", true); err != nil || !got.Stale {
		t.Fatalf("got %+v, %v", got, err)
	}
	time.Sleep(5 * time.Millisecond)
	got, err := c.ProviderQuota("q", true)
	if err != nil || got.Windows[0].UsedPercent != 10 || got.Stale || q.calls.Load() != 2 {
		t.Fatalf("a refresh inside the 429 backoff must not call, and the fresh reading is not stale: %+v, %v, calls=%d", got, err, q.calls.Load())
	}
}

// Disconnecting through the API drops the cached reading at once.
func TestQuotaEndpointForgetsOnDisconnect(t *testing.T) {
	shortQuotaRules(t, time.Hour, time.Millisecond, time.Hour)
	q := &quotaProvider{toggleProvider: newToggle("q")}
	q.answer(okQuota(10))
	c := quotaServer(t, q)
	if _, err := c.ProviderQuota("q", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DisconnectProvider("q"); err != nil {
		t.Fatal(err)
	}
	q.answer(okQuota(77))
	got, err := c.ProviderQuota("q", false)
	if err != nil || got.Windows[0].UsedPercent != 77 {
		t.Fatalf("after a disconnect the old reading must be gone: %+v, %v", got, err)
	}
}
