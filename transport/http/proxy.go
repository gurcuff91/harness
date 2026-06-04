package http

import (
	"sync"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/types"
)

// SessionProxy wraps an agent session and broadcasts events to SSE clients.
// It subscribes to the session once and fans out to all connected clients.
type SessionProxy struct {
	session *agent.Session

	mu      sync.RWMutex
	clients map[chan<- []byte]struct{} // SSE client channels (formatted JSON lines)
}

func newSessionProxy(sess *agent.Session) *SessionProxy {
	p := &SessionProxy{
		session: sess,
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

// broadcast formats an agent event as JSON and sends it to all connected clients.
// Non-blocking: slow clients are dropped (event is lost for them).
func (p *SessionProxy) broadcast(e types.Event) {
	line := formatEvent(e)
	if line == nil {
		return
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	for ch := range p.clients {
		select {
		case ch <- line:
		default:
		}
	}
}
