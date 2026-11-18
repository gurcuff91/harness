package slack

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/types"
)

// typingInterval is how often the typing indicator is repeated while a turn
// works (Slack clears it after ~5 s). A variable for tests.
var typingInterval = 4 * time.Second

// publisher posts the turns of one conversation, in order. Events are queued
// by HandleEvent (which must never block the session) and consumed by one
// goroutine — the same behavior as the transport's pump: the turn's text is
// accumulated with a typing indicator and posted at every natural cut
// (text_end, before a tool runs, turn_end, max iterations), processed first
// (uploads, mrkdwn, splitting); errors and compaction notices are posted too.
type publisher struct {
	c  *Channel
	id string

	mu     sync.Mutex
	queue  []types.Event
	wake   chan struct{}
	closed bool
	done   chan struct{}

	buf          strings.Builder
	typingCancel context.CancelFunc
}

func newPublisher(c *Channel, id string) *publisher {
	p := &publisher{c: c, id: id, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go p.run()
	return p
}

// push queues one event without blocking.
func (p *publisher) push(e types.Event) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.queue = append(p.queue, e)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// close stops the publisher once what is queued has been handled.
func (p *publisher) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	<-p.done
}

func (p *publisher) run() {
	defer close(p.done)
	defer p.stopTyping()
	ctx := context.Background()
	for {
		p.mu.Lock()
		batch := p.queue
		p.queue = nil
		closed := p.closed
		p.mu.Unlock()
		for _, e := range batch {
			p.handle(ctx, e)
		}
		if closed && len(batch) == 0 {
			return
		}
		if len(batch) == 0 {
			<-p.wake
		}
	}
}

// postTimeout bounds every Slack call a publisher makes, so a stalled Slack
// can never hold a session's Close (which waits for the queue) forever.
var postTimeout = 30 * time.Second

func (p *publisher) handle(parent context.Context, e types.Event) {
	ctx, cancel := context.WithTimeout(parent, postTimeout)
	defer cancel()
	switch e.Type {
	case types.EventTurnStart:
		p.startTyping()
	case types.EventStreamTextDelta:
		p.buf.WriteString(e.Delta)
	case types.EventStreamTextEnd, types.EventToolCall, types.EventMaxIterationsReached:
		p.flush(ctx)
	case types.EventTurnEnd:
		p.flush(ctx)
		p.stopTyping()
	case types.EventCompactStart:
		p.post(ctx, "🗜 Context almost full — compacting automatically…")
	case types.EventCompactEnd:
		p.post(ctx, "✅ Conversation compacted.")
	case types.EventStop:
		p.stopTyping()
	case types.EventError:
		p.buf.Reset()
		p.stopTyping()
		if e.Message != "" || len(e.Details) > 0 {
			p.post(ctx, formatError(e.Message, e.Details))
		}
	}
}

// flush posts the accumulated text (uploading the files it asks for).
func (p *publisher) flush(ctx context.Context) {
	text := strings.TrimSpace(p.buf.String())
	p.buf.Reset()
	if text == "" {
		return
	}
	paths, clean := extractUploads(text)
	if len(paths) == 0 {
		p.post(ctx, clean)
		return
	}
	comment := toMrkdwn(clean)
	for i, path := range paths {
		c := ""
		if i == 0 {
			c = comment
		}
		if err := p.c.bot.UploadFile(ctx, p.id, path, c); err != nil {
			p.c.logger.Error("slack-channel", "upload_file", "channel", p.id, "path", path, "error", err.Error())
			if i == 0 && comment != "" {
				p.post(ctx, clean)
			}
			p.post(ctx, "⚠️ Couldn't upload file: "+path)
		}
	}
}

// post sends text (CommonMark → mrkdwn, split to Slack's size limit).
func (p *publisher) post(ctx context.Context, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	for _, chunk := range splitMessage(toMrkdwn(text)) {
		if err := p.c.bot.PostMessage(ctx, p.id, chunk); err != nil {
			p.c.logger.Error("slack-channel", "post", "channel", p.id, "error", err.Error())
		}
	}
	p.c.logger.Info("slack-channel", "reply", "channel", p.id, "text", oneLine(text, 200))
}

func (p *publisher) startTyping() {
	if p.typingCancel != nil {
		return
	}
	tctx, cancel := context.WithCancel(context.Background())
	p.typingCancel = cancel
	go func() {
		p.c.sendTyping(p.id)
		t := time.NewTicker(typingInterval)
		defer t.Stop()
		for {
			select {
			case <-tctx.Done():
				return
			case <-t.C:
				p.c.sendTyping(p.id)
			}
		}
	}()
}

func (p *publisher) stopTyping() {
	if p.typingCancel != nil {
		p.typingCancel()
		p.typingCancel = nil
	}
}
