package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// TestGetSessionToolsHitsExpectedPath verifies GetSessionTools requests the
// correct endpoint and decodes the response into []types.ToolDef.
func TestGetSessionToolsHitsExpectedPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`[{"name":"Bash","description":"run shell","input_schema":{"type":"object"}}]`))
	}))
	defer srv.Close()
	c := New(srv.Listener.Addr().String())

	defs, err := c.GetSessionTools("sess-abc")
	if err != nil {
		t.Fatalf("GetSessionTools: %v", err)
	}
	if gotPath != "/api/sessions/sess-abc/tools" {
		t.Errorf("path = %q, want /api/sessions/sess-abc/tools", gotPath)
	}
	if len(defs) != 1 || defs[0].Name != "Bash" || defs[0].Description != "run shell" {
		t.Errorf("unexpected decoded defs: %+v", defs)
	}
	if len(defs[0].InputSchema) == 0 {
		t.Error("expected a non-empty input_schema to survive the round trip")
	}
}

// TestCustomProviderClientMethodsHitExpectedPaths verifies
// GetCustomProviders/PutCustomProvider/DeleteCustomProvider request the
// correct singular "/api/settings/provider" paths (matching MCP's own
// singular "/api/settings/mcp" style) and decode/encode correctly.
func TestCustomProviderClientMethodsHitExpectedPaths(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if r.Body != nil {
			json.NewDecoder(r.Body).Decode(&gotBody)
		}
		switch r.Method {
		case "GET":
			w.Write([]byte(`{"my-proxy":{"type":"openai","url":"https://x"}}`))
		case "PUT":
			w.Write([]byte(`{"type":"openai","url":"https://my-proxy.internal/v1","display":"My Proxy"}`))
		case "DELETE":
			w.Write([]byte(`{"status":{"code":"deleted"}}`))
		}
	}))
	defer srv.Close()
	c := New(srv.Listener.Addr().String())

	all, err := c.GetCustomProviders()
	if err != nil {
		t.Fatalf("GetCustomProviders: %v", err)
	}
	if gotMethod != "GET" || gotPath != "/api/settings/provider" {
		t.Errorf("GET: method=%q path=%q, want GET /api/settings/provider", gotMethod, gotPath)
	}
	if _, ok := all["my-proxy"]; !ok {
		t.Errorf("unexpected decoded collection: %+v", all)
	}

	saved, err := c.PutCustomProvider("my-proxy", CustomProvider{Type: "openai", URL: "https://my-proxy.internal/v1", Display: "My Proxy"})
	if err != nil {
		t.Fatalf("PutCustomProvider: %v", err)
	}
	if gotMethod != "PUT" || gotPath != "/api/settings/provider/my-proxy" {
		t.Errorf("PUT: method=%q path=%q, want PUT /api/settings/provider/my-proxy", gotMethod, gotPath)
	}
	if gotBody["type"] != "openai" || gotBody["url"] != "https://my-proxy.internal/v1" {
		t.Errorf("PUT body missing expected fields: %+v", gotBody)
	}
	if saved.Display != "My Proxy" {
		t.Errorf("unexpected saved provider: %+v", saved)
	}

	if _, err := c.DeleteCustomProvider("my-proxy"); err != nil {
		t.Fatalf("DeleteCustomProvider: %v", err)
	}
	if gotMethod != "DELETE" || gotPath != "/api/settings/provider/my-proxy" {
		t.Errorf("DELETE: method=%q path=%q, want DELETE /api/settings/provider/my-proxy", gotMethod, gotPath)
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

// TestNewDefaultsToBackgroundContext confirms New() without WithContext
// behaves exactly as before this option existed — a plain, uncancellable
// request succeeds normally. Guards against WithContext's addition
// accidentally changing the zero-option default.
func TestNewDefaultsToBackgroundContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	if _, err := c.do("GET", "/api/x", nil); err != nil {
		t.Fatalf("do with no WithContext: %v", err)
	}
}

// TestWithContextCancellationAbortsInFlightRequest is the direct regression
// test for the real incident this option exists to fix: a caller's context
// being cancelled (e.g. Session.Stop()) must abort a request THIS client is
// making, even one with no per-call timeout of its own (do()/decode(), not
// just Ask/AskWithImages which already had their own timeout mechanism).
func TestWithContextCancellationAbortsInFlightRequest(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never responds until the test tears down
	}))
	defer srv.Close()
	defer close(block) // must close BEFORE srv.Close() — see colleague_test.go's identical note

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	c := New(srv.Listener.Addr().String(), WithContext(ctx))
	start := time.Now()
	_, err := c.do("GET", "/api/x", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error after ctx cancellation, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("do() took %v after ctx cancellation, want it to return promptly", elapsed)
	}
}

// TestAskStillLayersItsOwnTimeoutOverWithContext confirms Ask/AskWithImages'
// existing per-call timeout still works ON TOP of a WithContext-supplied
// parent — askCtx wraps context.WithTimeout(c.ctx, timeout), so a client
// with WithContext(context.Background()) (the common case: no caller
// cancellation, just a bounded wait) still honors its own timeout exactly
// as before this change.
func TestAskStillLayersItsOwnTimeoutOverWithContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	c := New(srv.Listener.Addr().String(), WithContext(context.Background()))
	start := time.Now()
	_, err := c.Ask("some-session", "hello", 200*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Ask took %v, want it bounded by its own 200ms timeout", elapsed)
	}
}
