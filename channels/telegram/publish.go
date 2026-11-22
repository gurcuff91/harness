package telegram

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/types"
)

// typingInterval repeats the typing status while a turn works (Telegram clears
// it after ~5 s). A variable for tests.
var typingInterval = 4 * time.Second

// postTimeout bounds every Telegram call a publisher makes, so a stalled API
// can never hold a session's Close (which waits for the queue) forever.
var postTimeout = 30 * time.Second

// publisher posts the turns answering one chat, in order. HandleEvent queues
// (it must never block the session); one goroutine posts: the turn's text is
// accumulated with a typing status and sent at every natural cut (text_end,
// before a tool runs, turn_end, max iterations), its uploads sent after it;
// errors and compaction notices are posted too.
type publisher struct {
	c    *Channel
	chat int64

	mu     sync.Mutex
	queue  []types.Event
	wake   chan struct{}
	closed bool
	done   chan struct{}

	buf          strings.Builder
	typingCancel context.CancelFunc
}

func newPublisher(c *Channel, chat int64) *publisher {
	p := &publisher{c: c, chat: chat, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go p.run()
	return p
}

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
	for {
		p.mu.Lock()
		batch := p.queue
		p.queue = nil
		closed := p.closed
		p.mu.Unlock()
		for _, e := range batch {
			p.handle(e)
		}
		if closed && len(batch) == 0 {
			return
		}
		if len(batch) == 0 {
			<-p.wake
		}
	}
}

func (p *publisher) handle(e types.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), postTimeout)
	defer cancel()
	switch e.Type {
	case types.EventTurnStart, types.EventReceivedPrompt, types.EventFollowUpStart:
		p.startTyping()
	case types.EventStreamTextDelta:
		p.buf.WriteString(e.Delta)
	case types.EventStreamTextEnd, types.EventToolCall, types.EventMaxIterationsReached:
		p.flush(ctx)
	case types.EventTurnEnd:
		p.flush(ctx)
		p.stopTyping()
	case types.EventCompactStart:
		sendText(ctx, p.c, p.chat, "🗜 Context almost full — compacting automatically…")
	case types.EventCompactEnd:
		sendText(ctx, p.c, p.chat, "✅ Conversation compacted.")
	case types.EventStop:
		p.stopTyping()
	case types.EventError:
		p.buf.Reset()
		p.stopTyping()
		if e.Message != "" || len(e.Details) > 0 {
			sendText(ctx, p.c, p.chat, formatError(e.Message, e.Details))
		}
	}
}

// flush posts the accumulated text, then the files it asks for.
func (p *publisher) flush(ctx context.Context) {
	text := strings.TrimSpace(p.buf.String())
	p.buf.Reset()
	if text == "" {
		return
	}
	paths, clean := extractUploads(text)
	sendText(ctx, p.c, p.chat, clean)
	for _, path := range paths {
		if err := sendUpload(ctx, p.c.bot, p.chat, path); err != nil {
			p.c.logger.Error("telegram-channel", "upload", "chat", p.chat, "path", path, "error", err.Error())
			sendText(ctx, p.c, p.chat, "⚠️ Couldn't send file: "+path)
		}
	}
}

// sendText posts text as MarkdownV2 in chunks of Telegram's size, each falling
// back to plain text when Telegram rejects the markup.
func sendText(ctx context.Context, c *Channel, chat int64, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	for _, chunk := range splitMessage(text) {
		err := c.bot.SendMessage(ctx, chat, toMarkdownV2(chunk), "MarkdownV2")
		if err != nil && errorCode(err) == 400 {
			err = c.bot.SendMessage(ctx, chat, chunk, "")
		}
		if err != nil {
			c.logger.Error("telegram-channel", "send", "chat", chat, "error", err.Error())
		}
	}
	c.logger.Info("telegram-channel", "reply", "chat", chat, "text", oneLine(text, 200))
}

func (p *publisher) startTyping() {
	if p.typingCancel != nil {
		return
	}
	tctx, cancel := context.WithCancel(context.Background())
	p.typingCancel = cancel
	go func() {
		send := func() {
			ctx, cancel := context.WithTimeout(tctx, 10*time.Second)
			defer cancel()
			_ = p.c.bot.SendChatAction(ctx, p.chat, "typing")
		}
		send()
		t := time.NewTicker(typingInterval)
		defer t.Stop()
		for {
			select {
			case <-tctx.Done():
				return
			case <-t.C:
				send()
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
