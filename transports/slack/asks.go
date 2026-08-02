package slack

import (
	"context"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/types"
)

// askReply is what a pending SlackAsk receives once the human replies —
// captured from the exact same RTMEvent handleEvent would otherwise have
// turned into a normal agent prompt (see tryDeliverAsk). text is the raw
// reply text (untouched — no <slack:*> tags); images/attachTags are
// produced by the SAME t.handleFiles helper the normal prompt path uses, so
// a reply carrying an image or a text file gets identical treatment to any
// other incoming Slack message.
type askReply struct {
	text       string
	images     []types.ImageData
	attachTags []string
}

// registerAsk registers a pending ask for channelID, returning the channel
// the reply (or a synthetic timeout — see waitForAskReply) will arrive on.
// Returns an error if this DM already has a pending ask — SlackAsk allows
// at most one in flight per channel at a time, so a second concurrent
// question doesn't race the first for the same incoming reply.
func (t *Transport) registerAsk(channelID string) (chan askReply, error) {
	t.asksMu.Lock()
	defer t.asksMu.Unlock()
	if _, exists := t.pendingAsks[channelID]; exists {
		return nil, fmt.Errorf("already waiting for a reply in this DM — wait for it to resolve or time out first")
	}
	ch := make(chan askReply, 1)
	t.pendingAsks[channelID] = ch
	return ch, nil
}

// unregisterAsk removes a pending ask — called via defer by SlackAsk's
// executor so the entry is cleaned up on EVERY exit path (a real reply
// arriving, the timeout firing, or ctx being cancelled), guaranteeing a
// later SlackAsk to the same DM is never blocked by a stale entry.
func (t *Transport) unregisterAsk(channelID string) {
	t.asksMu.Lock()
	defer t.asksMu.Unlock()
	delete(t.pendingAsks, channelID)
}

// tryDeliverAsk checks whether evt.Channel has a pending SlackAsk waiting
// for a reply and, if so, delivers this event's content there (and reports
// true, telling handleEvent to stop — this message is the awaited reply,
// not a new prompt for the agent). Declines (returns false) for a slash
// command — a human typing "/stop" while an ask is pending is almost
// certainly the command, not their answer, so it falls through to
// handleCommand as usual.
func (t *Transport) tryDeliverAsk(ctx context.Context, evt *RTMEvent) bool {
	trimmed := strings.TrimSpace(evt.Text)
	if strings.HasPrefix(trimmed, "/") {
		return false
	}

	t.asksMu.Lock()
	ch, ok := t.pendingAsks[evt.Channel]
	t.asksMu.Unlock()
	if !ok {
		return false
	}

	var images []types.ImageData
	var attachTags []string
	if len(evt.Files) > 0 {
		images, attachTags = t.handleFiles(ctx, evt.Channel, evt.Files)
	}
	if trimmed == "" && len(images) == 0 && len(attachTags) == 0 {
		// Genuinely empty event (shouldn't normally reach here given the
		// earlier evt.Channel == "" guard already ran, but stay defensive) —
		// nothing meaningful to deliver as a reply.
		return false
	}

	// Non-blocking send: the channel is buffered (size 1) and there is
	// exactly one registered receiver per entry, so this never blocks in
	// practice — the guard only protects against a delivery racing a
	// timeout that already fired and removed the entry.
	select {
	case ch <- askReply{text: trimmed, images: images, attachTags: attachTags}:
		return true
	default:
		return false
	}
}
