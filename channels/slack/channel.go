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
	pubs  map[string]*publisher // conversation id → its reply pump
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
			p := c.pubs[id]
			delete(c.pubs, id)
			c.pubMu.Unlock()
			if p != nil {
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
	if evt.Type != "message" || evt.Channel == "" || evt.BotID != "" || evt.SubType != "" {
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
	c.logger.Info("slack-channel", "prompt", "channel", evt.Channel, "user", evt.User,
		"text", oneLine(text, 200), "images", len(images), "files", len(atts))
	deliver(agent.Inbound{Text: buildPrompt(evt.User, evt.Channel, text, atts), Images: images})
}

// sendTyping shows the typing indicator in a conversation (Slack clears it
// after ~5 s; callers repeat it).
func (c *Channel) sendTyping(id string) {
	c.connMu.Lock()
	conn := c.conn
	c.typingSeq++
	seq := c.typingSeq
	c.connMu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.WriteJSON(map[string]any{"id": seq, "type": "typing", "channel": id})
}

// HandleEvent implements agent.Channel: the events of a turn started from
// conversation id go to its publisher, which posts them in order.
func (c *Channel) HandleEvent(id string, evt types.Event) {
	c.pubMu.Lock()
	p := c.pubs[id]
	if p == nil {
		p = newPublisher(c, id)
		c.pubs[id] = p
	}
	c.pubMu.Unlock()
	p.push(evt)
}

// Tools implements agent.Channel (the Slack tools act as the account, not as
// one conversation: ids are not needed).
func (c *Channel) Tools(_ []string) []atools.Tool {
	c.idMu.Lock()
	me := c.myID
	c.idMu.Unlock()
	return slackTools(c.bot, me)
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
