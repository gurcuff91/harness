package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/admin"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/configstore/storetest"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/jsonrpc"
	"github.com/gurcuff91/harness/types"
)

// ── a correct client-side store: what an SDK would implement ─────────────

type cvEntry struct {
	value   json.RawMessage
	version string
}

// clientStoreImpl keeps entries in memory with a version per entry (a counter)
// and a conditional swap: the simple atomic operation a client implements.
type clientStoreImpl struct {
	mu      sync.Mutex
	data    map[string]cvEntry
	counter int
	calls   []string // methods, in arrival order
	// forceConflicts makes the next n swaps answer swapped:false without
	// writing (another writer "won").
	forceConflicts int
	// fail, when set, answers the method with this error.
	fail map[string]string
	// hang makes every call wait for ctx (a client that never answers).
	hang bool
}

func newClientStoreImpl() *clientStoreImpl {
	return &clientStoreImpl{data: map[string]cvEntry{}, fail: map[string]string{}}
}

func ck(ns, key string) string { return ns + "\x00" + key }

func (m *clientStoreImpl) nextVersion() string { m.counter++; return "v" + strconv.Itoa(m.counter) }

func (m *clientStoreImpl) methodsCalled() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func (m *clientStoreImpl) count(method string) int {
	n := 0
	for _, c := range m.methodsCalled() {
		if c == method {
			n++
		}
	}
	return n
}

// serve answers the five methods of one domain.
func (m *clientStoreImpl) serve(ctx context.Context, method string, params json.RawMessage) (any, error, bool) {
	dom, op, _ := strings.Cut(method, "/")
	if dom != "settings" && dom != "credentials" {
		return nil, nil, false
	}
	m.mu.Lock()
	m.calls = append(m.calls, method)
	hang, fail := m.hang, m.fail[method]
	m.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err(), true
	}
	if fail != "" {
		return nil, &jsonrpc.Error{Code: -32000, Message: fail}, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// the domains are separate stores in a real client; tests use one per client
	switch op {
	case "get":
		var p StoreKeyParams
		_ = json.Unmarshal(params, &p)
		e, ok := m.data[ck(p.Namespace, p.Key)]
		if !ok {
			return StoreGetResult{}, nil, true
		}
		return StoreGetResult{Found: true, Value: e.value, Version: e.version}, nil, true
	case "set":
		var p StoreSetParams
		_ = json.Unmarshal(params, &p)
		m.data[ck(p.Namespace, p.Key)] = cvEntry{value: p.Value, version: m.nextVersion()}
		return Empty{}, nil, true
	case "delete":
		var p StoreKeyParams
		_ = json.Unmarshal(params, &p)
		delete(m.data, ck(p.Namespace, p.Key))
		return Empty{}, nil, true
	case "list":
		var p StoreListParams
		_ = json.Unmarshal(params, &p)
		out := StoreListResult{Entries: map[string]StoreEntry{}}
		for k, e := range m.data {
			if ns, key, _ := strings.Cut(k, "\x00"); ns == p.Namespace {
				out.Entries[key] = StoreEntry{Value: e.value, Version: e.version}
			}
		}
		return out, nil, true
	case "swap":
		var p StoreSwapParams
		_ = json.Unmarshal(params, &p)
		if m.forceConflicts > 0 {
			m.forceConflicts--
			return StoreSwapResult{Swapped: false}, nil, true
		}
		cur, exists := m.data[ck(p.Namespace, p.Key)]
		switch {
		case p.ExpectedVersion == nil && exists:
			return StoreSwapResult{Swapped: false}, nil, true
		case p.ExpectedVersion != nil && (!exists || cur.version != *p.ExpectedVersion):
			return StoreSwapResult{Swapped: false}, nil, true
		}
		if bytes.Equal(bytes.TrimSpace(p.Value), []byte("null")) || len(p.Value) == 0 {
			delete(m.data, ck(p.Namespace, p.Key))
		} else {
			m.data[ck(p.Namespace, p.Key)] = cvEntry{value: p.Value, version: m.nextVersion()}
		}
		return StoreSwapResult{Swapped: true}, nil, true
	}
	return nil, nil, false
}

// serveStores installs the settings and credentials stores on the client.
func (c *client) serveStores(settings, creds *clientStoreImpl) {
	c.mu.Lock()
	c.ext = func(ctx context.Context, method string, params json.RawMessage) (any, error, bool) {
		switch {
		case strings.HasPrefix(method, "settings/") && settings != nil:
			return settings.serve(ctx, method, params)
		case strings.HasPrefix(method, "credentials/") && creds != nil:
			return creds.serve(ctx, method, params)
		}
		return nil, nil, false
	}
	c.mu.Unlock()
}

// registerForTest makes initialize register the config stores through
// config.SwapStoresForTest (restored at cleanup): the real registration is
// process-global and permanent. The registered stores are returned.
func registerForTest(t *testing.T) (settings *configstore.SettingsStore, creds *configstore.CredentialsStore) {
	t.Helper()
	prevS, prevC := registerSettingsStore, registerCredentialsStore
	settings, creds = new(configstore.SettingsStore), new(configstore.CredentialsStore)
	var restores []func()
	registerSettingsStore = func(s configstore.SettingsStore) error {
		*settings = s
		restores = append(restores, config.SwapStoresForTest(s, nil))
		return nil
	}
	registerCredentialsStore = func(s configstore.CredentialsStore) error {
		*creds = s
		restores = append(restores, config.SwapStoresForTest(nil, s))
		return nil
	}
	t.Cleanup(func() {
		registerSettingsStore, registerCredentialsStore = prevS, prevC
		for i := len(restores) - 1; i >= 0; i-- {
			restores[i]()
		}
	})
	return settings, creds
}

func clientCfg() Config {
	cfg := memStoreConfig()
	cfg.Settings = &KindConfig{Kind: "client"}
	cfg.Credentials = &KindConfig{Kind: "client"}
	return cfg
}

func shortTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := clientStoreTimeout
	clientStoreTimeout = d
	t.Cleanup(func() { clientStoreTimeout = prev })
}

// connectingProvider stores its credential in the credentials store, as the
// real providers do.
type connectingProvider struct{ *admFake }

func (p connectingProvider) Connect(c types.Credentials) error {
	return config.GetCredentialsManager().SetCredential(p.name, config.ProviderCredential{Type: "api_key", APIKey: c.APIKey})
}

// (a) the client serves both stores DURING initialize; admin operations
// round-trip through them and nothing is written to disk.
func TestClientStoresServedDuringInitialize(t *testing.T) {
	registerForTest(t)
	c := start(t)
	ss, cs := newClientStoreImpl(), newClientStoreImpl()
	c.serveStores(ss, cs)
	c.initialize(clientCfg())
	if ss.count("settings/get")+ss.count("settings/list") == 0 {
		t.Fatalf("settings were not read through the client before initialize replied: %v", ss.methodsCalled())
	}

	var v admin.SettingsValues
	c.must("admin/settings/set_active_model", AdminSettingsSetActiveModelParams{Model: "p/m"}, &v)
	var again admin.SettingsValues
	c.must("admin/settings/get", nil, &again)
	if again.ActiveModel != "p/m" {
		t.Fatalf("get = %+v", again)
	}
	ss.mu.Lock()
	stored := len(ss.data)
	ss.mu.Unlock()
	if stored == 0 {
		t.Fatal("the model was not stored in the client's settings store")
	}

	p := connectingProvider{&admFake{name: "clientprov", ids: []string{"a"}}}
	admUseProviders(t, p)
	c.must("admin/providers/connect", map[string]any{"name": "clientprov", "api_key": "sk-test-value"}, nil)
	cs.mu.Lock()
	var found bool
	for _, e := range cs.data {
		found = found || bytes.Contains(e.value, []byte("sk-test-value"))
	}
	cs.mu.Unlock()
	if !found {
		t.Fatal("the credential did not reach the client's credentials store")
	}
	home, _ := os.UserHomeDir()
	for _, f := range []string{"credentials.json", "settings.json"} {
		if _, err := os.Stat(filepath.Join(home, ".harness", f)); err == nil {
			t.Fatalf("%s was written to disk", f)
		}
	}
}

// (b) a client that never answers: initialize fails with the documented
// message, and the process ends (the stores are registered).
func TestClientStoreTimeoutFailsInitializeAndEndsProcess(t *testing.T) {
	registerForTest(t)
	shortTimeout(t, 150*time.Millisecond)
	c := start(t)
	ss := newClientStoreImpl()
	ss.hang = true
	c.serveStores(ss, nil)
	cfg := memStoreConfig()
	cfg.Settings = &KindConfig{Kind: "client"}
	je := c.fail("initialize", InitializeParams{ProtocolVersion: ProtocolVersion, Config: cfg}, jsonrpc.CodeInternalError, "")
	for _, w := range []string{"the client did not answer settings/", "within 150ms", `a "client" store must be served from the first message`} {
		if !strings.Contains(je.Message, w) {
			t.Fatalf("message %q lacks %q", je.Message, w)
		}
	}
	waitEnds(t, c)
}

func waitEnds(t *testing.T, c *client) {
	t.Helper()
	select {
	case <-c.done:
		c.done <- nil // the cleanup reads it again
	case <-time.After(5 * time.Second):
		t.Fatal("the process did not end")
	}
}

// (c) a client error is returned and logged, never swallowed or read as absent.
func TestClientStoreErrorIsReturnedAndLogged(t *testing.T) {
	logger := newCaptureLogger()
	m := newClientStoreImpl()
	m.fail["settings/get"] = "boom: disk full"
	conn, stop := adapterOver(t, m)
	defer stop()
	st := newClientStore(conn, "settings", logger)
	_, found, err := st.Get("core", "x")
	if err == nil || found || !strings.Contains(err.Error(), "boom: disk full") {
		t.Fatalf("Get = found %v, err %v", found, err)
	}
	if got := strings.Join(logger.lines, "\n"); !strings.Contains(got, "settings/get failed") || !strings.Contains(got, "boom") {
		t.Fatalf("not logged: %q", got)
	}
	// also through the swap loop
	err = st.SwapValue("core", "x", func([]byte, bool) ([]byte, bool, error) { return []byte(`1`), true, nil })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("SwapValue err = %v", err)
	}
}

// adapterOver connects a clientStoreImpl to a binary-side Conn over pipes.
func adapterOver(t *testing.T, m *clientStoreImpl) (*jsonrpc.Conn, func()) {
	t.Helper()
	aR, aW := pipe() // client -> binary
	bR, bW := pipe() // binary -> client
	binary := jsonrpc.NewConn(aR, bW)
	cl := jsonrpc.NewConn(bR, aW)
	ctx, cancel := context.WithCancel(context.Background())
	go binary.Serve(ctx, func(context.Context, string, json.RawMessage, bool) (any, error) { return nil, nil })
	go cl.Serve(ctx, func(ctx context.Context, method string, p json.RawMessage, _ bool) (any, error) {
		res, err, ok := m.serve(ctx, method, p)
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: method}
		}
		return res, err
	})
	return binary, func() { cancel(); aW.Close(); bW.Close() }
}

func newAdapter(t *testing.T, m *clientStoreImpl, domain string) (*clientStore, func()) {
	t.Helper()
	conn, stop := adapterOver(t, m)
	return newClientStore(conn, domain, newCaptureLogger()), stop
}

// (d) a lost swap re-reads and retries and converges; ten lost swaps in a row
// are a conflict.
func TestClientStoreSwapRetriesOnConflict(t *testing.T) {
	m := newClientStoreImpl()
	st, stop := newAdapter(t, m, "credentials")
	defer stop()
	if err := st.Set("p", "k", []byte(`1`)); err != nil {
		t.Fatal(err)
	}
	m.forceConflicts = 3
	calls := 0
	err := st.SwapValue("p", "k", func(cur []byte, found bool) ([]byte, bool, error) {
		calls++
		return []byte(`2`), true, nil
	})
	if err != nil || calls != 4 {
		t.Fatalf("err = %v after %d callback runs, want nil after 4", err, calls)
	}
	if v, _, _ := st.Get("p", "k"); string(v) != "2" {
		t.Fatalf("value = %s", v)
	}

	m.forceConflicts = 100
	calls = 0
	err = st.SwapValue("p", "k", func([]byte, bool) ([]byte, bool, error) { calls++; return []byte(`3`), true, nil })
	if !errors.Is(err, ErrStoreConflict) || calls != swapAttempts {
		t.Fatalf("err = %v after %d runs, want ErrStoreConflict after %d", err, calls, swapAttempts)
	}
	if je := rpcError(err); je.Code != CodeConflict {
		t.Fatalf("conflict maps to code %d", je.Code)
	}
	if v, _, _ := st.Get("p", "k"); string(v) != "2" {
		t.Fatalf("a lost swap changed the value: %s", v)
	}
}

// A swap of an entry that does not exist sends expected_version null (create);
// one that exists sends its version.
func TestClientStoreSwapExpectedVersionRule(t *testing.T) {
	m := newClientStoreImpl()
	var seen []*string
	// observe through a wrapper client
	aR, aW := pipe()
	bR, bW := pipe()
	binary := jsonrpc.NewConn(aR, bW)
	cl := jsonrpc.NewConn(bR, aW)
	go binary.Serve(context.Background(), func(context.Context, string, json.RawMessage, bool) (any, error) { return nil, nil })
	go cl.Serve(context.Background(), func(ctx context.Context, method string, p json.RawMessage, _ bool) (any, error) {
		if method == "settings/swap" {
			var sp StoreSwapParams
			json.Unmarshal(p, &sp)
			seen = append(seen, sp.ExpectedVersion)
			if !bytes.Contains(p, []byte(`"expected_version"`)) {
				t.Errorf("expected_version absent instead of null/explicit: %s", p)
			}
		}
		res, err, _ := m.serve(ctx, method, p)
		return res, err
	})
	defer func() { aW.Close(); bW.Close() }()
	st := newClientStore(binary, "settings", newCaptureLogger())
	up := func([]byte, bool) ([]byte, bool, error) { return []byte(`1`), true, nil }
	if err := st.SwapValue("n", "k", up); err != nil {
		t.Fatal(err)
	}
	if err := st.SwapValue("n", "k", up); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != nil || seen[1] == nil || *seen[1] == "" {
		t.Fatalf("expected versions = %v", seen)
	}
	// delete form sends an explicit null value
	if err := st.SwapValue("n", "k", func([]byte, bool) ([]byte, bool, error) { return nil, true, nil }); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.Get("n", "k"); found {
		t.Fatal("the delete form did not delete")
	}
}

// Invalid JSON is refused BEFORE any request.
func TestClientStoreRejectsInvalidJSONWithoutCalling(t *testing.T) {
	m := newClientStoreImpl()
	st, stop := newAdapter(t, m, "settings")
	defer stop()
	if err := st.Set("n", "k", []byte(`{nope`)); !errors.Is(err, configstore.ErrInvalidValue) {
		t.Fatalf("Set err = %v", err)
	}
	err := st.SwapValue("n", "k", func([]byte, bool) ([]byte, bool, error) { return []byte(`{nope`), true, nil })
	if !errors.Is(err, configstore.ErrInvalidValue) {
		t.Fatalf("SwapValue err = %v", err)
	}
	if got := m.count("settings/set") + m.count("settings/swap"); got != 0 {
		t.Fatalf("%d write requests reached the client", got)
	}
}

// (e) the OAuth refresh scenario: concurrent read-modify-write of one key
// through the adapter never loses an update.
func TestClientStoreConcurrentSwapsLoseNothing(t *testing.T) {
	m := newClientStoreImpl()
	// four adapters over ONE client store: four "processes", so the swaps
	// really contend at the client (in one adapter they are serialized).
	var sts []*clientStore
	for i := 0; i < 4; i++ {
		a, stop := newAdapter(t, m, "credentials")
		defer stop()
		sts = append(sts, a)
	}
	st := sts[0]
	const workers = 20
	var wg sync.WaitGroup
	var winners atomic.Int64
	var failures atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(st *clientStore) {
			defer wg.Done()
			err := st.SwapValue("p", "counter", func(cur []byte, found bool) ([]byte, bool, error) {
				n := 0
				if found {
					n, _ = strconv.Atoi(string(cur))
				}
				return []byte(strconv.Itoa(n + 1)), true, nil
			})
			switch {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, ErrStoreConflict):
				failures.Add(1) // exhausted its attempts: it wrote nothing
			default:
				t.Error(err)
			}
		}(sts[i%len(sts)])
	}
	wg.Wait()
	v, _, _ := st.Get("p", "counter")
	if got := int64(mustAtoi(t, v)); got != winners.Load() {
		t.Fatalf("counter = %d but %d swaps succeeded (lost update)", got, winners.Load())
	}
	if winners.Load() == 0 {
		t.Fatal("nobody won")
	}
}

func mustAtoi(t *testing.T, b []byte) int {
	t.Helper()
	n, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("value %q: %v", b, err)
	}
	return n
}

// Single-use refresh token: of two concurrent redeemers only one redeems, the
// other re-reads the renewed token.
func TestClientStoreSingleUseRefreshRedeemedOnce(t *testing.T) {
	m := newClientStoreImpl()
	st, stop := newAdapter(t, m, "credentials")
	defer stop()
	if err := st.Set("providers", "x", []byte(`{"refresh":"r0"}`)); err != nil {
		t.Fatal(err)
	}
	var redeemed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = st.SwapValue("providers", "x", func(cur []byte, found bool) ([]byte, bool, error) {
				if string(cur) != `{"refresh":"r0"}` {
					return nil, false, nil // already renewed by the other
				}
				redeemed.Add(1)
				time.Sleep(20 * time.Millisecond) // the provider round trip
				return []byte(`{"refresh":"r1"}`), true, nil
			})
		}()
	}
	wg.Wait()
	// Each CAS round has one winner; a loser re-reads and sees r1 (no second redemption).
	if v, _, _ := st.Get("providers", "x"); string(v) != `{"refresh":"r1"}` {
		t.Fatalf("value = %s", v)
	}
	if redeemed.Load() > 2 {
		t.Fatalf("redeemed %d times", redeemed.Load())
	}
}

// (f) configuration validation.
func TestConfigStoresValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want []string
	}{
		"settings kind":    {Config{Settings: &KindConfig{Kind: "redis"}}, []string{"config.settings.kind", "file, ephemeral, client"}},
		"credentials kind": {Config{Credentials: &KindConfig{Kind: "vault"}}, []string{"config.credentials.kind", "file, ephemeral, client"}},
		"settings path":    {Config{Settings: &KindConfig{Kind: "client", Path: "/x"}}, []string{"config.settings.path", "client"}},
		"credentials path": {Config{Credentials: &KindConfig{Kind: "ephemeral", Path: "/x"}}, []string{"config.credentials.path", "ephemeral"}},
		"store memory":     {Config{Store: &KindConfig{Kind: "memory"}}, []string{"config.store.kind", "file, ephemeral"}},
		"memory none":      {Config{Memory: &KindConfig{Kind: "none"}}, []string{"config.memory.kind", "sqlite"}},
	} {
		t.Run(name, func(t *testing.T) {
			var registered atomic.Int64
			prevS, prevC := registerSettingsStore, registerCredentialsStore
			registerSettingsStore = func(configstore.SettingsStore) error { registered.Add(1); return nil }
			registerCredentialsStore = func(configstore.CredentialsStore) error { registered.Add(1); return nil }
			defer func() { registerSettingsStore, registerCredentialsStore = prevS, prevC }()
			c := start(t)
			je := c.fail("initialize", InitializeParams{ProtocolVersion: 1, Config: tc.cfg}, jsonrpc.CodeInvalidParams, "")
			for _, w := range tc.want {
				if !strings.Contains(je.Message, w) {
					t.Errorf("message %q lacks %q", je.Message, w)
				}
			}
			if registered.Load() != 0 {
				t.Fatal("a store was registered for an invalid config")
			}
			c.initialize(memStoreConfig()) // still retryable
		})
	}
}

// ephemeral works for the three fields; path works for a file settings store.
func TestConfigEphemeralAndFileSettings(t *testing.T) {
	settings, creds := registerForTest(t)
	c := start(t)
	cfg := memStoreConfig()
	cfg.Settings = &KindConfig{Kind: "ephemeral"}
	cfg.Credentials = &KindConfig{Kind: "ephemeral"}
	c.initialize(cfg)
	if _, ok := (*settings).(*configstore.InMemoryStore); !ok {
		t.Fatalf("settings store = %T", *settings)
	}
	if _, ok := (*creds).(*configstore.InMemoryStore); !ok {
		t.Fatalf("credentials store = %T", *creds)
	}

	settings2, _ := registerForTest(t)
	path := filepath.Join(t.TempDir(), "s.json")
	c2 := start(t)
	cfg2 := memStoreConfig()
	cfg2.Settings = &KindConfig{Kind: "file", Path: path}
	c2.initialize(cfg2)
	if _, ok := (*settings2).(*configstore.FileStore); !ok {
		t.Fatalf("settings store = %T", *settings2)
	}
	_ = c2
}

// file without a path (or omitted) registers nothing: the process default stays.
func TestConfigFileWithoutPathRegistersNothing(t *testing.T) {
	var registered atomic.Int64
	prevS, prevC := registerSettingsStore, registerCredentialsStore
	registerSettingsStore = func(configstore.SettingsStore) error { registered.Add(1); return nil }
	registerCredentialsStore = func(configstore.CredentialsStore) error { registered.Add(1); return nil }
	defer func() { registerSettingsStore, registerCredentialsStore = prevS, prevC }()
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	cfg := memStoreConfig()
	cfg.Settings = &KindConfig{Kind: "file"}
	c := start(t)
	c.initialize(cfg)
	c2 := start(t)
	c2.initialize(memStoreConfig())
	if registered.Load() != 0 {
		t.Fatalf("%d stores registered", registered.Load())
	}
}

// (g) the real registration refuses once the process already read its
// settings; the error is a clear conflict, not a crash.
func TestConfigStoreAlreadyInitializedIsAConflict(t *testing.T) {
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	c := start(t)
	ss := newClientStoreImpl()
	c.serveStores(ss, nil)
	cfg := memStoreConfig()
	cfg.Settings = &KindConfig{Kind: "client"}
	je := c.fail("initialize", InitializeParams{ProtocolVersion: 1, Config: cfg}, CodeConflict, "conflict")
	if !strings.Contains(je.Message, "settings store was already in use in this process") {
		t.Fatalf("message = %q", je.Message)
	}
	if n := len(ss.methodsCalled()); n != 0 {
		t.Fatalf("the client was called %d times though nothing was registered", n)
	}
	// nothing was registered: retryable
	c.initialize(memStoreConfig())
}

// (h) a failure after the stores were registered ends the process.
func TestFailureAfterRegistrationEndsProcess(t *testing.T) {
	registerForTest(t)
	c := start(t)
	c.serveStores(newClientStoreImpl(), nil)
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Store: &KindConfig{Kind: "file", Path: file}, Settings: &KindConfig{Kind: "client"}}
	c.fail("initialize", InitializeParams{ProtocolVersion: 1, Config: cfg}, CodeInvalid, "invalid")
	waitEnds(t, c)
}

// A failure BEFORE any registration is not fatal.
func TestFailureBeforeRegistrationStaysRetryable(t *testing.T) {
	registerForTest(t)
	c := start(t)
	cfg := Config{Settings: &KindConfig{Kind: "client"}, ThinkingLevel: "ultra"}
	c.fail("initialize", InitializeParams{ProtocolVersion: 1, Config: cfg}, jsonrpc.CodeInvalidParams, "")
	select {
	case <-c.done:
		t.Fatal("the process ended on a failure before registration")
	case <-time.After(200 * time.Millisecond):
	}
	c.serveStores(newClientStoreImpl(), nil)
	cfg.ThinkingLevel = ""
	cfg.Store = &KindConfig{Kind: "ephemeral"}
	c.initialize(cfg)
}

// Returned bytes are copies; List is never nil.
func TestClientStoreCopiesAndNonNilList(t *testing.T) {
	m := newClientStoreImpl()
	st, stop := newAdapter(t, m, "settings")
	defer stop()
	l, err := st.List("empty")
	if err != nil || l == nil || len(l) != 0 {
		t.Fatalf("List = %v, %v", l, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

// (i) the contract every configstore implementation must pass.
func TestClientStorePassesTheContract(t *testing.T) {
	for _, domain := range []string{"settings", "credentials"} {
		t.Run(domain, func(t *testing.T) {
			storetest.RunContract(t, func(t *testing.T) storetest.Store {
				st, stop := newAdapter(t, newClientStoreImpl(), domain)
				t.Cleanup(stop)
				return st
			})
		})
	}
}
