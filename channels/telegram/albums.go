package telegram

import (
	"context"
	"sync"
	"time"
)

// albumWait is how long the messages of one album (one media_group_id) are
// gathered before they become ONE prompt: Telegram sends an album as separate
// messages, back to back. A variable for tests.
var albumWait = time.Second

// albums gathers album messages per media group.
type albums struct {
	c       *Channel
	mu      sync.Mutex
	pending map[string]*albumBuffer
}

type albumBuffer struct {
	msgs  []*Message
	timer *time.Timer
}

func newAlbums(c *Channel) *albums { return &albums{c: c, pending: map[string]*albumBuffer{}} }

// add buffers m; the album is delivered albumWait after its last message.
func (a *albums) add(m *Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.pending[m.MediaGroupID]
	if b == nil {
		b = &albumBuffer{}
		a.pending[m.MediaGroupID] = b
	}
	b.msgs = append(b.msgs, m)
	if b.timer != nil {
		b.timer.Stop()
	}
	id := m.MediaGroupID
	b.timer = time.AfterFunc(albumWait, func() { a.fire(id) })
}

func (a *albums) fire(id string) {
	a.mu.Lock()
	b := a.pending[id]
	delete(a.pending, id)
	a.mu.Unlock()
	if b != nil {
		a.c.deliverMessages(context.Background(), b.msgs)
	}
}

// flushAll delivers what is pending now (used when listening stops).
func (a *albums) flushAll() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.pending))
	for id, b := range a.pending {
		b.timer.Stop()
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.fire(id)
	}
}
