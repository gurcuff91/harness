package agent

import (
	"sort"
	"sync"
	"time"

	"github.com/gurcuff91/harness/types"
)

// watchControlTimeout bounds how long a control event waits for room in a
// watcher's buffer before it is dropped (and reported through onDrop).
// Session.emit delivers synchronously from the turn, so a dead observer must
// not stall the agent loop for longer than this per control event.
const watchControlTimeout = 500 * time.Millisecond

// defaultWatchBuffer is the buffer used when Watch gets buffer <= 0 (the same
// size the HTTP server uses for each SSE client).
const defaultWatchBuffer = 4096

// isControlEvent reports whether t is a lifecycle/control event that must not
// be dropped silently — everything except the high-volume streaming deltas.
// An explicit list of the droppable types, so a FUTURE event type defaults to
// "protected".
func isControlEvent(t types.EventType) bool {
	switch t {
	case types.EventStreamTextDelta, types.EventStreamThinkingDelta, types.EventToolArgsDelta:
		return false
	default:
		return true
	}
}

// watchSet is a session's set of watchers. The zero value is ready to use.
// Its own lock is never held while delivering, and it is never s.mu, so
// emitting from inside a turn (which holds s.mu) cannot deadlock with it.
type watchSet struct {
	mu     sync.Mutex
	closed bool
	set    map[*Watcher]struct{}
}

func (ws *watchSet) add(w *Watcher) bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return false
	}
	if ws.set == nil {
		ws.set = make(map[*Watcher]struct{})
	}
	ws.set[w] = struct{}{}
	return true
}

func (ws *watchSet) remove(w *Watcher) {
	ws.mu.Lock()
	delete(ws.set, w)
	ws.mu.Unlock()
}

func (ws *watchSet) snapshot() []*Watcher {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if len(ws.set) == 0 {
		return nil
	}
	out := make([]*Watcher, 0, len(ws.set))
	for w := range ws.set {
		out = append(out, w)
	}
	return out
}

// closeAll closes every watcher and refuses new ones.
func (ws *watchSet) closeAll() {
	ws.mu.Lock()
	ws.closed = true
	all := ws.set
	ws.set = nil
	ws.mu.Unlock()
	for w := range all {
		w.closeChan()
	}
}

// deliver fans e out. Every watcher is tried without waiting first, so a
// stalled one never delays another; only then does a control event wait (up
// to watchControlTimeout) for the watchers that were full.
func (ws *watchSet) deliver(e types.Event) {
	all := ws.snapshot()
	if len(all) == 0 {
		return
	}
	control := isControlEvent(e.Type)
	var full []*Watcher
	for _, w := range all {
		if w.tryDeliver(e) == deliveryFull && control {
			full = append(full, w)
		}
	}
	for _, w := range full {
		w.deliverWait(e)
	}
}

type deliveryResult int

const (
	deliveryOK deliveryResult = iota
	deliveryFull
	deliveryClosed
)

// Watcher is one observer of a live session's events, created by
// Session.Watch. Events arrive in order on Events().
type Watcher struct {
	sess   *Session
	ch     chan types.Event
	onDrop func(types.Event)

	done     chan struct{} // closed first by Close, to release a blocked send
	doneOnce sync.Once

	mu     sync.RWMutex // senders hold R; closing the channel holds W
	closed bool
}

// Watch registers an observer of this session's events and returns it. It
// works alongside Subscribe (which stays the single handler): both are fed
// from the same emit. A watcher added mid-turn sees events from then on.
//
// buffer is the channel capacity (<= 0 picks a default of 4096). When the
// buffer is full, streaming deltas (text, thinking, tool arguments) are
// dropped without waiting; any other (control) event waits up to 500 ms for
// room and is then dropped, and onDrop (may be nil) is called with it, on the
// emitting goroutine, so onDrop must not block. A slow watcher never delays
// another, and delays the turn by at most 500 ms per control event.
//
// Close it when done; it is also closed when the session closes (watching an
// already-closed session returns a closed watcher).
func (s *Session) Watch(buffer int, onDrop func(types.Event)) *Watcher {
	if buffer <= 0 {
		buffer = defaultWatchBuffer
	}
	w := &Watcher{
		sess:   s,
		ch:     make(chan types.Event, buffer),
		onDrop: onDrop,
		done:   make(chan struct{}),
	}
	if !s.watchers.add(w) {
		w.closeChan()
	}
	return w
}

// Events is the channel the events arrive on; it is closed by Close or when
// the session closes.
func (w *Watcher) Events() <-chan types.Event { return w.ch }

// Close stops the watcher and closes its channel. Idempotent, safe from any
// goroutine, also while events are being delivered.
func (w *Watcher) Close() {
	w.sess.watchers.remove(w)
	w.closeChan()
}

func (w *Watcher) closeChan() {
	w.doneOnce.Do(func() { close(w.done) })
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
}

func (w *Watcher) tryDeliver(e types.Event) deliveryResult {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return deliveryClosed
	}
	select {
	case w.ch <- e:
		return deliveryOK
	default:
	}
	return deliveryFull // a delta is dropped here, nobody is told
}

func (w *Watcher) deliverWait(e types.Event) {
	dropped := false
	w.mu.RLock()
	if !w.closed {
		t := time.NewTimer(watchControlTimeout)
		select {
		case w.ch <- e:
		case <-w.done:
		case <-t.C:
			dropped = true
		}
		t.Stop()
	}
	w.mu.RUnlock()
	if dropped && w.onDrop != nil {
		w.onDrop(e)
	}
}

// ActiveSessions returns the ids of the sessions open in this process
// (created, resumed, forked or imported and not yet closed), sorted. It is
// what admin.WithActiveSessions takes.
func (a *Agent) ActiveSessions() []string {
	a.sessMu.Lock()
	ids := make([]string, 0, len(a.activeSessions))
	for id := range a.activeSessions {
		ids = append(ids, id)
	}
	a.sessMu.Unlock()
	sort.Strings(ids)
	return ids
}
