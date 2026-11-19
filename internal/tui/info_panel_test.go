package tui

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/client"
)

// /info shows the session id, the system and the terminal (what the
// SessionInfo tool reports), and, when there are any, the channels.
func TestInfoPanelShowsIdSystemAndTerminal(t *testing.T) {
	f := newFallbackFixtureWith(t, agent.AgentOptions{Terminal: "Workspace (t9)"})
	tu := f.tui(f.sessionID)
	if err := tu.autoConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	tu.showInfo()
	got := stripANSI(screenText(tu))
	for _, want := range []string{
		"id        " + f.sessionID,
		"system    " + runtime.GOOS + "/" + runtime.GOARCH,
		"terminal  Workspace (t9)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel lacks %q:\n%s", want, got)
		}
	}
}

// An agent with no terminal of its own (serve) shows no terminal line.
func TestInfoPanelOmitsAnUnknownTerminal(t *testing.T) {
	f := newFallbackFixture(t)
	tu := f.tui(f.sessionID)
	if err := tu.autoConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	tu.showInfo()
	if got := stripANSI(screenText(tu)); strings.Contains(got, "terminal  ") {
		t.Errorf("a terminal line without a terminal:\n%s", got)
	}
}

func TestChannelGroups(t *testing.T) {
	g := channelGroups([]client.ChannelInfo{
		{Type: "slack", ID: "C1", Name: "#a"}, {Type: "telegram", ID: "T1", Name: "@bot"}, {Type: "slack", ID: "D9"},
	})
	if len(g) != 2 || g[0].typ != "slack" || strings.Join(g[0].names, ",") != "#a,D9" || g[1].typ != "telegram" {
		t.Errorf("groups = %+v", g)
	}
}
