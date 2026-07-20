package telegram

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// chatPump owns one chat's harness session and the goroutine that drains its
// SSE stream, turning the agent's text output into outgoing Telegram messages.
// One pump per active chat; it stays alive for the process so scheduled prompts
// (routed to this session by owner) are delivered to the chat too.
type chatPump struct {
	chatID    int64
	sessionID string
	buf       strings.Builder // accumulates the current turn's text
	typingMu  sync.Mutex
	typingCancel context.CancelFunc // stops the current typing heartbeat, if any
}

// startTyping keeps a "typing…" indicator alive in the chat until stopTyping is
// called. Telegram clears the status after ~5s, so a goroutine re-sends it every
// few seconds. Calling it again while active is a no-op (the existing heartbeat
// keeps running).
func (p *chatPump) startTyping(ctx context.Context, bot *Bot) {
	p.typingMu.Lock()
	defer p.typingMu.Unlock()
	if p.typingCancel != nil {
		return // already beating
	}
	tctx, cancel := context.WithCancel(ctx)
	p.typingCancel = cancel
	go func() {
		_ = bot.SendChatAction(tctx, p.chatID, "typing")
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-tctx.Done():
				return
			case <-ticker.C:
				_ = bot.SendChatAction(tctx, p.chatID, "typing")
			}
		}
	}()
}

// stopTyping halts the typing heartbeat (no-op if not running).
func (p *chatPump) stopTyping() {
	p.typingMu.Lock()
	defer p.typingMu.Unlock()
	if p.typingCancel != nil {
		p.typingCancel()
		p.typingCancel = nil
	}
}

// pumpFor returns the chat's live pump, creating the session (or resuming the
// stored one) and starting the SSE drain on first use.
func (t *Transport) pumpFor(ctx context.Context, chatID int64) (*chatPump, error) {
	t.mu.Lock()
	if p := t.pumps[chatID]; p != nil {
		t.mu.Unlock()
		return p, nil
	}
	t.mu.Unlock()

	sessionID, err := t.acquireSession(chatID)
	if err != nil {
		return nil, err
	}

	p := &chatPump{chatID: chatID, sessionID: sessionID}
	t.mu.Lock()
	t.pumps[chatID] = p
	t.mu.Unlock()

	events, err := t.api.StreamEvents(ctx, sessionID)
	if err != nil {
		t.mu.Lock()
		delete(t.pumps, chatID)
		t.mu.Unlock()
		return nil, err
	}
	go t.drain(ctx, p, events)
	return p, nil
}

// acquireSession resolves the chat's session id: resume the stored one if it
// still exists, otherwise create a fresh session and persist the mapping.
func (t *Transport) acquireSession(chatID int64) (string, error) {
	if id, ok := t.store.sessionFor(chatID); ok {
		if resumed, err := t.api.ResumeSession(id); err == nil && resumed {
			return id, nil
		}
		// Stored session is gone or failed to resume — fall through to create.
	}
	id, err := t.api.CreateSession(t.model, t.cwd)
	if err != nil {
		return "", err
	}
	if err := t.store.bind(chatID, id); err != nil {
		log.Printf("telegram: persist chat mapping: %v", err)
	}
	return id, nil
}

// resetChat closes the chat's current session and clears its mapping, so the
// next message (or /new) starts fresh.
func (t *Transport) resetChat(ctx context.Context, chatID int64) {
	t.mu.Lock()
	p := t.pumps[chatID]
	delete(t.pumps, chatID)
	t.mu.Unlock()
	if p != nil {
		_ = t.api.CloseSession(p.sessionID)
	}
	_ = t.store.unbind(chatID)
}

// drain consumes a session's SSE events. Text accumulates as it streams and is
// flushed to the chat at natural boundaries: before each tool call (so the user
// sees the agent's running commentary as it happens, not one lump at the end)
// and at turn end. A "typing" indicator is kept alive with a heartbeat while the
// agent is working, since Telegram clears it after ~5s. The pump exits when the
// stream closes (ctx cancelled or server ended it).
func (t *Transport) drain(ctx context.Context, p *chatPump, events <-chan map[string]any) {
	for evt := range events {
		typ, _ := evt["type"].(string)
		switch typ {
		case "turn_start":
			p.startTyping(ctx, t.bot)
		case "text":
			if d, _ := evt["delta"].(string); d != "" {
				p.buf.WriteString(d)
			}
		case "received_prompt":
			// A prompt the transport didn't send — i.e. a scheduled one fired by the
			// engine into this session. Log it, and keep typing alive for it too.
			if origin, _ := evt["origin"].(string); origin == "scheduled" {
				text, _ := evt["text"].(string)
				log.Printf("telegram: ◷ scheduled prompt → chat %d: %s", p.chatID, oneLine(text, 200))
				p.startTyping(ctx, t.bot)
			}
		case "text_end":
			// A text block finished streaming (before a tool call, or at turn end).
			// Flush it now so the user sees each piece of commentary in real time
			// rather than bundled at the end.
			t.flush(ctx, p)
		case "tool_call":
			name, _ := evt["tool_name"].(string)
			log.Printf("telegram:   ⚙ tool %s (chat %d)", name, p.chatID)
		case "turn_end":
			t.flush(ctx, p)
			p.stopTyping()
		case "error":
			p.buf.Reset()
			p.stopTyping()
			if msg, _ := evt["message"].(string); msg != "" {
				t.reply(ctx, p.chatID, "⚠️ "+msg)
			}
		case "max_turns_reached":
			t.flush(ctx, p)
			p.stopTyping()
		}
	}
	p.stopTyping()
}

// flush sends the buffered text (if any) to the chat and clears the buffer.
func (t *Transport) flush(ctx context.Context, p *chatPump) {
	text := strings.TrimSpace(p.buf.String())
	p.buf.Reset()
	if text != "" {
		t.reply(ctx, p.chatID, text)
	}
}

// reply delivers agent text to a chat. It first extracts any <tel:uploadFile>
// action tags (always stripping them so they never leak to the user), sends the
// cleaned text as MarkdownV2 (falling back to plain text on a 400), then uploads
// the tagged files. A parse/upload failure is a no-op for the user — the text is
// still cleaned and sent.
func (t *Transport) reply(ctx context.Context, chatID int64, text string) {
	uploads, text := extractUploads(text)
	if text != "" {
		t.sendText(ctx, chatID, text)
	}
	if len(uploads) > 0 {
		t.sendUploads(ctx, chatID, uploads)
	}
}

// sendText sends plain agent text as MarkdownV2, splitting long messages and
// falling back to plain text if Telegram rejects the markdown (a 400).
func (t *Transport) sendText(ctx context.Context, chatID int64, text string) {
	chunks := splitMessage(text)
	for _, chunk := range chunks {
		md := toMarkdownV2(chunk)
		err := t.bot.SendMessage(ctx, chatID, md, "MarkdownV2")
		if err != nil && errorCode(err) == 400 {
			// Markdown parse failed — send the original as plain text so the user
			// still gets the message.
			err = t.bot.SendMessage(ctx, chatID, chunk, "")
		}
		if err != nil {
			log.Printf("telegram: send to %d: %v", chatID, err)
		}
	}
	if n := len(chunks); n > 0 {
		suffix := ""
		if n > 1 {
			suffix = fmt.Sprintf(" [%d msgs]", n)
		}
		log.Printf("telegram: → reply to chat %d%s: %s", chatID, suffix, oneLine(text, 200))
	}
}
