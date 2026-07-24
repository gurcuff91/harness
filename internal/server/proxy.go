package server

import (
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/logx"
	"github.com/gurcuff91/harness/types"
)

// controlBroadcastTimeout bounds how long broadcast will block trying to
// deliver a control event (see isControlEvent) to a slow/stuck client before
// giving up and dropping it as a last resort. Long enough to ride out a
// consumer that's momentarily behind (a big render, a burst of buffered
// deltas draining), short enough that a genuinely dead client can't stall
// the agent loop for real — s.emit (and therefore the whole ReAct iteration)
// calls broadcast synchronously.
const controlBroadcastTimeout = 500 * time.Millisecond

// SessionProxy wraps an agent session and broadcasts events to SSE clients.
// It subscribes to the session once and fans out to all connected clients.
type SessionProxy struct {
	session *agent.Session
	verbose bool // gates the dropped-control-event warning — see broadcast

	mu      sync.RWMutex
	clients map[chan<- []byte]struct{} // SSE client channels (formatted JSON lines)
}

// newSessionProxy wraps sess for SSE fan-out. verbose must mirror the owning
// Server's — the TUI's in-process server runs with Verbose: false specifically
// because it shares stdout/stderr with the raw-mode terminal UI, so ANY
// unconditional log line here would corrupt the render. See broadcast's
// dropped-control-event warning, the one log call on this path.
func newSessionProxy(sess *agent.Session, verbose bool) *SessionProxy {
	p := &SessionProxy{
		session: sess,
		verbose: verbose,
		clients: make(map[chan<- []byte]struct{}),
	}
	sess.Subscribe(p.broadcast)
	return p
}

// addClient registers a new SSE client.
func (p *SessionProxy) addClient(ch chan<- []byte) {
	p.mu.Lock()
	p.clients[ch] = struct{}{}
	p.mu.Unlock()
}

// removeClient unregisters an SSE client.
func (p *SessionProxy) removeClient(ch chan<- []byte) {
	p.mu.Lock()
	delete(p.clients, ch)
	p.mu.Unlock()
}

// clientCount returns the number of connected SSE clients.
func (p *SessionProxy) clientCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.clients)
}

// close disconnects all SSE clients and closes the underlying session.
func (p *SessionProxy) close() {
	p.mu.Lock()
	for ch := range p.clients {
		close(ch)
	}
	p.clients = nil
	p.mu.Unlock()
	p.session.Close()
}

// broadcast formats an agent event as JSON and sends it to all connected
// clients.
//
// High-volume streaming deltas (thinking/text/tool_args — many per turn, one
// per LLM token) are sent non-blocking: a slow client just misses a fragment,
// which is harmless and the right trade-off — s.emit calls broadcast
// synchronously from the agent's ReAct loop, so blocking here would stall the
// whole turn waiting on a slow renderer.
//
// Control events (turn/loop lifecycle, stop, error, compaction — see
// isControlEvent) are different: losing one leaves the client with no way to
// know the agent is done or was cancelled, which is exactly the "spinner
// stuck forever, Esc does nothing" freeze this guards against. For those,
// broadcast blocks briefly (controlBroadcastTimeout) to ride out a client
// that's momentarily behind on a backlog, only dropping as an absolute last
// resort (logged, so it's visible in the field instead of silent).
func (p *SessionProxy) broadcast(e types.Event) {
	line := formatEvent(e)
	if line == nil {
		return
	}
	control := isControlEvent(e.Type)

	p.mu.RLock()
	defer p.mu.RUnlock()
	for ch := range p.clients {
		select {
		case ch <- line:
			continue
		default:
		}
		if !control {
			continue // streaming delta: fine to drop, already tried non-blocking above
		}
		// Control event and the channel was full — worth a bounded wait instead
		// of dropping immediately.
		select {
		case ch <- line:
		case <-time.After(controlBroadcastTimeout):
			// Gated by verbose: this runs on the agent's own event-emitting
			// goroutine, which for the TUI's in-process server is the SAME
			// process driving the raw-mode terminal. An unconditional log.Print
			// here (stdout/stderr) would corrupt the TUI's rendering — exactly
			// why Server.verbose exists and gates requestLogger too. `harness
			// serve`/telegram run with Verbose: true and do want this visible.
			if p.verbose {
				logx.Warn("sse", "control_event_dropped",
					"event_type", int(e.Type), "timeout_ms", controlBroadcastTimeout.Milliseconds())
			}
		}
	}
}

// isControlEvent reports whether e is a lifecycle/control event that must
// never be silently dropped — as opposed to a high-volume streaming delta
// (thinking/text/tool_args deltas), where losing one is harmless. Control
// events are everything the client needs to track turn/loop state, tool
// pairing, and terminal outcomes (stop/error/compaction); deltas are
// everything else. Listed as an explicit denylist of the droppable types so a
// FUTURE event type defaults to "protected" — the safer failure mode.
func isControlEvent(t types.EventType) bool {
	switch t {
	case types.EventStreamTextDelta, types.EventStreamThinkingDelta, types.EventToolArgsDelta:
		return false
	default:
		return true
	}
}
