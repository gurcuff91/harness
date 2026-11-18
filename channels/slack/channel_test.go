package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/types"
)

// fakeSlack is a Slack workspace: the web API over HTTP and an RTM socket the
// test can push events into.
type fakeSlack struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	posts    []post // chat.postMessage
	uploads  []post // completed uploads (channel + comment)
	typing   []string
	rtmConns int
	conn     *websocket.Conn
	connCh   chan *websocket.Conn
	authFail bool
	channels []SlackChannel
	files    map[string][]byte // served at /files/<name>
}

type post struct{ channel, text string }

func newFakeSlack(t *testing.T) *fakeSlack {
	f := &fakeSlack{t: t, connCh: make(chan *websocket.Conn, 4), files: map[string][]byte{},
		channels: []SlackChannel{{ID: "C111", Name: "producto-y"}, {ID: "C222", Name: "general"}}}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }} // the bot sends Slack's own Origin
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case "auth.test":
			if f.authFail {
				_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"user_id":"UME","user":"jade","team":"T"}`))
		case "rtm.connect":
			f.rtmConns++
			_, _ = w.Write([]byte(`{"ok":true,"url":"ws` + strings.TrimPrefix(f.srv.URL, "http") + `/rtm"}`))
		case "chat.postMessage":
			f.posts = append(f.posts, post{r.Form.Get("channel"), r.Form.Get("text")})
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "conversations.list":
			b, _ := json.Marshal(map[string]any{"ok": true, "channels": f.channels})
			_, _ = w.Write(b)
		case "conversations.open":
			_, _ = w.Write([]byte(`{"ok":true,"channel":{"id":"D` + r.Form.Get("users") + `"}}`))
		case "files.getUploadURLExternal":
			_, _ = w.Write([]byte(`{"ok":true,"upload_url":"` + f.srv.URL + `/upload","file_id":"F1"}`))
		case "files.completeUploadExternal":
			f.uploads = append(f.uploads, post{r.Form.Get("channel_id"), r.Form.Get("initial_comment")})
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_, _ = w.Write([]byte(`{"ok":false,"error":"unknown_method"}`))
		}
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/files/")
		if name == "broken" {
			w.WriteHeader(500)
			return
		}
		f.mu.Lock()
		body := f.files[name]
		f.mu.Unlock()
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/rtm", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		f.connCh <- c
		for { // read typing indicators until the client goes away
			var m map[string]any
			if err := c.ReadJSON(&m); err != nil {
				return
			}
			if m["type"] == "typing" {
				f.mu.Lock()
				f.typing = append(f.typing, m["channel"].(string))
				f.mu.Unlock()
			}
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) waitConn() *websocket.Conn {
	f.t.Helper()
	select {
	case c := <-f.connCh:
		return c
	case <-time.After(5 * time.Second):
		f.t.Fatal("the channel never connected to RTM")
		return nil
	}
}

func (f *fakeSlack) send(c *websocket.Conn, evt map[string]any) {
	f.t.Helper()
	if err := c.WriteJSON(evt); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSlack) snapshot() (posts, uploads []post, typing []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]post(nil), f.posts...), append([]post(nil), f.uploads...), append([]string(nil), f.typing...)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

type inbox struct {
	mu  sync.Mutex
	got []agent.Inbound
}

func (i *inbox) deliver(in agent.Inbound) { i.mu.Lock(); i.got = append(i.got, in); i.mu.Unlock() }
func (i *inbox) all() []agent.Inbound {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]agent.Inbound(nil), i.got...)
}

func TestResolve(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	ctx := context.Background()
	for ref, want := range map[string]string{
		"C111": "C111", "G0AB": "G0AB", "D0XY": "D0XY", // ids as is
		"producto-y": "C111", "#general": "C222", " #producto-y ": "C111", // names
		"U42": "DU42", // a person → their DM
	} {
		got, err := c.Resolve(ctx, ref)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	for _, bad := range []string{"", "nope", "#missing"} {
		if _, err := c.Resolve(ctx, bad); err == nil {
			t.Errorf("Resolve(%q) succeeded", bad)
		}
	}
	f.mu.Lock()
	f.authFail = true
	f.mu.Unlock()
	if _, err := New(f.srv.URL, "x", "y").Resolve(ctx, "C111"); err == nil {
		t.Error("Resolve with a rejected login succeeded")
	}
}

// Only what is meant for the session becomes a prompt: a mention in a claimed
// channel, any message in a claimed DM — never our own, a bot's, a subtype, or
// a conversation nobody here listens to.
func TestOnlyMentionsAndDMsOfClaimedConversations(t *testing.T) {
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	var ch, dm inbox
	stop1, err := c.Listen(context.Background(), "C111", ch.deliver)
	if err != nil {
		t.Fatal(err)
	}
	defer stop1()
	stop2, err := c.Listen(context.Background(), "D9", dm.deliver)
	if err != nil {
		t.Fatal(err)
	}
	defer stop2()
	conn := f.waitConn()

	msgs := []map[string]any{
		{"type": "message", "channel": "C111", "user": "U1", "text": "no mention here"},
		{"type": "message", "channel": "C111", "user": "UME", "text": "<@UME> myself"},
		{"type": "message", "channel": "C111", "user": "U1", "bot_id": "B1", "text": "<@UME> a bot"},
		{"type": "message", "channel": "C111", "user": "U1", "subtype": "channel_join", "text": "<@UME> joined"},
		{"type": "message", "channel": "C999", "user": "U1", "text": "<@UME> not ours"},
		{"type": "reaction_added", "channel": "C111", "user": "U1", "text": "<@UME>"},
		{"type": "message", "channel": "C111", "user": "U1", "text": "<@UME>   "},
		{"type": "message", "channel": "C111", "user": "U1", "text": "hey <@UME> check this"},
		{"type": "message", "channel": "D9", "user": "U2", "text": "a dm, no mention"},
	}
	for _, m := range msgs {
		f.send(conn, m)
	}
	eventually(t, "the DM", func() bool { return len(dm.all()) == 1 })
	time.Sleep(50 * time.Millisecond)
	got := ch.all()
	if len(got) != 1 {
		t.Fatalf("channel prompts = %d (%+v), want 1", len(got), got)
	}
	want := "<slack user=\"U1\" channel=\"C111\">\nhey  check this\n</slack>"
	if got[0].Text != want {
		t.Errorf("channel prompt = %q\nwant %q", got[0].Text, want)
	}
	if d := dm.all()[0].Text; d != "<slack user=\"U2\" channel=\"D9\">\na dm, no mention\n</slack>" {
		t.Errorf("dm prompt = %q", d)
	}
}

// One socket for all conversations; it closes with the last listener and a new
// Listen opens another. A dropped socket reconnects.
func TestOneSocketReconnectsAndClosesWithTheLastListener(t *testing.T) {
	old := rtmBackoff
	rtmBackoff = 10 * time.Millisecond
	t.Cleanup(func() { rtmBackoff = old })
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	var in inbox
	stopA, _ := c.Listen(context.Background(), "C111", in.deliver)
	stopB, _ := c.Listen(context.Background(), "C222", in.deliver)
	conn := f.waitConn()
	if _, err := c.Listen(context.Background(), "C111", in.deliver); err == nil {
		t.Error("a second listener on the same conversation was accepted")
	}
	f.mu.Lock()
	n := f.rtmConns
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d RTM connections for two listeners, want 1", n)
	}
	conn.Close() // Slack drops us
	conn = f.waitConn()
	f.send(conn, map[string]any{"type": "message", "channel": "C222", "user": "U1", "text": "<@UME> after reconnect"})
	eventually(t, "a prompt after reconnecting", func() bool { return len(in.all()) == 1 })

	stopA()
	stopA() // idempotent
	stopB()
	c.mu.Lock()
	running := c.rtmCancel != nil
	c.mu.Unlock()
	if running {
		t.Fatal("the socket is still open with no listener")
	}
	stopC, _ := c.Listen(context.Background(), "C111", in.deliver)
	defer stopC()
	f.waitConn()
}

// What a turn publishes: its text at every cut (before a tool, at the end),
// with a typing indicator, uploads, errors and compaction notices — in order.
func TestPublishesTheTurnLikeTheTransport(t *testing.T) {
	old := typingInterval
	typingInterval = 10 * time.Millisecond
	t.Cleanup(func() { typingInterval = old })
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	stop, _ := c.Listen(context.Background(), "C111", func(agent.Inbound) {})
	f.waitConn()

	file := filepath.Join(t.TempDir(), "r.txt")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	evs := []types.Event{
		{Type: types.EventReceivedPrompt, Origin: "slack:C111"},
		{Type: types.EventTurnStart},
		{Type: types.EventStreamTextDelta, Delta: "Let me **check**"},
		{Type: types.EventToolCall, ToolName: "Bash"},
		{Type: types.EventCompactStart},
		{Type: types.EventCompactEnd},
		{Type: types.EventStreamTextDelta, Delta: "Done. <slack user=\"U1\" channel=\"C111\">\n"},
		{Type: types.EventStreamTextDelta, Delta: "<slack-upload path=\"" + file + "\"/>"},
		{Type: types.EventStreamTextEnd},
		{Type: types.EventError, Message: "provider 429"},
		{Type: types.EventTurnEnd},
	}
	for _, e := range evs {
		c.HandleEvent("C111", e)
	}
	eventually(t, "the error to be posted", func() bool {
		p, _, _ := f.snapshot()
		return len(p) == 4
	})
	posts, uploads, typing := f.snapshot()
	want := []string{"Let me *check*", "🗜 Context almost full — compacting automatically…", "✅ Conversation compacted.", "⚠️ provider 429"}
	for i, w := range want {
		if posts[i].channel != "C111" || posts[i].text != w {
			t.Errorf("post %d = %+v, want %q", i, posts[i], w)
		}
	}
	if len(uploads) != 1 || uploads[0].channel != "C111" || uploads[0].comment() != "Done." {
		t.Errorf("uploads = %+v, want one with the comment \"Done.\" (inbound tag stripped)", uploads)
	}
	if len(typing) == 0 || typing[0] != "C111" {
		t.Errorf("typing = %v", typing)
	}
	// The turn ended: the typing indicator stops (while still listening).
	time.Sleep(30 * time.Millisecond)
	_, _, t1 := f.snapshot()
	time.Sleep(60 * time.Millisecond)
	if _, _, t2 := f.snapshot(); len(t2) != len(t1) {
		t.Error("typing continued after the turn ended")
	}
	stop()
}

func (p post) comment() string { return p.text }

// Tools are the four Slack tools, bound to this channel's account, without SlackAsk.
func TestToolsAndDirective(t *testing.T) {
	c := New("https://x.slack.com", "a", "b")
	var names []string
	for _, tl := range c.Tools() {
		names = append(names, tl.Def.Name)
		var schema map[string]any
		if err := json.Unmarshal(tl.Def.InputSchema, &schema); err != nil {
			t.Errorf("%s: invalid schema: %v", tl.Def.Name, err)
		}
	}
	if strings.Join(names, ",") != "SlackListChannels,SlackListUsers,SlackPost,SlackMessages" {
		t.Errorf("tools = %v", names)
	}
	d := c.Directive([]string{"C111", "D9"})
	for _, want := range []string{"`C111` (a channel)", "`D9` (a direct message", "<slack user=", "<slack-attach name=", "skipped=", "pdftotext", "<slack-upload path=", "SlackPost",
		"### Who you obey from Slack", "never the authority of the person who runs this session", "Never run anything destructive", "Never read or reveal secrets", "Do not expose this machine", "cannot verify that from Slack"} {
		if !strings.Contains(d, want) {
			t.Errorf("directive lacks %q", want)
		}
	}
	if strings.Contains(d, "SlackAsk") || strings.Contains(d, "<slack:") {
		t.Error("directive mentions SlackAsk or the transport's old tags")
	}
}

// A clean turn (no error) stops typing at turn_end and posts its text once.
func TestCleanTurnStopsTypingAtTurnEnd(t *testing.T) {
	old := typingInterval
	typingInterval = 10 * time.Millisecond
	t.Cleanup(func() { typingInterval = old })
	f := newFakeSlack(t)
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	stop, _ := c.Listen(context.Background(), "C111", func(agent.Inbound) {})
	defer stop()
	f.waitConn()
	for _, e := range []types.Event{
		{Type: types.EventTurnStart},
		{Type: types.EventStreamTextDelta, Delta: "hello"},
		{Type: types.EventTurnEnd},
	} {
		c.HandleEvent("C111", e)
	}
	eventually(t, "the reply", func() bool { p, _, _ := f.snapshot(); return len(p) == 1 })
	time.Sleep(30 * time.Millisecond)
	_, _, t1 := f.snapshot()
	time.Sleep(60 * time.Millisecond)
	if _, _, t2 := f.snapshot(); len(t2) != len(t1) {
		t.Error("typing continued after turn_end")
	}
	if p, _, _ := f.snapshot(); p[0].text != "hello" {
		t.Errorf("posted %q", p[0].text)
	}
}

// Every attachment reaches the model: any non-image file by path (a .docx
// included), an image as an image; what is too large or fails is listed as
// skipped, never dropped.
func TestAttachmentsAreNeverDroppedSilently(t *testing.T) {
	old := maxAttachmentBytes
	maxAttachmentBytes = 100
	t.Cleanup(func() { maxAttachmentBytes = old })
	f := newFakeSlack(t)
	png := []byte("\x89PNG fake")
	f.files["report.docx"] = []byte("PK docx bytes")
	f.files["shot.png"] = png
	f.files["liar.bin"] = make([]byte, 101) // announces a small size, sends more
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	url := func(n string) string { return f.srv.URL + "/files/" + n }
	images, atts := fetchFiles(context.Background(), c.bot, []SlackFile{
		{ID: "F1", Name: "report.docx", MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", URLPrivate: url("report.docx"), Size: 13},
		{ID: "F2", Name: "shot.png", MimeType: "image/png", URLPrivate: url("shot.png"), Size: int64(len(png))},
		{ID: "F3", Name: "huge.mov", MimeType: "video/quicktime", URLPrivate: url("huge.mov"), Size: 101},
		{ID: "F4", Name: "liar.bin", URLPrivate: url("liar.bin"), Size: 10},
		{ID: "F5", Name: "gone.pdf", MimeType: "application/pdf", URLPrivate: url("broken")},
		{ID: "F6", Name: "noext", MimeType: "application/octet-stream", URLPrivate: url("report.docx")},
	})
	if len(images) != 1 || images[0].MimeType != "image/png" {
		t.Errorf("images = %+v", images)
	}
	want := map[string]string{
		"report.docx": "path", "huge.mov": "too large (over 100 bytes)", "liar.bin": "too large (over 100 bytes)",
		"gone.pdf": "download failed", "noext": "path",
	}
	if len(atts) != len(want) {
		t.Fatalf("attachments = %+v", atts)
	}
	for _, a := range atts {
		w := want[a.Name]
		if w == "path" {
			body, err := os.ReadFile(a.Path)
			if err != nil || string(body) != "PK docx bytes" || !strings.HasSuffix(a.Path, "-"+a.Name) {
				t.Errorf("%s: path=%q err=%v body=%q", a.Name, a.Path, err, body)
			}
			_ = os.Remove(a.Path)
		} else if a.Path != "" || a.Skipped != w {
			t.Errorf("%s: %+v, want skipped %q", a.Name, a, w)
		}
	}
}

// End to end: a mention with only a .docx still becomes a prompt that carries it.
func TestAFileOnlyMentionReachesTheSession(t *testing.T) {
	f := newFakeSlack(t)
	f.files["spec.docx"] = []byte("docx")
	c := New(f.srv.URL, "xoxc-1", "xoxd-1")
	var in inbox
	stop, _ := c.Listen(context.Background(), "C111", in.deliver)
	defer stop()
	conn := f.waitConn()
	f.send(conn, map[string]any{"type": "message", "channel": "C111", "user": "U1", "text": "<@UME>",
		"files": []map[string]any{{"id": "F1", "name": "spec.docx", "mimetype": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "url_private": f.srv.URL + "/files/spec.docx", "size": 4}}})
	eventually(t, "the prompt", func() bool { return len(in.all()) == 1 })
	got := in.all()[0].Text
	if !strings.Contains(got, `<slack-attach name="spec.docx" path="`) || !strings.Contains(got, `-spec.docx"/>`) {
		t.Errorf("prompt = %q", got)
	}
}

func TestTheDefaultLimitIs50MB(t *testing.T) {
	if maxAttachmentBytes != 50<<20 || tooLargeReason() != "too large (over 50 MB)" {
		t.Errorf("limit = %d, reason %q", maxAttachmentBytes, tooLargeReason())
	}
}
