package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

// fakeProvider stands in for the live MiniMax provider; tests inject
// active/inactive states without touching the disk or the real network.
type fakeProvider struct {
	active bool
	creds  types.Credentials
	err    error
}

func (f *fakeProvider) IsActive() bool { return f.active }
func (f *fakeProvider) ResolveCredentials() (types.Credentials, error) {
	if f.err != nil {
		return types.Credentials{}, f.err
	}
	return f.creds, nil
}

// input builds the JSON payload the model would normally emit for the tool.
func input(t *testing.T, query string, limit, timeout int) json.RawMessage {
	t.Helper()
	body := map[string]any{"query": query}
	if limit > 0 {
		body["limit"] = limit
	}
	if timeout > 0 {
		body["timeout"] = timeout
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// overrideSearchURL swaps the package-level endpoint for one shot so the
// httptest server can be reached. Avoids touching production code only
// for tests; restored via the returned defer.
func overrideSearchURL(u string) func() {
	prev := webSearchEndpoint
	webSearchEndpoint = u
	return func() { webSearchEndpoint = prev }
}

// newHTTPServer wraps httptest.NewServer with a function that fails the
// test if the server can't be reached; the caller `defer`s the cleanup.
func newHTTPServer(t *testing.T, handler http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

func TestWebSearchReturnsOrganicTruncatedToLimit(t *testing.T) {
	srv, httpClient := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("MM-API-Source") == "" {
			t.Error("MM-API-Source header missing")
		}
		var got map[string]string
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got["q"] != "harness github cli" {
			t.Errorf("query = %q", got["q"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"organic":[
{"title":"r1","link":"https://example.com/1","snippet":"s1","date":"yesterday"},
{"title":"r2","link":"https://example.com/2","snippet":"s2","date":"today"},
{"title":"r3","link":"https://example.com/3","snippet":"s3","date":"tomorrow"}
],"base_resp":{"status_code":0,"status_msg":""}}`)
	}))
	defer overrideSearchURL(srv.URL)()

	prov := &fakeProvider{active: true, creds: types.APIKeyCredentials("test-key")}
	out, err := runWebSearch(context.Background(), prov, httpClient,
		input(t, "harness github cli", 2, 0))
	if err != nil {
		t.Fatalf("runWebSearch: %v", err)
	}
	var got []searchResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2 (limit)", len(got))
	}
	if got[0].Title != "r1" || got[1].Title != "r2" {
		t.Errorf("results = %+v, want first two entries", got)
	}
}

func TestWebSearchEmptyOrganicReturnsEmptyArray(t *testing.T) {
	srv, httpClient := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"organic":[],"base_resp":{"status_code":0,"status_msg":""}}`)
	}))
	defer overrideSearchURL(srv.URL)()

	out, err := runWebSearch(context.Background(),
		&fakeProvider{active: true, creds: types.APIKeyCredentials("k")},
		httpClient, input(t, "any", 0, 0))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", strings.TrimSpace(out))
	}
}

func TestWebSearchMissingOrganicFieldStillValidJSON(t *testing.T) {
	srv, httpClient := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"base_resp":{"status_code":0,"status_msg":""}}`)
	}))
	defer overrideSearchURL(srv.URL)()

	out, err := runWebSearch(context.Background(),
		&fakeProvider{active: true, creds: types.APIKeyCredentials("k")},
		httpClient, input(t, "anything", 0, 0))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", out)
	}
}

func TestWebSearchHTTPErrorSurfacedAsProviderAPIError(t *testing.T) {
	cases := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError}
	for _, status := range cases {
		srv, httpClient := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", status)
		}))
		defer overrideSearchURL(srv.URL)()
		_, err := runWebSearch(context.Background(),
			&fakeProvider{active: true, creds: types.APIKeyCredentials("k")},
			httpClient, input(t, "any", 0, 0))
		if err == nil {
			t.Fatalf("expected error for HTTP %d, got nil", status)
		}
		want := fmt.Sprintf("minimax API error %d", status)
		if !strings.Contains(err.Error(), want) {
			t.Errorf("HTTP %d: error %q does not contain %q", status, err.Error(), want)
		}
	}
}

func TestWebSearchBaseRespNonZeroIsError(t *testing.T) {
	srv, httpClient := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"organic":[],"base_resp":{"status_code":2049,"status_msg":"invalid api key"}}`)
	}))
	defer overrideSearchURL(srv.URL)()
	_, err := runWebSearch(context.Background(),
		&fakeProvider{active: true, creds: types.APIKeyCredentials("k")},
		httpClient, input(t, "any", 0, 0))
	if err == nil || !strings.Contains(err.Error(), "2049") {
		t.Errorf("expected 2049 in error, got %v", err)
	}
}

func TestWebSearchTimesOut(t *testing.T) {
	srv, _ := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"organic":[]}`)
	}))
	defer overrideSearchURL(srv.URL)()
	c := &http.Client{Timeout: 10 * time.Millisecond}
	_, err := runWebSearch(context.Background(),
		&fakeProvider{active: true, creds: types.APIKeyCredentials("k")},
		c, input(t, "any", 0, 0))
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestWebSearchInactiveProviderReturnsConnectHint(t *testing.T) {
	_, err := runWebSearch(context.Background(),
		&fakeProvider{active: false},
		&http.Client{}, input(t, "any", 0, 0))
	if err == nil || !strings.Contains(err.Error(), "minimax") {
		t.Fatalf("got %v, want hint about connecting the minimax provider", err)
	}
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "MINIMAX_API_KEY") {
		t.Errorf("hint should mention how to connect: %v", err)
	}
}

func TestWebSearchRejectsEmptyQuery(t *testing.T) {
	_, err := runWebSearch(context.Background(),
		&fakeProvider{active: true, creds: types.APIKeyCredentials("k")},
		&http.Client{}, []byte(`{"query":""}`))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("got %v, want a required-field error", err)
	}
}

func TestNormalizeSearchInputClampsDefaults(t *testing.T) {
	cases := []struct {
		name        string
		in          searchInput
		wantLimit   int
		wantTimeout time.Duration
	}{
		{"zero uses defaults", searchInput{}, webSearchDefaultLimit, webSearchDefaultTimeout},
		{"huge limit clamps", searchInput{Limit: 9999}, webSearchMaxLimit, webSearchDefaultTimeout},
		{"negative limit to default", searchInput{Limit: -1}, webSearchDefaultLimit, webSearchDefaultTimeout},
		{"timeout honored", searchInput{Limit: 5, Timeout: 5}, 5, 5 * time.Second},
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
