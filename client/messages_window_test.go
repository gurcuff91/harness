package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetMessagesWithQueryAndHeaders(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("X-Messages-From", "7")
		w.Header().Set("X-Messages-Total", "20")
		w.Write([]byte(`[{"role":"user","parts":[{"text":"hi"}]}]`))
	}))
	defer srv.Close()
	c := New(srv.URL)

	for _, tc := range []struct {
		opts  MessagesOptions
		query string
	}{
		{MessagesOptions{}, ""},
		{MessagesOptions{From: 4}, "from=4"},
		{MessagesOptions{Compactions: 2}, "compactions=2"},
	} {
		msgs, from, total, err := c.GetMessagesWith("s1", tc.opts)
		if err != nil || from != 7 || total != 20 || len(msgs) != 1 {
			t.Fatalf("%+v: msgs=%d from=%d total=%d err=%v", tc.opts, len(msgs), from, total, err)
		}
		if gotQuery != tc.query {
			t.Errorf("%+v: query = %q, want %q", tc.opts, gotQuery, tc.query)
		}
	}
}

// A server that doesn't send the paging headers (an older harness) is a clear
// error, not a silent from=0/total=0 that would mislabel indices.
func TestGetMessagesWithRequiresHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	if _, _, _, err := New(srv.URL).GetMessagesWith("s1", MessagesOptions{Compactions: 2}); err == nil {
		t.Fatal("want an error when X-Messages-* headers are missing")
	}
}

func TestGetMessagesWithServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"from and compactions are mutually exclusive"}}`))
	}))
	defer srv.Close()
	_, _, _, err := New(srv.URL).GetMessagesWith("s1", MessagesOptions{From: 1, Compactions: 1})
	if err == nil || err.Error() == "" {
		t.Fatalf("want the server's 400 as an error, got %v", err)
	}
}

func TestProviderBaseURL(t *testing.T) {
	c := New("127.0.0.1:9")
	if got := c.ProviderBaseURL("claude-oauth"); got != "http://127.0.0.1:9/api/providers/claude-oauth/proxy" {
		t.Errorf("got %s", got)
	}
	if got := c.ProviderBaseURL("a b/c"); got != "http://127.0.0.1:9/api/providers/a%20b%2Fc/proxy" {
		t.Errorf("name must be path-escaped, got %s", got)
	}
}

// ProviderDo returns the upstream's response untouched — including an error
// status, which is the provider's answer, not a client error — and streams.
func TestProviderDo(t *testing.T) {
	var gotPath, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotCT = r.URL.Path, r.Header.Get("Content-Type")
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		if r.URL.Path == "/api/providers/p/proxy/v1/boom" {
			w.WriteHeader(529)
			w.Write([]byte(`{"error":"overloaded"}`))
			return
		}
		w.Header().Set("X-Up", "1")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)

	resp, err := c.ProviderDo(context.Background(), "p", "POST", "/v1/messages", []byte(`{"a":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotPath != "/api/providers/p/proxy/v1/messages" || gotCT != "application/json" || gotBody != `{"a":1}` || resp.Header.Get("X-Up") != "1" {
		t.Errorf("path=%s ct=%s body=%s", gotPath, gotCT, gotBody)
	}

	resp, err = c.ProviderDo(context.Background(), "p", "POST", "v1/boom", []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("an upstream 529 must not be a Go error: %v", err)
	}
	b := make([]byte, 64)
	n, _ := resp.Body.Read(b)
	resp.Body.Close()
	if resp.StatusCode != 529 || string(b[:n]) != `{"error":"overloaded"}` {
		t.Errorf("status=%d body=%s", resp.StatusCode, b[:n])
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ProviderDo(ctx, "p", "GET", "/v1/models", nil, nil); err == nil {
		t.Error("a cancelled context must fail the call")
	}
}

// GetModel escapes each segment of the id but keeps its slashes, so ids like
// "org/beta:7b" or ones with spaces/percent reach the server intact.
func TestGetModelEscapesIDSegments(t *testing.T) {
	var gotRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.EscapedPath()
		w.Write([]byte(`{"provider":"p","model":"p/x","id":"x","dialect":"openai","sources":{"pricing":"unknown"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	for id, want := range map[string]string{
		"plain":       "/api/models/p%20q/plain",
		"org/beta:7b": "/api/models/p%20q/org/beta:7b",
		"a b/c%d":     "/api/models/p%20q/a%20b/c%25d",
		"x?y#z":       "/api/models/p%20q/x%3Fy%23z",
	} {
		m, err := c.GetModel("p q", id)
		if err != nil {
			t.Fatal(err)
		}
		if gotRaw != want {
			t.Errorf("id %q: request path %q, want %q", id, gotRaw, want)
		}
		if m.Dialect != "openai" || m.Sources["pricing"] != "unknown" {
			t.Errorf("decoded model = %+v", m)
		}
	}
}

func TestListProviderModelsQuery(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL).ListProviderModels("a&b c"); err != nil || gotQuery != "provider=a%26b+c" {
		t.Errorf("query = %q err=%v", gotQuery, err)
	}
}
