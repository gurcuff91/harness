package slack

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gurcuff91/harness/internal/client"
	"github.com/gurcuff91/harness/internal/logx"
)

// channelPump owns one Slack channel's harness session and the goroutine that
// drains its SSE stream, turning the agent's text output into outgoing Slack
// messages. One pump per active channel (DM or mention channel); it stays alive
// for the process so scheduled prompts are also delivered.
type channelPump struct {
	channelID string
	sessionID string
	model     string          // the session's actual model (for logs)
	buf       strings.Builder // accumulates the current turn's text
	lastUser  string          // Slack user ID of the last message sender (for @mention in channels)

	mu              sync.Mutex
	typingCancel    context.CancelFunc // stops the current typing heartbeat, if any
	workingCtx      context.Context    // cancelled when pump is reset
	workingStop     context.CancelFunc
	compactExpected atomic.Bool
}

// pumpFor returns the live pump for a channel, creating the session (or
// resuming the stored one) and starting the SSE drain on first use.
func (t *Transport) pumpFor(ctx context.Context, channelID string) (*channelPump, error) {
	t.mu.Lock()
	if p := t.pumps[channelID]; p != nil {
		t.mu.Unlock()
		return p, nil
	}
	t.mu.Unlock()

	sessionID, err := t.acquireSession(channelID)
	if err != nil {
		return nil, err
	}

	pCtx, pCancel := context.WithCancel(ctx)
	p := &channelPump{
		channelID:   channelID,
		sessionID:   sessionID,
		workingCtx:  pCtx,
		workingStop: pCancel,
	}
	if meta, err := t.api.GetSession(sessionID); err == nil {
		p.model = meta.Model
	}

	t.mu.Lock()
	t.pumps[channelID] = p
	t.mu.Unlock()

	events, err := t.api.StreamEvents(pCtx, sessionID)
	if err != nil {
		t.mu.Lock()
		delete(t.pumps, channelID)
		t.mu.Unlock()
		pCancel()
		return nil, err
	}
	go t.drain(ctx, p, events)
	return p, nil
}

// acquireSession resolves the channel's session: resume the stored one if it
// still exists, otherwise create a fresh session and persist the mapping.
func (t *Transport) acquireSession(channelID string) (string, error) {
	if id, ok := t.store.sessionFor(channelID); ok {
		if _, err := t.api.ResumeSession(id); err == nil {
			return id, nil
		}
		// Stored session gone — fall through to create a new one.
		_ = t.store.unbind(channelID)
	}

	sess, err := t.api.CreateSession(t.model, t.cwd, slackSessionName())
	if err != nil {
		return "", err
	}
	if err := t.store.bind(channelID, sess.ID); err != nil {
		logx.Error("slack", "persist_mapping", "channel", channelID, "error", err.Error())
	}
	return sess.ID, nil
}

// resetChannel closes the channel's current session and clears its mapping so
// the next message starts a fresh session.
func (t *Transport) resetChannel(ctx context.Context, channelID string) {
	t.mu.Lock()
	p := t.pumps[channelID]
	delete(t.pumps, channelID)
	t.mu.Unlock()

	if p != nil {
		p.workingStop()
		_, _ = t.api.CloseSession(p.sessionID)
	}
	_ = t.store.unbind(channelID)
}

// startTyping keeps a typing indicator alive in the channel until stopTyping is
// called. Slack clears it after ~5s, so a goroutine re-sends every 4s.
// Calling it again while active is a no-op (the existing heartbeat continues).
func (t *Transport) startTyping(ctx context.Context, p *channelPump) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.typingCancel != nil {
		return // already beating
	}
	tctx, cancel := context.WithCancel(ctx)
	p.typingCancel = cancel
	go func() {
		t.SendTyping(p.channelID)
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-tctx.Done():
				return
			case <-ticker.C:
				t.SendTyping(p.channelID)
			}
		}
	}()
}

// stopTyping halts the typing heartbeat.
func (t *Transport) stopTyping(p *channelPump) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.typingCancel != nil {
		p.typingCancel()
		p.typingCancel = nil
	}
}

// drain consumes a channel session's SSE events. Text accumulates and is
// flushed to the Slack channel at natural boundaries (text_end, turn_end).
func (t *Transport) drain(ctx context.Context, p *channelPump, events <-chan client.Event) {
	for evt := range events {
		switch evt.Type {
		case "turn_start":
			t.startTyping(ctx, p)
		case "text":
			if evt.Delta != "" {
				p.buf.WriteString(evt.Delta)
			}
		case "text_end":
			// Text block finished streaming — flush immediately so the user sees
			// the agent's commentary before the tool executes.
			t.flushReason(ctx, p, "text_end")
		case "tool_call":
			// Flush any accumulated text before executing the tool, so
			// mid-turn commentary ("Let me check that…") reaches the user
			// in real time rather than being held until turn_end.
			t.flushReason(ctx, p, "tool_call")
			logx.Info("slack", "tool", "channel", p.channelID, "name", evt.ToolName)
		case "turn_end":
			t.flushReason(ctx, p, "turn_end")
			t.stopTyping(p)
		case "compact_start":
			if !p.compactExpected.Swap(false) {
				t.send(ctx, p.channelID, "🗜 Context almost full — compacting automatically…")
			}
		case "compact_end":
			t.send(ctx, p.channelID, "✅ Conversation compacted.")
			t.stopTyping(p)
		case "stop":
			t.stopTyping(p)
		case "error":
			p.buf.Reset()
			t.stopTyping(p)
			if evt.Message != "" || len(evt.Details) > 0 {
				t.send(ctx, p.channelID, formatError(evt.Message, evt.Details))
			}
		case "max_iterations_reached":
			t.flushReason(ctx, p, "max_iterations_reached")
		case "received_prompt":
			if evt.Origin == "scheduled" {
				logx.Info("slack", "scheduled_prompt",
					"channel", p.channelID, "text", oneLine(evt.Text, 200))
				t.startTyping(ctx, p)
			}
		}
	}
	t.stopTyping(p)
}

// flushReason sends the buffered text (if any) to the channel and resets the
// buffer. reason is the SSE event that triggered the flush — it is passed to
// sendWithUploads so the reply log can record why this flush happened, giving
// visibility into mid-turn text (flushed on tool_call) vs end-of-turn text.
func (t *Transport) flushReason(ctx context.Context, p *channelPump, reason string) {
	text := strings.TrimSpace(p.buf.String())
	p.buf.Reset()
	if text != "" {
		t.sendWithUploads(ctx, p.channelID, text, reason)
	}
}

// send delivers text to a Slack channel without a trigger reason.
// Used for system messages (errors, compact notices, command replies).
func (t *Transport) send(ctx context.Context, channelID, text string) {
	t.sendLogged(ctx, channelID, text, "")
}

// sendLogged delivers text to a Slack channel, converting CommonMark to mrkdwn,
// splitting if needed, and logging the reply with an optional trigger reason.
// In channels (C…) the first chunk is prefixed with <@USER> to mention the
// sender — the agent doesn't know who wrote; the transport adds it here.
func (t *Transport) sendLogged(ctx context.Context, channelID, text, reason string) {
	text = toMrkdwn(text)
	chunks := splitMessage(text)

	// Prepend @mention on the first chunk for channel messages.
	if strings.HasPrefix(channelID, "C") && len(chunks) > 0 {
		t.mu.Lock()
		p := t.pumps[channelID]
		t.mu.Unlock()
		if p != nil {
			p.mu.Lock()
			user := p.lastUser
			p.mu.Unlock()
			if user != "" {
				chunks[0] = "<@" + user + "> " + chunks[0]
			}
		}
	}

	for _, chunk := range chunks {
		if err := t.bot.PostMessage(ctx, channelID, chunk); err != nil {
			logx.Error("slack", "send", "channel", channelID, "error", err.Error())
		}
	}
	if n := len(chunks); n > 0 {
		kv := []any{"channel", channelID}
		t.mu.Lock()
		if p := t.pumps[channelID]; p != nil && p.model != "" {
			kv = append(kv, "model", p.model)
		}
		t.mu.Unlock()
		kv = append(kv, "text", oneLine(text, 200))
		if n > 1 {
			kv = append(kv, "messages", n)
		}
		if reason != "" {
			kv = append(kv, "trigger", reason)
		}
		logx.Info("slack", "reply", kv...)
	}
}

// replyError delivers an error to a channel. A *client.Error with details gets
// rich rendering; plain errors get the ⚠️ prefix.
func (t *Transport) replyError(ctx context.Context, channelID string, err error) {
	var ae *client.Error
	if errors.As(err, &ae) {
		t.send(ctx, channelID, formatAgentError(ae))
		return
	}
	t.send(ctx, channelID, "⚠️ "+err.Error())
}
