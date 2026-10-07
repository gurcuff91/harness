package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
)

// newMemoryTestServer runs the real router over an agent with a real SQLite
// memory store, and returns a real SDK client pointed at it — the whole
// path an SDK consumer uses, nothing mocked.
func newMemoryTestServer(t *testing.T, withMemory bool) (*client.Client, string) {
	t.Helper()
	opts := agent.AgentOptions{Store: store.NewInMemoryStore()}
	if withMemory {
		mem, err := memory.OpenSQLite(filepath.Join(t.TempDir(), "mem.db"))
		if err != nil {
			t.Fatal(err)
		}
		opts.Memory = mem
	}
	a := agent.New(opts)
	t.Cleanup(func() { a.Close() })
	ts := httptest.NewServer(NewServer(a, ServerOptions{Logger: logx.NewNilLogger()}).handler())
	t.Cleanup(ts.Close)
	return client.New(ts.URL), ts.URL
}

func apiStatus(t *testing.T, err error) int {
	t.Helper()
	var ae *client.Error
	if err == nil {
		return http.StatusOK
	}
	if !errors.As(err, &ae) {
		t.Fatalf("want *client.Error, got %T: %v", err, err)
	}
	// The client doesn't carry the HTTP code; the server's messages identify it
	// (TestMemoryEndpointsStatusCodes pins the real codes over raw HTTP).
	switch {
	case ae.Message == "memory is not enabled on this agent":
		return http.StatusServiceUnavailable
	case strings.HasPrefix(ae.Message, "memory not found"):
		return http.StatusNotFound
	}
	return -1
}

func TestMemoryCRUDThroughSDKClient(t *testing.T) {
	c, _ := newMemoryTestServer(t, true)

	// Create (project) → 201 + stored memory.
	m, err := c.PutMemory("/proj", "deploy-notes", "use make release")
	if err != nil {
		t.Fatalf("PutMemory create: %v", err)
	}
	if m.Slug != "deploy-notes" || m.CWD != "/proj" || m.Content != "use make release" || m.CreatedAt == 0 {
		t.Fatalf("created memory = %+v", m)
	}

	// Update → same identity, new content, created_at preserved.
	m2, err := c.PutMemory("/proj", "deploy-notes", "use make release + release-push")
	if err != nil {
		t.Fatalf("PutMemory update: %v", err)
	}
	if m2.Content != "use make release + release-push" || m2.CreatedAt != m.CreatedAt {
		t.Fatalf("updated memory = %+v (created_at was %d)", m2, m.CreatedAt)
	}

	// Read one.
	got, err := c.GetMemory("/proj", "deploy-notes")
	if err != nil || got.Content != m2.Content {
		t.Fatalf("GetMemory = %+v, %v", got, err)
	}

	// Global memory via the sentinel; isolated from the project scope.
	if _, err := c.PutMemory(client.MemoryGlobalCWD, "lang", "spanish"); err != nil {
		t.Fatalf("PutMemory global: %v", err)
	}
	g, err := c.GetMemory(client.MemoryGlobalCWD, "lang")
	if err != nil || g.CWD != client.MemoryGlobalCWD || g.Content != "spanish" {
		t.Fatalf("GetMemory global = %+v, %v", g, err)
	}
	if _, err := c.GetMemory("/proj", "lang"); apiStatus(t, err) != http.StatusNotFound {
		t.Fatalf("global memory must not resolve under a project cwd: %v", err)
	}

	// Search: a project sees its own + globals; globals-only sees just one.
	res, err := c.SearchMemories(client.MemoryQuery{CWD: "/proj"})
	if err != nil || res.Total != 2 {
		t.Fatalf("SearchMemories project = %+v, %v", res, err)
	}
	res, err = c.SearchMemories(client.MemoryQuery{CWD: client.MemoryGlobalCWD})
	if err != nil || res.Total != 1 || res.Results[0].Slug != "lang" {
		t.Fatalf("SearchMemories globals = %+v, %v", res, err)
	}
	res, err = c.SearchMemories(client.MemoryQuery{Query: "release", WithoutContent: true})
	if err != nil || res.Total != 1 || res.Results[0].Content != "" {
		t.Fatalf("SearchMemories full-text = %+v, %v", res, err)
	}

	// Slugs are path-escaped by the client: a "/" survives the round trip.
	if _, err := c.PutMemory("/proj", "a/b c", "odd slug"); err != nil {
		t.Fatalf("PutMemory odd slug: %v", err)
	}
	if odd, err := c.GetMemory("/proj", "a/b c"); err != nil || odd.Slug != "a/b c" {
		t.Fatalf("GetMemory odd slug = %+v, %v", odd, err)
	}

	// Delete → then 404 on read and on a second delete.
	st, err := c.DeleteMemory("/proj", "deploy-notes")
	if err != nil || st.Code != "deleted" {
		t.Fatalf("DeleteMemory = %+v, %v", st, err)
	}
	if _, err := c.GetMemory("/proj", "deploy-notes"); apiStatus(t, err) != http.StatusNotFound {
		t.Fatalf("read after delete: %v", err)
	}
	if _, err := c.DeleteMemory("/proj", "deploy-notes"); apiStatus(t, err) != http.StatusNotFound {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := c.DeleteMemory(client.MemoryGlobalCWD, "lang"); err != nil {
		t.Fatalf("DeleteMemory global: %v", err)
	}
}

// Raw HTTP status codes and validation (what a non-Go consumer sees).
func TestMemoryEndpointsStatusCodes(t *testing.T) {
	_, base := newMemoryTestServer(t, true)
	do := func(method, path, body string) int {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"create", "PUT", "/api/memories/x?cwd=/p", `{"content":"one"}`, http.StatusCreated},
		{"update", "PUT", "/api/memories/x?cwd=/p", `{"content":"two"}`, http.StatusOK},
		{"get", "GET", "/api/memories/x?cwd=/p", "", http.StatusOK},
		{"missing cwd", "GET", "/api/memories/x", "", http.StatusBadRequest},
		{"empty content", "PUT", "/api/memories/x?cwd=/p", `{"content":"  "}`, http.StatusUnprocessableEntity},
		{"bad json", "PUT", "/api/memories/x?cwd=/p", `{`, http.StatusBadRequest},
		{"get missing", "GET", "/api/memories/nope?cwd=/p", "", http.StatusNotFound},
		{"delete", "DELETE", "/api/memories/x?cwd=/p", "", http.StatusOK},
		{"delete missing", "DELETE", "/api/memories/x?cwd=/p", "", http.StatusNotFound},
	}
	for _, c := range cases {
		if got := do(c.method, c.path, c.body); got != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, c.path, got, c.want)
		}
	}
}

// An agent without memory: single-memory endpoints say so (503); the listing
// keeps its historical empty 200.
func TestMemoryEndpointsWithoutMemory(t *testing.T) {
	c, _ := newMemoryTestServer(t, false)
	if _, err := c.GetMemory("/p", "x"); apiStatus(t, err) != http.StatusServiceUnavailable {
		t.Fatalf("GetMemory without memory: %v", err)
	}
	if _, err := c.PutMemory("/p", "x", "c"); apiStatus(t, err) != http.StatusServiceUnavailable {
		t.Fatalf("PutMemory without memory: %v", err)
	}
	res, err := c.SearchMemories(client.MemoryQuery{})
	if err != nil || res.Total != 0 {
		t.Fatalf("SearchMemories without memory = %+v, %v", res, err)
	}
}

// The client's sentinel must never drift from the store's.
func TestClientMemoryGlobalCWDMatchesStore(t *testing.T) {
	if client.MemoryGlobalCWD != memory.GlobalCWD {
		t.Fatalf("client.MemoryGlobalCWD = %q, memory.GlobalCWD = %q", client.MemoryGlobalCWD, memory.GlobalCWD)
	}
}

// The hand-written OpenAPI spec must stay valid JSON, document the memory
// CRUD, and only reference schemas that exist.
func TestOpenAPISpecDocumentsMemoryCRUD(t *testing.T) {
	var spec struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	raw := openAPISpecJSON("127.0.0.1:0")
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("spec is not valid JSON: %v", err)
	}
	for _, m := range []string{"get", "put", "delete"} {
		if _, ok := spec.Paths["/api/memories/{slug}"][m]; !ok {
			t.Errorf("spec lacks %s /api/memories/{slug}", strings.ToUpper(m))
		}
	}
	for _, ref := range regexp.MustCompile(`#/components/schemas/(\w+)`).FindAllStringSubmatch(raw, -1) {
		if _, ok := spec.Components.Schemas[ref[1]]; !ok {
			t.Errorf("dangling $ref to schema %q", ref[1])
		}
	}
}
