package slack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/gurcuff91/harness/agent"
	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// Type is this channel's name: the InputChannel type and the prefix of its
// prompts' origin ("slack:C0123").
const Type = "slack"

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

// Channel implements agent.Channel over one Slack account (the xoxc/xoxd
// browser-session pair). It opens ONE RTM socket, at the first Listen, and
// closes it with the last stop; every bound conversation is served by it.
//
// Every harness process listening with the same account receives every event;
// each one keeps only the conversations its sessions claimed (the 1:1 claim
// registry guarantees no two sessions claim the same one).
type Channel struct {
	bot    *Bot
	logger logx.Logger

	idMu sync.Mutex
	myID string // our own user id, from auth.test (lazily)

	mu        sync.Mutex
	listeners map[string]func(agent.Inbound) // conversation id → its session
	rtmCancel context.CancelFunc
	rtmDone   chan struct{}

	connMu    sync.Mutex
	conn      *websocket.Conn
	typingSeq int64

	pubMu sync.Mutex
	pubs  map[string]*publisher // "<conversation>" or "<conversation>:<thread_ts>" → its reply pump

	// inTurn: conversation → the thread its running turn answers ("" = the
	// channel itself), from its prompt echo to turn_end, as HandleEvent sees
	// them — before any tool of that turn runs. SlackPost refuses to post into
	// that very channel and thread: the turn's answer is published there
	// already (see slackPostTool).
	turnMu sync.Mutex
	inTurn map[string]string
	// current: the thread the running turn answers (set from the echo, cleared
	// at turn_end; the session runs one turn at a time).
	curMu   sync.Mutex
	current threadRef
}

var _ agent.Channel = (*Channel)(nil)

// New builds a Slack channel for the given workspace URL and session tokens.
// Building it contacts nothing; the account is checked at the first use.
//
// Credentials are passed in explicitly, so an SDK caller can use any account;
// LoadCredentials returns the one `harness slack login` saved.
func New(workspace, xoxc, xoxd string, opts ...Option) *Channel {
	c := &Channel{
		bot:       NewBot(strings.TrimRight(workspace, "/"), xoxc, xoxd),
		logger:    logx.NewNilLogger(),
		listeners: map[string]func(agent.Inbound){},
		pubs:      map[string]*publisher{},
		inTurn:    map[string]string{},
	}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	return c
}

// Type implements agent.Channel.
func (c *Channel) Type() string { return Type }

// self returns our own user id (auth.test, once).
func (c *Channel) self(ctx context.Context) (string, error) {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	if c.myID != "" {
		return c.myID, nil
	}
	r, err := c.bot.AuthTest(ctx)
	if err != nil {
		return "", err
	}
	if r.UserID == "" {
		return "", errors.New("slack auth.test returned no user id")
	}
	c.myID = r.UserID
	return c.myID, nil
}

// Resolve implements agent.Channel: a channel id (C…/G…) or a DM id (D…) is
// used as is, a user id (U…/W…) opens (or finds) the DM with that person, and
// anything else is a channel name, with or without "#".
func (c *Channel) Resolve(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("empty reference")
	}
	if _, err := c.self(ctx); err != nil {
		return "", err
	}
	if looksLikeID(ref) {
		switch ref[0] {
		case 'C', 'G', 'D':
			return ref, nil
		case 'U', 'W':
			return c.bot.OpenDM(ctx, ref)
		}
	}
	return resolveChannelName(ctx, c.bot, strings.TrimPrefix(ref, "#"))
}

// looksLikeID reports whether s has the shape of a Slack id: an upper-case
// type letter followed by upper-case letters and digits.
func looksLikeID(s string) bool {
	if len(s) < 3 || !strings.ContainsRune("CGDUW", rune(s[0])) {
		return false
	}
	for _, r := range s[1:] {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Listen implements agent.Channel.
func (c *Channel) Listen(ctx context.Context, id string, deliver func(agent.Inbound)) (func(), error) {
	if _, err := c.self(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if _, taken := c.listeners[id]; taken {
		c.mu.Unlock()
		return nil, fmt.Errorf("slack conversation %s is already listened to in this process", id)
	}
	c.listeners[id] = deliver
	if c.rtmCancel == nil {
		rctx, cancel := context.WithCancel(context.Background())
		c.rtmCancel = cancel
		c.rtmDone = make(chan struct{})
		go c.rtmLoop(rctx, c.rtmDone)
	}
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.listeners, id)
			var cancel context.CancelFunc
			var done chan struct{}
			if len(c.listeners) == 0 && c.rtmCancel != nil {
				cancel, done = c.rtmCancel, c.rtmDone
				c.rtmCancel, c.rtmDone = nil, nil
			}
			c.mu.Unlock()
			c.pubMu.Lock()
			var closing []*publisher
			for k, p := range c.pubs {
				if k == id || strings.HasPrefix(k, id+":") {
					closing = append(closing, p)
					delete(c.pubs, k)
				}
			}
			c.pubMu.Unlock()
			for _, p := range closing {
				p.close() // waits for what is queued to be posted
			}
			if cancel != nil {
				cancel()
				<-done
			}
		})
	}, nil
}

// rtmBackoff is the pause before reconnecting the RTM socket (a variable for tests).
var rtmBackoff = backoff

// rtmLoop keeps the RTM socket connected until ctx ends, reconnecting with a
// pause after any failure.
func (c *Channel) rtmLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for ctx.Err() == nil {
		wsURL, err := c.bot.RTMConnect(ctx)
		if err == nil {
			var conn *websocket.Conn
			conn, err = c.bot.DialRTM(ctx, wsURL)
			if err == nil {
				c.logger.Info("slack-channel", "rtm_connected")
				c.connMu.Lock()
				c.conn = conn
				c.connMu.Unlock()
				err = c.readLoop(ctx, conn)
				c.connMu.Lock()
				c.conn = nil
				c.connMu.Unlock()
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.logger.Error("slack-channel", "rtm", "error", err.Error(), "action", "reconnecting")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(rtmBackoff):
		}
	}
}

// readLoop reads RTM events until the socket closes or ctx ends.
func (c *Channel) readLoop(ctx context.Context, conn *websocket.Conn) error {
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()
	for {
		var evt RTMEvent
		if err := conn.ReadJSON(&evt); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		c.handleEvent(ctx, &evt)
	}
}

// handleEvent turns one RTM event into a prompt for the session listening to
// its conversation — only a person's message that mentions us (in a DM, any
// message: there is no mention there).
func (c *Channel) handleEvent(ctx context.Context, evt *RTMEvent) {
	// A thread reply also sent to the channel ("thread_broadcast") is a real
	// message of a person: it is kept; every other subtype is not a message.
	if evt.Type != "message" || evt.Channel == "" || evt.BotID != "" || (evt.SubType != "" && evt.SubType != "thread_broadcast") {
		return
	}
	c.idMu.Lock()
	me := c.myID
	c.idMu.Unlock()
	if me == "" || evt.User == "" || evt.User == me {
		return
	}
	c.mu.Lock()
	deliver := c.listeners[evt.Channel]
	c.mu.Unlock()
	if deliver == nil {
		return // not a conversation any session here claimed
	}
	mention := "<@" + me + ">"
	text := strings.TrimSpace(evt.Text)
	if !strings.HasPrefix(evt.Channel, "D") {
		if !strings.Contains(text, mention) {
			return
		}
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, mention, ""))
	var images []types.ImageData
	var atts []attachment
	if len(evt.Files) > 0 {
		images, atts = fetchFiles(ctx, c.bot, evt.Files)
	}
	if text == "" && len(images) == 0 && len(atts) == 0 {
		return
	}
	c.logger.Info("slack-channel", "prompt", "channel", evt.Channel, "thread", evt.ThreadTS, "user", evt.User,
		"text", oneLine(text, 200), "images", len(images), "files", len(atts))
	thread := ""
	origin := ""
	if evt.InThread() {
		thread = evt.ThreadTS
		origin = agent.ChannelOrigin(Type, evt.Channel+":"+thread)
	}
	deliver(agent.Inbound{Text: buildPrompt(evt.User, evt.Channel, thread, text, atts), Images: images, Origin: origin})
}

// sendTyping shows the typing indicator in a conversation (Slack clears it
// after ~5 s; callers repeat it).
func (c *Channel) sendTyping(id string) {
	// connMu is held for the WRITE too: gorilla/websocket allows ONE writer at
	// a time, and the publishers of several threads of a channel (and of
	// several channels) type concurrently.
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn == nil {
		return
	}
	c.typingSeq++
	_ = c.conn.WriteJSON(map[string]any{"id": c.typingSeq, "type": "typing", "channel": id})
}

// HandleEvent implements agent.Channel: the events of a turn started from
// conversation id go to its publisher, which posts them in order.
func (c *Channel) HandleEvent(id string, evt types.Event) {
	switch evt.Type {
	case types.EventReceivedPrompt, types.EventFollowUpStart:
		thread := threadOf(evt.Origin, id)
		c.turnMu.Lock()
		c.inTurn[id] = thread
		c.turnMu.Unlock()
		c.curMu.Lock()
		c.current = threadRef{id: id, thread: thread}
		c.curMu.Unlock()
	}
	c.curMu.Lock()
	cur := c.current
	if evt.Type == types.EventTurnEnd {
		c.current = threadRef{}
	}
	c.curMu.Unlock()
	if evt.Type == types.EventTurnEnd {
		c.turnMu.Lock()
		delete(c.inTurn, id)
		c.turnMu.Unlock()
	}
	if cur.id == "" {
		cur = threadRef{id: id} // an event outside a turn: the channel itself
	}
	c.pubMu.Lock()
	key := cur.key()
	p := c.pubs[key]
	if p == nil {
		p = newPublisher(c, cur.id, cur.thread)
		c.pubs[key] = p
	}
	c.pubMu.Unlock()
	p.push(evt)
}

// Tools implements agent.Channel. The tools act as the account; ids (the
// session's conversations) only tell SlackPost which of them answer the
// running turn by themselves.
func (c *Channel) Tools(ids []string) []atools.Tool {
	c.idMu.Lock()
	me := c.myID
	c.idMu.Unlock()
	own := map[string]bool{}
	for _, id := range ids {
		own[id] = true
	}
	// answering: is (channel, thread) where the running turn's answer is
	// published? Same channel AND same thread ("" is the channel itself), so
	// another thread of the same channel stays open to SlackPost.
	answering := func(id, thread string) bool {
		if !own[id] {
			return false
		}
		c.turnMu.Lock()
		defer c.turnMu.Unlock()
		t, running := c.inTurn[id]
		return running && t == thread
	}
	return slackTools(c.bot, me, answering)
}

// Name implements agent.Channel: "#name" for a channel, "@person" for a DM,
// the id itself when Slack does not say.
func (c *Channel) Name(ctx context.Context, id string) string {
	info, err := c.bot.ConversationInfo(ctx, id)
	if err != nil {
		return id
	}
	if info.IsIM {
		if u, err := c.bot.UserInfo(ctx, info.User); err == nil {
			if n := u.displayName(); n != "" {
				return "@" + n
			}
		}
		return id
	}
	if info.Name != "" {
		return "#" + info.Name
	}
	return id
}

// threadRef is a conversation and, inside it, a thread ("" = the channel itself).
type threadRef struct{ id, thread string }

// key is the publisher's key: "<conversation>" or "<conversation>:<thread_ts>".
func (r threadRef) key() string {
	if r.thread == "" {
		return r.id
	}
	return r.id + ":" + r.thread
}

// threadOf reads the thread out of a turn's origin "slack:<id>:<thread_ts>"
// for conversation id; "" for "slack:<id>" (a channel message) or any other.
func threadOf(origin, id string) string {
	rest, ok := strings.CutPrefix(origin, Type+":"+id+":")
	if !ok {
		return ""
	}
	return rest
}
