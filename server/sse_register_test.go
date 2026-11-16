package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// headerProbe is a ResponseWriter that records, at the instant the response headers are written,
// how many SSE clients the session already has. The headers are what a client waits for
// (client.StreamEvents returns when it has them), so a client that has "connected" must already be
// registered — otherwise an event emitted right after StreamEvents returns reaches no one.
type headerProbe struct {
	*httptest.ResponseRecorder
	proxy                *SessionProxy
	clientsAtWriteHeader atomic.Int64 // -1 until WriteHeader is called
	clientsAtFlush       atomic.Int64 // -1 until Flush is called
	flushed              chan struct{}
	once                 sync.Once
}

func newHeaderProbe(proxy *SessionProxy) *headerProbe {
	h := &headerProbe{ResponseRecorder: httptest.NewRecorder(), proxy: proxy, flushed: make(chan struct{})}
	h.clientsAtWriteHeader.Store(-1)
	h.clientsAtFlush.Store(-1)
	return h
}

func (h *headerProbe) WriteHeader(code int) {
	h.clientsAtWriteHeader.CompareAndSwap(-1, int64(h.proxy.clientCount()))
	h.ResponseRecorder.WriteHeader(code)
}

func (h *headerProbe) Flush() {
	h.clientsAtFlush.CompareAndSwap(-1, int64(h.proxy.clientCount()))
	h.once.Do(func() { close(h.flushed) })
	h.ResponseRecorder.Flush()
}

func TestAnSSEClientIsRegisteredBeforeItsHeadersAreSent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), nil))
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer gw.Close()
	if err := agent.NewOpenAIProvider("ssereg", gw.URL, agent.ProviderWithFetchModels(func() ([]types.ModelMeta, error) {
		return []types.ModelMeta{{ID: "m", ContextWindow: 100000, MaxTokens: 4096}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider("ssereg") })
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}})
	defer a.Close()
	srv := NewServer(a, ServerOptions{Logger: logx.NewNilLogger()})
	sess, err := a.NewSession(t.TempDir(), "ssereg/m")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	proxy := newSessionProxy(sess, logx.NewNilLogger())
	srv.mu.Lock()
	srv.sessions[sess.ID()] = proxy
	srv.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/sessions/"+sess.ID()+"/events", nil).WithContext(ctx)
	probe := newHeaderProbe(proxy)
	done := make(chan struct{})
	go func() { srv.handler().ServeHTTP(probe, req); close(done) }()

	select {
	case <-probe.flushed:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never flushed its headers")
	}
	cancel()
	<-done
	w, f := probe.clientsAtWriteHeader.Load(), probe.clientsAtFlush.Load()
	if w != 1 || f != 1 {
		t.Errorf("clients registered when the headers were written/flushed: %d/%d, want 1/1 — an event emitted right after the client sees them would be lost", w, f)
	}
}

// An event emitted BEFORE the headers are flushed (while the client is already registered) is not
// lost: it waits in the client's buffer and is written, in order, right after the headers.
func TestAnEventEmittedBeforeTheFlushIsDeliveredAfterIt(t *testing.T) {
	proxy := newTestProxy()
	ch := make(chan []byte, sseClientBufferSize)
	proxy.addClient(ch)
	proxy.broadcast(types.Event{Type: types.EventTurnStart})
	proxy.broadcast(types.Event{Type: types.EventTurnEnd})
	if len(ch) != 2 {
		t.Fatalf("%d events buffered for a registered client, want 2", len(ch))
	}
}
