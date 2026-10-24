package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

var regSeq atomic.Int64

// registryServer serves an agent with three runtime-registered providers:
// a working one with two models, one whose model fetch fails, and (via
// settings) nothing else we depend on.
func registryServer(t *testing.T) (c *client.Client, good, broken string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	n := regSeq.Add(1)
	good, broken = fmt.Sprintf("reg-good-%d", n), fmt.Sprintf("reg-broken-%d", n)
	if err := agent.NewOpenAIProvider(good, "http://127.0.0.1:1",
		agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
			return []types.ModelMeta{
				{ID: "alpha", DisplayName: "Alpha", ContextWindow: 1000, MaxTokens: 100, Vision: true, InputPrice: 1.5,
					Sources: map[string]string{types.FieldPricing: types.SourceProvider}},
				{ID: "org/beta:7b", ContextWindow: 2000, MaxTokens: 200},
			}, nil
		})); err != nil {
		t.Fatal(err)
	}
	if err := agent.NewOpenAIProvider(broken, "http://127.0.0.1:1",
		agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
			return nil, errors.New("token expired: re-authenticate")
		})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(good); agent.UnregisterCustomProvider(broken) })
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return client.New(srv.Addr()), good, broken
}

func findModel(ms []client.Model, id string) *client.Model {
	for i := range ms {
		if ms[i].ID == id {
			return &ms[i]
		}
	}
	return nil
}

// The unfiltered listing keeps its contract (a failing provider contributes
// nothing, never fails the call) and now carries each model's dialect.
func TestListModelsKeepsContractAndAddsDialect(t *testing.T) {
	c, good, broken := registryServer(t)
	ms, err := c.ListModels()
	if err != nil {
		t.Fatalf("a failing provider must not fail the listing: %v", err)
	}
	a := findModel(ms, "alpha")
	if a == nil || a.Provider != good || a.Model != good+"/alpha" || a.Dialect != "openai" {
		t.Fatalf("alpha = %+v", a)
	}
	if a.ContextWindow != 1000 || !a.Vision || a.InputPrice != 1.5 || a.Sources[types.FieldPricing] != types.SourceProvider {
		t.Errorf("metadata not passed through: %+v", a)
	}
	for _, m := range ms {
		if m.Provider == broken {
			t.Errorf("a provider whose fetch failed must contribute nothing, got %+v", m)
		}
	}
}

// httpStatus GETs path on the server c talks to and returns status and body.
func httpStatus(t *testing.T, c *client.Client, path string) (int, string) {
	t.Helper()
	base := c.ProviderBaseURL("x") // http://host/api/providers/x/proxy
	base = base[:strings.Index(base, "/api/")]
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func TestListModelsFilterByProvider(t *testing.T) {
	c, good, broken := registryServer(t)
	ms, err := c.ListProviderModels(good)
	if err != nil || len(ms) != 2 {
		t.Fatalf("filtered: %v len=%d", err, len(ms))
	}
	for _, m := range ms {
		if m.Provider != good {
			t.Errorf("model of another provider leaked into the filter: %+v", m)
		}
	}
	for _, tc := range []struct {
		name, query string
		want        int
		body        string
	}{
		{"unknown provider", "no-such-provider", 404, ""},
		{"fetch fails", broken, 502, "token expired"},            // the provider's own message is surfaced
		{"inactive provider", "anthropic", 409, "not connected"}, // not connected in this isolated HOME
		{"empty provider", "", 400, ""},
	} {
		code, body := httpStatus(t, c, "/api/models?provider="+url.QueryEscape(tc.query))
		if code != tc.want || !strings.Contains(body, tc.body) {
			t.Errorf("%s: %d %s, want %d containing %q", tc.name, code, body, tc.want, tc.body)
		}
	}
	// the client turns those into errors
	if _, err := c.ListProviderModels(broken); err == nil || !strings.Contains(err.Error(), "token expired") {
		t.Errorf("client error for a failing provider = %v", err)
	}
}

func TestGetModel(t *testing.T) {
	c, good, broken := registryServer(t)
	m, err := c.GetModel(good, "alpha")
	if err != nil || m.ID != "alpha" || m.DisplayName != "Alpha" || m.Dialect != "openai" || m.ContextWindow != 1000 {
		t.Fatalf("alpha: %v %+v", err, m)
	}
	// ids with slashes and colons round-trip.
	m, err = c.GetModel(good, "org/beta:7b")
	if err != nil || m.ID != "org/beta:7b" || m.Model != good+"/org/beta:7b" {
		t.Fatalf("org/beta:7b: %v %+v", err, m)
	}
	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"unknown model", "/api/models/" + good + "/nope", 404},
		{"unknown provider", "/api/models/no-such-provider/alpha", 404},
		{"not connected", "/api/models/anthropic/claude", 409},
		{"fetch fails", "/api/models/" + broken + "/x", 502},
	} {
		if code, body := httpStatus(t, c, tc.path); code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, code, body, tc.want)
		}
		if _, err := c.GetModel(strings.Split(tc.path, "/")[3], strings.Join(strings.Split(tc.path, "/")[4:], "/")); err == nil {
			t.Errorf("%s: the client must return an error", tc.name)
		}
	}
}

// GET /api/providers now says WHY an active provider has no models, and gives
// the dialect and proxy root a proxy client needs.
func TestProvidersReportErrorDialectAndProxyPath(t *testing.T) {
	c, good, broken := registryServer(t)
	ps, err := c.GetProviders()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]client.Provider{}
	for _, p := range ps {
		by[p.Name] = p
	}
	g, b := by[good], by[broken]
	if g.Error != "" || g.ModelCount != 2 || g.Dialect != "openai" || g.ProxyPath != "/api/providers/"+good+"/proxy" {
		t.Errorf("good provider = %+v", g)
	}
	if !b.Active || b.ModelCount != 0 || !strings.Contains(b.Error, "token expired") {
		t.Errorf("a provider that is active but can't list models must say why: %+v", b)
	}
	for name, dialect := range map[string]string{
		"claude-oauth": "anthropic", "anthropic": "anthropic", "codex-oauth": "openai-responses",
		"openai": "openai", "minimax": "openai", "opencode-go": "openai", "ollama-cloud": "openai", "ollama": "ollama",
	} {
		if p, ok := by[name]; !ok || p.Dialect != dialect {
			t.Errorf("%s dialect = %q, want %q", name, by[name].Dialect, dialect)
		}
	}
	// an inactive provider is not an error
	if p := by["anthropic"]; p.Active || p.Error != "" {
		t.Errorf("inactive provider must not report an error: %+v", p)
	}
	// the proxy path is joinable with the server address into a working base URL
	if got := c.ProviderBaseURL(good); !strings.HasSuffix(got, g.ProxyPath) {
		t.Errorf("ProviderBaseURL %q does not end with proxy_path %q", got, g.ProxyPath)
	}
}

// The OpenAPI Model schema documents every field the API actually returns.
func TestOpenAPIModelSchemaCoversTheWire(t *testing.T) {
	var spec struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(openAPISpecJSON("127.0.0.1:0")), &spec); err != nil {
		t.Fatal(err)
	}
	// every json key a client.Model can carry
	b, _ := json.Marshal(client.Model{Dialect: "x", ModelMeta: types.ModelMeta{
		EffortLevels: map[string]bool{"low": true}, Sources: map[string]string{"a": "b"},
		ThinkingAdaptive: true, ThinkingLegacy: true,
	}})
	var wire map[string]any
	json.Unmarshal(b, &wire)
	for k := range wire {
		if _, ok := spec.Components.Schemas["Model"].Properties[k]; !ok {
			t.Errorf("Model schema does not document %q", k)
		}
	}
	// and the provider fields
	pb, _ := json.Marshal(providerInfo{})
	var pw map[string]any
	json.Unmarshal(pb, &pw)
	for k := range pw {
		if _, ok := spec.Components.Schemas["Provider"].Properties[k]; !ok {
			t.Errorf("Provider schema does not document %q", k)
		}
	}
	if _, ok := spec.Paths["/api/models/{provider}/{id}"]["get"]; !ok {
		t.Error("spec lacks GET /api/models/{provider}/{id}")
	}
}
