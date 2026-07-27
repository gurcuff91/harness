package slack

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/client"
	"github.com/gurcuff91/harness/internal/logx"
	"github.com/gurcuff91/harness/internal/server"
	"github.com/gurcuff91/harness/types"
)

// Options configures the Slack transport.
type Options struct {
	Workspace string // e.g. "https://myco.slack.com" (required)
	XoxC      string // xoxc-... browser session API token (required)
	XoxD      string // xoxd-... browser session cookie (required)
	Model     string // model override; empty = server default
	Thinking  string // thinking level override; empty = server default
	Scheduler bool   // run the cron engine (schedules fire into their owner channel)
}

// Transport is the running Slack integration: it owns the agent, the in-process
// server, the bot client, and one live SSE pump per active channel.
type Transport struct {
	opts  Options
	agent *agent.Agent
	api   *client.Client
	bot   *Bot
	store *store
	model string
	cwd   string
	myID  string // our own Slack user ID (to detect mentions)

	mu    sync.Mutex
	pumps map[string]*channelPump // channel ID → its live session pump

	// Active RTM WebSocket connection. Set/cleared by rtmLoop under connMu.
	// Used by SendTyping to write typing events without blocking the read goroutine.
	// gorilla/websocket requires that concurrent reads and writes use separate locks.
	connMu    sync.Mutex
	conn      *websocket.Conn
	typingSeq int64 // monotonic id for RTM messages
}

// Run starts the Slack transport and blocks until ctx is cancelled.
// Credentials are resolved with precedence: flags > env > ~/.harness/slack.json.
// Three Slack-specific tools (SlackPost, SlackListChannels, SlackListUsers) are
// injected into the agent so it can proactively post messages and resolve names.
func Run(ctx context.Context, a *agent.Agent, opts Options) error {
	// Fill missing credentials from saved login (~/.harness/slack.json).
	if opts.Workspace == "" || opts.XoxC == "" || opts.XoxD == "" {
		if saved, err := LoadCredentials(); err == nil && saved != nil {
			if opts.Workspace == "" {
				opts.Workspace = saved.Workspace
			}
			if opts.XoxC == "" {
				opts.XoxC = saved.XoxC
			}
			if opts.XoxD == "" {
				opts.XoxD = saved.XoxD
			}
		}
	}
	if opts.Workspace == "" || opts.XoxC == "" || opts.XoxD == "" {
		return fmt.Errorf("slack: credentials required — run 'harness slack login' or pass --workspace, --xoxc and --xoxd")
	}

	st, err := openStore("")
	if err != nil {
		return fmt.Errorf("slack: open store: %w", err)
	}

	// In-process server — same pattern as TUI and Telegram.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("slack: bind server: %w", err)
	}
	srv := server.NewServer(a, server.ServerOptions{Verbose: false})
	go srv.Serve(listener) //nolint:errcheck

	cwd, _ := os.Getwd()
	bot := NewBot(opts.Workspace, opts.XoxC, opts.XoxD)
	t := &Transport{
		opts:  opts,
		agent: a,
		api:   client.New(listener.Addr().String()),
		bot:   bot,
		store: st,
		cwd:   cwd,
		pumps: make(map[string]*channelPump),
	}

	// Resolve model.
	if err := t.resolveModel(); err != nil {
		return err
	}

	// Verify tokens and get our user ID (needed to detect @mentions).
	me, err := t.bot.AuthTest(ctx)
	if err != nil {
		return fmt.Errorf("slack: invalid tokens: %w", err)
	}
	t.myID = me.UserID
	logx.Info("slack", "connected",
		"user", me.UserID, "team", me.Team,
		"default_model", t.model, "scheduler", opts.Scheduler)

	// Inject Slack-specific tools into the agent so the model can proactively
	// post messages, resolve channels and users by name, etc.
	for _, tool := range SlackTools(bot, me.UserID) {
		a.RegisterTool(tool)
	}

	return t.rtmLoop(ctx)
}

// resolveModel picks the model: the override if active, else the persisted
// default, else the first active model.
func (t *Transport) resolveModel() error {
	models, err := t.api.ListModels()
	if err != nil {
		return fmt.Errorf("slack: reach server: %w", err)
	}
	if len(models) == 0 {
		return fmt.Errorf("slack: no active providers — connect one first (harness connect ...)")
	}
	active := map[string]bool{}
	first := ""
	for _, m := range models {
		if m.Model != "" {
			active[m.Model] = true
			if first == "" {
				first = m.Model
			}
		}
	}
	if t.opts.Model != "" && active[t.opts.Model] {
		t.model = t.opts.Model
		return nil
	}
	if s, err := t.api.GetSettings(); err == nil {
		if s.ActiveModel != "" && active[s.ActiveModel] {
			t.model = s.ActiveModel
			return nil
		}
	}
	t.model = first
	return nil
}

// rtmLoop connects to the Slack RTM WebSocket and dispatches events. It
// reconnects automatically on disconnect (same backoff pattern as Telegram).
func (t *Transport) rtmLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		wsURL, err := t.bot.RTMConnect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logx.Error("slack", "rtm_connect", "error", err.Error(), "action", "retrying")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}

		conn, err := t.bot.DialRTM(ctx, wsURL)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logx.Error("slack", "rtm_dial", "error", err.Error(), "action", "retrying")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}

		logx.Info("slack", "rtm_connected")
		t.connMu.Lock()
		t.conn = conn
		t.connMu.Unlock()

		if err := t.readLoop(ctx, conn); err != nil && ctx.Err() == nil {
			logx.Error("slack", "rtm_read", "error", err.Error(), "action", "reconnecting")
		}

		t.connMu.Lock()
		t.conn = nil
		t.connMu.Unlock()

		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
}

// SendTyping sends a typing indicator for the given channel over the active
// RTM WebSocket. gorilla/websocket serialises writes with its own internal
// mutex, so this is safe to call from any goroutine while readLoop is running.
// Slack clears the typing indicator after ~5 seconds — callers must repeat.
func (t *Transport) SendTyping(channelID string) {
	t.connMu.Lock()
	conn := t.conn
	t.typingSeq++
	seq := t.typingSeq
	t.connMu.Unlock()

	if conn == nil {
		return
	}
	msg := map[string]any{
		"id":      seq,
		"type":    "typing",
		"channel": channelID,
	}
	// WriteJSON acquires gorilla's write lock internally — safe with concurrent ReadJSON.
	if err := conn.WriteJSON(msg); err != nil {
		logx.Error("slack", "typing", "channel", channelID, "error", err.Error())
	}
}

// readLoop reads RTM events from the WebSocket until the connection closes or
// ctx is cancelled.
func (t *Transport) readLoop(ctx context.Context, conn *websocket.Conn) error {
	defer conn.Close()

	// Close the connection when ctx is cancelled.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		var evt RTMEvent
		if err := conn.ReadJSON(&evt); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		t.handleEvent(ctx, &evt)
	}
}

// handleEvent routes one RTM event. We only care about real user messages —
// our own messages, bot messages and sub-typed events are skipped.
func (t *Transport) handleEvent(ctx context.Context, evt *RTMEvent) {
	if evt.Type != "message" {
		return
	}
	// Skip our own messages, bot messages, and system sub-types.
	if evt.User == t.myID || evt.BotID != "" || evt.SubType != "" {
		return
	}
	if evt.Channel == "" || evt.Text == "" {
		return
	}

	isDM := strings.HasPrefix(evt.Channel, "D")
	isMention := strings.Contains(evt.Text, "<@"+t.myID+">")

	// Only respond to: direct messages OR explicit @mentions in channels.
	if !isDM && !isMention {
		return
	}

	// Strip the @mention prefix from the text so the model doesn't see it.
	text := strings.TrimSpace(evt.Text)
	if isMention {
		text = strings.TrimSpace(strings.ReplaceAll(text, "<@"+t.myID+">", ""))
	}
	if text == "" {
		return
	}

	// Handle /commands (typed as "@harness /compact", "@harness /new", etc.)
	if strings.HasPrefix(text, "/") {
		t.handleCommand(ctx, evt.Channel, text)
		return
	}

	// Process file attachments — images go to SendPromptWithImages, text files
	// become <slack:attach> tags appended to the prompt.
	var images []types.ImageData
	var attachTags []string
	if len(evt.Files) > 0 {
		images, attachTags = t.handleFiles(ctx, evt.Channel, evt.Files)
	}

	// Build the final prompt text (user text + attach tags for text files).
	prompt := buildPrompt(text, attachTags)

	logx.Info("slack", "prompt",
		"channel", evt.Channel, "user", evt.User,
		"text", oneLine(prompt, 200),
		"images", len(images), "files", len(attachTags))

	pump, err := t.pumpFor(ctx, evt.Channel)
	if err != nil {
		t.replyError(ctx, evt.Channel, err)
		return
	}
	// In channels (not DMs), store sender so replies can @mention them.
	if strings.HasPrefix(evt.Channel, "C") && evt.User != "" {
		pump.mu.Lock()
		pump.lastUser = evt.User
		pump.mu.Unlock()
	}

	var sendErr error
	if len(images) > 0 {
		_, sendErr = t.api.SendPromptWithImages(pump.sessionID, prompt, images)
	} else if prompt != "" {
		_, sendErr = t.api.SendPrompt(pump.sessionID, prompt)
	}
	if sendErr != nil {
		t.replyError(ctx, evt.Channel, sendErr)
	}
}

// handleCommand routes slash commands sent to the bot via Slack.
func (t *Transport) handleCommand(ctx context.Context, channelID, text string) {
	fields := strings.Fields(text)
	cmd := strings.TrimPrefix(fields[0], "/")
	logx.Info("slack", "command", "channel", channelID, "name", cmd)

	switch cmd {
	case "new":
		t.resetChannel(ctx, channelID)
		if _, err := t.pumpFor(ctx, channelID); err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		t.send(ctx, channelID, "Started a fresh session.")

	case "stop":
		t.mu.Lock()
		p := t.pumps[channelID]
		t.mu.Unlock()
		if p == nil {
			t.send(ctx, channelID, "Nothing is running.")
			return
		}
		if _, err := t.api.StopSession(p.sessionID); err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		t.send(ctx, channelID, "Stopped.")

	case "compact":
		p, err := t.pumpFor(ctx, channelID)
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		p.compactExpected.Store(true)
		if _, err := t.api.ExecCommand(p.sessionID, "compact", nil); err != nil {
			p.compactExpected.Store(false)
			t.replyError(ctx, channelID, err)
			return
		}
		t.send(ctx, channelID, "🗜 Compacting the conversation…")

	case "info":
		p, err := t.pumpFor(ctx, channelID)
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		info, err := t.api.GetSessionInfo(p.sessionID)
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		t.send(ctx, channelID, formatInfo(info))

	default:
		t.send(ctx, channelID,
			fmt.Sprintf("Unknown command `/%s`. Available: /new /stop /compact /info", cmd))
	}
}

// formatInfo renders session info as a Slack message.
func formatInfo(info *client.SessionInfo) string {
	sess := info.Session
	stats := sess.Stats
	var b strings.Builder
	fmt.Fprintf(&b, "*📊 Session info*\n")
	fmt.Fprintf(&b, "harness %s\n", info.Version)
	if sess.Name != "" {
		fmt.Fprintf(&b, "session: %s\n", sess.Name)
	}
	fmt.Fprintf(&b, "model: %s\n", sess.Model)
	if sess.Thinking != "" {
		fmt.Fprintf(&b, "thinking: %s\n", sess.Thinking)
	}
	if stats.ContextWindow > 0 {
		fmt.Fprintf(&b, "context: %.1f%% of %s tokens\n",
			stats.ContextUsage*100, compactNum(int64(stats.ContextWindow)))
	}
	fmt.Fprintf(&b, "tokens: ↑%s ↓%s  cost: $%.4f\n",
		compactNum(int64(stats.InputTokens)),
		compactNum(int64(stats.OutputTokens)),
		stats.CostUSD)
	fmt.Fprintf(&b, "mcps: %d connected", info.MCPConnected)
	return strings.TrimRight(b.String(), "\n")
}

// slackSessionName returns the default name for new sessions created via the
// Slack transport, e.g. "Slack 2026-07-27 16:30". Makes sessions easily
// identifiable in `harness sessions` vs TUI or Telegram sessions.
func slackSessionName() string {
	return "Slack " + time.Now().Format("2006-01-02 15:04")
}

func compactNum(n int64) string {
	switch {
	case n >= 1_000_000:
		f := fmt.Sprintf("%.1f", float64(n)/1_000_000)
		return strings.TrimSuffix(f, ".0") + "M"
	case n >= 1_000:
		f := fmt.Sprintf("%.1f", float64(n)/1_000)
		return strings.TrimSuffix(f, ".0") + "k"
	default:
		return fmt.Sprintf("%d", n)
	}
}
