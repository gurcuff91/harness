package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// Three icons, no more: the person at this terminal, a schedule, and
// everything that came from outside (any other origin, present or future).
func TestPromptIconPerOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"":              userIcon,
		"user":          userIcon,
		"scheduled":     scheduleIcon,
		"colleague":     outsideIcon,
		"colleague:x":   outsideIcon,
		"herdr":         outsideIcon,
		"slack:C0123":   outsideIcon,
		"telegram":      outsideIcon,
		"something-new": outsideIcon,
	} {
		if got := promptIcon(origin); got != want {
			t.Errorf("promptIcon(%q) = %q, want %q", origin, got, want)
		}
	}
	if outsideIcon == userIcon || outsideIcon == scheduleIcon || userIcon == scheduleIcon {
		t.Error("the three icons must be distinct")
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
	tu.takeSSECancel()()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events, err := f.c.StreamEvents(ctx, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	go tu.consumeEvents(ctx, events)

	// A colleague prompts the session the TUI holds (the colleague channel's origin).
	go f.c.Ask(f.sessionID, "colleague says hi", 20*time.Second, client.WithOrigin("colleague:raiden-god")) //nolint:errcheck
	deadline := time.Now().Add(10 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if got = screenText(tu); strings.Contains(got, "colleague says hi") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(got, outsideIcon+" colleague says hi") {
		t.Fatalf("the TUI did not echo the colleague's prompt with its icon %q:\n%s", outsideIcon, got)
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
	for _, want := range []string{userIcon + " typed by the user", outsideIcon + " sent by a colleague", scheduleIcon + " fired by the scheduler"} {
		if !strings.Contains(got, want) {
			t.Errorf("the reloaded history lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, userIcon+" sent by a colleague") || strings.Contains(got, userIcon+" fired by the scheduler") {
		t.Errorf("a colleague's or scheduled prompt shows as typed by the user once reloaded:\n%s", got)
	}
}

// A message Herdr pastes into the TUI goes to the session with origin "herdr":
// echoed with the outside icon and stored with that origin, so a resumed
// history shows it the same way.
func TestAHerdrPromptIsSentWithItsOrigin(t *testing.T) {
	f := newFallbackFixture(t)
	tu := f.tui(f.sessionID)
	tu.autoConnect(context.Background())
	tu.takeSSECancel()()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events, err := f.c.StreamEvents(ctx, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	go tu.consumeEvents(ctx, events)

	msg := `<hc from="t17 jade">which module path?</hc>`
	tu.submitPrompt(msg)
	tu.submitPrompt("typed by the person")
	deadline := time.Now().Add(10 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if got = screenText(tu); strings.Contains(got, "typed by the person") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(got, outsideIcon+" "+msg) || !strings.Contains(got, userIcon+" typed by the person") {
		t.Fatalf("echo:\n%s", got)
	}
	msgs, err := f.c.GetMessages(f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	origins := map[string]string{}
	for _, m := range msgs {
		if m.Role == "user" && len(m.Parts) > 0 {
			o := ""
			if m.Meta != nil {
				o = m.Meta.Origin
			}
			origins[m.Parts[0].Text] = o
		}
	}
	if origins[msg] != "herdr" || origins["typed by the person"] != "" {
		t.Errorf("stored origins = %v", origins)
	}
}

// Tool icons that must stay distinct from the prompt icons: a colleague tool
// is not a prompt from outside, and a channel's tools (named after its type)
// have their own.
func TestToolIconsForColleaguesAndChannels(t *testing.T) {
	icon := func(n string) string { _, i := toolStyle(n); return i }
	for name, want := range map[string]string{
		"ColleagueList": "⚇", "ColleagueAsk": "⚇",
		"SlackPost": "⌁", "SlackListChannels": "⌁", "SlackMessages": "⌁",
		"mcp__x__Search": "◈", "Unknown": "◈", "Bash": "$",
	} {
		if got := icon(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	for _, n := range []string{"ColleagueAsk", "SlackPost"} {
		if i := icon(n); i == outsideIcon || i == userIcon || i == scheduleIcon {
			t.Errorf("%s uses a prompt icon %q", n, i)
		}
	}
	if outsideIcon != "⇌" {
		t.Errorf("the outside-prompt icon is %q", outsideIcon)
	}
}
