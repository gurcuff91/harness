package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/internal/providers"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// stubChannel resolves any ref to its upper case, except "missing".
type stubChannel struct{}

func (stubChannel) Type() string { return "stub" }
func (stubChannel) Resolve(_ context.Context, ref string) (string, error) {
	if ref == "missing" {
		return "", errors.New("channel #missing not found")
	}
	return strings.ToUpper(ref), nil
}
func (stubChannel) Listen(context.Context, string, func(agent.Inbound)) (func(), error) {
	return func() {}, nil
}
func (stubChannel) HandleEvent(string, types.Event)          {}
func (stubChannel) Name(_ context.Context, id string) string { return "#" + strings.ToLower(id) }
func (stubChannel) Directive([]string) string                { return "" }
func (stubChannel) Tools([]string) []tools.Tool              { return nil }

func channelServer(t *testing.T) *client.Client {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	providers.EnsureRegistry()
	t.Cleanup(providers.SetForTest([]providers.Provider{newToggle("chsrv", "m")}))
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), ResourceLoader: resources.NilLoader{}, Channels: []agent.Channel{stubChannel{}}})
	t.Cleanup(func() { a.Close() })
	srv, err := Start(a, "", ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return client.New(srv.Addr())
}

func statusOf(err error) int {
	var ce *client.Error
	if errors.As(err, &ce) {
		return ce.Status
	}
	return 0
}

func TestSessionChannelsOverTheAPI(t *testing.T) {
	c := channelServer(t)
	cwd := t.TempDir()
	s1, err := c.CreateSession("chsrv/m", cwd, "", client.SessionWithChannels(types.InputChannel{Type: "stub", Ref: "c1"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s1.Channels) != 1 || s1.Channels[0] != (client.ChannelInfo{Type: "stub", ID: "C1", Name: "#c1"}) {
		t.Errorf("created session channels = %v", s1.Channels)
	}
	if got, _ := c.GetSession(s1.ID); len(got.Channels) != 1 {
		t.Errorf("GET session channels = %v", got.Channels)
	}

	for name, tc := range map[string]struct {
		ch   types.InputChannel
		want int
	}{
		"taken":        {types.InputChannel{Type: "stub", Ref: "C1"}, http.StatusConflict},
		"unknown type": {types.InputChannel{Type: "nope", Ref: "x"}, http.StatusBadRequest},
		"unresolvable": {types.InputChannel{Type: "stub", Ref: "missing"}, http.StatusBadRequest},
	} {
		_, err := c.CreateSession("chsrv/m", cwd, "", client.SessionWithChannels(tc.ch))
		if statusOf(err) != tc.want {
			t.Errorf("%s: err=%v status=%d, want %d", name, err, statusOf(err), tc.want)
		}
	}

	// Resume of an open session asking for a channel it lacks → 400; the same → ok.
	if _, err := c.ResumeSession(s1.ID, client.SessionWithChannels(types.InputChannel{Type: "stub", Ref: "c2"})); statusOf(err) != http.StatusBadRequest {
		t.Errorf("rebind open session: %v", err)
	}
	if _, err := c.ResumeSession(s1.ID, client.SessionWithChannels(types.InputChannel{Type: "stub", Ref: "c1"})); err != nil {
		t.Errorf("same channel on open session: %v", err)
	}
	// Closed then resumed: without channels it listens to none; with them, it binds.
	if _, err := c.CloseSession(s1.ID); err != nil {
		t.Fatal(err)
	}
	r, err := c.ResumeSession(s1.ID)
	if err != nil || len(r.Channels) != 0 {
		t.Fatalf("plain resume: channels=%v err=%v", r.Channels, err)
	}
	_, _ = c.CloseSession(s1.ID)
	// A resume that fails on a channel is a 400, not a 404 ("not found" in the text).
	if _, err := c.ResumeSession(s1.ID, client.SessionWithChannels(types.InputChannel{Type: "stub", Ref: "missing"})); statusOf(err) != http.StatusBadRequest {
		t.Errorf("unresolvable on resume: status %d (%v)", statusOf(err), err)
	}
	r, err = c.ResumeSession(s1.ID, client.SessionWithChannels(types.InputChannel{Type: "stub", Ref: "c2"}))
	if err != nil || len(r.Channels) != 1 || r.Channels[0].ID != "C2" || r.Channels[0].Name != "#c2" {
		t.Fatalf("resume with channel: %v %v", r, err)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"channels":[{"type":"stub","id":"C2","name":"#c2"}]`) {
		t.Errorf("wire form: %s", b)
	}
}
