package admin_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/agent/memory"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/types"
)

// isolate gives the test its own settings/credentials stores and HOME.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
}

// fake is a provider whose behavior the test sets.
type fake struct {
	name     string
	on       atomic.Bool
	ids      []string
	fetchErr error
	connect  func(types.Credentials) error
	quota    func() (*types.Quota, error)
	credType types.CredentialType

	mu         sync.Mutex
	gotCreds   types.Credentials
	quotaCalls atomic.Int32
}

func newFake(name string, ids ...string) *fake {
	f := &fake{name: name, ids: ids, credType: types.CredTypeAPIKey}
	f.on.Store(true)
	return f
}

func (p *fake) CompleteStream(context.Context, *types.Request, types.StreamCallback) (*types.Response, error) {
	return nil, errors.New("unused")
}
func (p *fake) Name() string        { return p.name }
func (p *fake) DisplayName() string { return "Fake " + p.name }
func (p *fake) Description() string { return "fake" }
func (p *fake) IsActive() bool      { return p.on.Load() }
func (p *fake) Models() []types.ModelMeta {
	if p.fetchErr != nil {
		return nil
	}
	var out []types.ModelMeta
	for _, id := range p.ids {
		out = append(out, types.ModelMeta{ID: id, ContextWindow: 1000})
	}
	return out
}
func (p *fake) FetchModels() ([]types.ModelMeta, error) { return p.Models(), p.fetchErr }
func (p *fake) ModelMeta(string) *types.ModelMeta       { return nil }
func (p *fake) CredentialType() types.CredentialType    { return p.credType }
func (p *fake) ResolveCredentials() (types.Credentials, error) {
	return types.Credentials{Type: p.credType}, nil
}
func (p *fake) ActivationSource() providers.ActivationSource { return providers.ActivationAuto }
func (p *fake) Connect(c types.Credentials) error {
	p.mu.Lock()
	p.gotCreds = c
	p.mu.Unlock()
	if p.connect != nil {
		return p.connect(c)
	}
	return nil
}
func (p *fake) Disconnect() error { return nil }
func (p *fake) Dialect() string   { return "openai" }
func (p *fake) Upstream() (providers.Upstream, error) {
	return providers.Upstream{}, providers.ErrNoUpstream
}

// quotaFake is a fake that reports a quota.
type quotaFake struct{ *fake }

func (p quotaFake) Quota(context.Context) (*types.Quota, error) {
	p.quotaCalls.Add(1)
	return p.quota()
}

func useProviders(t *testing.T, ps ...providers.Provider) {
	t.Helper()
	t.Cleanup(providers.SetForTest(ps))
	for _, p := range ps {
		name := p.Name()
		providers.ForgetQuota(name)
		t.Cleanup(func() { providers.ForgetQuota(name) })
	}
}

func newAdmin(t *testing.T, opts ...admin.Option) *admin.Admin {
	t.Helper()
	isolate(t)
	ad := admin.New(opts...)
	t.Cleanup(func() { ad.Close() })
	return ad
}

// seedSession stores a session with n messages.
func seedSession(t *testing.T, st store.SessionStore, id, name string, n int) store.SessionMeta {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	meta := store.SessionMeta{ID: id, CWD: "/proj", Name: name, Model: "p/m", Thinking: "off", CreatedAt: now, LastActiveAt: now}
	if err := st.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := st.AppendMessage(id, types.NewUserTextMessage("hello")); err != nil {
			t.Fatal(err)
		}
	}
	return meta
}

func newMemory(t *testing.T) memory.Store {
	t.Helper()
	m, err := memory.OpenSQLite(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}
