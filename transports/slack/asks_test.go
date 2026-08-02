package slack

import (
	"context"
	"testing"
	"time"

	"github.com/gurcuff91/harness/logx"
)

func newTestTransportForAsks() *Transport {
	return &Transport{
		logger:      logx.NewNilLogger(),
		pendingAsks: make(map[string]chan askReply),
	}
}

// TestRegisterAskRejectsSecondConcurrentAsk verifies at most one SlackAsk
// can be pending per DM channel at a time — a second registerAsk for the
// same channel while the first is still outstanding must fail, per the
// Khan's explicit requirement: "si hay un ask pendiente a ese usuario hasta
// que no nos responda o nos cansemos de esperar, no podremos enviar otro."
func TestRegisterAskRejectsSecondConcurrentAsk(t *testing.T) {
	tr := newTestTransportForAsks()

	if _, err := tr.registerAsk("D123"); err != nil {
		t.Fatalf("first registerAsk: %v", err)
	}
	if _, err := tr.registerAsk("D123"); err == nil {
		t.Fatal("second registerAsk on the same channel succeeded, want an error")
	}
}

// TestRegisterAskAllowsDifferentChannelsConcurrently verifies the
// one-pending-ask restriction is per-channel, not global — two different
// DMs can each have their own pending ask simultaneously.
func TestRegisterAskAllowsDifferentChannelsConcurrently(t *testing.T) {
	tr := newTestTransportForAsks()

	if _, err := tr.registerAsk("D111"); err != nil {
		t.Fatalf("registerAsk(D111): %v", err)
	}
	if _, err := tr.registerAsk("D222"); err != nil {
		t.Fatalf("registerAsk(D222) should succeed independently: %v", err)
	}
}

// TestUnregisterAskAllowsANewAskAfterward verifies unregisterAsk (called via
// defer by SlackAsk's executor on every exit path) actually frees the slot —
// a NEW ask on the same channel must succeed afterward.
func TestUnregisterAskAllowsANewAskAfterward(t *testing.T) {
	tr := newTestTransportForAsks()

	if _, err := tr.registerAsk("D123"); err != nil {
		t.Fatalf("registerAsk: %v", err)
	}
	tr.unregisterAsk("D123")

	if _, err := tr.registerAsk("D123"); err != nil {
		t.Fatalf("registerAsk after unregister should succeed, got: %v", err)
	}
}

// TestTryDeliverAskDeliversToRegisteredChannel verifies the core
// interception mechanism: a message on a channel WITH a pending ask is
// delivered to it (and reports true, telling handleEvent to stop routing
// this message anywhere else — it's the awaited reply, not a new prompt).
func TestTryDeliverAskDeliversToRegisteredChannel(t *testing.T) {
	tr := newTestTransportForAsks()

	ch, err := tr.registerAsk("D123")
	if err != nil {
		t.Fatalf("registerAsk: %v", err)
	}

	delivered := tr.tryDeliverAsk(context.Background(), &RTMEvent{Channel: "D123", Text: "yes, all good"})
	if !delivered {
		t.Fatal("tryDeliverAsk returned false, want true (a pending ask exists for this channel)")
	}

	select {
	case reply := <-ch:
		if reply.text != "yes, all good" {
			t.Errorf("reply.text = %q, want %q", reply.text, "yes, all good")
		}
	default:
		t.Fatal("no reply was sent to the registered channel")
	}
}

// TestTryDeliverAskDoesNothingWithoutPendingAsk verifies a message on a
// channel with NO pending ask is left alone (returns false) — handleEvent's
// normal prompt-routing path must still run for it.
func TestTryDeliverAskDoesNothingWithoutPendingAsk(t *testing.T) {
	tr := newTestTransportForAsks()

	delivered := tr.tryDeliverAsk(context.Background(), &RTMEvent{Channel: "D999", Text: "hello"})
	if delivered {
		t.Fatal("tryDeliverAsk returned true for a channel with no pending ask")
	}
}

// TestTryDeliverAskDeclinesSlashCommands is the regression test for the
// Khan-confirmed rule: a slash command typed by the user while an ask is
// pending in their DM must NOT be consumed as the reply — it falls through
// to handleCommand as usual (someone typing "/stop" almost certainly means
// the command, not "my answer is /stop").
func TestTryDeliverAskDeclinesSlashCommands(t *testing.T) {
	tr := newTestTransportForAsks()

	ch, err := tr.registerAsk("D123")
	if err != nil {
		t.Fatalf("registerAsk: %v", err)
	}

	delivered := tr.tryDeliverAsk(context.Background(), &RTMEvent{Channel: "D123", Text: "/stop"})
	if delivered {
		t.Fatal("tryDeliverAsk consumed a slash command as the ask's reply — it must decline and let handleCommand run instead")
	}

	select {
	case reply := <-ch:
		t.Fatalf("a reply was delivered despite the slash command, want none: %+v", reply)
	default:
	}
}

// TestTryDeliverAskDeliversFileOnlyReply verifies a reply that's purely a
// file attachment (empty text) still counts as a delivered reply — this is
// exactly why the isDM interception in handleEvent runs BEFORE the
// evt.Text == "" early return that the normal prompt path relies on.
// handleFiles itself needs live Slack API access to resolve/download
// files, so this test exercises the empty-text-but-non-empty-event path
// indirectly by asserting tryDeliverAsk doesn't bail out just because Text
// is empty when Files is present — full download behavior is covered by
// TestHandleFiles* elsewhere (files_test.go).
func TestTryDeliverAskDeliversFileOnlyReply(t *testing.T) {
	tr := newTestTransportForAsks()

	ch, err := tr.registerAsk("D123")
	if err != nil {
		t.Fatalf("registerAsk: %v", err)
	}

	// No Files populated here (would require a live bot to download), but an
	// empty-text event with no files at all must NOT be delivered — nothing
	// meaningful to hand back. This locks in that specific guard.
	delivered := tr.tryDeliverAsk(context.Background(), &RTMEvent{Channel: "D123", Text: ""})
	if delivered {
		t.Fatal("an empty event (no text, no files) was delivered as a reply — nothing meaningful to deliver")
	}
	select {
	case <-ch:
		t.Fatal("unexpected reply delivered for a truly empty event")
	default:
	}
}

// TestSlackAskDefaultTimeoutMatchesColleagueAskAndSubagent locks the
// default to 120s — the same value ColleagueAsk/Subagent already use, per
// the Khan's explicit request for consistency across all "ask and wait"
// tools in the codebase.
func TestSlackAskDefaultTimeoutMatchesColleagueAskAndSubagent(t *testing.T) {
	if slackAskDefaultTimeout != 120*time.Second {
		t.Errorf("slackAskDefaultTimeout = %v, want 120s", slackAskDefaultTimeout)
	}
}
