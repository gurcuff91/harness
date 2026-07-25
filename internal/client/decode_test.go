package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveJSON spins up a test server that replies to any request with the given
// status code and body, capturing the last request path for assertions.
func serveJSON(t *testing.T, status int, body string) (*Client, *string) {
	t.Helper()
	lastPath := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*lastPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.Listener.Addr().String()), lastPath
}

// TestListSessionsDecodesTyped verifies a list endpoint decodes into typed
// structs (not map[string]any) with embedded store.SessionMeta fields
// populated — the core promise of the typed SDK.
func TestListSessionsDecodesTyped(t *testing.T) {
	c, _ := serveJSON(t, 200, `[
		{"id":"s1","cwd":"/a","name":"one","model":"anthropic/x","thinking":"off"},
		{"id":"s2","cwd":"/b","model":"openai/y","thinking":"high"}
	]`)
	sessions, err := c.ListSessions("")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("len = %d, want 2", len(sessions))
	}
	if sessions[0].ID != "s1" || sessions[0].Name != "one" || sessions[0].Model != "anthropic/x" {
		t.Errorf("session[0] = %+v", sessions[0])
	}
	if sessions[1].Thinking != "high" {
		t.Errorf("session[1].Thinking = %q, want high", sessions[1].Thinking)
	}
}

// TestGetSessionEmbedsMaxIterations verifies the single-session accessor
// surfaces the runtime max_iterations field the server layers on top of the
// persisted meta.
func TestGetSessionEmbedsMaxIterations(t *testing.T) {
	c, path := serveJSON(t, 200, `{"id":"s1","cwd":"/a","model":"m","thinking":"off","max_iterations":120}`)
	sess, err := c.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if *path != "/api/sessions/s1" {
		t.Errorf("path = %q", *path)
	}
	if sess.MaxIterations != 120 {
		t.Errorf("MaxIterations = %d, want 120", sess.MaxIterations)
	}
}

// TestDecodeStatusUnwrapsEnvelope verifies action endpoints unwrap the
// {"status": {...}} envelope into a *Status (code + message).
func TestDecodeStatusUnwrapsEnvelope(t *testing.T) {
	c, _ := serveJSON(t, 202, `{"status":{"code":"queued","message":"busy"}}`)
	st, err := c.SendPrompt("s1", "hi")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if st.Code != "queued" || st.Message != "busy" {
		t.Errorf("status = %+v, want {queued busy}", st)
	}
}

// TestDeleteSessionNoContent verifies DeleteSession treats a 204 (empty body)
// as success, not a decode error.
func TestDeleteSessionNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := New(srv.Listener.Addr().String())
	if err := c.DeleteSession("s1"); err != nil {
		t.Errorf("DeleteSession: %v", err)
	}
}

// TestGetMCPServersDecodesConfigType verifies the settings-collection endpoint
// decodes into the reused config.MCPServer type, with the inferred-transport
// helpers (IsRemote/Argv) working off the decoded fields.
func TestGetMCPServersDecodesConfigType(t *testing.T) {
	c, _ := serveJSON(t, 200, `{
		"fs":{"command":"npx","args":["-y","@mcp/fs"]},
		"api":{"url":"https://x/mcp","disabled":true}
	}`)
	servers, err := c.GetMCPServers()
	if err != nil {
		t.Fatalf("GetMCPServers: %v", err)
	}
	if servers["fs"].IsRemote() {
		t.Error("fs should be local (has command)")
	}
	if got := strings.Join(servers["fs"].Argv(), " "); got != "npx -y @mcp/fs" {
		t.Errorf("fs Argv = %q", got)
	}
	if !servers["api"].IsRemote() || !servers["api"].Disabled {
		t.Errorf("api = %+v, want remote+disabled", servers["api"])
	}
}

// TestStreamEventTypedFieldsAndRaw verifies decoded events populate the typed
// fields for their kind AND preserve the exact original payload in Raw (used by
// the CLI's json passthrough).
func TestStreamEventTypedFieldsAndRaw(t *testing.T) {
	toolResult := `{"type":"tool_result","tool_id":"t1","output":"ok","duration":12.5,"is_error":false}`
	tokens := `{"type":"tokens","input":100,"total_output":50,"cost_usd":0.01,"context_usage":0.3,"context_window":200000}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", toolResult)
		fmt.Fprintf(w, "data: %s\n\n", tokens)
		fl.Flush()
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := c.StreamEvents(ctx, "s1")
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}

	var got []Event
	for e := range events {
		got = append(got, e)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}

	tr := got[0]
	if tr.Type != "tool_result" || tr.ToolID != "t1" || tr.Output != "ok" || tr.Duration != 12.5 {
		t.Errorf("tool_result fields = %+v", tr)
	}
	// Raw must be the exact bytes the server sent (byte-for-byte passthrough).
	if string(tr.Raw) != toolResult {
		t.Errorf("Raw = %q, want %q", tr.Raw, toolResult)
	}

	tk := got[1]
	if tk.Input != 100 || tk.TotalOutput != 50 || tk.CostUSD != 0.01 || tk.ContextWindow != 200000 {
		t.Errorf("tokens fields = %+v", tk)
	}
}

// TestRawIsNotReEncoded guards the specific reason Event carries Raw: an
// omitempty re-encode of the struct would drop zero-valued fields (e.g.
// is_error:false), so consumers needing the canonical wire shape must read Raw,
// not re-marshal the Event. This asserts Raw still contains is_error while a
// re-encode of the struct would not.
func TestRawIsNotReEncoded(t *testing.T) {
	payload := `{"type":"tool_result","tool_id":"t1","output":"x","is_error":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, _ := c.StreamEvents(ctx, "s1")
	e := <-events

	if !strings.Contains(string(e.Raw), `"is_error":false`) {
		t.Errorf("Raw dropped a zero-valued field: %s", e.Raw)
	}
	// A struct re-encode drops it (omitempty) — this documents WHY Raw exists.
	reEncoded, _ := json.Marshal(e)
	if strings.Contains(string(reEncoded), "is_error") {
		t.Errorf("re-encode unexpectedly kept is_error: %s", reEncoded)
	}
}
