package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// The icon picked for each origin: a colleague's prompt has its own, distinct from
// the scheduler's clock and from the user's own prompt.
func TestPromptIconPerOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"":          userIcon,
		"user":      userIcon,
		"scheduled": scheduleIcon,
		"colleague": colleagueIcon,
		"telegram":  userIcon,
	} {
		if got := promptIcon(origin); got != want {
			t.Errorf("promptIcon(%q) = %q, want %q", origin, got, want)
		}
	}
	if colleagueIcon == userIcon || colleagueIcon == scheduleIcon {
		t.Errorf("the colleague icon %q must be distinct from user %q and scheduled %q", colleagueIcon, userIcon, scheduleIcon)
	}
}

// The live path: a prompt a colleague sends into the session the TUI is showing
// arrives as a received_prompt event with origin "colleague" and is echoed with the
// colleague icon — the same single echo path the scheduler's prompts use.
func TestTUIEchoesAColleaguePromptWithItsIcon(t *testing.T) {
	f := newFallbackFixture(t)
	tu := f.tui(f.sessionID)
	tu.autoConnect(context.Background())
	if tu.sessionID != f.sessionID {
		t.Fatalf("setup: the TUI is on %q, want %q", tu.sessionID, f.sessionID)
	}

	// autoConnect opens the event stream in a goroutine, so a prompt sent right
	// away can be emitted before the stream is subscribed and its echo is lost.
	// Replace it with a stream opened here: StreamEvents returns once the server
	// has subscribed this client, so everything sent after it is delivered.
	tu.sseCancel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events, err := f.c.StreamEvents(ctx, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	go tu.consumeEvents(ctx, events)

	// A colleague prompts the session the TUI holds — exactly what ColleagueAsk does.
	go f.c.Ask(f.sessionID, "colleague says hi", 20*time.Second, client.WithOrigin("colleague"), client.WithoutQueue()) //nolint:errcheck
	deadline := time.Now().Add(10 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if got = screenText(tu); strings.Contains(got, "colleague says hi") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(got, colleagueIcon+" colleague says hi") {
		t.Fatalf("the TUI did not echo the colleague's prompt with its icon %q:\n%s", colleagueIcon, got)
	}
	if strings.Contains(got, userIcon+" colleague says hi") {
		t.Errorf("a colleague's prompt must NOT be shown as typed by the user:\n%s", got)
	}
}

// A reloaded history keeps the distinction: the persisted origin picks the icon.
// The messages are written the way the session writes them (Meta.Origin on the
// user message), so this exercises the TUI's history rendering, not a model.
func TestTUIHistoryShowsPersistedOrigins(t *testing.T) {
	f := newFallbackFixture(t)
	user := func(text, origin string) types.Message {
		m := types.NewUserTextMessage(text)
		if origin != "" {
			m.Meta = &types.MessageMeta{Origin: origin}
		}
		return m
	}
	for _, m := range []types.Message{
		user("typed by the user", ""),
		types.NewAssistantToolCallMessage("answer one", "", "", nil),
		user("sent by a colleague", "colleague"),
		types.NewAssistantToolCallMessage("answer two", "", "", nil),
		user("fired by the scheduler", "scheduled"),
		types.NewAssistantToolCallMessage("answer three", "", "", nil),
	} {
		if err := f.store.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	tu := f.tui(f.sessionID)
	tu.autoConnect(context.Background())
	got := screenText(tu)
	for _, want := range []string{userIcon + " typed by the user", colleagueIcon + " sent by a colleague", scheduleIcon + " fired by the scheduler"} {
		if !strings.Contains(got, want) {
			t.Errorf("the reloaded history lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, userIcon+" sent by a colleague") || strings.Contains(got, userIcon+" fired by the scheduler") {
		t.Errorf("a colleague's or scheduled prompt shows as typed by the user once reloaded:\n%s", got)
	}
}
