package tui

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/term"
)

// statusLines renders the real UI tree and returns its last n lines, stripped.
func statusLines(t *testing.T, tu *TUI, n int) []string {
	t.Helper()
	lines := tu.tui.Render(120)
	if len(lines) < n {
		t.Fatalf("only %d lines rendered", len(lines))
	}
	out := lines[len(lines)-n:]
	for i := range out {
		out[i] = stripANSI(out[i])
	}
	return out
}

func layoutTUI(t *testing.T) *TUI {
	t.Helper()
	tu := newTestTUIForEvents()
	it := &inputTerminal{recTerminal: recTerminal{mockTerminal: mockTerminal{cols: 120, rows: 24}}}
	tu.newTerminal = func() term.Terminal { return it }
	tu.buildUI()
	tu.sessionName = "New Session"
	tu.model = "p/m"
	return tu
}

// Without channels the status bar keeps its two lines (no empty line where
// the channels would go); with channels, a line of their own comes first.
func TestTheChannelsGetALineOfTheirOwn(t *testing.T) {
	tu := layoutTUI(t)
	tu.updateInfo()
	two := statusLines(t, tu, 3)
	if strings.HasPrefix(two[1], "channels") || strings.HasPrefix(two[0], "channels") {
		t.Fatalf("a channels line without channels: %q", two)
	}
	if !strings.Contains(two[1], "New Session") || !strings.Contains(two[2], "p/m") {
		t.Fatalf("status lines = %q", two)
	}
	before := len(tu.tui.Render(120))

	tu.channelInfos = []client.ChannelInfo{{Type: "slack", ID: "C1", Name: "#social"}, {Type: "telegram", ID: "T1", Name: "@bot"}}
	tu.updateInfo()
	three := statusLines(t, tu, 3)
	if three[0] != "channels [slack: #social] [telegram: @bot]" {
		t.Errorf("channels line = %q", three[0])
	}
	if !strings.Contains(three[1], "New Session") || strings.Contains(three[1], "slack") {
		t.Errorf("session line = %q (the channels must not be on it any more)", three[1])
	}
	if !strings.Contains(three[2], "p/m") {
		t.Errorf("footer = %q", three[2])
	}
	if after := len(tu.tui.Render(120)); after != before+1 {
		t.Errorf("%d lines with channels, %d without: want exactly one more", after, before)
	}

	tu.channelInfos = nil
	tu.updateInfo()
	if got := len(tu.tui.Render(120)); got != before {
		t.Errorf("the channels line did not go away: %d lines, want %d", got, before)
	}
}
