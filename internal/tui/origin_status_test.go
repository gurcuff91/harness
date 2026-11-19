package tui

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/components"
)

func TestOriginOfAHerdrPrompt(t *testing.T) {
	for text, want := range map[string]string{
		`<hc from="t17 jade">hola</hc>`: "herdr",
		"  \n<hc from=\"t1\">x</hc>":    "herdr",
		"<hc>a note about t3</hc>":      "herdr",
		"fix the <hc> parser":           "",
		"<hcx>":                         "",
		"hello":                         "",
		"":                              "",
	} {
		if got := originOf(text); got != want {
			t.Errorf("originOf(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestChannelsLabel(t *testing.T) {
	if f, s := channelsLabel(nil); f != "" || s != "" {
		t.Errorf("none: %q %q", f, s)
	}
	f, s := channelsLabel([]client.ChannelInfo{
		{Type: "slack", ID: "C1", Name: "#producto-y"},
		{Type: "slack", ID: "D9", Name: "@gus"},
		{Type: "other", ID: "X1"}, // no name: its id
	})
	if f != "⇄ #producto-y, @gus, X1" || s != "⇄ slack ×2, other" {
		t.Errorf("full %q, short %q", f, s)
	}
}

// The status line shows the channels by name, and the short form when the
// names do not fit.
func TestStatusLineShortensTheChannels(t *testing.T) {
	tt := components.NewTruncatedText("", 0)
	full, short := channelsLabel([]client.ChannelInfo{{Type: "slack", ID: "C1", Name: "#a-very-long-channel-name"}, {Type: "slack", ID: "C2", Name: "#another-long-one"}})
	tt.SetTexts("~/p • Session • "+full, "~/p • Session • "+short)
	wide := ansi.StripEscapes(tt.Render(200)[0])
	if !strings.Contains(wide, "#a-very-long-channel-name, #another-long-one") {
		t.Errorf("wide: %q", wide)
	}
	narrow := ansi.StripEscapes(tt.Render(40)[0])
	if !strings.Contains(narrow, "⇄ slack ×2") {
		t.Errorf("narrow: %q", narrow)
	}
}
