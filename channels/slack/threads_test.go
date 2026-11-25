package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

// ── Input ────────────────────────────────────────────────────────────────

// A mention inside a thread carries its thread in the block AND in the turn's
// origin; a channel message carries neither; a thread's root is a channel
// message; a thread reply also sent to the channel is delivered; without a
// mention nothing is, in a thread as anywhere.
func TestAThreadMessageCarriesItsThread(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	var ch, dm inbox
	stop1, _ := c.Listen(context.Background(), "C111", ch.deliver)
	defer stop1()
	stop2, _ := c.Listen(context.Background(), "D9", dm.deliver)
	defer stop2()
	conn := f.waitConn()

	for _, m := range []map[string]any{
		// a reply inside the thread rooted at 100.1
		{"type": "message", "channel": "C111", "user": "U1", "ts": "100.5", "thread_ts": "100.1", "text": "<@UME> what do you think?"},
		// the thread's ROOT mentioning us: a channel message (thread_ts == ts)
		{"type": "message", "channel": "C111", "user": "U1", "ts": "200.1", "thread_ts": "200.1", "reply_count": 3, "text": "<@UME> root"},
		// a plain channel message
		{"type": "message", "channel": "C111", "user": "U1", "ts": "300.1", "text": "<@UME> plain"},
		// a reply sent to the channel too
		{"type": "message", "channel": "C111", "user": "U1", "ts": "400.5", "thread_ts": "400.1", "subtype": "thread_broadcast", "text": "<@UME> broadcast"},
		// no mention in a thread: ignored, like in the channel
		{"type": "message", "channel": "C111", "user": "U1", "ts": "500.5", "thread_ts": "500.1", "text": "no mention"},
		// another subtype in a thread is still not a message
		{"type": "message", "channel": "C111", "user": "U1", "ts": "600.5", "thread_ts": "600.1", "subtype": "message_changed", "text": "<@UME> edited"},
		// a DM thread: every message of a DM, threaded or not
		{"type": "message", "channel": "D9", "user": "U2", "ts": "700.5", "thread_ts": "700.1", "text": "in a dm thread"},
	} {
		f.send(conn, m)
	}
	eventually(t, "the DM and four channel prompts", func() bool { return len(dm.all()) == 1 && len(ch.all()) == 4 })
	time.Sleep(50 * time.Millisecond)
	got := ch.all()
	if len(got) != 4 {
		t.Fatalf("channel prompts = %d, want 4: %+v", len(got), got)
	}
	type want struct{ block, origin string }
	for i, w := range []want{
		{`<slack user="U1" channel="C111" thread="100.1">` + "\nwhat do you think?\n</slack>", "slack:C111:100.1"},
		{`<slack user="U1" channel="C111">` + "\nroot\n</slack>", ""},
		{`<slack user="U1" channel="C111">` + "\nplain\n</slack>", ""},
		{`<slack user="U1" channel="C111" thread="400.1">` + "\nbroadcast\n</slack>", "slack:C111:400.1"},
	} {
		if got[i].Text != w.block || got[i].Origin != w.origin {
			t.Errorf("prompt %d = %q (origin %q)\nwant %q (origin %q)", i, got[i].Text, got[i].Origin, w.block, w.origin)
		}
	}
	d := dm.all()[0]
	if d.Text != `<slack user="U2" channel="D9" thread="700.1">`+"\nin a dm thread\n</slack>" || d.Origin != "slack:D9:700.1" {
		t.Errorf("dm thread prompt = %q (origin %q)", d.Text, d.Origin)
	}
}

func TestInThread(t *testing.T) {
	for _, c := range []struct {
		ts, thread string
		want       bool
	}{
		{"1.1", "", false},
		{"1.1", "1.1", false}, // the root itself
		{"1.5", "1.1", true},
		{"", "1.1", true},
	} {
		if got := (&RTMEvent{TS: c.ts, ThreadTS: c.thread}).InThread(); got != c.want {
			t.Errorf("InThread(ts=%q, thread_ts=%q) = %v, want %v", c.ts, c.thread, got, c.want)
		}
	}
}

func TestThreadOfAnOrigin(t *testing.T) {
	for origin, want := range map[string]string{
		"slack:C111":           "",
		"slack:C111:100.1":     "100.1",
		"slack:C222:100.1":     "", // another conversation
		"colleague:x":          "",
		"telegram:C111:100.1":  "",
		"":                     "",
		"slack:C111:100.1:odd": "100.1:odd",
	} {
		if got := threadOf(origin, "C111"); got != want {
			t.Errorf("threadOf(%q) = %q, want %q", origin, got, want)
		}
	}
}

// ── Output ───────────────────────────────────────────────────────────────

// runTurn feeds the events of one turn answering origin into the channel.
func runTurn(c *Channel, id, origin string, deltas ...string) {
	c.HandleEvent(id, types.Event{Type: types.EventReceivedPrompt, Origin: origin})
	c.HandleEvent(id, types.Event{Type: types.EventTurnStart})
	for _, d := range deltas {
		c.HandleEvent(id, types.Event{Type: types.EventStreamTextDelta, Delta: d})
	}
	c.HandleEvent(id, types.Event{Type: types.EventStreamTextEnd})
	c.HandleEvent(id, types.Event{Type: types.EventTurnEnd})
}

// A thread turn's whole answer — text, upload, error, notices — goes into its
// thread; a channel turn's goes to the channel; two threads of one channel keep
// to their own.
func TestTheAnswerStaysInItsThread(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	stop, _ := c.Listen(context.Background(), "C111", func(agent.Inbound) {})
	defer stop()
	f.waitConn()
	file := filepath.Join(t.TempDir(), "r.txt")
	_ = os.WriteFile(file, []byte("x"), 0o600)

	runTurn(c, "C111", "slack:C111:100.1", "in thread A", "\n<slack-upload path=\""+file+"\"/>")
	c.HandleEvent("C111", types.Event{Type: types.EventReceivedPrompt, Origin: "slack:C111:100.1"})
	c.HandleEvent("C111", types.Event{Type: types.EventError, Message: "boom in A"})
	c.HandleEvent("C111", types.Event{Type: types.EventTurnEnd})
	runTurn(c, "C111", "slack:C111:200.1", "in thread B")
	runTurn(c, "C111", "slack:C111", "in the channel")
	// (thread A's text travels as the comment of its upload.)
	eventually(t, "all posts", func() bool {
		p, u, _ := f.snapshot()
		return len(p) == 3 && len(u) == 1
	})
	posts, uploads, _ := f.snapshot()
	for _, w := range []struct{ text, thread string }{
		{"⚠️ boom in A", "100.1"}, {"in thread B", "200.1"}, {"in the channel", ""},
	} {
		found := false
		for _, p := range posts {
			if p.text == w.text {
				found = true
				if p.thread != w.thread || p.channel != "C111" {
					t.Errorf("%q posted in thread %q of %s, want thread %q", p.text, p.thread, p.channel, w.thread)
				}
			}
		}
		if !found {
			t.Errorf("%q was never posted: %+v", w.text, posts)
		}
	}
	if uploads[0].thread != "100.1" || uploads[0].text != "in thread A" {
		t.Errorf("the upload = %+v, want it in thread 100.1 with the turn's text as its comment", uploads[0])
	}
	c.pubMu.Lock()
	keys := len(c.pubs)
	c.pubMu.Unlock()
	if keys != 3 {
		t.Errorf("%d publishers, want one per thread plus the channel's (3)", keys)
	}
}

// A thread whose root is gone (thread_not_found) must not swallow the answer:
// it goes to the channel.
func TestAGoneThreadFallsBackToTheChannel(t *testing.T) {
	f := newFakeSlack(t)
	f.goneThreads["100.1"] = true
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	stop, _ := c.Listen(context.Background(), "C111", func(agent.Inbound) {})
	defer stop()
	f.waitConn()
	file := filepath.Join(t.TempDir(), "r.txt")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	// a plain reply and a reply with a file: both fall back
	runTurn(c, "C111", "slack:C111:100.1", "still answered")
	runTurn(c, "C111", "slack:C111:100.1", "with a file", "\n<slack-upload path=\""+file+"\"/>")
	eventually(t, "the fallback post and upload", func() bool {
		p, u, _ := f.snapshot()
		return len(p) == 1 && len(u) == 1
	})
	posts, uploads, _ := f.snapshot()
	if posts[0].channel != "C111" || posts[0].thread != "" || posts[0].text != "still answered" || uploads[0].thread != "" || uploads[0].text != "with a file" {
		t.Errorf("fallback: post %+v, upload %+v — want the channel itself", posts[0], uploads[0])
	}
}

// Closing the listener waits for the publishers of every thread of the channel.
func TestStoppingClosesEveryThreadPublisher(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	stop, _ := c.Listen(context.Background(), "C111", func(agent.Inbound) {})
	f.waitConn()
	runTurn(c, "C111", "slack:C111:100.1", "last words A")
	runTurn(c, "C111", "slack:C111:200.1", "last words B")
	stop() // must not return before both are posted
	posts, _, _ := f.snapshot()
	if len(posts) != 2 {
		t.Errorf("stop returned with %d of 2 posts done: %+v", len(posts), posts)
	}
	c.pubMu.Lock()
	left := len(c.pubs)
	c.pubMu.Unlock()
	if left != 0 {
		t.Errorf("%d publishers left after stop", left)
	}
}

// Events outside a turn reach the channel itself, never a stale thread.
func TestEventsAfterATurnDoNotReuseItsThread(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	stop, _ := c.Listen(context.Background(), "C111", func(agent.Inbound) {})
	defer stop()
	f.waitConn()
	runTurn(c, "C111", "slack:C111:100.1", "first")
	eventually(t, "the first", func() bool { p, _, _ := f.snapshot(); return len(p) == 1 })
	c.HandleEvent("C111", types.Event{Type: types.EventCompactStart}) // a manual compaction
	eventually(t, "the notice", func() bool { p, _, _ := f.snapshot(); return len(p) == 2 })
	if p, _, _ := f.snapshot(); !strings.Contains(p[1].text, "compacting") || p[1].thread != "" {
		t.Errorf("the notice went to thread %q: %+v", p[1].thread, p[1])
	}
}

// ── Tools ────────────────────────────────────────────────────────────────

func toolOf(t *testing.T, c *Channel, ids []string, name string) atools.Tool {
	t.Helper()
	if _, err := c.Resolve(context.Background(), "C111"); err != nil { // learns our own id
		t.Fatal(err)
	}
	for _, tl := range c.Tools(ids) {
		if tl.Def.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %s", name)
	return atools.Tool{}
}

// SlackPost's refusal looks at the THREAD too: the thread the running turn
// answers is refused, another thread of the same channel (or the channel
// itself) is not; a post with a thread arrives as a reply in it.
func TestSlackPostDistinguishesThreads(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	post := toolOf(t, c, []string{"C111"}, "SlackPost")
	call := func(args string) error {
		_, err := post.Execute(context.Background(), json.RawMessage(args))
		return err
	}
	// a turn answering thread 100.1 of C111 is running
	c.HandleEvent("C111", types.Event{Type: types.EventReceivedPrompt, Origin: "slack:C111:100.1"})
	if err := call(`{"channel":"C111","text":"x","thread":"100.1"}`); err == nil || !strings.Contains(err.Error(), "posted there automatically") || !strings.Contains(err.Error(), "100.1") {
		t.Errorf("the thread being answered: err = %v, want the refusal naming it", err)
	}
	if err := call(`{"channel":"#producto-y","text":"x","thread":" 100.1 "}`); err == nil {
		t.Error("the same thread by channel name (padded) was allowed")
	}
	if err := call(`{"channel":"C111","text":"to the channel"}`); err != nil {
		t.Errorf("the channel itself while a thread is answered: %v", err)
	}
	if err := call(`{"channel":"C111","text":"to another thread","thread":"200.1"}`); err != nil {
		t.Errorf("another thread of the channel: %v", err)
	}
	c.HandleEvent("C111", types.Event{Type: types.EventTurnEnd})

	// a turn answering the CHANNEL: its own thread-less post is refused, a thread of it is fine
	c.HandleEvent("C111", types.Event{Type: types.EventReceivedPrompt, Origin: "slack:C111"})
	if err := call(`{"channel":"C111","text":"x"}`); err == nil {
		t.Error("the channel being answered was allowed")
	}
	if err := call(`{"channel":"C111","text":"into a thread","thread":"300.1"}`); err != nil {
		t.Errorf("a thread while the channel is answered: %v", err)
	}
	c.HandleEvent("C111", types.Event{Type: types.EventTurnEnd})

	posts, _, _ := f.snapshot()
	got := map[string]string{}
	for _, p := range posts {
		got[p.text] = p.thread
	}
	for text, thread := range map[string]string{"to the channel": "", "to another thread": "200.1", "into a thread": "300.1"} {
		if g, ok := got[text]; !ok || g != thread {
			t.Errorf("%q arrived in thread %q (posted: %v), want %q", text, g, ok, thread)
		}
	}
	if len(posts) != 3 {
		t.Errorf("%d posts, want 3 (the refused ones must not arrive): %+v", len(posts), posts)
	}
}

func threadMsgs(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"user": "U1", "text": fmt.Sprintf("m%d", i), "ts": fmt.Sprintf("100.%d", i+1), "thread_ts": "100.1"}
	}
	out[0]["reply_count"] = n - 1
	return out
}

// SlackMessages with a thread reads THAT thread: its messages in order, the
// root first, with the thread attribute; without one it lists the channel as
// before, flagging the roots that have replies.
func TestSlackMessagesReadsAThread(t *testing.T) {
	f := newFakeSlack(t)
	f.replies["100.1"] = threadMsgs(4)
	f.history = []map[string]any{
		{"user": "U1", "text": "later channel message", "ts": "300.1"},
		{"user": "U1", "text": "m0", "ts": "100.1", "thread_ts": "100.1", "reply_count": 3},
	}
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	msgs := toolOf(t, c, nil, "SlackMessages")
	run := func(args string) (string, error) {
		out, _, err := msgs.ExecuteRich(context.Background(), json.RawMessage(args))
		return out, err
	}

	out, err := run(`{"channel":"C111","thread":"100.1"}`)
	if err != nil {
		t.Fatal(err)
	}
	var got []historyMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if len(got) != 4 || got[0].Text != "m0" || got[3].Text != "m3" || got[0].Thread != "100.1" || got[0].ReplyCount != 3 {
		t.Fatalf("thread = %+v", got)
	}
	f.mu.Lock()
	call := f.replyCalls[len(f.replyCalls)-1]
	f.mu.Unlock()
	if call["channel"] != "C111" || call["ts"] != "100.1" {
		t.Errorf("conversations.replies called with %v", call)
	}

	// The channel listing flags a root with replies.
	out, err = run(`{"channel":"C111"}`)
	if err != nil || !strings.Contains(out, `"reply_count": 3`) || !strings.Contains(out, "later channel message") {
		t.Errorf("channel listing (err %v):\n%s", err, out)
	}
	// The description tells the model about threads.
	for _, want := range []string{"thread", "reply_count", "root first"} {
		if !strings.Contains(msgs.Def.Description, want) {
			t.Errorf("SlackMessages' description lacks %q", want)
		}
	}
}

// A long thread: the root and the most recent messages, with a note of how many
// earlier ones were left out; limit 1 is just the root.
func TestSlackMessagesLimitsAThreadKeepingTheRoot(t *testing.T) {
	f := newFakeSlack(t)
	f.replies["100.1"] = threadMsgs(10)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	msgs := toolOf(t, c, nil, "SlackMessages")
	out, _, err := msgs.ExecuteRich(context.Background(), json.RawMessage(`{"channel":"C111","thread":"100.1","limit":4}`))
	if err != nil {
		t.Fatal(err)
	}
	note, body, _ := strings.Cut(out, "\n\n")
	if !strings.Contains(note, "6 earlier message(s) left out") || !strings.Contains(note, "root and the 3 most recent") {
		t.Errorf("note = %q", note)
	}
	var got []historyMessage
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%v:\n%s", err, body)
	}
	if len(got) != 4 || got[0].Text != "m0" || got[1].Text != "m7" || got[3].Text != "m9" {
		t.Errorf("kept %v", got)
	}
	out, _, _ = msgs.ExecuteRich(context.Background(), json.RawMessage(`{"channel":"C111","thread":"100.1","limit":1}`))
	var one []historyMessage
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n\n", 2)[1]), &one); err != nil || len(one) != 1 || one[0].Text != "m0" {
		t.Errorf("limit 1 = %v (%v)", one, err)
	}
	// Not long: no note.
	f.replies["200.1"] = threadMsgs(3)
	out, _, _ = msgs.ExecuteRich(context.Background(), json.RawMessage(`{"channel":"C111","thread":"200.1"}`))
	if strings.Contains(out, "left out") {
		t.Errorf("a short thread carries an omission note:\n%s", out)
	}
}

// A thread that does not exist, or is in another channel, is an error that says
// so — not an empty list.
func TestSlackMessagesUnknownThreadIsAnError(t *testing.T) {
	f := newFakeSlack(t)
	f.replies["100.1"] = threadMsgs(2)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	msgs := toolOf(t, c, nil, "SlackMessages")
	for _, args := range []string{`{"channel":"C111","thread":"999.9"}`, `{"channel":"C222","thread":"100.1"}`} {
		_, _, err := msgs.ExecuteRich(context.Background(), json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "thread_not_found") {
			t.Errorf("%s: err = %v, want thread_not_found", args, err)
		}
	}
}

// A thread's files come back like the channel's: paths when asked, images shown.
func TestSlackMessagesThreadFiles(t *testing.T) {
	f := newFakeSlack(t)
	f.files["t.png"] = []byte("\x89PNG")
	msgs := threadMsgs(2)
	msgs[1]["files"] = []map[string]any{{"id": "F1", "name": "t.png", "mimetype": "image/png", "url_private": f.srv.URL + "/files/t.png", "size": 4}}
	f.replies["100.1"] = msgs
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	tool := toolOf(t, c, nil, "SlackMessages")
	out, imgs, err := tool.ExecuteRich(context.Background(), json.RawMessage(`{"channel":"C111","thread":"100.1","files":true}`))
	if err != nil || len(imgs) != 1 || !strings.Contains(out, `"path"`) || strings.Contains(out, "url_private") {
		t.Errorf("thread files: err %v, %d images:\n%s", err, len(imgs), out)
	}
}

// A thread that is more than one page long is read whole.
func TestAThreadIsReadAcrossPages(t *testing.T) {
	f := newFakeSlack(t)
	f.replies["100.1"] = threadMsgs(5)
	f.pageSize = 2
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	got, omitted, err := c.bot.GetThreadReplies(context.Background(), "C111", "100.1", 200)
	if err != nil || omitted != 0 || len(got) != 5 || got[4].Text != "m4" {
		t.Fatalf("got %d messages (omitted %d), %v", len(got), omitted, err)
	}
}

// The directive tells the model what a thread block means and the rule: read it
// with SlackMessages BEFORE answering; the reply goes to the thread by itself.
func TestTheDirectiveHasTheThreadRule(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "c", "d")
	d := c.Directive([]string{"C111"})
	for _, want := range []string{
		`thread="1700000000.000100"`,
		"### Threads",
		"call SlackMessages with that channel and that thread",
		"Before you answer a message that has a thread attribute",
		"into the same thread when the message has thread",
		"into the channel when it has not",
		"its thread parameter",
		"reply_count",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("the directive lacks %q", want)
		}
	}
	for _, name := range []string{"SlackPost", "SlackMessages"} {
		tl := toolOf(t, c, nil, name)
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tl.Def.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if _, ok := schema.Properties["thread"]; !ok {
			t.Errorf("%s has no thread parameter", name)
		}
	}
}

// SlackPost into a thread with a file: the upload lands in that thread.
func TestSlackPostUploadsIntoAThread(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	post := toolOf(t, c, []string{"C111"}, "SlackPost")
	file := filepath.Join(t.TempDir(), "r.txt")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	args, _ := json.Marshal(map[string]string{"channel": "C222", "thread": "300.1", "text": "see this\n<slack-upload path=\"" + file + "\"/>"})
	if _, err := post.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	_, uploads, _ := f.snapshot()
	if len(uploads) != 1 || uploads[0].channel != "C222" || uploads[0].thread != "300.1" || uploads[0].text != "see this" {
		t.Errorf("uploads = %+v, want one into thread 300.1 of C222", uploads)
	}
}

// The not-found answer is a typed error (errors.Is), whichever call it came
// from — it is what makes the publisher fall back to the channel.
func TestThreadNotFoundIsATypedError(t *testing.T) {
	f := newFakeSlack(t)
	f.goneThreads["9.9"] = true
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	ctx := context.Background()
	if err := c.bot.PostMessageInThread(ctx, "C111", "9.9", "x"); !errors.Is(err, ErrThreadNotFound) {
		t.Errorf("post: %v", err)
	}
	file := filepath.Join(t.TempDir(), "r.txt")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	if err := c.bot.UploadFileInThread(ctx, "C111", "9.9", file, "x"); !errors.Is(err, ErrThreadNotFound) {
		t.Errorf("upload: %v", err)
	}
	if _, _, err := c.bot.GetThreadReplies(ctx, "C111", "9.9", 10); !errors.Is(err, ErrThreadNotFound) {
		t.Errorf("replies: %v", err)
	}
}
