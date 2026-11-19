package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	slack "github.com/gurcuff91/harness/channels/slack"
	"github.com/gurcuff91/harness/types"
)

func TestStoredChannelsNeedASlackLogin(t *testing.T) {
	none := func() (*slack.Credentials, error) { return nil, nil }
	broken := func() (*slack.Credentials, error) { return nil, errors.New("boom") }
	half := func() (*slack.Credentials, error) { return &slack.Credentials{Workspace: "https://x.slack.com"}, nil }
	full := func() (*slack.Credentials, error) {
		return &slack.Credentials{Workspace: "https://x.slack.com", XoxC: "c", XoxD: "d"}, nil
	}
	for name, load := range map[string]func() (*slack.Credentials, error){"none": none, "broken": broken, "half": half} {
		if got := storedChannels(load, false); len(got) != 0 {
			t.Errorf("%s: %d channels registered", name, len(got))
		}
	}
	got := storedChannels(full, false)
	if len(got) != 1 || got[0].Type() != "slack" {
		t.Fatalf("with a login: %v", got)
	}
}

func TestSlackChannelFlag(t *testing.T) {
	slackOnly := []agent.Channel{storedChannels(func() (*slack.Credentials, error) {
		return &slack.Credentials{Workspace: "https://x.slack.com", XoxC: "c", XoxD: "d"}, nil
	}, false)[0]}

	got, err := slackInputChannels([]string{"producto-y", " ", "#general", "C0123"}, slackOnly)
	want := []types.InputChannel{{Type: "slack", Ref: "producto-y"}, {Type: "slack", Ref: "#general"}, {Type: "slack", Ref: "C0123"}}
	if err != nil || len(got) != len(want) {
		t.Fatalf("got %v, %v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if got, err := slackInputChannels(nil, nil); err != nil || got != nil {
		t.Errorf("no flag: %v %v", got, err)
	}
	if _, err := slackInputChannels([]string{"C1"}, nil); err == nil || err.Error() != errNoSlackLogin {
		t.Errorf("no login: %v", err)
	}
}

// The flag is repeatable and parsed as given (no comma splitting: a value is one ref).
func TestSlackChannelFlagParses(t *testing.T) {
	p, err := newParser()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Parse([]string{"--slack-channel", "producto-y", "--slack-channel", "#a,b"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(CLI.TUI.SlackChannels, "|") != "producto-y|#a,b" {
		t.Errorf("parsed %q", CLI.TUI.SlackChannels)
	}
	if _, err := p.Parse([]string{"serve", ":0", "--slack-channel", "x"}); err == nil {
		t.Error("serve accepted --slack-channel")
	}
}

// Only the TUI runs in a terminal of its own: its agent reports one to
// SessionInfo; serve (and every other command) does not guess one.
func TestOnlyTheTUIAgentHasATerminal(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "")
	t.Setenv("TERM_PROGRAM", "ghostty")
	a := (&tuiCmd{}).newTUIAgent(extras{})
	defer a.Close()
	if got := a.Options().Terminal; got != "Ghostty" {
		t.Errorf("TUI agent terminal = %q", got)
	}
	parseFor(t, "serve", ":0")
	s, err := CLI.Serve.newAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Options().Terminal; got != "" {
		t.Errorf("serve agent terminal = %q, want none", got)
	}
}
