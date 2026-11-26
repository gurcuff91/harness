package server

import (
	"sync/atomic"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// proxiesCreated counts proxies ever built — a session must get exactly one
// per server; tests assert on it.
var proxiesCreated atomic.Int64

// SessionProxy is the server's handle on one live session: the session itself
// plus the SSE clients watching it. The fan-out (one buffer per client, deltas
// dropped when it is full, control events waiting a bounded time) is
// agent.Session.Watch's — the server only formats what a watcher yields.
type SessionProxy struct {
	session *agent.Session
	logger  logx.Logger // never nil — used by the dropped-control-event warning

	clients atomic.Int64
}

// newSessionProxy wraps sess. logger must mirror the owning Server's — the
// TUI's in-process server runs with logx.NewNilLogger() specifically because
// it shares stdout/stderr with the raw-mode terminal UI, so ANY unconditional
// log line here would corrupt the render. The warning in onDrop is the one log
// call on this path.
func newSessionProxy(sess *agent.Session, logger logx.Logger) *SessionProxy {
	proxiesCreated.Add(1)
	return &SessionProxy{session: sess, logger: logger}
}

// watch registers an SSE client: a Watcher with the given buffer, already
// receiving when it returns. The caller must call the returned release func
// when the client is gone.
func (p *SessionProxy) watch(buffer int) (w *agent.Watcher, release func()) {
	w = p.session.Watch(buffer, p.onDrop)
	p.clients.Add(1)
	return w, func() {
		w.Close()
		p.clients.Add(-1)
	}
}

// clientCount returns the number of connected SSE clients.
func (p *SessionProxy) clientCount() int { return int(p.clients.Load()) }

// close closes the underlying session; that also ends every watcher (and so
// every SSE stream).
func (p *SessionProxy) close() { p.session.Close() }

// onDrop reports a control event a watcher could not take in time. It runs on
// the agent's own event-emitting goroutine — for the TUI's in-process server
// the SAME process that drives the raw-mode terminal — which is why p.logger is
// logx.NewNilLogger() there and this call is always safe to make.
func (p *SessionProxy) onDrop(e types.Event) {
	p.logger.Warn("sse", "control_event_dropped",
		"event_type", int(e.Type), "timeout_ms", int64(500))
}
