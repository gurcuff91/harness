package cli

import (
	"errors"
	"os"
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

// --with-colleagues: every session the agent opens is a colleague — new,
// resumed, forked — each with a fresh id of its own, and without it none is.
func TestWithColleaguesEverySessionIsAColleague(t *testing.T) {
	isolate(t)
	t.Setenv("HC_TERMINAL_ID", "")
	a := newInteractiveAgent(extras{colleagues: true})
	defer a.Close()
	model := fakeProviderModel(t)
	idOf := func(s *agent.Session) string {
		for _, c := range s.Channels() {
			if c.Type == "colleague" {
				return c.ID
			}
		}
		return ""
	}
	s1, err := a.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := a.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	id1, id2 := idOf(s1), idOf(s2)
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("colleague ids %q, %q: want two different ones", id1, id2)
	}
	f, err := a.ForkSession(s1.ID())
	if err != nil {
		t.Fatal(err)
	}
	if idf := idOf(f); idf == "" || idf == id1 || idf == id2 {
		t.Errorf("fork id %q", idf)
	}
	sid := s2.ID()
	_ = s2.Close()
	r, err := a.ResumeSession(sid)
	if err != nil {
		t.Fatal(err)
	}
	if idOf(r) == "" {
		t.Error("a resumed session is not a colleague")
	}

	plain := newInteractiveAgent(extras{})
	defer plain.Close()
	p, err := plain.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	if idOf(p) != "" {
		t.Error("a colleague id without --with-colleagues")
	}
}

// A closed terminal (or `hc kill`) hangs the process up: SIGHUP must cancel
// the command's context like SIGINT/SIGTERM, or the process dies without
// closing its sessions and leaves their channel sockets and claims behind.
func TestSignalContextHandlesHangup(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)") {
		t.Error("signalContext does not handle SIGHUP")
	}
	for _, f := range []string{"../tui/tui.go", "cli.go"} {
		b, _ := os.ReadFile(f)
		if !strings.Contains(string(b), "syscall.SIGHUP") {
			t.Errorf("%s does not handle SIGHUP", f)
		}
	}
}
