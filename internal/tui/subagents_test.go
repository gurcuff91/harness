package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/client"
)

func names(tu *TUI) []string { return tu.view().subagentNames }

func TestSubagentsLabelFullAndShort(t *testing.T) {
	if f, s := subagentsLabel(nil); f != "" || s != "" {
		t.Errorf("none = %q %q", f, s)
	}
	f, s := subagentsLabel([]string{"scorpion-hero", "kano-storm"})
	if f != "[scorpion-hero, kano-storm]" || s != "[2]" {
		t.Errorf("label = %q %q", f, s)
	}
}

// The sub-agents come BEFORE the channels, two spaces apart.
func TestStatusLineTextsOrderSubagentsBeforeChannels(t *testing.T) {
	chs := []client.ChannelInfo{{Type: "slack", ID: "C1", Name: "#x"}}
	full, short := statusLineTexts([]string{"a", "b"}, chs)
	if full != "subagents [a, b]  channels [slack: #x]" {
		t.Errorf("full = %q", full)
	}
	if short != "subagents [2]  channels [slack: 1]" {
		t.Errorf("short = %q", short)
	}
	if f, _ := statusLineTexts([]string{"a"}, nil); f != "subagents [a]" {
		t.Errorf("only subagents = %q", f)
	}
	if f, _ := statusLineTexts(nil, chs); f != "channels [slack: #x]" {
		t.Errorf("only channels = %q", f)
	}
	if f, s := statusLineTexts(nil, nil); f != "" || s != "" {
		t.Errorf("nothing = %q %q", f, s)
	}
}

func TestSubagentsLineInTheRenderedTree(t *testing.T) {
	tu := layoutTUI(t)
	tu.updateInfo()
	before := len(tu.tui.Render(120))

	ctx := context.Background()
	tu.applySubagentEvent(ctx, client.Event{Type: "subagent_start", Name: "scorpion-hero"})
	tu.applySubagentEvent(ctx, client.Event{Type: "subagent_start", Name: "kano-storm"})
	lines := statusLines(t, tu, 3)
	if lines[0] != "subagents [scorpion-hero, kano-storm]" {
		t.Errorf("line = %q", lines[0])
	}
	if got := len(tu.tui.Render(120)); got != before+1 {
		t.Errorf("%d lines, want %d", got, before+1)
	}

	tu.withView(func() { tu.channelInfos = []client.ChannelInfo{{Type: "slack", ID: "C1", Name: "#x"}} })
	tu.updateInfo()
	if l := statusLines(t, tu, 3)[0]; l != "subagents [scorpion-hero, kano-storm]  channels [slack: #x]" {
		t.Errorf("combined = %q", l)
	}

	tu.applySubagentEvent(ctx, client.Event{Type: "subagent_stop", Name: "scorpion-hero", Result: "ok"})
	if l := statusLines(t, tu, 3)[0]; l != "subagents [kano-storm]  channels [slack: #x]" {
		t.Errorf("after stop = %q", l)
	}
	tu.applySubagentEvent(ctx, client.Event{Type: "subagent_stop", Name: "kano-storm"})
	if l := statusLines(t, tu, 3)[0]; l != "channels [slack: #x]" {
		t.Errorf("only channels = %q", l)
	}
	tu.withView(func() { tu.channelInfos = nil })
	tu.updateInfo()
	if got := len(tu.tui.Render(120)); got != before {
		t.Errorf("the line did not go away: %d, want %d", got, before)
	}
}

func TestSubagentsShortFormWhenItDoesNotFit(t *testing.T) {
	tu := layoutTUI(t)
	var all []string
	for i := range 10 {
		all = append(all, fmt.Sprintf("a-very-long-subagent-name-%d", i))
	}
	tu.withView(func() { tu.subagentNames = all })
	tu.updateInfo()
	if l := statusLines(t, tu, 3)[0]; l != "subagents [10]" {
		t.Errorf("line = %q", l)
	}
}

func TestSubagentEventsAddRemoveNoDuplicates(t *testing.T) {
	tu := newTestTUIForEvents()
	ctx := context.Background()
	start := func(n string) { tu.applySubagentEvent(ctx, client.Event{Type: "subagent_start", Name: n}) }
	stop := func(n string) { tu.applySubagentEvent(ctx, client.Event{Type: "subagent_stop", Name: n}) }
	start("a")
	start("b")
	start("a")
	if got := strings.Join(names(tu), ","); got != "a,b" {
		t.Fatalf("duplicates: %s", got)
	}
	stop("zzz") // unknown: nothing
	stop("a")
	if got := strings.Join(names(tu), ","); got != "b" {
		t.Fatalf("after stop: %s", got)
	}
	stop("b")
	if len(names(tu)) != 0 {
		t.Fatalf("not empty: %v", names(tu))
	}
}

// Through the real consumer: the SSE events reach the list.
func TestConsumeEventsAppliesSubagentEvents(t *testing.T) {
	tu := newTestTUIForEvents()
	ch := make(chan client.Event, 4)
	ch <- client.Event{Type: "subagent_start", Name: "a"}
	ch <- client.Event{Type: "subagent_start", Name: "b"}
	ch <- client.Event{Type: "subagent_stop", Name: "a", Result: "ok"}
	close(ch)
	tu.consumeEvents(context.Background(), ch)
	if got := strings.Join(names(tu), ","); got != "b" {
		t.Fatalf("names = %s", got)
	}
}

func TestSessionSwitchResetsAndInitializesFromTheDetail(t *testing.T) {
	tu := newTestTUIForEvents()
	tu.switchView("s1", "p/m", "off", 10, nil, []string{"x", "y"})
	if got := strings.Join(names(tu), ","); got != "x,y" {
		t.Fatalf("initial from detail: %s", got)
	}
	tu.switchView("s2", "p/m", "off", 10, nil, nil)
	if len(names(tu)) != 0 {
		t.Fatalf("a switch must reset: %v", names(tu))
	}
	// the caller's slice is copied, never aliased
	src := []string{"q"}
	tu.switchView("s3", "p/m", "off", 10, nil, src)
	src[0] = "mutated"
	if names(tu)[0] != "q" {
		t.Fatal("aliased the detail's slice")
	}
}

// An event of a stream that was replaced must not touch the new session.
func TestStaleStreamSubagentEventsAreIgnored(t *testing.T) {
	tu := newTestTUIForEvents()
	tu.switchView("s2", "p/m", "off", 10, nil, []string{"keep"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tu.applySubagentEvent(ctx, client.Event{Type: "subagent_start", Name: "ghost"})
	tu.applySubagentEvent(ctx, client.Event{Type: "subagent_stop", Name: "keep"})
	if got := strings.Join(names(tu), ","); got != "keep" {
		t.Fatalf("names = %s", got)
	}
	ch := make(chan client.Event, 1)
	ch <- client.Event{Type: "subagent_start", Name: "ghost"}
	tu.consumeEvents(ctx, ch)
	if got := strings.Join(names(tu), ","); got != "keep" {
		t.Fatalf("names after consume = %s", got)
	}
}

func TestSubagentReplyPromptUsesTheOutsideIcon(t *testing.T) {
	for _, o := range []string{"subagent:scorpion-hero"} {
		if got := promptIcon(o, `<subagent name="scorpion-hero" reply>x</subagent>`); got != outsideIcon {
			t.Errorf("icon = %q", got)
		}
	}
}

func TestSubagentToolShowsItsPrompt(t *testing.T) {
	if primaryParam["Subagent"] != "prompt" {
		t.Fatalf("Subagent primary param = %q", primaryParam["Subagent"])
	}
}

func TestSubagentStateIsSafeAcrossGoroutines(t *testing.T) {
	tu := newTestTUIForEvents()
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 300 {
			n := fmt.Sprintf("n%d", i%7)
			tu.applySubagentEvent(ctx, client.Event{Type: "subagent_start", Name: n})
			tu.applySubagentEvent(ctx, client.Event{Type: "subagent_stop", Name: n})
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 300 {
			tu.switchView(fmt.Sprintf("s%d", i), "p/m", "off", 1, nil, []string{"x"})
		}
	}()
	for range 300 {
		tu.updateInfo()
	}
	wg.Wait()
}
