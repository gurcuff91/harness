package telegram

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent"
	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/infoview"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// Type is this channel's name: the InputChannel type and the prefix of its
// prompts' origin ("telegram:<bot>:<chat>").
const Type = "telegram"

// Option configures a Channel.
type Option func(*Channel)

// WithLogger sets what receives the channel's log lines (default: silent).
func WithLogger(l logx.Logger) Option {
	return func(c *Channel) {
		if l != nil {
			c.logger = l
		}
	}
}

// Channel implements agent.Channel over ONE Telegram bot.
//
// The conversation a session binds is the BOT: the id is the bot's own user
// id, so the 1:1 claim registry makes one session (in one process) the bot's
// only listener — Telegram allows one getUpdates consumer per token anyway.
// The reference that binds it is the list of trusted usernames (see Resolve):
// only their messages reach the session, from any chat (a private chat, a
// group); everyone else is ignored without an answer. Each prompt's origin
// carries its chat ("telegram:<bot>:<chat>"), and the turn's reply goes back
// to that chat.
type Channel struct {
	bot    *Bot
	logger logx.Logger

	meMu sync.Mutex
	me   *User // the bot's own account (getMe, once)

	mu      sync.Mutex
	allowed map[string]map[string]bool // bot id → trusted usernames (lower case, no "@")
	deliver func(agent.Inbound)        // the bound session (one per bot)
	view    agent.SessionView          // read-only view of it, for /info and /context
	cancel  context.CancelFunc
	done    chan struct{}

	albums *albums

	pubMu sync.Mutex
	pubs  map[int64]*publisher // chat id → its reply pump
	// current is the chat the running turn answers (from its prompt echo to
	// turn_end); the session runs one turn at a time.
	current int64
}

var _ agent.Channel = (*Channel)(nil)

// New builds a Telegram channel for a bot token. Building it contacts nothing;
// the token is checked when a session binds the bot (Resolve). The token is
// passed explicitly so an SDK caller can use any bot; LoadToken returns the
// one `harness telegram token` saved.
func New(token string, opts ...Option) *Channel {
	c := &Channel{
		bot:     NewBot(strings.TrimSpace(token)),
		logger:  logx.NewNilLogger(),
		allowed: map[string]map[string]bool{},
		pubs:    map[int64]*publisher{},
	}
	c.albums = newAlbums(c)
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	return c
}

// Type implements agent.Channel.
func (c *Channel) Type() string { return Type }

// self returns the bot's own account (getMe, once).
func (c *Channel) self(ctx context.Context) (*User, error) {
	c.meMu.Lock()
	defer c.meMu.Unlock()
	if c.me != nil {
		return c.me, nil
	}
	u, err := c.bot.GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram bot token rejected or unreachable: %w", err)
	}
	c.me = u
	return u, nil
}

// ParseAllowed normalizes a list of Telegram usernames: "@", spaces and case
// do not matter, commas separate several, duplicates collapse. It fails on an
// empty list or a name Telegram could not have (5–32 of a-z, 0-9, "_").
func ParseAllowed(ref string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(ref, ",") {
		u := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(part), "@"))
		if u == "" {
			continue
		}
		if !validUsername(u) {
			return nil, fmt.Errorf("%q is not a Telegram username (5-32 letters, digits or _)", part)
		}
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("at least one trusted Telegram @username is required")
	}
	sort.Strings(out)
	return out, nil
}

func validUsername(u string) bool {
	if len(u) < 5 || len(u) > 32 {
		return false
	}
	for _, r := range u {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// Resolve implements agent.Channel: ref is the list of trusted usernames
// ("gus,ana"); the conversation is the bot (its user id, from getMe). The list
// is kept for the binding that follows.
func (c *Channel) Resolve(ctx context.Context, ref string) (string, error) {
	users, err := ParseAllowed(ref)
	if err != nil {
		return "", err
	}
	me, err := c.self(ctx)
	if err != nil {
		return "", err
	}
	id := strconv.FormatInt(me.ID, 10)
	set := map[string]bool{}
	for _, u := range users {
		set[u] = true
	}
	c.mu.Lock()
	if c.deliver == nil { // never retarget a live binding's list
		c.allowed[id] = set
	}
	c.mu.Unlock()
	return id, nil
}

// Name implements agent.Channel: "@botname".
func (c *Channel) Name(ctx context.Context, id string) string {
	if me, err := c.self(ctx); err == nil && me.Username != "" {
		return "@" + me.Username
	}
	return id
}

// pollTimeout is getUpdates' long-poll duration (seconds); pollBackoff the
// pause after a failed poll. Variables for tests.
var (
	pollTimeout = 30
	pollBackoff = 3 * time.Second
)

// Listen implements agent.Channel: starts the bot's long-poll loop.
func (c *Channel) Listen(ctx context.Context, id string, deliver func(agent.Inbound)) (func(), error) {
	me, err := c.self(ctx)
	if err != nil {
		return nil, err
	}
	if strconv.FormatInt(me.ID, 10) != id {
		return nil, fmt.Errorf("telegram: this channel serves bot %d, not %s", me.ID, id)
	}
	view, _ := agent.SessionViewOf(ctx)
	c.mu.Lock()
	if c.deliver != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("telegram bot @%s is already listened to in this process", me.Username)
	}
	if len(c.allowed[id]) == 0 {
		c.mu.Unlock()
		return nil, errors.New("telegram: no trusted users (Resolve the binding first)")
	}
	c.deliver, c.view = deliver, view
	pctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.done = cancel, make(chan struct{})
	done := c.done
	c.mu.Unlock()

	setCommands(c.bot)
	c.logger.Info("telegram-channel", "listening", "bot", "@"+me.Username, "allowed", strings.Join(c.allowedList(id), ","))
	go c.pollLoop(pctx, done)

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			c.albums.flushAll()
			c.mu.Lock()
			c.deliver, c.view, c.cancel, c.done = nil, nil, nil, nil
			c.mu.Unlock()
			c.pubMu.Lock()
			pubs := c.pubs
			c.pubs = map[int64]*publisher{}
			c.pubMu.Unlock()
			for _, p := range pubs {
				p.close() // waits for what is queued to be posted
			}
		})
	}, nil
}

func (c *Channel) allowedList(id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for u := range c.allowed[id] {
		out = append(out, "@"+u)
	}
	sort.Strings(out)
	return out
}

// pollLoop long-polls getUpdates until ctx ends. The offset lives here only:
// a restart re-reads what Telegram still holds (unconfirmed for 24 h at most).
func (c *Channel) pollLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	offset := 0
	for ctx.Err() == nil {
		ups, err := c.bot.GetUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			msg := err.Error()
			if errorCode(err) == 409 {
				msg = "another program is reading this bot's updates (Telegram allows one): " + msg
			}
			c.logger.Error("telegram-channel", "poll", "error", msg, "action", "retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollBackoff):
			}
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			if u.Message != nil {
				c.handleMessage(ctx, u.Message)
			}
		}
	}
}

// handleMessage turns one message of a trusted user into a prompt (or answers
// a read-only command); every other message is ignored without an answer.
func (c *Channel) handleMessage(ctx context.Context, m *Message) {
	if m.From == nil || m.From.IsBot {
		return
	}
	me, err := c.self(ctx)
	if err != nil {
		return
	}
	id := strconv.FormatInt(me.ID, 10)
	user := strings.ToLower(m.From.Username)
	c.mu.Lock()
	ok := user != "" && c.allowed[id][user]
	deliver, view := c.deliver, c.view
	c.mu.Unlock()
	if !ok || deliver == nil {
		c.logger.Warn("telegram-channel", "ignored", "chat", m.Chat.ID, "user", "@"+m.From.Username, "reason", "not a trusted user")
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	if cmd, ok := command(text, me.Username); ok {
		c.runCommand(ctx, m.Chat.ID, cmd, view, text)
		if cmd != "" {
			return
		}
	}
	if m.MediaGroupID != "" {
		c.albums.add(m)
		return
	}
	c.deliverMessages(ctx, []*Message{m})
}

// deliverMessages makes ONE prompt of one message, or of an album's messages.
func (c *Channel) deliverMessages(ctx context.Context, msgs []*Message) {
	if len(msgs) == 0 {
		return
	}
	c.mu.Lock()
	deliver := c.deliver
	c.mu.Unlock()
	if deliver == nil {
		return
	}
	first := msgs[0]
	var texts []string
	var files []incomingFile
	for _, m := range msgs {
		t := strings.TrimSpace(m.Text)
		if t == "" {
			t = strings.TrimSpace(m.Caption)
		}
		if t != "" {
			texts = append(texts, t)
		}
		files = append(files, filesOf(m)...)
	}
	var images []types.ImageData
	var atts []attachment
	if len(files) > 0 {
		images, atts = fetchFiles(ctx, c.bot, files)
	}
	text := strings.Join(texts, "\n")
	if text == "" && len(images) == 0 && len(atts) == 0 {
		return
	}
	me, _ := c.self(ctx)
	origin := Type + ":" + strconv.FormatInt(me.ID, 10) + ":" + strconv.FormatInt(first.Chat.ID, 10)
	c.logger.Info("telegram-channel", "prompt", "chat", first.Chat.ID, "user", "@"+first.From.Username,
		"text", oneLine(text, 200), "images", len(images), "files", len(atts))
	deliver(agent.Inbound{
		Text:   buildPrompt(first.Chat.ID, first.From.Username, text, atts),
		Images: images,
		Origin: origin,
	})
}

// HandleEvent implements agent.Channel: the events of a turn go to the
// publisher of the chat its prompt came from (the origin of its echo).
func (c *Channel) HandleEvent(id string, evt types.Event) {
	if evt.Type == types.EventReceivedPrompt || evt.Type == types.EventFollowUpStart {
		chat, ok := chatOf(evt.Origin, id)
		c.pubMu.Lock()
		c.current = 0
		if ok {
			c.current = chat
		}
		c.pubMu.Unlock()
	}
	c.pubMu.Lock()
	chat := c.current
	if evt.Type == types.EventTurnEnd {
		c.current = 0
	}
	var p *publisher
	if chat != 0 {
		p = c.pubs[chat]
		if p == nil {
			p = newPublisher(c, chat)
			c.pubs[chat] = p
		}
	}
	c.pubMu.Unlock()
	if p != nil {
		p.push(evt)
	}
}

// chatOf reads the chat of an origin "telegram:<bot>:<chat>" for bot id.
func chatOf(origin, id string) (int64, bool) {
	rest, ok := strings.CutPrefix(origin, Type+":"+id+":")
	if !ok {
		return 0, false
	}
	chat, err := strconv.ParseInt(rest, 10, 64)
	return chat, err == nil && chat != 0
}

// Directive implements agent.Channel.
func (c *Channel) Directive(ids []string) string {
	bot := ""
	if len(ids) > 0 {
		bot = c.Name(context.Background(), ids[0])
	}
	var users []string
	for _, id := range ids {
		users = append(users, c.allowedList(id)...)
	}
	return directive(bot, users)
}

// Tools implements agent.Channel: none (replies go back on their own).
func (c *Channel) Tools([]string) []atools.Tool { return nil }

// ── Read-only commands ──────────────────────────────────────────────────

// command reports whether text is a bot command ("/info", "/info@mybot"),
// returning its name ("" for one this channel does not handle as a command,
// which then goes to the model as a message — except /start, which is
// Telegram's own and is dropped).
func command(text, botName string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	word := strings.Fields(text)[0][1:]
	if name, at, ok := strings.Cut(word, "@"); ok {
		if !strings.EqualFold(at, botName) {
			return "", false // a command for another bot in the group
		}
		word = name
	}
	switch strings.ToLower(word) {
	case "info", "context", "start":
		return strings.ToLower(word), true
	}
	return "", true
}

// runCommand answers a read-only command in the chat. Nothing reaches the
// model; nothing changes the session.
func (c *Channel) runCommand(ctx context.Context, chat int64, cmd string, view agent.SessionView, _ string) {
	if cmd == "" || cmd == "start" || view == nil {
		return
	}
	var text string
	switch cmd {
	case "info":
		text = infoText(ctx, view, time.Now())
	case "context":
		text = contextText(view)
	}
	pctx, cancel := context.WithTimeout(ctx, postTimeout)
	defer cancel()
	sendText(pctx, c, chat, text)
}

// infoText is the shared plain /info panel (internal/infoview) of the session.
func infoText(ctx context.Context, v agent.SessionView, now time.Time) string {
	s := v.Info()
	q, qerr := v.Quota(ctx)
	chans := make([]client.ChannelInfo, 0, len(s.Channels))
	for _, ch := range s.Channels {
		chans = append(chans, client.ChannelInfo{Type: ch.Type, ID: ch.ID, Name: ch.Name})
	}
	return infoview.InfoPlain(infoview.Info{
		Version: s.Version, ID: s.ID, Name: s.Name, Model: s.Model, Thinking: s.Thinking,
		MaxIterations: s.MaxIterations, Stats: s.Stats, Channels: chans,
		Busy: s.Busy, QueueDepth: s.QueueDepth, MCPConnected: s.MCPConnected,
		ScheduleCount: s.ScheduleCount, OS: s.OS, Arch: s.Arch,
		Quota: q, QuotaErr: qerr,
	}, now)
}

func contextText(v agent.SessionView) string {
	b := v.Context()
	return infoview.ContextPlain(infoview.Context{
		System: b.System, Tools: b.Tools, Conversation: b.Conversation,
		EstimatedTotal: b.EstimatedTotal, LastRealTotal: b.LastRealTotal,
		ContextWindow: b.ContextWindow, FreeSpace: b.FreeSpace,
	})
}

// setCommands registers the bot's command menu (best effort).
func setCommands(bot *Bot) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = bot.SetMyCommands(ctx, []BotCommand{
		{Command: "info", Description: "Session info: model, usage, quota"},
		{Command: "context", Description: "How the context window is used"},
	})
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
