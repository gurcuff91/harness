package server

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// toggleProvider is a provider that can be "disconnected" by a test.
type toggleProvider struct {
	name string
	on   atomic.Bool
	ids  []string
}

func newToggle(name string, ids ...string) *toggleProvider {
	p := &toggleProvider{name: name, ids: ids}
	p.on.Store(true)
	return p
}
func (p *toggleProvider) CompleteStream(_ context.Context, _ *types.Request, cb types.StreamCallback) (*types.Response, error) {
	if cb != nil {
		cb(types.StreamEvent{Type: types.StreamTextDelta, Delta: "ok"})
	}
	return &types.Response{Text: "ok", Message: types.NewAssistantToolCallMessage("ok", "", "", nil)}, nil
}
func (p *toggleProvider) Name() string        { return p.name }
func (p *toggleProvider) DisplayName() string { return p.name }
func (p *toggleProvider) Description() string { return "fake" }
func (p *toggleProvider) IsActive() bool      { return p.on.Load() }
func (p *toggleProvider) Models() []types.ModelMeta {
	var out []types.ModelMeta
	for _, id := range p.ids {
		out = append(out, types.ModelMeta{ID: id, ContextWindow: 1000})
	}
	return out
}
func (p *toggleProvider) FetchModels() ([]types.ModelMeta, error) { return p.Models(), nil }
func (p *toggleProvider) ModelMeta(id string) *types.ModelMeta {
	for _, m := range p.ids {
		if m == id {
			return &types.ModelMeta{ID: id, ContextWindow: 1000}
		}
	}
	return nil
}
func (p *toggleProvider) CredentialType() types.CredentialType { return types.CredTypeNone }
func (p *toggleProvider) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: types.CredTypeNone}, nil
}
func (p *toggleProvider) ActivationSource() providers.ActivationSource {
	return providers.ActivationAuto
}
func (p *toggleProvider) Connect(types.Credentials) error { return nil }
func (p *toggleProvider) Disconnect() error               { return nil }
func (p *toggleProvider) Dialect() string                 { return "openai" }
func (p *toggleProvider) Upstream() (providers.Upstream, error) {
	return providers.Upstream{}, providers.ErrNoUpstream
}

func fallbackServer(t *testing.T, ps ...*toggleProvider) (*Server, *client.Client, *config.SettingsManager) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), nil))
	providers.EnsureRegistry()
	list := make([]providers.Provider, len(ps))
	for i, p := range ps {
		list[i] = p
	}
	t.Cleanup(providers.SetForTest(list))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, client.New(srv.Addr()), config.GetSettingsManager()
}

// The bug as the user met it, over HTTP: a closed session whose provider was
// disconnected is resumed — it must succeed, on the default model, and the
// response must say what happened.
func TestResumeEndpointFallsBackAndReportsIt(t *testing.T) {
	saved, other := newToggle("rf-saved", "m1"), newToggle("rf-other", "o1")
	srv, c, sm := fallbackServer(t, saved, other)
	sm.SetActiveModel("rf-other/o1")

	sess, err := c.CreateSession("rf-saved/m1", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ModelFallback != nil {
		t.Errorf("a fresh session has no fallback: %+v", sess.ModelFallback)
	}
	if _, err := c.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	saved.on.Store(false) // the provider is disconnected

	got, err := c.ResumeSession(sess.ID)
	if err != nil {
		t.Fatalf("resume must succeed through the fallback: %v", err)
	}
	if got.Model != "rf-other/o1" {
		t.Errorf("model = %q, want rf-other/o1", got.Model)
	}
	f := got.ModelFallback
	if f == nil || f.From != "rf-saved/m1" || f.To != "rf-other/o1" || f.Reason == "" {
		t.Errorf("model_fallback = %+v", f)
	}
	// raw JSON carries it too (this is the contract other clients read)
	code, _, body := rawGet(t, srv, "/api/sessions/"+sess.ID)
	if code != 200 || !strings.Contains(string(body), `"model_fallback":{"from":"rf-saved/m1","to":"rf-other/o1"`) {
		t.Errorf("GET /api/sessions/{id}: %d %s", code, body)
	}
	// the session actually runs on the new model
	if out, err := c.Ask(sess.ID, "hi", 30*time.Second); err != nil || !strings.Contains(out, "ok") {
		t.Errorf("a turn on the fallback model: %q %v", out, err)
	}
}

// A normal resume carries no model_fallback at all (omitted, not null).
func TestResumeEndpointOmitsFallbackWhenNothingChanged(t *testing.T) {
	p := newToggle("rf-saved", "m1")
	srv, c, _ := fallbackServer(t, p)
	sess, _ := c.CreateSession("rf-saved/m1", t.TempDir(), "")
	c.CloseSession(sess.ID)

	req, _ := http.NewRequest("POST", "http://"+srv.Addr()+"/api/sessions/"+sess.ID+"/resume", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf)
		b.Write(buf[:n])
		if e != nil {
			break
		}
	}
	if resp.StatusCode != 200 || strings.Contains(b.String(), "model_fallback") {
		t.Errorf("%d %s — an ordinary resume must not mention model_fallback", resp.StatusCode, b.String())
	}
}

// Nothing to fall back to: refused with a 409 that says what to do — the one case
// where a session may not be reopened.
func TestResumeEndpointRefusesWithAnActionable409(t *testing.T) {
	saved := newToggle("rf-saved", "m1")
	srv, c, _ := fallbackServer(t, saved)
	sess, _ := c.CreateSession("rf-saved/m1", t.TempDir(), "")
	c.CloseSession(sess.ID)
	saved.on.Store(false)

	req, _ := http.NewRequest("POST", "http://"+srv.Addr()+"/api/sessions/"+sess.ID+"/resume", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf)
		b.Write(buf[:n])
		if e != nil {
			break
		}
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", resp.StatusCode, b.String())
	}
	for _, want := range []string{"rf-saved/m1", "connect a provider"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("error %q lacks %q", b.String(), want)
		}
	}
	if _, err := c.ResumeSession(sess.ID); err == nil {
		t.Error("the client must surface the refusal as an error")
	}
}

// Fork follows the same rule over HTTP.
func TestForkEndpointFallsBackAndRefusesLikeResume(t *testing.T) {
	saved, other := newToggle("rf-saved", "m1"), newToggle("rf-other", "o1")
	srv, c, sm := fallbackServer(t, saved, other)
	sm.SetActiveModel("rf-other/o1")
	sess, _ := c.CreateSession("rf-saved/m1", t.TempDir(), "")
	c.CloseSession(sess.ID)
	saved.on.Store(false)

	fork, err := c.ForkSession(sess.ID)
	if err != nil {
		t.Fatalf("fork must succeed through the fallback: %v", err)
	}
	if fork.Model != "rf-other/o1" || fork.ModelFallback == nil || fork.ModelFallback.From != "rf-saved/m1" {
		t.Errorf("fork = model %q fallback %+v", fork.Model, fork.ModelFallback)
	}
	other.on.Store(false)
	req, _ := http.NewRequest("POST", "http://"+srv.Addr()+"/api/sessions/"+sess.ID+"/fork", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("fork with nothing available = %d, want 409", resp.StatusCode)
	}
}

// The OpenAPI document describes the new response field.
func TestOpenAPIDocumentsModelFallback(t *testing.T) {
	spec := openAPISpecJSON("127.0.0.1:0")
	if !strings.Contains(spec, "model_fallback") {
		t.Error("the spec does not document model_fallback")
	}
}
