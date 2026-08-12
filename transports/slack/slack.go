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
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
	"github.com/gurcuff91/harness/types"
)

// Options configures the Slack transport. These are all concerns of the
// transport ITSELF — credentials, and what model/thinking level sessions it
// creates should use — never the underlying agent's own construction-time
// config (thinking level, scheduler, tools, …), which the caller decides
// before ever calling Run by how it builds the *agent.Agent passed in (see
// agent.AgentOptions / harness.AgentWith*). In particular, there is
// deliberately no Scheduler field here: enabling the cron engine is
// entirely an agent.AgentOptions.EnableScheduler decision made at
// construction time, before Run is ever called — this package has no
// separate scheduler concept of its own to configure.
type Options struct {
	Workspace string // e.g. "https://myco.slack.com" (required)
	XoxC      string // xoxc-... browser session API token (required)
	XoxD      string // xoxd-... browser session cookie (required)
	// SessionModel overrides the model for sessions THIS transport creates —
	// empty means the server-wide default active model. Named to make clear
	// this configures per-CHANNEL sessions, not the agent passed to Run.
	SessionModel string
	// SessionThinking overrides the thinking level for sessions this transport
	// creates or resumes (from the --thinking launch flag). Applied via
	// applySessionOverrides, alongside SessionModel.
	SessionThinking string

	// logger is set via WithLogger — unexported since Options is otherwise a
	// plain data struct; only the functional Option constructors populate it.
	logger logx.Logger
}

// Option configures a Run call — a functional-options wrapper over Options,
// applied left to right (later options win), matching the same pattern
// every other runner in this codebase uses (server.Option, acp.Option,
// harness.AgentOption).
type Option func(*Options)

// WithWorkspace sets the Slack workspace URL (e.g. "https://myco.slack.com").
// Required unless already saved via `harness slack login` (Run falls back to
// ~/.harness/slack.json for any of Workspace/XoxC/XoxD left empty).
func WithWorkspace(url string) Option {
	return func(o *Options) { o.Workspace = url }
}

// WithXoxC sets the xoxc- browser session API token. See WithWorkspace for
// the saved-credentials fallback.
func WithXoxC(token string) Option {
	return func(o *Options) { o.XoxC = token }
}

// WithXoxD sets the xoxd- browser session cookie. See WithWorkspace for the
// saved-credentials fallback.
func WithXoxD(cookie string) Option {
	return func(o *Options) { o.XoxD = cookie }
}

// WithSessionModel overrides the model for sessions this transport creates.
func WithSessionModel(model string) Option {
	return func(o *Options) { o.SessionModel = model }
}

// WithSessionThinking overrides the thinking level for sessions this
// transport creates or resumes (applied alongside WithSessionModel).
func WithSessionThinking(level string) Option {
	return func(o *Options) { o.SessionThinking = level }
}

// WithLogger sets the Logger this transport uses for its own log lines
// (connection status, per-message events, errors). Default: logx.NewNilLogger()
// (silent) — an SDK consumer that never configures one gets no output at
// all; harness's own CLI always passes internal/logx.NewHarnessLogger()
// explicitly (see internal/cli/kong_run_slack.go). This transport's
// in-process server.Server always gets logx.NewNilLogger() regardless of what
// this option sets, so logs are never duplicated between the two layers.
func WithLogger(l logx.Logger) Option {
	return func(o *Options) { o.logger = l }
}

// Transport is the running Slack integration: it owns the agent, the in-process
// server, the bot client, and one live SSE pump per active channel.
type Transport struct {
	opts   Options
	agent  *agent.Agent
	api    *client.Client
	bot    *Bot
	store  *store
	srv    *server.Server
	logger logx.Logger // never nil — defaults to logx.NewNilLogger()
	model  string
	cwd    string
	myID   string // our own Slack user ID (to detect mentions)

	mu    sync.Mutex
	pumps map[string]*channelPump // channel ID → its live session pump

	// pendingAsks tracks SlackAsk calls waiting for a reply, keyed by DM
	// channel ID (D...) — at most one per channel, enforced by SlackAsk
	// itself. handleEvent checks this map before routing an incoming DM
	// message to the agent as a normal prompt: a hit means the message is
	// the awaited reply, consumed here instead of becoming a prompt. See
	// tryDeliverAsk and slackAskTool (tools.go).
	asksMu      sync.Mutex
	pendingAsks map[string]chan askReply

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
func Run(ctx context.Context, a *agent.Agent, opts ...Option) error {
	o := Options{logger: logx.NewNilLogger()}
	for _, opt := range opts {
		opt(&o)
	}
	return runWithOptions(ctx, a, o)
}

// runWithOptions is Run's actual body, taking the fully-assembled Options —
// split out so the WithX-option-application step above stays a thin,
// separately testable layer over the real logic.
func runWithOptions(ctx context.Context, a *agent.Agent, opts Options) error {
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

	logger := opts.logger
	if logger == nil {
		logger = logx.NewNilLogger() // defensive — Run's default already sets this
	}

	// In-process server — same pattern as TUI and Telegram. Always
	// logx.NewNilLogger() here, never this transport's own `logger`: THIS
	// transport is the one logging (via t.logger below), so its inner server
	// must stay silent rather than duplicating every request as a second log
	// line.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("slack: bind server: %w", err)
	}
	srv := server.NewServer(a, server.ServerOptions{Logger: logx.NewNilLogger(), Transport: "slack"})
	go srv.Serve(listener) //nolint:errcheck

	cwd, _ := os.Getwd()
	bot := NewBot(opts.Workspace, opts.XoxC, opts.XoxD)
	t := &Transport{
		opts:        opts,
		agent:       a,
		api:         client.New(listener.Addr().String()),
		bot:         bot,
		store:       st,
		srv:         srv,
		logger:      logger,
		cwd:         cwd,
		pumps:       make(map[string]*channelPump),
		pendingAsks: make(map[string]chan askReply),
	}

	// Verify tokens FIRST (and get our user ID for @mention detection) — bad
	// credentials are the most fundamental prerequisite, so they surface
	// "invalid tokens" rather than being masked by an unrelated "no active
	// providers" from resolveModel below.
	me, err := t.bot.AuthTest(ctx)
	if err != nil {
		return fmt.Errorf("slack: invalid tokens: %w", err)
	}
	t.myID = me.UserID

	// Resolve model.
	if err := t.resolveModel(); err != nil {
		return err
	}
	// "scheduler" reflects the AGENT's own construction-time config
	// (agent.AgentOptions.EnableScheduler, decided by the caller before Run
	// was ever invoked — see Options' doc comment for why there's no
	// separate Options.Scheduler here) rather than anything this transport
	// itself configures.
	t.logger.Info("slack", "connected",
		"user", me.UserID, "team", me.Team,
		"default_model", t.model, "scheduler", a.Options().EnableScheduler)

	// Inject Slack-specific tools into the agent so the model can proactively
	// post messages, resolve channels and users by name, etc.
	for _, tool := range SlackTools(bot, me.UserID, t) {
		a.RegisterTool(tool)
	}

	t.prewarmPumps(ctx)

	err = t.rtmLoop(ctx)
	t.srv.Close()
	return err
}

// prewarmPumps opens a pump (SSE consumer) for every stored channel mapping at
// startup so scheduled prompts are never lost — when the scheduler fires into a
// session, there is already a live drain goroutine writing the output back to
// the Slack channel. Errors are logged but never fatal: a failed pre-warm just
// means that channel's pump will be created lazily on first user message.
func (t *Transport) prewarmPumps(ctx context.Context) {
	for channelID := range t.store.allSessions() {
		if _, err := t.pumpFor(ctx, channelID); err != nil {
			t.logger.Warn("slack", "prewarm", "channel", channelID, "error", err.Error())
		}
	}
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
	if t.opts.SessionModel != "" && active[t.opts.SessionModel] {
		t.model = t.opts.SessionModel
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
			t.logger.Error("slack", "rtm_connect", "error", err.Error(), "action", "retrying")
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
			t.logger.Error("slack", "rtm_dial", "error", err.Error(), "action", "retrying")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}

		t.logger.Info("slack", "rtm_connected")
		t.connMu.Lock()
		t.conn = conn
		t.connMu.Unlock()

		if err := t.readLoop(ctx, conn); err != nil && ctx.Err() == nil {
			t.logger.Error("slack", "rtm_read", "error", err.Error(), "action", "reconnecting")
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
		t.logger.Error("slack", "typing", "channel", channelID, "error", err.Error())
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
	if evt.Channel == "" {
		return
	}

	isDM := strings.HasPrefix(evt.Channel, "D")

	// SlackAsk interception: if this DM has a pending ask waiting for a
	// reply, deliver the message there instead of routing it to the agent
	// as a normal prompt — BEFORE the "no text" early-return below, since a
	// reply that's only a file attachment (no text) must still count. A
	// slash command is the one exception: a human typing "/stop" while an
	// ask is pending almost certainly means the command, not "my answer is
	// /stop", so tryDeliverAsk itself checks for that and declines,
	// letting it fall through to handleCommand as usual.
	if isDM {
		if t.tryDeliverAsk(ctx, evt) {
			return
		}
	}

	if evt.Text == "" {
		return
	}
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

	// Handle /commands (typed as "@harness /compact", "@harness /reset", etc.)
	if strings.HasPrefix(text, "/") {
		t.handleCommand(ctx, evt.Channel, evt.User, text)
		return
	}

	// Process file attachments — images go to SendPromptWithImages, text files
	// become <slack:attach> tags appended to the prompt.
	var images []types.ImageData
	var attachTags []string
	if len(evt.Files) > 0 {
		images, attachTags = t.handleFiles(ctx, evt.Channel, evt.Files)
	}

	// Build the final prompt: context tags (channel/user) + text + attach tags.
	// channelID is empty for DMs so only the user tag is emitted.
	contextChannel := evt.Channel
	if strings.HasPrefix(evt.Channel, "D") {
		contextChannel = "" // DM — suppress channel tag
	}
	prompt := buildPrompt(contextChannel, evt.User, text, attachTags)

	t.logger.Info("slack", "prompt",
		"channel", evt.Channel, "user", evt.User,
		"text", oneLine(prompt, 200),
		"images", len(images), "files", len(attachTags))

	pump, err := t.pumpFor(ctx, evt.Channel)
	if err != nil {
		t.replyError(ctx, evt.Channel, err)
		return
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

// adminOnlyCommands are the commands that require the caller to be in the admin
// list. Read-only commands (/help, /info, /context) are always public.
var adminOnlyCommands = map[string]bool{
	"stop":     true,
	"compact":  true,
	"reset":    true,
	"thinking": true,
	"model":    true,
}

// handleCommand routes slash commands sent to the bot via Slack.
func (t *Transport) handleCommand(ctx context.Context, channelID, senderID, text string) {
	fields := strings.Fields(text)
	cmd := strings.TrimPrefix(fields[0], "/")
	t.logger.Info("slack", "command", "channel", channelID, "user", senderID, "name", cmd)

	// Admin-only commands require the sender to be in the admin list — but
	// only in shared spaces. A DM (channel ID starts with "D") is 1-to-1
	// with a single user, so there's nobody to protect against: the sender
	// is the sole interlocutor and may run any command freely. Every other
	// space (public/private channel "C", multi-person group DM "G") is
	// shared, so one user's /reset or /model would affect everyone else —
	// there the admin gate stays enforced.
	if adminOnlyCommands[cmd] && !strings.HasPrefix(channelID, "D") {
		ok, err := IsAdmin(senderID)
		if err != nil || !ok {
			t.send(ctx, channelID, fmt.Sprintf(
				"⛔ You don't have permission to run `/%s`.\n"+
					"Ask an admin to run: `harness slack admin %s`",
				cmd, senderID))
			return
		}
	}

	switch cmd {
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

	case "reset":
		p, err := t.pumpFor(ctx, channelID)
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		if _, err := t.api.ExecCommand(p.sessionID, "reset", nil); err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		t.send(ctx, channelID, "🔄 Session history and stats wiped. Starting fresh.")

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

	case "help":
		t.send(ctx, channelID, slackHelp(""))

	case "thinking":
		t.cmdThinking(ctx, channelID, fields)

	case "model":
		t.cmdModel(ctx, channelID, fields)

	case "context":
		p, err := t.pumpFor(ctx, channelID)
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		bd, err := t.api.GetSessionContext(p.sessionID)
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		t.send(ctx, channelID, formatContext(bd))

	default:
		t.send(ctx, channelID, slackHelp(cmd)) // cmd is already the clean name without "/"
	}
}

// formatInfo renders session info as a Slack message.
func formatInfo(info *client.SessionInfo) string {
	sess := info.Session
	stats := sess.Stats

	row := func(label, value string) string {
		return fmt.Sprintf("%-10s%s\n", label, value)
	}

	var data strings.Builder
	data.WriteString(row("harness", info.Version))
	name := sess.Name
	if name == "" {
		name = sess.ID[:8]
	}
	data.WriteString(row("session", name))
	data.WriteByte('\n')
	data.WriteString(row("model", sess.Model))
	thinking := sess.Thinking
	if thinking == "" {
		thinking = "off"
	}
	data.WriteString(row("thinking", thinking))
	data.WriteString(row("iters", fmt.Sprintf("max %d", sess.MaxIterations)))
	if stats.ContextWindow > 0 {
		data.WriteString(row("context", fmt.Sprintf("%.1f%% of %s tokens",
			stats.ContextUsage*100, compactNum(int64(stats.ContextWindow)))))
	}
	data.WriteByte('\n')
	data.WriteString(row("tokens", fmt.Sprintf("↑%s ↓%s",
		compactNum(int64(stats.InputTokens)), compactNum(int64(stats.OutputTokens)))))
	if stats.CacheRead > 0 || stats.CacheWrite > 0 {
		data.WriteString(row("cache", fmt.Sprintf("R%s W%s",
			compactNum(int64(stats.CacheRead)), compactNum(int64(stats.CacheWrite)))))
	}
	data.WriteString(row("cost", fmt.Sprintf("$%.4f", stats.CostUSD)))
	data.WriteByte('\n')
	data.WriteString(row("mcps", fmt.Sprintf("%d connected", info.MCPConnected)))
	if info.ScheduleCount > 0 {
		data.WriteString(row("schedules", fmt.Sprintf("%d", info.ScheduleCount)))
	}
	if info.Busy {
		queued := ""
		if info.QueueDepth > 0 {
			queued = fmt.Sprintf(" (%d queued)", info.QueueDepth)
		}
		data.WriteString("\n⚙ busy" + queued + "\n")
	}

	var b strings.Builder
	b.WriteString("📊 *Session info*\n```\n")
	b.WriteString(strings.TrimRight(data.String(), "\n"))
	b.WriteString("\n```")
	return b.String()
}

// slackSessionName returns the default name for new sessions created via the
// Slack transport, e.g. "Slack 2026-07-27 16:30". Makes sessions easily
// identifiable in `harness sessions` vs TUI or Telegram sessions.
func slackSessionName() string {
	return "Slack " + time.Now().Format("2006-01-02 15:04")
}

// slackHelp returns the help message shown on /help or an unknown command.
func slackHelp(unknown string) string {
	var b strings.Builder
	if unknown != "" {
		fmt.Fprintf(&b, "Unknown command `/%s`.\n\n", unknown)
	}
	b.WriteString("*Available commands:*\n")
	b.WriteString("• `/stop` — interrupt the current work mid-turn\n")
	b.WriteString("• `/compact` — summarize and compact the conversation to free context space\n")
	b.WriteString("• `/reset` — wipe history and stats, start fresh\n")
	b.WriteString("• `/info` — show session info (model, thinking, tokens, cost, MCPs)\n")
	b.WriteString("• `/context` — show context window breakdown (system, tools, conversation, free space)\n")
	b.WriteString("• `/thinking [level]` — show current thinking level or set it (off/low/medium/high/xhigh)\n")
	b.WriteString("• `/model [model]` — show current model or switch to a new one\n")
	b.WriteString("• `/help` — show this help")
	return b.String()
}

// thinkingLevels are the valid thinking levels in order.
var thinkingLevels = []string{"off", "low", "medium", "high", "xhigh"}

// cmdThinking handles /thinking and /thinking <level>.
func (t *Transport) cmdThinking(ctx context.Context, channelID string, fields []string) {
	p, err := t.pumpFor(ctx, channelID)
	if err != nil {
		t.replyError(ctx, channelID, err)
		return
	}

	// No argument → list levels + show current.
	if len(fields) < 2 {
		info, err := t.api.GetSessionInfo(p.sessionID)
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		current := info.Session.Thinking
		if current == "" {
			current = "off"
		}
		var b strings.Builder
		b.WriteString("🧠 *Thinking level*\n")
		b.WriteString(fmt.Sprintf("Current: `%s`\n\n", current))
		b.WriteString("Available levels: ")
		for i, l := range thinkingLevels {
			if i > 0 {
				b.WriteString(" · ")
			}
			if l == current {
				b.WriteString("*" + l + "*")
			} else {
				b.WriteString(l)
			}
		}
		b.WriteString("\n\nUsage: `/thinking <level>`")
		t.send(ctx, channelID, b.String())
		return
	}

	// With argument → set the level. Strip backticks for the same reason as /model.
	level := strings.ToLower(strings.ReplaceAll(fields[1], "`", ""))
	valid := false
	for _, l := range thinkingLevels {
		if l == level {
			valid = true
			break
		}
	}
	if !valid {
		t.send(ctx, channelID, fmt.Sprintf(
			"⚠️ Invalid thinking level `%s`. Valid values: %s",
			level, strings.Join(thinkingLevels, " · ")))
		return
	}

	_, err = t.api.ExecCommand(p.sessionID, "thinking", map[string]any{"level": level})
	if err != nil {
		t.replyError(ctx, channelID, err)
		return
	}
	t.send(ctx, channelID, fmt.Sprintf("✓ Thinking set to `%s`", level))
}

// cmdModel handles /model and /model <provider/model>.
func (t *Transport) cmdModel(ctx context.Context, channelID string, fields []string) {
	p, err := t.pumpFor(ctx, channelID)
	if err != nil {
		t.replyError(ctx, channelID, err)
		return
	}

	// No argument → list available models + show current.
	if len(fields) < 2 {
		info, err := t.api.GetSessionInfo(p.sessionID)
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		models, err := t.api.ListModels()
		if err != nil {
			t.replyError(ctx, channelID, err)
			return
		}
		current := info.Session.Model

		var b strings.Builder
		b.WriteString("🤖 *Model*\n")
		b.WriteString(fmt.Sprintf("Current: `%s`\n\n", current))
		b.WriteString("*Available models:*\n")

		// Group by provider.
		type entry struct{ provider, model string }
		var entries []entry
		providerSeen := map[string]bool{}
		var providerOrder []string
		providerModels := map[string][]string{}
		for _, m := range models {
			if !providerSeen[m.Provider] {
				providerSeen[m.Provider] = true
				providerOrder = append(providerOrder, m.Provider)
			}
			providerModels[m.Provider] = append(providerModels[m.Provider], m.Model)
			_ = entries
		}
		for _, prov := range providerOrder {
			b.WriteString(fmt.Sprintf("*%s:*\n", prov))
			for _, model := range providerModels[prov] {
				marker := ""
				if model == current {
					marker = " ✓"
				}
				b.WriteString(fmt.Sprintf("  • `%s`%s\n", model, marker))
			}
		}
		b.WriteString("\nUsage: `/model <provider/model>`")
		t.send(ctx, channelID, b.String())
		return
	}

	// With argument → switch model.
	// Strip backticks in case the user copied the model from the code-formatted
	// list (Slack renders `model` with backticks that get included on copy).
	model := strings.ReplaceAll(strings.Join(fields[1:], " "), "`", "")
	_, err = t.api.ExecCommand(p.sessionID, "model", map[string]any{"model": model})
	if err != nil {
		t.replyError(ctx, channelID, err)
		return
	}
	short := model
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		short = model[idx+1:]
	}
	t.send(ctx, channelID, fmt.Sprintf("✓ Model switched to `%s`", short))
}

// formatContext renders the context breakdown as a Slack monospace code block.
func formatContext(bd *client.ContextBreakdown) string {
	if bd == nil {
		return "⚠️ No context data available."
	}
	win := bd.ContextWindow
	cn := func(n int) string { return compactNum(int64(n)) }

	row := func(label, value string) string {
		return fmt.Sprintf("%-14s%s\n", label, value)
	}
	sub := func(label, value string) string {
		return fmt.Sprintf("  %-12s%s\n", label, value)
	}

	var data strings.Builder
	data.WriteString(row("system prompt", cn(bd.System)+" tokens"))
	data.WriteString(row("tools", cn(bd.Tools)+" tokens"))
	data.WriteString(row("conversation", cn(bd.Conversation)+" tokens"))
	data.WriteString("\n" + strings.Repeat("─", 36) + "\n")
	data.WriteString(row("estimated", "~"+cn(bd.EstimatedTotal)+" tokens"))
	if bd.LastRealTotal > 0 && win > 0 {
		pct := float64(bd.LastRealTotal) / float64(win) * 100
		data.WriteString(row("actual", cn(bd.LastRealTotal)+fmt.Sprintf(" tokens (%.1f%%)", pct)))
		data.WriteString(sub("free", cn(bd.FreeSpace)+" of "+cn(win)))
	} else {
		data.WriteString("(no turn yet)\n")
	}

	var b strings.Builder
	b.WriteString("📐 *Context breakdown*\n```\n")
	b.WriteString(strings.TrimRight(data.String(), "\n"))
	b.WriteString("\n```")
	return b.String()
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
