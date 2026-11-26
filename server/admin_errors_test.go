package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
)

// The table: every admin sentinel maps to its status. ErrUnprocessable (422)
// wins over ErrInvalid (400) when an error carries both.
func TestAdminStatusTable(t *testing.T) {
	both := fmt.Errorf("x: %w, %w", admin.ErrInvalid, admin.ErrUnprocessable)
	for name, c := range map[string]struct {
		err  error
		want int
	}{
		"not found":         {admin.ErrNotFound, 404},
		"unprocessable":     {admin.ErrUnprocessable, 422},
		"invalid":           {admin.ErrInvalid, 400},
		"invalid+unproc":    {both, 422},
		"session active":    {admin.ErrSessionActive, 409},
		"conflict":          {admin.ErrConflict, 409},
		"not connected":     {admin.ErrNotConnected, 409},
		"not removable":     {admin.ErrProviderNotRemovable, 409},
		"upstream":          {admin.ErrUpstream, 502},
		"not configured":    {admin.ErrNotConfigured, 503},
		"anything else":     {errors.New("boom"), 500},
		"wrapped not found": {fmt.Errorf("ctx: %w", admin.ErrNotFound), 404},
		"wrapped upstream":  {fmt.Errorf("ctx: %w", admin.ErrUpstream), 502},
	} {
		if got := adminStatus(c.err); got != c.want {
			t.Errorf("%s: adminStatus = %d, want %d", name, got, c.want)
		}
	}
}

// Through the real handlers: sentinel → status.
func TestAdminErrorsThroughRealHandlers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore()}) // no memory
	t.Cleanup(func() { a.Close() })
	ts := httptest.NewServer(NewServer(a, ServerOptions{Logger: logx.NewNilLogger()}).handler())
	t.Cleanup(ts.Close)
	do := func(method, path, body string) int {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for name, c := range map[string]struct {
		method, path, body string
		want               int
	}{
		"ErrNotFound (mcp delete)":        {"DELETE", "/api/settings/mcp/ghost", "", 404},
		"ErrNotFound (custom provider)":   {"DELETE", "/api/settings/provider/ghost", "", 404},
		"ErrNotFound (disconnect)":        {"POST", "/api/providers/ghost/disconnect", "", 404},
		"ErrNotFound (quota)":             {"GET", "/api/providers/ghost/quota", "", 404},
		"ErrNotFound (model)":             {"GET", "/api/models?provider=ghost", "", 404},
		"ErrUnprocessable (bad thinking)": {"PATCH", "/api/settings", `{"thinking_level":"bogus"}`, 422},
		"ErrUnprocessable (invalid mcp)":  {"PUT", "/api/settings/mcp/x", `{}`, 422},
		"ErrUnprocessable (custom prov.)": {"PUT", "/api/settings/provider/x", `{"type":"nope","url":"http://x"}`, 422},
		"ErrNotConfigured (memory)":       {"GET", "/api/memories/a?cwd=/x", "", 503},
		"ErrNotConnected (models)":        {"GET", "/api/models?provider=anthropic", "", 409},
		"ErrNotFound (oauth, no flow)":    {"POST", "/api/oauth/ghost", "", 404},
		"ErrInvalid (oauth, no verifier)": {"POST", "/api/oauth/claude-oauth", `{"exchange_code":"c"}`, 400},
	} {
		if got := do(c.method, c.path, c.body); got != c.want {
			t.Errorf("%s: %s %s = %d, want %d", name, c.method, c.path, got, c.want)
		}
	}
}

// DELETE of a session open on the server closes it FIRST (admin refuses to
// delete an open one) and answers 204 — and an unknown id is 204 too.
func TestDeleteOpenSessionClosesItAndAnswers204(t *testing.T) {
	srv, a, model := sharedServer(t, nil)
	c := client.New(srv.Addr())
	sess, err := c.CreateSession(model, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.ActiveSessions(); len(got) != 1 || got[0] != sess.ID {
		t.Fatalf("setup: active = %v", got)
	}
	if err := c.DeleteSession(sess.ID); err != nil {
		t.Fatalf("delete of an open session: %v", err)
	}
	if got := a.ActiveSessions(); len(got) != 0 {
		t.Errorf("the session is still open after DELETE: %v", got)
	}
	if _, err := c.GetSession(sess.ID); err == nil {
		t.Error("the session is still stored after DELETE")
	}
	if err := c.DeleteSession("no-such-session"); err != nil {
		t.Errorf("delete of an unknown id must be 204, got %v", err)
	}
}
