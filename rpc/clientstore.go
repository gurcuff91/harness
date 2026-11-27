package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/jsonrpc"
	"github.com/gurcuff91/harness/logx"
)

// clientStoreTimeout is the deadline of each request to the client. A variable
// so tests can shorten it.
var clientStoreTimeout = 30 * time.Second

// swapAttempts bounds the compare-and-swap loop of SwapValue.
const swapAttempts = 10

// ErrStoreConflict is returned by a client store's SwapValue when the client
// kept answering "version mismatch" (another writer won every attempt). It
// maps to the `conflict` error kind.
var ErrStoreConflict = errors.New("conflict")

// clientStore is a configstore.SettingsStore AND CredentialsStore whose data
// lives in the client: every method is a request to it (`<domain>/<method>`).
// It has no cache (a second state that can drift) and is as atomic as the
// client's `swap`. SwapValue is a compare-and-swap loop whose callback runs in
// this process; it never crosses the pipe.
type clientStore struct {
	conn    *jsonrpc.Conn
	domain  string // "settings" | "credentials"
	timeout time.Duration
	logger  logx.Logger

	// locks serializes the SwapValue calls of THIS process per key: forty
	// goroutines racing one counter could not all win a 10-attempt optimistic
	// loop. The client's swap still arbitrates against other writers (other
	// processes, other adapters), which is what the retry loop is for.
	lockMu sync.Mutex
	locks  map[string]*sync.Mutex
}

var (
	_ configstore.SettingsStore    = (*clientStore)(nil)
	_ configstore.CredentialsStore = (*clientStore)(nil)
)

func newClientStore(conn *jsonrpc.Conn, domain string, logger logx.Logger) *clientStore {
	return &clientStore{conn: conn, domain: domain, timeout: clientStoreTimeout, logger: logger}
}

// call sends one request and decodes the result into out. Every failure is
// logged and returned.
func (c *clientStore) call(method string, params, out any) error {
	full := c.domain + "/" + method
	timeout := c.timeout
	if timeout <= 0 {
		timeout = clientStoreTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	raw, err := c.conn.Call(ctx, full, params)
	if err == nil && out != nil {
		if uerr := json.Unmarshal(raw, out); uerr != nil {
			err = fmt.Errorf("invalid %s result: %w", full, uerr)
		}
	}
	if err == nil {
		return nil
	}
	var je *jsonrpc.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		err = fmt.Errorf("the client did not answer %s within %s: a \"client\" store must be served from the first message", full, timeout)
	case errors.As(err, &je):
		err = fmt.Errorf("%s: client error %d: %s", full, je.Code, je.Message)
	case errors.Is(err, jsonrpc.ErrClosed):
		err = fmt.Errorf("%s: the client disconnected", full)
	}
	c.logger.Warn(component, full+" failed", "error", err.Error())
	return fmt.Errorf("%w: %w", ErrClientBackend, err)
}

func (c *clientStore) Get(namespace, key string) ([]byte, bool, error) {
	v, _, found, err := c.get(namespace, key)
	return v, found, err
}

func (c *clientStore) get(namespace, key string) (value []byte, version string, found bool, err error) {
	var res StoreGetResult
	if err := c.call("get", StoreKeyParams{Namespace: namespace, Key: key}, &res); err != nil {
		return nil, "", false, err
	}
	if !res.Found {
		return nil, "", false, nil
	}
	return append([]byte(nil), res.Value...), res.Version, true, nil
}

func (c *clientStore) Set(namespace, key string, value []byte) error {
	if !json.Valid(value) {
		return configstore.ErrInvalidValue
	}
	return c.call("set", StoreSetParams{Namespace: namespace, Key: key, Value: json.RawMessage(value)}, nil)
}

func (c *clientStore) Delete(namespace, key string) error {
	return c.call("delete", StoreKeyParams{Namespace: namespace, Key: key}, nil)
}

func (c *clientStore) List(namespace string) (map[string][]byte, error) {
	var res StoreListResult
	if err := c.call("list", StoreListParams{Namespace: namespace}, &res); err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(res.Entries))
	for k, e := range res.Entries {
		out[k] = append([]byte(nil), e.Value...)
	}
	return out, nil
}

func (c *clientStore) keyLock(namespace, key string) *sync.Mutex {
	c.lockMu.Lock()
	defer c.lockMu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	id := namespace + "\x00" + key
	m := c.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		c.locks[id] = m
	}
	return m
}

func (c *clientStore) SwapValue(namespace, key string, fn func(current []byte, found bool) (next []byte, write bool, err error)) error {
	l := c.keyLock(namespace, key)
	l.Lock()
	defer l.Unlock()
	for attempt := 0; attempt < swapAttempts; attempt++ {
		cur, version, found, err := c.get(namespace, key)
		if err != nil {
			return err
		}
		next, write, err := fn(cur, found)
		if err != nil {
			return err
		}
		if !write {
			return nil
		}
		if next != nil && !json.Valid(next) {
			return configstore.ErrInvalidValue
		}
		p := StoreSwapParams{Namespace: namespace, Key: key}
		if found {
			v := version
			p.ExpectedVersion = &v
		}
		if next != nil {
			p.Value = json.RawMessage(next)
		} // nil Value serializes as null: the delete form
		var res StoreSwapResult
		if err := c.call("swap", p, &res); err != nil {
			return err
		}
		if res.Swapped {
			return nil
		}
	}
	err := fmt.Errorf("%w: %s/swap of %s/%s lost %d attempts in a row", ErrStoreConflict, c.domain, namespace, key, swapAttempts)
	c.logger.Warn(component, c.domain+"/swap failed", "error", err.Error())
	return err
}

// Close does nothing: the connection is the server's, and the client releases
// its own resources.
func (c *clientStore) Close() error { return nil }

// probe asks the client for one (absent) entry: the readiness check that makes
// `initialize` fail, instead of the agent silently starting on empty settings,
// when the client does not serve the store.
func (c *clientStore) probe() error {
	_, _, err := c.Get("core", "probe")
	return err
}
