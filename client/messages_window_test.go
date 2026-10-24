package client

import (
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
