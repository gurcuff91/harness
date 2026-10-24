package providers

import (
	"net/http"
	"net/url"
)

// AddForTest appends a provider to the registry (copy-on-write, under the
// lock). For tests that inject a fake provider; production code registers
// through RegisterOpenAI.
func AddForTest(ps ...Provider) {
	EnsureRegistry()
	registryMu.Lock()
	all = append(append(make([]Provider, 0, len(all)+len(ps)), all...), ps...)
	registryMu.Unlock()
}

// SetForTest replaces the whole registry and returns a func restoring the
// previous one — the snapshot/restore tests use around global registration.
func SetForTest(list []Provider) (restore func()) {
	EnsureRegistry()
	registryMu.Lock()
	prev := all
	all = list
	registryMu.Unlock()
	return func() {
		registryMu.Lock()
		all = prev
		registryMu.Unlock()
	}
}

// RedirectForTest points a provider's native-API HTTP client at target (a test
// server) whatever host the provider hardcodes, so an end-to-end test can
// drive a real provider's Upstream() against a fake. It returns the Provider
// to register (via SetForTest/AddForTest).
func RedirectForTest(p Provider, target string) Provider {
	u, err := url.Parse(target)
	if err != nil {
		panic(err)
	}
	c := &http.Client{Transport: redirectRT{u}}
	switch v := p.(type) {
	case *ClaudeOAuth:
		v.client = c
	case *CodexOAuth:
		v.client = c
	case *Anthropic:
		v.client = c
	default:
		panic("RedirectForTest: unsupported provider")
	}
	return p
}

type redirectRT struct{ target *url.URL }

func (r redirectRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}
