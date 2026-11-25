package slack

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/schedule"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// threadModel is an OpenAI-compatible endpoint that behaves as the directive
// asks: a message whose block has thread="…" makes it call SlackMessages with
// that channel and thread FIRST, and only then answer (quoting what the thread
// said); a plain channel message is answered at once. "REMIND ME" in the
// message makes it call Schedule before answering.
type threadModel struct {
	srv *httptest.Server
	mu  sync.Mutex
	// toolArgs: the arguments each tool call was made with, in order.
	toolArgs []string
}

func newThreadModel(t *testing.T) *threadModel {
	m := &threadModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
			return
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		text := func(c any) string {
			switch v := c.(type) {
			case string:
				return v
			case []any:
				var b strings.Builder
				for _, p := range v {
					if pm, ok := p.(map[string]any); ok && pm["type"] == "text" {
						b.WriteString(fmt.Sprint(pm["text"]))
					}
				}
				return b.String()
			}
			return ""
		}
		lastUser, lastRole, lastTool := "", "", ""
		for _, msg := range req.Messages {
			lastRole = msg.Role
			if msg.Role == "user" {
				lastUser = text(msg.Content)
			}
			if msg.Role == "tool" {
				lastTool = text(msg.Content)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(delta any) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		call := func(name string, args map[string]string) {
			a, _ := json.Marshal(args)
			m.mu.Lock()
			m.toolArgs = append(m.toolArgs, name+" "+string(a))
			m.mu.Unlock()
			send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": name, "arguments": string(a)}}}})
		}
		attr := func(name string) string {
			i := strings.Index(lastUser, name+`="`)
			if i < 0 {
				return ""
			}
			rest := lastUser[i+len(name)+2:]
			return rest[:strings.Index(rest, `"`)]
		}
		switch {
		case lastRole == "tool" && strings.Contains(lastTool, "Scheduled"):
			send(map[string]string{"content": "scheduled it"})
		case lastRole == "tool":
			send(map[string]string{"content": "I read the thread: " + firstLine(lastTool)})
		case attr("thread") != "" && strings.Contains(lastUser, "REMIND ME"):
			call("Schedule", map[string]string{"slug": "standup-reminder", "cron": "@daily", "prompt": "remind the team"})
		case attr("thread") != "":
			call("SlackMessages", map[string]string{"channel": attr("channel"), "thread": attr("thread")})
		default:
			send(map[string]string{"content": "plain channel answer"})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, `"text"`) {
			return strings.TrimSpace(l)
		}
	}
	return s
}

func (m *threadModel) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.toolArgs...)
}

// The whole feature, with a real agent: a mention inside a thread → the model
// reads THAT thread with SlackMessages first → its answer is posted inside the
// thread; a mention in the channel is answered in the channel without reading
// anything; and a schedule created from the thread fires back into it.
func TestEndToEndAMentionInAThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	f := newFakeSlack(t)
	f.replies["100.1"] = []map[string]any{
		{"user": "U2", "text": "the deploy fails with a timeout", "ts": "100.1", "thread_ts": "100.1", "reply_count": 2},
		{"user": "U3", "text": "since when?", "ts": "100.2", "thread_ts": "100.1"},
	}
	model := newThreadModel(t)
	if err := agent.NewOpenAIProvider("threadmodel", model.srv.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider("threadmodel") })

	ch := New(f.srv.URL, "xoxc-1", "xoxd-1")
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{ch}, EnableScheduler: true})
	defer a.Close()
	sess, err := a.NewSession(t.TempDir(), "threadmodel/m", agent.WithSessionChannels(types.InputChannel{Type: Type, Ref: "C111"}))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	conn := f.waitConn()

	// 1. Mentioned inside the thread.
	f.send(conn, map[string]any{"type": "message", "channel": "C111", "user": "U1", "ts": "100.5", "thread_ts": "100.1", "text": "<@UME> what do you think?"})
	eventually(t, "the answer in the thread", func() bool { p, _, _ := f.snapshot(); return len(p) == 1 })
	posts, _, _ := f.snapshot()
	if posts[0].thread != "100.1" || posts[0].channel != "C111" || !strings.Contains(posts[0].text, "the deploy fails with a timeout") {
		t.Fatalf("answer = %+v, want it IN thread 100.1 and showing it read the thread", posts[0])
	}
	calls := model.calls()
	if len(calls) != 1 || calls[0] != `SlackMessages {"channel":"C111","thread":"100.1"}` {
		t.Errorf("tool calls = %v, want one SlackMessages read of that thread", calls)
	}
	f.mu.Lock()
	rc := f.replyCalls
	f.mu.Unlock()
	if len(rc) != 1 || rc[0]["ts"] != "100.1" || rc[0]["channel"] != "C111" {
		t.Errorf("conversations.replies calls = %v", rc)
	}
	eventually(t, "the turn to end", func() bool { return !sess.IsBusy() })

	// 2. Mentioned in the channel: no reading, answered in the channel.
	f.send(conn, map[string]any{"type": "message", "channel": "C111", "user": "U1", "ts": "200.1", "text": "<@UME> hello"})
	eventually(t, "the channel answer", func() bool { p, _, _ := f.snapshot(); return len(p) == 2 })
	posts, _, _ = f.snapshot()
	if posts[1].thread != "" || posts[1].text != "plain channel answer" {
		t.Errorf("channel answer = %+v", posts[1])
	}
	if n := len(model.calls()); n != 1 {
		t.Errorf("a channel message made the model call a tool (%d calls)", n)
	}
	eventually(t, "the turn to end", func() bool { return !sess.IsBusy() })

	// 3. A schedule created from the thread remembers it, and fires back into it.
	f.send(conn, map[string]any{"type": "message", "channel": "C111", "user": "U1", "ts": "100.9", "thread_ts": "100.1", "text": "<@UME> REMIND ME every day"})
	var stored schedule.Schedule
	eventually(t, "the schedule", func() bool {
		for _, sc := range a.Schedules().List() {
			if sc.Slug == "standup-reminder" && sc.Owner == sess.ID() {
				stored = sc
				return true
			}
		}
		return false
	})
	t.Cleanup(func() { _, _ = a.Schedules().Delete("standup-reminder", sess.ID()) })
	if stored.Origin != "slack:C111:100.1" {
		t.Fatalf("schedule origin = %q, want slack:C111:100.1 (the thread)", stored.Origin)
	}
	eventually(t, "the scheduling turn to end", func() bool { return !sess.IsBusy() })
	before, _, _ := f.snapshot()
	a.FireScheduleForTest(stored)
	eventually(t, "the fired schedule answered", func() bool { p, _, _ := f.snapshot(); return len(p) > len(before) })
	after, _, _ := f.snapshot()
	last := after[len(after)-1]
	if last.thread != "100.1" || last.channel != "C111" {
		t.Errorf("the schedule's answer = %+v, want it back IN thread 100.1", last)
	}
	_ = time.Second
}
