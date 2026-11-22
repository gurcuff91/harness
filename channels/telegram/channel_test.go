package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/configstore"
	"github.com/gurcuff91/harness/internal/config"
	"github.com/gurcuff91/harness/types"
)

// fakeBotAPI is a Telegram Bot API: getMe, getUpdates (serving queued
// updates), sendMessage/sendChatAction/setMyCommands (recorded), getFile and
// file downloads (serving files), and the upload methods (recorded).
type fakeBotAPI struct {
	srv *httptest.Server

	mu      sync.Mutex
	queue   []Update
	nextID  int
	sent    []sentMsg
	uploads []string // method:filename
	actions int
	files   map[string][]byte // file_id → content
	reject  bool              // answer MarkdownV2 with 400
}

type sentMsg struct {
	Chat      int64
	Text      string
	ParseMode string
}

const (
	botID    = 4242
	botToken = "4242:TEST-TOKEN"
)

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	f := &fakeBotAPI{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	prev := apiBase
	apiBase = f.srv.URL
	pt, pb, aw := pollTimeout, pollBackoff, albumWait
	pollTimeout, pollBackoff, albumWait = 0, 20*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { apiBase = prev; pollTimeout, pollBackoff, albumWait = pt, pb, aw })
	return f
}

func ok(w http.ResponseWriter, result any) {
	b, _ := json.Marshal(map[string]any{"ok": true, "result": result})
	_, _ = w.Write(b)
}

func (f *fakeBotAPI) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/file/bot") {
		id := filepath.Base(r.URL.Path)
		f.mu.Lock()
		data, found := f.files[id]
		f.mu.Unlock()
		if !found {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(data)
		return
	}
	method := filepath.Base(r.URL.Path)
	if !strings.HasPrefix(r.URL.Path, "/bot"+botToken+"/") {
		b, _ := json.Marshal(map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		_, _ = w.Write(b)
		return
	}
	switch method {
	case "sendPhoto", "sendAnimation", "sendDocument":
		_ = r.ParseMultipartForm(10 << 20)
		for _, fhs := range r.MultipartForm.File {
			for _, fh := range fhs {
				f.mu.Lock()
				f.uploads = append(f.uploads, method+":"+fh.Filename)
				f.mu.Unlock()
			}
		}
		ok(w, map[string]any{"message_id": 1})
		return
	}
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	switch method {
	case "getMe":
		ok(w, User{ID: botID, IsBot: true, Username: "harness_test_bot"})
	case "getUpdates":
		f.mu.Lock()
		ups := f.queue
		f.queue = nil
		f.mu.Unlock()
		if len(ups) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		ok(w, ups)
	case "sendMessage":
		mode, _ := body["parse_mode"].(string)
		f.mu.Lock()
		reject := f.reject
		f.mu.Unlock()
		if mode == "MarkdownV2" && reject {
			b, _ := json.Marshal(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: can't parse entities"})
			_, _ = w.Write(b)
			return
		}
		chat, _ := body["chat_id"].(float64)
		text, _ := body["text"].(string)
		f.mu.Lock()
		f.sent = append(f.sent, sentMsg{Chat: int64(chat), Text: text, ParseMode: mode})
		f.mu.Unlock()
		ok(w, map[string]any{"message_id": 1})
	case "sendChatAction":
		f.mu.Lock()
		f.actions++
		f.mu.Unlock()
		ok(w, true)
	case "setMyCommands":
		ok(w, true)
	case "getFile":
		id, _ := body["file_id"].(string)
		f.mu.Lock()
		data := f.files[id]
		f.mu.Unlock()
		ok(w, map[string]any{"file_id": id, "file_path": "x/" + id, "file_size": len(data)})
	default:
		ok(w, true)
	}
}

// message queues a message from user in chat.
func (f *fakeBotAPI) message(chat int64, user, text string, extra func(*Message)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	m := &Message{MessageID: f.nextID, From: &User{ID: 7, Username: user}, Chat: Chat{ID: chat, Type: "private"}, Text: text}
	if extra != nil {
		extra(m)
	}
	f.queue = append(f.queue, Update{UpdateID: f.nextID, Message: m})
}

func (f *fakeBotAPI) sentTo(chat int64) []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sentMsg
	for _, s := range f.sent {
		if s.Chat == chat {
			out = append(out, s)
		}
	}
	return out
}

func (f *fakeBotAPI) allSent() []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMsg(nil), f.sent...)
}

// echoModel is an OpenAI-compatible endpoint answering "answer to <last user
// text>"; when the text contains "UPLOAD <path>" it answers with an upload tag.
func echoModel(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		last := ""
		for _, m := range req.Messages {
			if m.Role != "user" {
				continue
			}
			switch c := m.Content.(type) {
			case string:
				last = c
			case []any:
				for _, p := range c {
					if pm, ok := p.(map[string]any); ok && pm["type"] == "text" {
						last, _ = pm["text"].(string)
					}
				}
			}
		}
		answer := "answer to " + last
		if i := strings.Index(last, "UPLOAD "); i >= 0 {
			path := strings.Fields(last[i+7:])[0]
			answer = "here it is\n<telegram-upload path=\"" + path + "\"/>"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": answer}}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type fixture struct {
	api   *fakeBotAPI
	ch    *Channel
	agent *agent.Agent
	sess  *agent.Session
	log   *eventLog
}

type eventLog struct {
	mu sync.Mutex
	ev []types.Event
}

func (l *eventLog) add(e types.Event) { l.mu.Lock(); l.ev = append(l.ev, e); l.mu.Unlock() }
func (l *eventLog) prompts() []types.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []types.Event
	for _, e := range l.ev {
		if e.Type == types.EventReceivedPrompt || e.Type == types.EventFollowUpStart {
			out = append(out, e)
		}
	}
	return out
}

func newFixture(t *testing.T, allow string) *fixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(config.SwapStoresForTest(configstore.NewInMemoryStore(), configstore.NewInMemoryStore()))
	api := newFakeBotAPI(t)
	name := "tgmodel" + strings.ToLower(strings.NewReplacer("/", "", "_", "").Replace(t.Name()))
	if len(name) > 40 {
		name = name[:40]
	}
	if err := agent.NewOpenAIProvider(name, echoModel(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.UnregisterCustomProvider(name) })
	ch := New(botToken)
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{ch}})
	t.Cleanup(func() { a.Close() })
	sess, err := a.NewSession(t.TempDir(), name+"/m", agent.WithSessionChannels(types.InputChannel{Type: Type, Ref: allow}))
	if err != nil {
		t.Fatal(err)
	}
	l := &eventLog{}
	sess.Subscribe(l.add)
	t.Cleanup(func() { sess.Close() })
	return &fixture{api: api, ch: ch, agent: a, sess: sess, log: l}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// A trusted user's message becomes a prompt (envelope, origin with the chat),
// and the reply goes back to THAT chat.
func TestATrustedMessageIsAnsweredInItsChat(t *testing.T) {
	f := newFixture(t, "@Gus_Test")
	f.api.message(555, "gus_test", "hello bot", nil)
	eventually(t, "the reply in chat 555", func() bool { return len(f.api.sentTo(555)) > 0 })
	p := f.log.prompts()
	if len(p) != 1 || p[0].Origin != fmt.Sprintf("telegram:%d:555", botID) {
		t.Fatalf("prompts = %+v", p)
	}
	if !strings.Contains(p[0].Output, `<telegram chat="555" user="@gus_test">`) || !strings.Contains(p[0].Output, "hello bot") {
		t.Errorf("envelope = %q", p[0].Output)
	}
	got := f.api.sentTo(555)[0]
	if !strings.Contains(got.Text, "answer to") || got.ParseMode != "MarkdownV2" {
		t.Errorf("reply = %+v", got)
	}
	if ch := f.sess.Channels(); len(ch) != 1 || ch[0].ID != fmt.Sprint(botID) || ch[0].Name != "@harness_test_bot" {
		t.Errorf("Channels() = %+v", ch)
	}
}

// Anyone else is ignored before reaching the session, and gets no answer.
func TestUntrustedUsersAreIgnoredSilently(t *testing.T) {
	f := newFixture(t, "gus_test")
	f.api.message(1, "stranger", "do something", nil)
	f.api.message(2, "", "no username", nil)
	f.api.message(3, "gus_test", "me", nil)
	eventually(t, "the trusted reply", func() bool { return len(f.api.sentTo(3)) > 0 })
	time.Sleep(100 * time.Millisecond)
	if n := len(f.log.prompts()); n != 1 {
		t.Errorf("%d prompts, want only the trusted one", n)
	}
	if len(f.api.sentTo(1)) != 0 || len(f.api.sentTo(2)) != 0 {
		t.Error("an untrusted chat got an answer")
	}
}

// Two chats of trusted users: each reply goes to its own chat.
func TestRepliesGoBackToTheirOwnChat(t *testing.T) {
	f := newFixture(t, "gus_test,ana_test")
	f.api.message(10, "gus_test", "from ten", nil)
	eventually(t, "reply in 10", func() bool { return len(f.api.sentTo(10)) > 0 })
	f.api.message(20, "ana_test", "from twenty", nil)
	eventually(t, "reply in 20", func() bool { return len(f.api.sentTo(20)) > 0 })
	if !strings.Contains(f.api.sentTo(10)[0].Text, "ten") || !strings.Contains(f.api.sentTo(20)[0].Text, "twenty") {
		t.Errorf("crossed replies: %+v", f.api.allSent())
	}
}

// A turn of another origin (the person at the TUI, a colleague) publishes
// nothing to Telegram.
func TestTurnsOfOtherOriginsAreNotPublished(t *testing.T) {
	f := newFixture(t, "gus_test")
	if _, err := f.sess.PromptAndWait(context.Background(), "from the terminal"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if s := f.api.allSent(); len(s) != 0 {
		t.Errorf("a TUI turn reached Telegram: %+v", s)
	}
}

// /info and /context are answered in the chat, without the model; any other
// /command is a message; /start is dropped.
func TestReadOnlyCommands(t *testing.T) {
	f := newFixture(t, "gus_test")
	f.api.message(7, "gus_test", "/info", nil)
	eventually(t, "/info", func() bool { return len(f.api.sentTo(7)) > 0 })
	info := f.api.sentTo(7)[0].Text
	if !strings.Contains(info, "Session info") || !strings.Contains(info, f.sess.ID()) || !strings.Contains(info, "telegram") {
		t.Errorf("/info = %q", info)
	}
	f.api.message(7, "gus_test", "/context@harness_test_bot", nil)
	eventually(t, "/context", func() bool { return len(f.api.sentTo(7)) > 1 })
	if !strings.Contains(f.api.sentTo(7)[1].Text, "Context breakdown") {
		t.Errorf("/context = %q", f.api.sentTo(7)[1].Text)
	}
	f.api.message(7, "gus_test", "/start", nil)
	f.api.message(7, "gus_test", "/info@other_bot", nil) // for another bot in a group: a message
	f.api.message(7, "gus_test", "/reset now", nil)
	eventually(t, "two prompts", func() bool { return len(f.log.prompts()) == 2 })
	time.Sleep(100 * time.Millisecond)
	p := f.log.prompts()
	if len(p) != 2 || !strings.Contains(p[0].Output, "/info@other_bot") || !strings.Contains(p[1].Output, "/reset now") {
		t.Fatalf("prompts = %+v", p)
	}
}

// A photo arrives as a prompt image AND a saved file; a document by path; a
// program is skipped; an upload tag in the reply sends the file.
func TestFilesInAndOut(t *testing.T) {
	f := newFixture(t, "gus_test")
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))
	f.api.mu.Lock()
	f.api.files["ph1"] = png
	f.api.files["doc1"] = []byte("hello from a text file")
	f.api.files["exe1"] = []byte("MZ\x90\x00 program")
	f.api.mu.Unlock()
	f.api.message(9, "gus_test", "", func(m *Message) {
		m.Caption = "look"
		m.Photo = []PhotoSize{{FileID: "ph1", Width: 10, Height: 10, FileSize: int64(len(png))}}
	})
	eventually(t, "photo prompt", func() bool { return len(f.log.prompts()) == 1 })
	p := f.log.prompts()[0]
	if len(p.Images) != 1 || !strings.Contains(p.Output, `image="true"`) || !strings.Contains(p.Output, "look") {
		t.Errorf("photo prompt = %+v", p)
	}
	f.api.message(9, "gus_test", "", func(m *Message) {
		m.Document = &Document{FileID: "doc1", FileName: "notes.txt", FileSize: 22}
	})
	f.api.message(9, "gus_test", "", func(m *Message) {
		m.Document = &Document{FileID: "exe1", FileName: "tool.bin", FileSize: 12}
	})
	eventually(t, "document prompts", func() bool { return len(f.log.prompts()) == 3 })
	ps := f.log.prompts()
	if !strings.Contains(ps[1].Output, `name="notes.txt" path="`) {
		t.Errorf("document prompt = %q", ps[1].Output)
	}
	if !strings.Contains(ps[2].Output, `name="tool.bin" skipped="executable"`) {
		t.Errorf("program prompt = %q", ps[2].Output)
	}
	// Upload: the model answers with a tag for a real file.
	out := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(out, []byte("r"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.api.message(9, "gus_test", "UPLOAD "+out, nil)
	eventually(t, "upload", func() bool {
		f.api.mu.Lock()
		defer f.api.mu.Unlock()
		return len(f.api.uploads) == 1
	})
	f.api.mu.Lock()
	up := f.api.uploads[0]
	f.api.mu.Unlock()
	if up != "sendDocument:report.txt" {
		t.Errorf("upload = %q", up)
	}
	for _, s := range f.api.sentTo(9) {
		if strings.Contains(s.Text, "telegram-upload") {
			t.Errorf("the upload tag leaked into a message: %q", s.Text)
		}
	}
}

// An album (one media_group_id) becomes ONE prompt with all its images.
func TestAnAlbumIsOnePrompt(t *testing.T) {
	f := newFixture(t, "gus_test")
	jpg := []byte("\xff\xd8\xff\xe0" + strings.Repeat("j", 64))
	f.api.mu.Lock()
	f.api.files["a1"], f.api.files["a2"] = jpg, jpg
	f.api.mu.Unlock()
	for i, id := range []string{"a1", "a2"} {
		f.api.message(4, "gus_test", "", func(m *Message) {
			m.MediaGroupID = "grp"
			if i == 0 {
				m.Caption = "two shots"
			}
			m.Photo = []PhotoSize{{FileID: id, FileSize: int64(len(jpg))}}
		})
	}
	eventually(t, "album prompt", func() bool { return len(f.log.prompts()) == 1 })
	time.Sleep(150 * time.Millisecond)
	p := f.log.prompts()
	if len(p) != 1 || len(p[0].Images) != 2 || !strings.Contains(p[0].Output, "two shots") {
		t.Fatalf("album prompts = %+v", p)
	}
}

// When Telegram rejects the MarkdownV2, the text goes again as plain text.
func TestPlainTextFallback(t *testing.T) {
	f := newFixture(t, "gus_test")
	f.api.mu.Lock()
	f.api.reject = true
	f.api.mu.Unlock()
	f.api.message(6, "gus_test", "hi", nil)
	eventually(t, "plain reply", func() bool { return len(f.api.sentTo(6)) > 0 })
	if s := f.api.sentTo(6)[0]; s.ParseMode != "" || !strings.Contains(s.Text, "answer to") {
		t.Errorf("fallback = %+v", s)
	}
}

// The bot is ONE conversation: a second session (any process) cannot bind it.
func TestTheBotIsOneConversation(t *testing.T) {
	f := newFixture(t, "gus_test")
	_, err := f.agent.NewSession(t.TempDir(), f.sess.Model(), agent.WithSessionChannels(types.InputChannel{Type: Type, Ref: "ana_test"}))
	if err == nil {
		t.Fatal("a second session bound the same bot")
	}
}

func TestBindingErrors(t *testing.T) {
	f := newFixture(t, "gus_test")
	bad := New("0:WRONG")
	a := agent.New(agent.AgentOptions{Store: store.NewInMemoryStore(), Channels: []agent.Channel{bad}})
	defer a.Close()
	if _, err := a.NewSession(t.TempDir(), f.sess.Model(), agent.WithSessionChannels(types.InputChannel{Type: Type, Ref: "gus_test"})); err == nil {
		t.Error("a rejected token must fail the open")
	}
	for _, ref := range []string{"", " , ", "@ab", "bad name!"} {
		if _, err := ParseAllowed(ref); err == nil {
			t.Errorf("ParseAllowed(%q) must fail", ref)
		}
	}
	got, err := ParseAllowed(" @Gus_Test,ana_test , @gus_test ")
	if err != nil || strings.Join(got, ",") != "ana_test,gus_test" {
		t.Errorf("ParseAllowed = %v, %v", got, err)
	}
}

// Closing the session stops polling: later messages reach nobody.
func TestClosingStopsListening(t *testing.T) {
	f := newFixture(t, "gus_test")
	f.api.message(8, "gus_test", "one", nil)
	eventually(t, "first reply", func() bool { return len(f.api.sentTo(8)) > 0 })
	f.sess.Close()
	f.api.message(8, "gus_test", "two", nil)
	time.Sleep(150 * time.Millisecond)
	if n := len(f.log.prompts()); n != 1 {
		t.Errorf("%d prompts after close, want 1", n)
	}
}

// Events outside a turn (a manual compaction) reach no chat: the turn's chat
// is forgotten at turn_end.
func TestEventsOutsideATurnReachNoChat(t *testing.T) {
	f := newFixture(t, "gus_test")
	f.api.message(31, "gus_test", "hi", nil)
	eventually(t, "reply", func() bool { return len(f.api.sentTo(31)) > 0 })
	eventually(t, "turn ends", func() bool { return !f.sess.IsBusy() })
	before := len(f.api.allSent())
	id := fmt.Sprint(botID)
	f.ch.HandleEvent(id, types.Event{Type: types.EventCompactStart})
	f.ch.HandleEvent(id, types.Event{Type: types.EventStreamTextDelta, Delta: "stray"})
	f.ch.HandleEvent(id, types.Event{Type: types.EventTurnEnd})
	time.Sleep(100 * time.Millisecond)
	if s := f.api.allSent(); len(s) != before {
		t.Errorf("an event outside a turn was posted: %+v", s[before:])
	}
}

// Closing the session waits for what is queued to be posted.
func TestCloseFlushesTheQueue(t *testing.T) {
	f := newFixture(t, "gus_test")
	f.api.message(32, "gus_test", "hi", nil)
	eventually(t, "reply", func() bool { return len(f.api.sentTo(32)) > 0 })
	eventually(t, "turn ends", func() bool { return !f.sess.IsBusy() })
	id := fmt.Sprint(botID)
	origin := fmt.Sprintf("telegram:%d:32", botID)
	f.ch.HandleEvent(id, types.Event{Type: types.EventReceivedPrompt, Origin: origin})
	f.ch.HandleEvent(id, types.Event{Type: types.EventStreamTextDelta, Delta: "last words"})
	f.ch.HandleEvent(id, types.Event{Type: types.EventTurnEnd})
	f.sess.Close() // must not return before "last words" is posted
	found := false
	for _, s := range f.api.sentTo(32) {
		if strings.Contains(s.Text, "last words") {
			found = true
		}
	}
	if !found {
		t.Errorf("Close returned before the queue was posted: %+v", f.api.sentTo(32))
	}
}

// Another bot is never a trusted sender, even with an allowed username.
func TestBotsAreNeverTrusted(t *testing.T) {
	f := newFixture(t, "gus_test,friend_bot")
	f.api.message(33, "friend_bot", "I am a bot", func(m *Message) { m.From.IsBot = true })
	f.api.message(33, "gus_test", "person", nil)
	eventually(t, "the person's reply", func() bool { return len(f.api.sentTo(33)) > 0 })
	time.Sleep(100 * time.Millisecond)
	if p := f.log.prompts(); len(p) != 1 || !strings.Contains(p[0].Output, "person") {
		t.Errorf("prompts = %+v", p)
	}
}

// The album timer restarts with each message: messages spread over more than
// albumWait (but each within it of the previous) are still ONE prompt.
func TestAnAlbumWaitsForItsLastMessage(t *testing.T) {
	f := newFixture(t, "gus_test")
	jpg := []byte("\xff\xd8\xff\xe0" + strings.Repeat("j", 64))
	f.api.mu.Lock()
	for _, id := range []string{"b1", "b2", "b3"} {
		f.api.files[id] = jpg
	}
	f.api.mu.Unlock()
	for _, id := range []string{"b1", "b2", "b3"} {
		f.api.message(5, "gus_test", "", func(m *Message) {
			m.MediaGroupID = "slow"
			m.Photo = []PhotoSize{{FileID: id, FileSize: int64(len(jpg))}}
		})
		time.Sleep(albumWait * 7 / 10)
	}
	eventually(t, "album", func() bool { return len(f.log.prompts()) >= 1 })
	time.Sleep(3 * albumWait)
	if p := f.log.prompts(); len(p) != 1 || len(p[0].Images) != 3 {
		t.Fatalf("got %d prompts (%d images in the first), want one with 3", len(p), len(p[0].Images))
	}
}
