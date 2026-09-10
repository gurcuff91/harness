package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixtures — stand up one httptest server per backend, in the dispatch
// order the dispatcher will use: minimax first, ollama-cloud second.
//
// Each server counts its own calls so fallback-skip / fallback-engage
// tests can assert which backend was actually contacted.

type fakeBackend struct {
	name      string
	url       string
	responder http.HandlerFunc
	calls     atomic.Int32
}

func newFakeBackend(t *testing.T, name string, h http.HandlerFunc) *fakeBackend {
	t.Helper()
	fb := &fakeBackend{name: name, responder: h}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fb.calls.Add(1)
		fb.responder(w, r)
	}))
	fb.url = srv.URL
	t.Cleanup(srv.Close)
	return fb
}

// stubLookup adapts the fakes into the ProviderLookup interface.
type stubLookup struct {
	backends []SearchBackend
}

func (s *stubLookup) ActiveSearchBackends() []SearchBackend { return s.backends }

// backendOf builds a backend descriptor from a fake.
func backendOf(fb *fakeBackend, key string) SearchBackend {
	return SearchBackend{Name: fb.name, URL: fb.url, Key: key}
}

// ── Validation (via the tool's Execute, since query validation lives there) ─

func TestWebSearchRejectsEmptyQuery(t *testing.T) {
	tool := WebSearch(&stubLookup{backends: []SearchBackend{{Name: "minimax", URL: "http://x", Key: "k"}}}, &http.Client{Timeout: 50 * time.Millisecond})
	_, err := tool.Execute(context.Background(), []byte(`{"query":""}`))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("got %v, want a required-field error", err)
	}
}

func TestNormalizeSearchInputClamps(t *testing.T) {
	cases := []struct {
		name        string
		in          searchInput
		wantLimit   int
		wantTimeout time.Duration
	}{
		{"zero uses defaults", searchInput{Query: "x"}, webSearchDefaultLimit, webSearchDefaultTimeout},
		{"huge limit clamps", searchInput{Query: "x", Limit: 9999}, webSearchMaxLimit, webSearchDefaultTimeout},
		{"explicit small limit", searchInput{Query: "x", Limit: 3}, 3, webSearchDefaultTimeout},
		{"timeout honored", searchInput{Query: "x", Timeout: 5}, webSearchDefaultLimit, 5 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			limit, timeout := normalizeSearchInput(c.in)
			if limit != c.wantLimit || timeout != c.wantTimeout {
				t.Fatalf("got limit=%d timeout=%s, want %d/%s", limit, timeout, c.wantLimit, c.wantTimeout)
			}
		})
	}
}

// ── No active backends ─────────────────────────────────────────────────────

// TestNoBackendsReturnsCanonicalMessage covers the "nothing connected"
// case — unlike the mid-dispatch aggregated error (which must never name
// a backend, see TestBothBackendsFailErrorHidesNames), THIS message is
// explicitly allowed — and expected — to name both providers, since the
// user needs to know exactly what to connect.
func TestNoBackendsReturnsCanonicalMessage(t *testing.T) {
	_, err := runWebSearch(context.Background(), &stubLookup{}, &http.Client{Timeout: 50 * time.Millisecond}, searchInput{Query: "x"})
	if err == nil {
		t.Fatal("expected error for empty backend list, got nil")
	}
	if !strings.Contains(err.Error(), "minimax") || !strings.Contains(err.Error(), "ollama-cloud") {
		t.Errorf("error should name both connectable providers: %q", err.Error())
	}
}

// ── Primary success: fallback MUST NOT be called ──────────────────────────

func TestPrimaryReturnsResultsSkipsFallback(t *testing.T) {
	primary := newFakeBackend(t, "minimax", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"organic":[{"title":"hit1","link":"https://e/1","snippet":"s1","date":"2026-01-01"},{"title":"hit2","link":"https://e/2","snippet":"s2"}],"base_resp":{"status_code":0,"status_msg":""}}`)
	})
	fallback := newFakeBackend(t, "ollama-cloud", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("fallback should not be called when primary returned results")
	})

	out, err := runWebSearch(context.Background(), &stubLookup{backends: []SearchBackend{backendOf(primary, "k"), backendOf(fallback, "k")}}, &http.Client{Timeout: 2 * time.Second}, searchInput{Query: "alpha"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if primary.calls.Load() != 1 {
		t.Errorf("primary.calls = %d, want 1", primary.calls.Load())
	}
	if fallback.calls.Load() != 0 {
		t.Errorf("fallback.calls = %d, want 0 (primary returned results)", fallback.calls.Load())
	}

	var got []searchResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got) != 2 || got[0].URL != "https://e/1" || got[0].Title != "hit1" {
		t.Errorf("results = %+v, want link→url mapping", got)
	}
}

// ── Fallback on empty result ───────────────────────────────────────────────

func TestPrimaryEmptyListFallsBack(t *testing.T) {
	primary := newFakeBackend(t, "minimax", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"organic":[],"base_resp":{"status_code":0,"status_msg":""}}`)
	})
	fallback := newFakeBackend(t, "ollama-cloud", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"title":"from-ollama","url":"https://o/1","content":"snippet 1"}]}`)
	})

	out, err := runWebSearch(context.Background(), &stubLookup{backends: []SearchBackend{backendOf(primary, "k"), backendOf(fallback, "k")}}, &http.Client{Timeout: 2 * time.Second}, searchInput{Query: "alpha"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if primary.calls.Load() != 1 || fallback.calls.Load() != 1 {
		t.Errorf("primary.calls=%d fallback.calls=%d, want 1/1", primary.calls.Load(), fallback.calls.Load())
	}
	var got []searchResult
	_ = json.Unmarshal([]byte(out), &got)
	if len(got) != 1 || got[0].Title != "from-ollama" || got[0].URL != "https://o/1" {
		t.Errorf("results = %+v, want Ollama payload mapped to unified shape", got)
	}
}

// ── Fallback on transport-level failure ──────────────────────────────────

func TestPrimaryTimeoutFallsBack(t *testing.T) {
	// primary's responder sleeps past the http.Client's short timeout.
	primary := newFakeBackend(t, "minimax", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"organic":[],"base_resp":{"status_code":0}}`)
	})
	fallback := newFakeBackend(t, "ollama-cloud", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"title":"t","url":"https://u","content":"c"}]}`)
	})

	out, err := runWebSearch(context.Background(), &stubLookup{backends: []SearchBackend{backendOf(primary, "k"), backendOf(fallback, "k")}}, &http.Client{Timeout: 50 * time.Millisecond}, searchInput{Query: "alpha", Timeout: 1})
	if err != nil {
		t.Fatalf("fallback should have rescued: %v", err)
	}
	if !strings.Contains(out, `"url": "https://u"`) {
		t.Errorf("output = %q, want Ollama result", out)
	}
}

// ── Backend errors merge into a single user-facing error ──────────────────

func TestBothBackendsFailErrorHidesNames(t *testing.T) {
	primary := newFakeBackend(t, "minimax", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	fallback := newFakeBackend(t, "ollama-cloud", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kapow", http.StatusInternalServerError)
	})

	_, err := runWebSearch(context.Background(), &stubLookup{backends: []SearchBackend{backendOf(primary, "k"), backendOf(fallback, "k")}}, &http.Client{Timeout: 2 * time.Second}, searchInput{Query: "alpha"})
	if err == nil {
		t.Fatal("expected error when both backends fail")
	}
	if strings.Contains(err.Error(), "minimax") || strings.Contains(err.Error(), "ollama-cloud") {
		t.Errorf("error must not mention backend names: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "no results from any backend") {
		t.Errorf("error missing consolidated header: %q", err.Error())
	}
	if strings.Count(err.Error(), "provider rejected the request (status 500)") != 1 {
		t.Errorf("expected dedup of identical reasons; got: %q", err.Error())
	}
}

func TestOnlyPrimaryActiveFailingReportsOneReason(t *testing.T) {
	primary := newFakeBackend(t, "minimax", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := runWebSearch(context.Background(), &stubLookup{backends: []SearchBackend{backendOf(primary, "k")}}, &http.Client{Timeout: 2 * time.Second}, searchInput{Query: "alpha"})
	if err == nil {
		t.Fatal("expected error")
	}
	count := 0
	for _, l := range strings.Split(err.Error(), "\n") {
		if strings.HasPrefix(l, "- ") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 reason line, got %d: %q", count, err.Error())
	}
}

// ── Both backends return 200 empty → [] ──────────────────────────────────

func TestBothEmptyReturnsEmptyArray(t *testing.T) {
	primary := newFakeBackend(t, "minimax", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"organic":[],"base_resp":{"status_code":0,"status_msg":""}}`)
	})
	fallback := newFakeBackend(t, "ollama-cloud", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	})

	out, err := runWebSearch(context.Background(), &stubLookup{backends: []SearchBackend{backendOf(primary, "k"), backendOf(fallback, "k")}}, &http.Client{Timeout: 2 * time.Second}, searchInput{Query: "alpha"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", out)
	}
}

// TestLookupIsConsultedOnEveryCallNotCachedAtConstruction is the regression
// test for a real bug: the agent used to snapshot ActiveSearchBackends()
// ONCE, at session-creation time (buildSessionTools calling
// a.webSearchLookup() a single time before wrapping WebSearch), so
// disconnecting both providers mid-session had no effect — every
// subsequent Execute kept "seeing" whatever was active when the session
// was built. A ProviderLookup MUST be re-consulted on every dispatcher
// call; this test uses a lookup whose backend list changes between two
// calls to WebSearch's own Execute (not runWebSearch directly) and
// asserts each call reflects the CURRENT state.
type dynamicLookup struct {
	get func() []SearchBackend
}

func (d *dynamicLookup) ActiveSearchBackends() []SearchBackend { return d.get() }

func TestLookupIsConsultedOnEveryCallNotCachedAtConstruction(t *testing.T) {
	backend := newFakeBackend(t, "minimax", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"organic":[{"title":"t","link":"https://u","snippet":"s"}],"base_resp":{"status_code":0}}`)
	})

	connected := false
	lookup := &dynamicLookup{get: func() []SearchBackend {
		if !connected {
			return nil
		}
		return []SearchBackend{backendOf(backend, "k")}
	}}

	tool := WebSearch(lookup, &http.Client{Timeout: 2 * time.Second})

	// First call: nothing connected yet — must fail with the canonical
	// "nothing connected" message, NOT silently succeed from a stale
	// snapshot taken before `tool` even existed.
	if _, err := tool.Execute(context.Background(), []byte(`{"query":"x"}`)); err == nil {
		t.Fatal("expected error before any provider is connected")
	}

	// Simulate the user running `harness connect minimax` mid-session —
	// no agent/session/tool reconstruction happens for that, exactly like
	// production: SwitchModel/Connect mutate global provider state, and the
	// SAME already-registered WebSearch tool must pick it up on its very
	// next call.
	connected = true
	out, err := tool.Execute(context.Background(), []byte(`{"query":"x"}`))
	if err != nil {
		t.Fatalf("expected success after connecting, got: %v", err)
	}
	if !strings.Contains(out, `"title": "t"`) {
		t.Errorf("output = %q, want the now-connected backend's result", out)
	}
}

// ── Primary skips silently when not in the lookup ─────────────────────────

func TestPrimaryInactiveFallsThroughToOllama(t *testing.T) {
	fallback := newFakeBackend(t, "ollama-cloud", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"title":"only","url":"https://o/x","content":"c"}]}`)
	})
	out, err := runWebSearch(context.Background(), &stubLookup{backends: []SearchBackend{backendOf(fallback, "k")}}, &http.Client{Timeout: 2 * time.Second}, searchInput{Query: "alpha"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(out, `"only"`) {
		t.Errorf("output = %q, want Ollama result", out)
	}
}

// ── Helpers / classified errors ───────────────────────────────────────────

func TestClassifyTransportErrorCategories(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&timeoutError{}, "request timed out"},
		{fmt.Errorf("dial tcp: connection refused"), "connection refused"},
		{fmt.Errorf("dial tcp: no such host"), "network unreachable"},
		{fmt.Errorf("dial tcp: i/o timeout"), "transport error"},
		{context.DeadlineExceeded, "request timed out"},
		{context.Canceled, "request cancelled"},
		{nil, ""},
	}
	for _, c := range cases {
		got := classifyTransportError(c.err)
		if got != c.want {
			t.Errorf("classifyTransportError(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestDedupeReasons(t *testing.T) {
	got := dedupeReasons([]string{"a", "b", "a", "c", "b", ""})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRenderResultsEmptyBecomesArrayNotNull(t *testing.T) {
	out, err := renderResults(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "[]" {
		t.Errorf("got %q, want []", out)
	}
}

func TestTruncateSnippet(t *testing.T) {
	short := strings.Repeat("a", 100)
	if got := truncateSnippet(short); got != short {
		t.Errorf("short should pass through: got %q", got)
	}
	long := strings.Repeat("a", snippetMaxChars+50)
	got := truncateSnippet(long)
	if !strings.HasSuffix(got, "…") || len(strings.TrimSuffix(got, "…")) != snippetMaxChars {
		t.Errorf("expected truncation at %d chars + suffix, got len=%d", snippetMaxChars, len(got))
	}
}

// timeoutError is a minimal net.Error implementation carrying Timeout()==true
// so classifyTransportError returns "request timed out" via the same path
// it would for a real context.DeadlineExceeded-typed transport error.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
