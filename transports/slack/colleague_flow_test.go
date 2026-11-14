package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	agentstore "github.com/gurcuff91/harness/agent/store"
	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

// End to end through the real server, session and pump: a colleague's prompt to a
// Slack-bound session runs the turn (the model answers, the history keeps it) but
// posts NOTHING to the channel, while a typed message right after is posted, and
// the model is never told the origin.
func TestColleaguePromptToSlackSessionRunsQuietEndToEnd(t *testing.T) {
	isolateConfig(t)
	t.Setenv("HOME", t.TempDir())

	var mu sync.Mutex
	var seenUser []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		last, _ := body.Messages[len(body.Messages)-1].Content.(string)
		mu.Lock()
		seenUser = append(seenUser, last)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		ev := func(s string) { w.Write([]byte("data: " + s + "\n\n")); fl.Flush() }
		ev(`{"choices":[{"delta":{"content":"ANSWER to: ` + strings.ReplaceAll(strings.ReplaceAll(last, `"`, ``), "\n", " ") + `"}}]}`)
		ev(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
		ev(`[DONE]`)
	}))
	defer model.Close()
	prov := "slack-quiet-flow"
	if err := agent.NewOpenAIProvider(prov, model.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(prov) })
	a := agent.New(agent.AgentOptions{Store: agentstore.NewInMemoryStore()})
	defer a.Close()
	srv, err := server.Start(a, "", server.ServerOptions{Logger: logx.NewNilLogger(), KeepAgentOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	var pmu sync.Mutex
	var posts []string
	slackAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/chat.postMessage") {
			r.ParseForm()
			pmu.Lock()
			posts = append(posts, r.Form.Get("text"))
			pmu.Unlock()
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer slackAPI.Close()
	postsNow := func() []string { pmu.Lock(); defer pmu.Unlock(); return append([]string(nil), posts...) }

	st, _ := openStore(t.TempDir())
	tr := &Transport{
		api: client.New(srv.Addr()), bot: NewBot(slackAPI.URL, "xoxc", "xoxd"), store: st, srv: srv,
		logger: logx.NewNilLogger(), model: prov + "/m", cwd: st.cwd, myID: "UME",
		pumps: map[string]*channelPump{}, pendingAsks: map[string]chan askReply{},
	}
	slackTools := SlackTools(tr.bot, tr.myID, tr)
	srv.RegisterProfile(ProfileName, server.Profile{
		Directives: []string{Directive},
		Tools:      func() []atools.Tool { return slackTools },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pump, err := tr.pumpFor(ctx, "C123")
	if err != nil {
		t.Fatal(err)
	}

	// 1. a colleague asks this channel's session (synchronously, like ColleagueAsk)
	c := client.New(srv.Addr())
	out, err := c.Ask(pump.sessionID, "what is the status?", 20*time.Second, client.WithOrigin("colleague"), client.WithoutQueue())
	if err != nil || !strings.Contains(out, "ANSWER to: what is the status?") {
		t.Fatalf("the colleague's answer: %q %v", out, err)
	}
	time.Sleep(300 * time.Millisecond) // let the pump drain turn_end
	if got := postsNow(); len(got) != 0 {
		t.Fatalf("Slack received %q: a colleague's answer must not be posted", got)
	}
	mu.Lock()
	if len(seenUser) != 1 || seenUser[0] != "what is the status?" {
		t.Errorf("the model saw %q: the text must arrive untouched, with no origin", seenUser)
	}
	mu.Unlock()

	// 2. the very next typed message is answered in the channel as usual
	if _, err := c.SendPrompt(pump.sessionID, "<slack:channel>C123</slack:channel><slack:user>U9</slack:user>hello", client.WithOrigin(ProfileName)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(postsNow()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := postsNow()
	if len(got) != 1 || !strings.Contains(got[0], "ANSWER to:") || !strings.Contains(got[0], "hello") {
		t.Fatalf("Slack received %q, want exactly the answer to the typed message", got)
	}
}
