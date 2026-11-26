package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/schedule"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// admStart isolates the process-global settings/credentials stores, starts the
// binary and initializes it with in-memory sessions and a temp SQLite memory.
func admStart(t *testing.T, withMemory bool) *client {
	t.Helper()
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	c := start(t)
	cfg := memStoreConfig()
	if withMemory {
		cfg.Memory = &KindConfig{Kind: "sqlite", Path: filepath.Join(t.TempDir(), "m.db")}
	}
	c.initialize(cfg)
	return c
}

// admFake is a provider the test controls.
type admFake struct {
	name     string
	mu       sync.Mutex
	active   bool
	ids      []string
	fetchErr error
	got      types.Credentials
	quota    func() (*types.Quota, error)
}

func (p *admFake) CompleteStream(context.Context, *types.Request, types.StreamCallback) (*types.Response, error) {
	return nil, errors.New("unused")
}
func (p *admFake) Name() string        { return p.name }
func (p *admFake) DisplayName() string { return "Fake " + p.name }
func (p *admFake) Description() string { return "fake" }
func (p *admFake) IsActive() bool      { p.mu.Lock(); defer p.mu.Unlock(); return p.active }
func (p *admFake) Models() []types.ModelMeta {
	if p.fetchErr != nil {
		return nil
	}
	var out []types.ModelMeta
	for _, id := range p.ids {
		out = append(out, types.ModelMeta{ID: id, ContextWindow: 1000})
	}
	return out
}
func (p *admFake) FetchModels() ([]types.ModelMeta, error) { return p.Models(), p.fetchErr }
func (p *admFake) ModelMeta(string) *types.ModelMeta       { return nil }
func (p *admFake) Dialect() string                         { return "openai" }
func (p *admFake) Upstream() (providers.Upstream, error) {
	return providers.Upstream{}, providers.ErrNoUpstream
}
func (p *admFake) CredentialType() types.CredentialType { return types.CredTypeAPIKey }
func (p *admFake) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: p.CredentialType()}, nil
}
func (p *admFake) ActivationSource() providers.ActivationSource { return providers.ActivationAuto }
func (p *admFake) Connect(c types.Credentials) error {
	p.mu.Lock()
	p.got, p.active = c, true
	p.mu.Unlock()
	return nil
}
func (p *admFake) Disconnect() error { p.mu.Lock(); p.active = false; p.mu.Unlock(); return nil }

// admQuotaFake also reports a quota.
type admQuotaFake struct{ *admFake }

func (p admQuotaFake) Quota(context.Context) (*types.Quota, error) { return p.quota() }

func admUseProviders(t *testing.T, ps ...providers.Provider) {
	t.Helper()
	t.Cleanup(providers.SetForTest(ps))
	for _, p := range ps {
		name := p.Name()
		providers.ForgetQuota(name)
		t.Cleanup(func() { providers.ForgetQuota(name) })
	}
}

func admKind(je interface{ Error() string }) string { return "" }

// admNewSession opens a session through the protocol and returns its id.
func admNewSession(t *testing.T, c *client, model, name string) string {
	t.Helper()
	var d struct {
		ID string `json:"id"`
	}
	c.must("session/new", map[string]any{"cwd": "/proj", "model": model, "name": name}, &d)
	return d.ID
}

// admPromptedSession opens a session on the fake model and runs one turn so
// the stored log has messages; it returns the id.
func admPromptedSession(t *testing.T, c *client, name string) string {
	t.Helper()
	model := registerModel(t, &fakeModel{})
	id := admNewSession(t, c, model, name)
	sess, err := c.srv.session(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.PromptAndWait(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	return id
}

// ── sessions ─────────────────────────────────────────────────────────────

func TestAdminSessionsLifecycle(t *testing.T) {
	c := admStart(t, false)
	id := admPromptedSession(t, c, "alpha")

	var list AdminSessionsListResult
	c.must("admin/sessions/list", nil, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].ID != id {
		t.Fatalf("list = %+v", list)
	}
	c.must("admin/sessions/list", AdminSessionsListParams{CWD: "/nowhere"}, &list)
	raw, _ := c.call("admin/sessions/list", AdminSessionsListParams{CWD: "/nowhere"})
	if len(list.Sessions) != 0 || !strings.Contains(string(raw), `"sessions":[]`) {
		t.Fatalf("empty list must be [] not null: %s", raw)
	}

	var meta store.SessionMeta
	c.must("admin/sessions/get", SessionIDParams{ID: id}, &meta)
	if meta.ID != id || meta.Name != "alpha" || meta.CWD != "/proj" {
		t.Fatalf("get = %+v", meta)
	}
	c.fail("admin/sessions/get", SessionIDParams{ID: "ghost"}, CodeNotFound, "not_found")
	c.fail("admin/sessions/get", SessionIDParams{}, CodeInvalid, "invalid")

	var msgs AdminSessionsMessagesResult
	c.must("admin/sessions/messages", AdminSessionsMessagesParams{ID: id}, &msgs)
	if msgs.Total != 2 || len(msgs.Messages) != 2 || msgs.From != 0 {
		t.Fatalf("messages = from %d total %d n %d", msgs.From, msgs.Total, len(msgs.Messages))
	}
	one := 1
	c.must("admin/sessions/messages", AdminSessionsMessagesParams{ID: id, From: &one}, &msgs)
	if msgs.From != 1 || len(msgs.Messages) != 1 || msgs.Total != 2 {
		t.Fatalf("window from 1 = %+v", msgs)
	}
	past := 99
	raw, _ = c.call("admin/sessions/messages", AdminSessionsMessagesParams{ID: id, From: &past})
	if !strings.Contains(string(raw), `"messages":[]`) {
		t.Fatalf("empty window must be []: %s", raw)
	}
	c.fail("admin/sessions/messages", AdminSessionsMessagesParams{ID: "ghost"}, CodeNotFound, "not_found")

	var fn AdminSessionsExportFileNameResult
	c.must("admin/sessions/export_file_name", SessionIDParams{ID: id}, &fn)
	if want := "alpha-" + id[:8] + ".export.jsonl"; fn.FileName != want {
		t.Fatalf("file name = %q, want %q", fn.FileName, want)
	}
	c.fail("admin/sessions/export_file_name", SessionIDParams{ID: "ghost"}, CodeNotFound, "not_found")
}

func TestAdminSessionsMessagesWindowValidation(t *testing.T) {
	c := admStart(t, false)
	id := admPromptedSession(t, c, "w")
	zero, one, neg := 0, 1, -1
	c.fail("admin/sessions/messages", AdminSessionsMessagesParams{ID: id, From: &zero, Compactions: &one}, CodeInvalid, "invalid")
	c.fail("admin/sessions/messages", AdminSessionsMessagesParams{ID: id, From: &neg}, CodeInvalid, "invalid")
	c.fail("admin/sessions/messages", AdminSessionsMessagesParams{ID: id, Compactions: &zero}, CodeInvalid, "invalid")
	var r AdminSessionsMessagesResult
	c.must("admin/sessions/messages", AdminSessionsMessagesParams{ID: id, Compactions: &one}, &r) // no checkpoint: the whole log
	if r.Total != 2 || r.From != 0 {
		t.Fatalf("compactions window = %+v", r)
	}
}

func TestAdminSessionsOpenSessionIsProtected(t *testing.T) {
	c := admStart(t, false)
	id := admPromptedSession(t, c, "busy")

	c.fail("admin/sessions/rename", AdminSessionsRenameParams{ID: id, Name: "x"}, CodeConflict, "session_active")
	c.fail("admin/sessions/delete", SessionIDParams{ID: id}, CodeConflict, "session_active")

	c.must("session/close", SessionIDParams{ID: id}, nil)
	c.must("admin/sessions/rename", AdminSessionsRenameParams{ID: id, Name: "renamed"}, nil)
	var meta store.SessionMeta
	c.must("admin/sessions/get", SessionIDParams{ID: id}, &meta)
	if meta.Name != "renamed" {
		t.Fatalf("name = %q", meta.Name)
	}
	c.must("admin/sessions/delete", SessionIDParams{ID: id}, nil)
	c.fail("admin/sessions/get", SessionIDParams{ID: id}, CodeNotFound, "not_found")
	c.fail("admin/sessions/delete", SessionIDParams{ID: id}, CodeNotFound, "not_found")
	c.fail("admin/sessions/rename", AdminSessionsRenameParams{ID: id, Name: "z"}, CodeNotFound, "not_found")
}

func TestAdminSessionsExportContentAndFile(t *testing.T) {
	c := admStart(t, false)
	id := admPromptedSession(t, c, "exp")
	c.must("session/close", SessionIDParams{ID: id}, nil)

	// No path: the JSONL comes back as a string, the admin's own export.
	var inline AdminSessionsExportResult
	c.must("admin/sessions/export", AdminSessionsExportParams{ID: id}, &inline)
	if inline.Path != "" || inline.Messages != 2 {
		t.Fatalf("inline = %+v", inline)
	}
	lines := strings.Split(strings.TrimSpace(inline.Content), "\n")
	if len(lines) != 3 {
		t.Fatalf("export has %d lines, want header + 2", len(lines))
	}
	var head admin.ExportHeader
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil || head.Format != admin.ExportFormat || head.Meta.ID != id {
		t.Fatalf("header = %+v %v", head, err)
	}

	// With a path in a directory that does not exist yet.
	path := filepath.Join(t.TempDir(), "deep", "dir", "out.export.jsonl")
	var file AdminSessionsExportResult
	c.must("admin/sessions/export", AdminSessionsExportParams{ID: id, Path: path}, &file)
	if file.Path != path || file.Messages != 2 || file.Content != "" {
		t.Fatalf("file result = %+v", file)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", st.Mode().Perm())
	}
	if dst, _ := os.Stat(filepath.Dir(path)); dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", dst.Mode().Perm())
	}
	body, _ := os.ReadFile(path)
	fl := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(fl) != 3 || fl[1] != lines[1] || fl[2] != lines[2] {
		t.Fatalf("file content differs from the admin export:\n%s", body)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}

	// Round trip through session/import.
	var imp struct {
		ID string `json:"id"`
	}
	c.must("session/import", SessionImportParams{Path: path}, &imp)
	if imp.ID == "" || imp.ID == id {
		t.Fatalf("import id = %q", imp.ID)
	}
	var m AdminSessionsMessagesResult
	c.must("admin/sessions/messages", AdminSessionsMessagesParams{ID: imp.ID}, &m)
	if m.Total != 2 {
		t.Fatalf("imported total = %d", m.Total)
	}

	// Errors.
	c.fail("admin/sessions/export", AdminSessionsExportParams{ID: id, Path: filepath.Join(t.TempDir(), "x.jsonl")}, CodeInvalid, "invalid")
	c.fail("admin/sessions/export", AdminSessionsExportParams{ID: id, Path: filepath.Join(t.TempDir(), "x.export.json")}, CodeInvalid, "invalid")
	c.fail("admin/sessions/export", AdminSessionsExportParams{ID: "ghost"}, CodeNotFound, "not_found")
	c.fail("admin/sessions/export", AdminSessionsExportParams{ID: "ghost", Path: path}, CodeNotFound, "not_found")
}

func TestAdminSessionsExportOfAnOpenSessionUsesTheLiveMeta(t *testing.T) {
	c := admStart(t, false)
	id := admPromptedSession(t, c, "live")
	var r AdminSessionsExportResult
	c.must("admin/sessions/export", AdminSessionsExportParams{ID: id}, &r)
	if r.Messages != 2 || !strings.Contains(r.Content, `"name":"live"`) {
		t.Fatalf("open session export = %+v", r)
	}
}

// ── providers ────────────────────────────────────────────────────────────

func TestAdminProvidersListModelsConnect(t *testing.T) {
	good := &admFake{name: "good", ids: []string{"alpha", "org/beta"}, active: true}
	off := &admFake{name: "off", ids: []string{"x"}}
	broken := &admFake{name: "broken", ids: []string{"x"}, active: true, fetchErr: errors.New("token expired")}
	c := admStart(t, false)
	admUseProviders(t, good, off, broken)

	var list AdminProvidersListResult
	c.must("admin/providers/list", nil, &list)
	if len(list.Providers) != 3 || list.Providers[0].Name != "good" || list.Providers[0].ModelCount != 2 {
		t.Fatalf("list = %+v", list)
	}
	if list.Providers[2].Error != "token expired" {
		t.Fatalf("broken = %+v", list.Providers[2])
	}

	var ms AdminProvidersModelsResult
	c.must("admin/providers/models", nil, &ms)
	if len(ms.Models) != 2 {
		t.Fatalf("all models = %+v", ms)
	}
	c.must("admin/providers/models", AdminProvidersModelsParams{Provider: "good"}, &ms)
	if len(ms.Models) != 2 || ms.Models[0].Model != "good/alpha" {
		t.Fatalf("one provider = %+v", ms)
	}
	c.fail("admin/providers/models", AdminProvidersModelsParams{Provider: "nope"}, CodeNotFound, "not_found")
	c.fail("admin/providers/models", AdminProvidersModelsParams{Provider: "off"}, CodeNotConnected, "not_connected")
	c.fail("admin/providers/models", AdminProvidersModelsParams{Provider: "broken"}, CodeUpstream, "upstream")

	var m types.ModelListing
	c.must("admin/providers/model", AdminProvidersModelParams{Provider: "good", ID: "org/beta"}, &m)
	if m.Model != "good/org/beta" {
		t.Fatalf("model = %+v", m)
	}
	c.fail("admin/providers/model", AdminProvidersModelParams{Provider: "good", ID: "zzz"}, CodeNotFound, "not_found")
	c.fail("admin/providers/model", AdminProvidersModelParams{Provider: "nope", ID: "a"}, CodeNotFound, "not_found")

	var ct AdminProvidersCredentialTypeResult
	c.must("admin/providers/credential_type", AdminProviderNameParams{Name: "good"}, &ct)
	if ct.CredentialType != types.CredTypeAPIKey {
		t.Fatalf("credential type = %q", ct.CredentialType)
	}
	c.fail("admin/providers/credential_type", AdminProviderNameParams{Name: "nope"}, CodeNotFound, "not_found")

	// connect passes the credentials on, and the listing never shows them.
	const secret = "sk-very-secret-value"
	var cr AdminProvidersConnectResult
	c.must("admin/providers/connect", map[string]any{"name": "off", "api_key": secret, "account_id": "acc"}, &cr)
	if cr.Models != 1 {
		t.Fatalf("connect = %+v", cr)
	}
	off.mu.Lock()
	got := off.got
	off.mu.Unlock()
	if got.APIKey != secret || got.AccountID != "acc" || got.Type != types.CredTypeAPIKey {
		t.Fatalf("creds reached the provider as %+v", got)
	}
	raw, je := c.call("admin/providers/list", nil)
	if je != nil || strings.Contains(string(raw), secret) || strings.Contains(string(raw), `"api_key":`) || strings.Contains(string(raw), `"access_token"`) {
		t.Fatalf("list leaks credentials: %s", raw)
	}
	raw, _ = c.call("admin/providers/models", nil)
	if strings.Contains(string(raw), secret) {
		t.Fatal("models leaks credentials")
	}
	c.fail("admin/providers/connect", map[string]any{"name": "nope"}, CodeNotFound, "not_found")
	c.fail("admin/providers/connect", map[string]any{"name": "off", "bogus": 1}, -32602, "")

	c.must("admin/providers/disconnect", AdminProviderNameParams{Name: "off"}, nil)
	c.fail("admin/providers/models", AdminProvidersModelsParams{Provider: "off"}, CodeNotConnected, "not_connected")
	c.fail("admin/providers/disconnect", AdminProviderNameParams{Name: "nope"}, CodeNotFound, "not_found")
}

func TestAdminProvidersQuota(t *testing.T) {
	q := admQuotaFake{&admFake{name: "q", ids: []string{"a"}, active: true}}
	q.quota = func() (*types.Quota, error) {
		return &types.Quota{Provider: "q", FetchedAt: time.Now().UTC(), Windows: []types.QuotaWindow{{Name: "weekly", UsedPercent: 10}}}, nil
	}
	plain := &admFake{name: "plain"}
	c := admStart(t, false)
	admUseProviders(t, q, plain)
	t.Cleanup(providers.SetQuotaRulesForTest(time.Hour, 0, time.Minute))

	var r AdminProvidersQuotaResult
	c.must("admin/providers/quota", AdminProvidersQuotaParams{Name: "q"}, &r)
	if r.Quota == nil || r.Quota.Provider != "q" || len(r.Quota.Windows) != 1 || r.RetryAfterSeconds != 0 {
		t.Fatalf("quota = %+v", r)
	}
	c.fail("admin/providers/quota", AdminProvidersQuotaParams{Name: "nope"}, CodeNotFound, "not_found")
	c.fail("admin/providers/quota", AdminProvidersQuotaParams{Name: "plain"}, CodeNotFound, "not_found")

	providers.ForgetQuota("q")
	q.quota = func() (*types.Quota, error) { return nil, providers.ErrNoUpstream }
	c.fail("admin/providers/quota", AdminProvidersQuotaParams{Name: "q", Refresh: true}, CodeNotConnected, "not_connected")

	// A Retry-After of 1.5 s is advertised as 2: rounded UP, like the HTTP header.
	providers.ForgetQuota("q")
	q.quota = func() (*types.Quota, error) {
		return nil, &providers.QuotaHTTPError{Provider: "q", Status: 429, RetryAfter: 1500 * time.Millisecond}
	}
	je := c.fail("admin/providers/quota", AdminProvidersQuotaParams{Name: "q", Refresh: true}, CodeUpstream, "upstream")
	d, _ := je.Data.(map[string]any)
	if d["retry_after_seconds"] != float64(2) {
		t.Fatalf("error data = %v, want retry_after_seconds 2", je.Data)
	}
}

func TestAdmRetrySecondsRoundsUp(t *testing.T) {
	for in, want := range map[time.Duration]int{
		-time.Second: 0, 0: 0, time.Nanosecond: 1, 999 * time.Millisecond: 1,
		time.Second: 1, 1001 * time.Millisecond: 2, 1500 * time.Millisecond: 2, 30 * time.Second: 30,
	} {
		if got := admRetrySeconds(in); got != want {
			t.Errorf("admRetrySeconds(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestAdminProvidersOAuth(t *testing.T) {
	c := admStart(t, false)
	var st AdminProvidersOAuthStartResult
	c.must("admin/providers/oauth_start", AdminProvidersOAuthStartParams{Provider: "claude-oauth"}, &st)
	if !strings.HasPrefix(st.AuthURL, "https://") || st.VerifierCode == "" {
		t.Fatalf("start = %+v", st)
	}
	raw, _ := c.call("admin/providers/oauth_start", AdminProvidersOAuthStartParams{Provider: "claude-oauth"})
	if !strings.Contains(string(raw), `"auth_url"`) || !strings.Contains(string(raw), `"verifier_code"`) {
		t.Fatalf("wire names: %s", raw)
	}
	c.fail("admin/providers/oauth_start", AdminProvidersOAuthStartParams{Provider: "openai"}, CodeNotFound, "not_found")
	c.fail("admin/providers/oauth_exchange", AdminProvidersOAuthExchangeParams{Provider: "openai", ExchangeCode: "c", VerifierCode: "v"}, CodeNotFound, "not_found")
	c.fail("admin/providers/oauth_exchange", AdminProvidersOAuthExchangeParams{Provider: "claude-oauth", ExchangeCode: "c"}, CodeInvalid, "invalid")
}

func TestAdminProvidersCustomAndRegistered(t *testing.T) {
	c := admStart(t, false)
	admUseProviders(t)

	var lc AdminProvidersListCustomResult
	raw, _ := c.call("admin/providers/list_custom", nil)
	if !strings.Contains(string(raw), `"providers":{}`) {
		t.Fatalf("empty custom map must be {}: %s", raw)
	}
	c.must("admin/providers/put_custom", AdminProvidersPutCustomParams{Name: "mine", Provider: types.CustomProvider{Type: "openai", URL: "http://127.0.0.1:1", Headers: map[string]string{"X-Api-Key": "k"}}}, nil)
	c.must("admin/providers/list_custom", nil, &lc)
	if lc.Providers["mine"].URL != "http://127.0.0.1:1" {
		t.Fatalf("list_custom = %+v", lc)
	}
	c.fail("admin/providers/put_custom", AdminProvidersPutCustomParams{Name: "bad", Provider: types.CustomProvider{Type: "nonsense", URL: "x"}}, CodeInvalid, "unprocessable")

	var rl AdminProvidersReloadCustomResult
	c.must("admin/providers/reload_custom", nil, &rl)
	if rl.Status != "reloaded" || rl.Message != "" {
		t.Fatalf("reload = %+v", rl)
	}
	var list AdminProvidersListResult
	c.must("admin/providers/list", nil, &list)
	found := false
	for _, p := range list.Providers {
		found = found || p.Name == "mine"
	}
	if !found {
		t.Fatal("reload must register the declared provider")
	}

	// A declared name clashing with a programmatic provider is NOT an error.
	c.must("admin/providers/register_openai", AdminProvidersRegisterOpenAIParams{Name: "clash", URL: "http://127.0.0.1:1", Display: "Clash", Headers: map[string]string{"X-Api-Key": "k"}, ReasoningSplit: true}, nil)
	c.must("admin/providers/put_custom", AdminProvidersPutCustomParams{Name: "clash", Provider: types.CustomProvider{Type: "openai", URL: "http://127.0.0.1:2"}}, nil)
	c.must("admin/providers/reload_custom", nil, &rl)
	if rl.Status != "reloaded_with_conflicts" || !strings.Contains(rl.Message, "clash") {
		t.Fatalf("conflicting reload = %+v", rl)
	}

	c.must("admin/providers/register_anthropic", AdminProvidersRegisterAnthropicParams{Name: "anth", URL: "http://127.0.0.1:1", Display: "A"}, nil)
	c.fail("admin/providers/register_anthropic", AdminProvidersRegisterAnthropicParams{Name: "anthropic", URL: "http://x"}, CodeInvalid, "invalid")
	c.fail("admin/providers/register_openai", AdminProvidersRegisterOpenAIParams{Name: "clash", URL: "http://127.0.0.1:1"}, CodeConflict, "conflict")

	c.must("admin/providers/unregister", AdminProviderNameParams{Name: "clash"}, nil)
	c.must("admin/providers/unregister", AdminProviderNameParams{Name: "anth"}, nil)
	c.fail("admin/providers/unregister", AdminProviderNameParams{Name: "clash"}, CodeNotFound, "provider_not_found")
	c.fail("admin/providers/unregister", AdminProviderNameParams{Name: "mine"}, CodeConflict, "provider_not_removable")

	c.fail("admin/providers/delete_custom", AdminProviderNameParams{Name: "ghost"}, CodeNotFound, "not_found")
	c.must("admin/providers/delete_custom", AdminProviderNameParams{Name: "mine"}, nil)
	c.must("admin/providers/delete_custom", AdminProviderNameParams{Name: "clash"}, nil)
	c.must("admin/providers/reload_custom", nil, &rl)
	lc = AdminProvidersListCustomResult{}
	c.must("admin/providers/list_custom", nil, &lc)
	if len(lc.Providers) != 0 {
		t.Fatalf("custom left: %+v", lc)
	}
}

// ── settings, mcp, schedules ─────────────────────────────────────────────

func TestAdminSettings(t *testing.T) {
	c := admStart(t, false)
	var v admin.SettingsValues
	c.must("admin/settings/get", nil, &v)
	if v.ThinkingLevel != "off" || v.ActiveModel != "" {
		t.Fatalf("defaults = %+v", v)
	}
	c.must("admin/settings/set_active_model", AdminSettingsSetActiveModelParams{Model: "p/m"}, &v)
	if v.ActiveModel != "p/m" {
		t.Fatalf("after set model = %+v", v)
	}
	c.must("admin/settings/set_thinking_level", AdminSettingsSetThinkingLevelParams{Level: "high"}, &v)
	if v.ThinkingLevel != "high" || v.ActiveModel != "p/m" {
		t.Fatalf("after set thinking = %+v", v)
	}
	var again admin.SettingsValues
	c.must("admin/settings/get", nil, &again)
	if again != v {
		t.Fatalf("get = %+v, want %+v", again, v)
	}
	c.fail("admin/settings/set_thinking_level", AdminSettingsSetThinkingLevelParams{Level: "bogus"}, CodeInvalid, "unprocessable")
}

func TestAdminMCP(t *testing.T) {
	c := admStart(t, false)
	raw, _ := c.call("admin/mcp/list", nil)
	if !strings.Contains(string(raw), `"servers":{}`) {
		t.Fatalf("empty list must be {}: %s", raw)
	}
	srv := types.MCPServer{Command: "echo", Args: []string{"hi"}}
	c.must("admin/mcp/put", AdminMCPPutParams{Name: "e", Server: srv}, nil)
	var l AdminMCPListResult
	c.must("admin/mcp/list", nil, &l)
	if l.Servers["e"].Command != "echo" {
		t.Fatalf("list = %+v", l)
	}
	var got types.MCPServer
	c.must("admin/mcp/get", AdminMCPNameParams{Name: "e"}, &got)
	if got.Command != "echo" || got.Disabled {
		t.Fatalf("get = %+v", got)
	}
	c.must("admin/mcp/set_enabled", AdminMCPSetEnabledParams{Name: "e", Enabled: false}, nil)
	c.must("admin/mcp/get", AdminMCPNameParams{Name: "e"}, &got)
	if !got.Disabled || got.Command != "echo" {
		t.Fatalf("after disable = %+v", got)
	}
	c.must("admin/mcp/set_enabled", AdminMCPSetEnabledParams{Name: "e", Enabled: true}, nil)
	got = types.MCPServer{}
	c.must("admin/mcp/get", AdminMCPNameParams{Name: "e"}, &got)
	if got.Disabled {
		t.Fatal("still disabled")
	}
	c.fail("admin/mcp/put", AdminMCPPutParams{Name: "bad", Server: types.MCPServer{}}, CodeInvalid, "unprocessable")
	c.fail("admin/mcp/get", AdminMCPNameParams{Name: "ghost"}, CodeNotFound, "not_found")
	c.fail("admin/mcp/set_enabled", AdminMCPSetEnabledParams{Name: "ghost", Enabled: true}, CodeNotFound, "not_found")
	c.fail("admin/mcp/delete", AdminMCPNameParams{Name: "ghost"}, CodeNotFound, "not_found")
	c.must("admin/mcp/delete", AdminMCPNameParams{Name: "e"}, nil)
	c.fail("admin/mcp/get", AdminMCPNameParams{Name: "e"}, CodeNotFound, "not_found")
}

func TestAdminSchedules(t *testing.T) {
	c := admStart(t, false)
	raw, _ := c.call("admin/schedules/list", nil)
	if !strings.Contains(string(raw), `"schedules":[]`) {
		t.Fatalf("empty list must be []: %s", raw)
	}
	st, err := schedule.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("b-job", "@daily", "do b", "owner2", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("a-job", "*/5 * * * *", "do a", "owner1", "slack:C1"); err != nil {
		t.Fatal(err)
	}
	var all AdminSchedulesListResult
	c.must("admin/schedules/list", nil, &all)
	if len(all.Schedules) != 2 || all.Schedules[0].Slug != "a-job" || all.Schedules[0].Cron != "*/5 * * * *" ||
		all.Schedules[0].Owner != "owner1" || all.Schedules[0].Origin != "slack:C1" {
		t.Fatalf("all = %+v", all)
	}
	raw, _ = c.call("admin/schedules/list", AdminSchedulesListParams{Owner: "owner2"})
	if !strings.Contains(string(raw), `"slug":"b-job"`) || strings.Contains(string(raw), "a-job") {
		t.Fatalf("owner filter: %s", raw)
	}
	var none AdminSchedulesListResult
	c.must("admin/schedules/list", AdminSchedulesListParams{Owner: "nobody"}, &none)
	if len(none.Schedules) != 0 {
		t.Fatalf("none = %+v", none)
	}
}

// ── memory ───────────────────────────────────────────────────────────────

func TestAdminMemory(t *testing.T) {
	c := admStart(t, true)
	var cf AdminMemoryConfiguredResult
	c.must("admin/memory/configured", nil, &cf)
	if !cf.Configured {
		t.Fatal("memory must be configured")
	}

	var put AdminMemoryPutResult
	c.must("admin/memory/put", AdminMemoryPutParams{CWD: "/proj", Slug: "note", Content: "the quick brown fox"}, &put)
	if !put.Created || put.Memory.Slug != "note" || put.Memory.Content != "the quick brown fox" {
		t.Fatalf("put = %+v", put)
	}
	c.must("admin/memory/put", AdminMemoryPutParams{CWD: "/proj", Slug: "note", Content: "the lazy dog"}, &put)
	if put.Created || put.Memory.Content != "the lazy dog" {
		t.Fatalf("replace = %+v", put)
	}
	c.must("admin/memory/put", AdminMemoryPutParams{CWD: memGlobal, Slug: "g", Content: "global fact"}, &put)
	if !put.Created {
		t.Fatalf("global put = %+v", put)
	}

	var g struct {
		Slug, Content string
	}
	c.must("admin/memory/get", AdminMemoryKeyParams{CWD: "/proj", Slug: "note"}, &g)
	if g.Content != "the lazy dog" {
		t.Fatalf("get = %+v", g)
	}
	c.must("admin/memory/get", AdminMemoryKeyParams{CWD: memGlobal, Slug: "g"}, &g)
	if g.Content != "global fact" {
		t.Fatalf("get global = %+v", g)
	}

	var res struct {
		Total, Returned, Skip, Limit int
		Results                      []struct{ Slug, Content string }
	}
	c.must("admin/memory/search", AdminMemorySearchParams{CWD: "/proj", Query: "lazy"}, &res)
	if res.Total != 1 || res.Limit != 10 || len(res.Results) != 1 || res.Results[0].Content == "" {
		t.Fatalf("search = %+v", res)
	}
	no := false
	res.Results = nil
	c.must("admin/memory/list", AdminMemoryListParams{CWD: "/proj", IncludeContent: &no}, &res)
	if res.Total != 2 || len(res.Results) != 2 || res.Results[0].Content != "" {
		t.Fatalf("list (no content) = %+v", res)
	}
	two := 1
	res.Results = nil
	c.must("admin/memory/list", AdminMemoryListParams{CWD: "/proj", Limit: &two, Skip: 1}, &res)
	if res.Returned != 1 || res.Skip != 1 || res.Limit != 1 {
		t.Fatalf("page = %+v", res)
	}
	neg := -1
	c.fail("admin/memory/list", AdminMemoryListParams{Limit: &neg}, CodeInvalid, "invalid")
	raw, _ := c.call("admin/memory/search", AdminMemorySearchParams{Query: "zzzzzz"})
	if !strings.Contains(string(raw), `"results":[]`) {
		t.Fatalf("empty results must be []: %s", raw)
	}

	c.fail("admin/memory/get", AdminMemoryKeyParams{CWD: "/proj", Slug: "ghost"}, CodeNotFound, "not_found")
	c.fail("admin/memory/get", AdminMemoryKeyParams{CWD: "", Slug: "note"}, CodeInvalid, "invalid")
	c.fail("admin/memory/get", AdminMemoryKeyParams{CWD: "/proj", Slug: " "}, CodeInvalid, "invalid")
	c.fail("admin/memory/put", AdminMemoryPutParams{CWD: "/proj", Slug: "e", Content: "  "}, CodeInvalid, "unprocessable")
	c.must("admin/memory/delete", AdminMemoryKeyParams{CWD: "/proj", Slug: "note"}, nil)
	c.fail("admin/memory/delete", AdminMemoryKeyParams{CWD: "/proj", Slug: "note"}, CodeNotFound, "not_found")
	c.must("admin/memory/delete", AdminMemoryKeyParams{CWD: memGlobal, Slug: "g"}, nil)
}

const memGlobal = "<global>"

func TestAdminMemoryNotConfigured(t *testing.T) {
	c := admStart(t, false)
	var cf AdminMemoryConfiguredResult
	c.must("admin/memory/configured", nil, &cf)
	if cf.Configured {
		t.Fatal("must not be configured")
	}
	raw, je := c.call("admin/memory/search", nil)
	if je != nil || !strings.Contains(string(raw), `"results":[]`) {
		t.Fatalf("reads are empty: %s %v", raw, je)
	}
	c.must("admin/memory/list", nil, nil)
	c.fail("admin/memory/put", AdminMemoryPutParams{CWD: "/p", Slug: "s", Content: "c"}, CodeNotConfigured, "not_configured")
	c.fail("admin/memory/get", AdminMemoryKeyParams{CWD: "/p", Slug: "s"}, CodeNotConfigured, "not_configured")
	c.fail("admin/memory/delete", AdminMemoryKeyParams{CWD: "/p", Slug: "s"}, CodeNotConfigured, "not_configured")
}

// ── errors ───────────────────────────────────────────────────────────────

// TestAdminSentinelsMapToTheDesignTable proves every sentinel of admin lands on
// the code and kind of the design's table (section 7), bare or wrapped.
func TestAdminSentinelsMapToTheDesignTable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		kind string
	}{
		{admin.ErrNotFound, -32000, "not_found"},
		{admin.ErrProviderNotFound, -32000, "provider_not_found"},
		{admin.ErrInvalid, -32001, "invalid"},
		{admin.ErrUnprocessable, -32001, "unprocessable"},
		{admin.ErrInvalidExport, -32001, "invalid_export"},
		{admin.ErrSessionActive, -32002, "session_active"},
		{admin.ErrConflict, -32002, "conflict"},
		{admin.ErrProviderNotRemovable, -32002, "provider_not_removable"},
		{admin.ErrProviderNameConflict, -32002, "provider_name_conflict"},
		{admin.ErrNotConnected, -32003, "not_connected"},
		{admin.ErrUpstream, -32004, "upstream"},
		{admin.ErrNotConfigured, -32005, "not_configured"},
		{fmt.Errorf("x: %w, %w", admin.ErrInvalid, admin.ErrUnprocessable), -32001, "unprocessable"},
	} {
		for _, err := range []error{tc.err, fmt.Errorf("ctx: %w", tc.err)} {
			je := rpcError(err)
			d, _ := je.Data.(map[string]any)
			if je.Code != tc.code || d["kind"] != tc.kind {
				t.Errorf("%v → code %d kind %v, want %d %s", err, je.Code, d["kind"], tc.code, tc.kind)
			}
		}
	}
	// Nothing but the table: every kind admin.Kind can say has a code.
	for _, k := range []string{"not_found", "unprocessable", "invalid", "session_active", "conflict", "not_connected", "upstream", "not_configured", "provider_not_found", "provider_not_removable", "provider_name_conflict", "invalid_export"} {
		if _, ok := kindCodes[k]; !ok {
			t.Errorf("kind %q has no code", k)
		}
	}
}

func TestAdminOpsRejectUnknownParams(t *testing.T) {
	c := admStart(t, false)
	c.fail("admin/mcp/get", map[string]any{"name": "x", "extra": 1}, -32602, "")
	c.fail("admin/settings/get", map[string]any{"extra": 1}, -32602, "")
}
