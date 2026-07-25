package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDoSuccessRoundTrip verifies do() sends the method/path/body correctly
// and returns the raw response body on success.
func TestDoSuccessRoundTrip(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	data, err := c.do("POST", "/api/x", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/api/x" {
		t.Errorf("method/path = %s %s, want POST /api/x", gotMethod, gotPath)
	}
	if gotBody["k"] != "v" {
		t.Errorf("body not sent correctly: %v", gotBody)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("data = %s", data)
	}
}

// TestDoStructuredError verifies a 4xx/5xx response in harness's standard
// {"error": {"message", "details"}} shape decodes into *Error with both
// fields accessible (not just collapsed into a string).
func TestDoStructuredError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad thing","details":{"code":"E1","retryable":false}}}`))
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	_, err := c.do("GET", "/api/x", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	ae, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if ae.Message != "bad thing" {
		t.Errorf("Message = %q, want %q", ae.Message, "bad thing")
	}
	if ae.Details["code"] != "E1" {
		t.Errorf("Details[code] = %v, want E1", ae.Details["code"])
	}
	if ae.Error() == "" || ae.Error() == "bad thing" {
		// Error() should append the marshaled details when present.
		t.Errorf("Error() should include details, got: %q", ae.Error())
	}
}

// TestDoNonStandardErrorFallsBackToRawBody verifies a 4xx/5xx response that
// ISN'T the standard error shape still returns a usable error (the raw body),
// instead of silently losing the failure.
func TestDoNonStandardErrorFallsBackToRawBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	_, err := c.do("GET", "/api/x", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := err.(*Error); ok {
		t.Error("non-standard body should not parse as *Error")
	}
	if err.Error() != "internal server error" {
		t.Errorf("error = %q, want the raw body", err.Error())
	}
}

// TestGetSchedulesOwnerFilter verifies the owner query param is included only
// when non-empty — the TUI (per-session view) and CLI/operator (full view)
// use the same method with different owner values.
func TestGetSchedulesOwnerFilter(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := New(srv.Listener.Addr().String())

	if _, err := c.GetSchedules(""); err != nil {
		t.Fatalf("GetSchedules(\"\"): %v", err)
	}
	if gotQuery != "" {
		t.Errorf("empty owner should send no query, got %q", gotQuery)
	}

	if _, err := c.GetSchedules("sess-123"); err != nil {
		t.Fatalf("GetSchedules(owner): %v", err)
	}
	if gotQuery != "owner=sess-123" {
		t.Errorf("query = %q, want owner=sess-123", gotQuery)
	}
}

// TestListSessionsCWDFilter mirrors TestGetSchedulesOwnerFilter for the
// sessions listing (all-cwds vs a single cwd).
func TestListSessionsCWDFilter(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := New(srv.Listener.Addr().String())

	if _, err := c.ListSessions(""); err != nil {
		t.Fatalf("ListSessions(\"\"): %v", err)
	}
	if gotQuery != "" {
		t.Errorf("empty cwd should send no query, got %q", gotQuery)
	}

	if _, err := c.ListSessions("/tmp/proj"); err != nil {
		t.Fatalf("ListSessions(cwd): %v", err)
	}
	// cwd is URL-escaped (so a path with reserved chars round-trips correctly);
	// the server decodes it back to /tmp/proj via r.URL.Query().Get.
	if gotQuery != "cwd=%2Ftmp%2Fproj" {
		t.Errorf("query = %q, want cwd=%%2Ftmp%%2Fproj", gotQuery)
	}
}
