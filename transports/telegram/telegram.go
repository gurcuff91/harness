package telegram

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/server"
)

// Options configures the Telegram transport. These are all concerns of the
// transport ITSELF — what model/thinking level sessions it creates should
// use, how it handles pairing — never the underlying agent's own
// construction-time config (thinking level, scheduler, tools, …), which the
// caller decides before ever calling Run by how it builds the *agent.Agent
// passed in (see agent.AgentOptions / harness.AgentWith*). In particular,
// there is deliberately no Scheduler field here: enabling the cron engine is
// entirely an agent.AgentOptions.EnableScheduler decision made at
// construction time, before Run is ever called — this package has no
// separate scheduler concept of its own to configure.
type Options struct {
	Token string // bot token (required)
	// SessionModel overrides the model for sessions THIS transport creates —
	// empty means the server-wide default active model. Named to make clear
	// this configures per-CHAT sessions, not the agent passed to Run.
	SessionModel string
	// SessionThinking overrides the thinking level for sessions this
	// transport creates. NOTE: currently unused — kept for API parity with
	// SessionModel and to preserve the CLI flag it was already threading
	// through, but nothing in this package reads it yet (a preexisting gap,
	// not something introduced by this rename).
	SessionThinking string
	AllowUnpair     bool // auto-pair: accept any chat, adding it to the allowlist on first contact

	// logger is set via WithLogger — unexported since Options is otherwise a
	// plain data struct built by kong_run_telegram.go's opts slice; only the
	// functional Option constructors below populate it.
	logger logx.Logger
}

// Option configures a Run call — a functional-options wrapper over Options,
// applied left to right (later options win), matching the same pattern
// every other runner in this codebase uses (server.Option, acp.Option,
// harness.AgentOption).
type Option func(*Options)

// WithToken sets the bot token (required — Run errors without one).
func WithToken(token string) Option {
	return func(o *Options) { o.Token = token }
}

// WithSessionModel overrides the model for sessions this transport creates.
func WithSessionModel(model string) Option {
	return func(o *Options) { o.SessionModel = model }
}

// WithSessionThinking overrides the thinking level for sessions this
// transport creates. See Options.SessionThinking's doc comment — currently
// unused internally, kept for API parity with WithSessionModel.
func WithSessionThinking(level string) Option {
	return func(o *Options) { o.SessionThinking = level }
}

// WithAllowUnpair enables auto-pairing: any chat is accepted and added to
// the allowlist on first contact, instead of requiring `harness telegram
// pair <chat_id>` first.
func WithAllowUnpair() Option {
	return func(o *Options) { o.AllowUnpair = true }
}

// WithLogger sets the Logger this transport uses for its own log lines
// (connection status, per-message events, errors — see the logx.Info/Warn/
// Error call sites throughout this package). Default: logx.NilLogger{}
// (silent) — an SDK consumer that never configures one gets no output at
// all; harness's own CLI always passes internal/logx.HarnessLogger{}
// explicitly (see internal/cli/kong_run_telegram.go). This transport's
// in-process server.Server always gets logx.NilLogger{} regardless of what
// this option sets, so logs are never duplicated between the two layers —
// see Run's construction of the server for where that's applied.
func WithLogger(l logx.Logger) Option {
	return func(o *Options) { o.logger = l }
}

// Transport is the running Telegram bot: it owns the agent, the in-process
// server, the bot API client, and one live SSE pump per active chat.
type Transport struct {
	opts   Options
	agent  *agent.Agent
	api    *client.Client
	bot    *Bot
	store  *store
	srv    *server.Server
	logger logx.Logger // never nil — defaults to logx.NilLogger{}
	model  string
	cwd    string

	mu    sync.Mutex
	pumps map[int64]*chatPump // chat id → its live session pump

	pendingAlbums *albums // in-flight photo albums, keyed by media_group_id
}

// Run starts the bot and blocks until ctx is cancelled. It builds the agent,
// launches the internal server, verifies the token, then long-polls for
// messages — each becoming a prompt for that chat's session.
func Run(ctx context.Context, a *agent.Agent, opts ...Option) error {
	o := Options{logger: logx.NilLogger{}}
	for _, opt := range opts {
		opt(&o)
	}
	return runWithOptions(ctx, a, o)
}

// runWithOptions is Run's actual body, taking the fully-assembled Options —
// split out so the WithX-option-application step above stays a thin,
// separately testable layer over the real logic.
func runWithOptions(ctx context.Context, a *agent.Agent, opts Options) error {
	st, err := openStore("")
	if err != nil {
		return err
	}

	// Fall back to the token saved via `harness telegram token <token>`
	// (~/.harness/telegram.json) when neither --token nor
	// TELEGRAM_BOT_TOKEN was given — same precedence Slack's Run applies to
	// its own credentials (flags/env > saved config).
	if opts.Token == "" {
		opts.Token = st.data.Token
	}
	if opts.Token == "" {
		return fmt.Errorf("telegram: a bot token is required — run 'harness telegram token <token>' or pass --token/TELEGRAM_BOT_TOKEN")
	}

	logger := opts.logger
	if logger == nil {
		logger = logx.NilLogger{} // defensive — Run's default already sets this
	}

	// In-process server — the transport talks to it over HTTP/SSE, exactly
	// like the TUI, keeping the frontend/backend split clean. Always
	// logx.NilLogger{} here, never this transport's own `logger`: THIS
	// transport is the one logging (via t.logger below), so its inner server
	// must stay silent rather than duplicating every request as a second log
	// line.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("telegram: bind server: %w", err)
	}
	srv := server.NewServer(a, server.ServerOptions{Logger: logx.NilLogger{}, Transport: "telegram"})
	go srv.Serve(listener) //nolint:errcheck

	cwd, _ := os.Getwd()
	t := &Transport{
		opts:          opts,
		agent:         a,
		api:           client.New(listener.Addr().String()),
		bot:           NewBot(opts.Token),
		store:         st,
		srv:           srv,
		logger:        logger,
		cwd:           cwd,
		pumps:         make(map[int64]*chatPump),
		pendingAlbums: newAlbums(),
	}

	// Resolve the model once (shared by all chats).
	if err := t.resolveModel(); err != nil {
		return err
	}

	// Verify the token before entering the loop.
	me, err := t.bot.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram: invalid token or unreachable API: %w", err)
	}
	// default_model is what new sessions get; a resumed chat keeps its own model
	// unless --model was passed. The per-prompt log reports the real model in use.
	// "scheduler" reflects the AGENT's own construction-time config
	// (agent.AgentOptions.EnableScheduler, decided by the caller before Run
	// was ever invoked — see Options' doc comment for why there's no
	// separate Options.Scheduler here) rather than anything this transport
	// itself configures.
	t.logger.Info("telegram", "connected",
		"bot", "@"+me.Username, "default_model", t.model,
		"scheduler", a.Options().EnableScheduler, "paired", len(st.allowlist()), "allow_unpair", opts.AllowUnpair)
	if len(st.allowlist()) == 0 && !opts.AllowUnpair {
		t.logger.Warn("telegram", "no_paired_chats", "hint", "run 'harness telegram pair <chat_id>' or use --allow-unpair")
	}

	// Register the command menu so Telegram suggests commands on "/". Best effort.
	if err := t.bot.SetMyCommands(ctx, botCommands); err != nil {
		t.logger.Warn("telegram", "set_commands", "error", err.Error())
	}

	t.prewarmPumps(ctx)

	err = t.pollLoop(ctx)
	t.srv.Close()
	return err
}

// prewarmPumps opens a pump (SSE consumer) for every stored chat mapping at
// startup so scheduled prompts are never lost — when the scheduler fires into
// a session, there is already a live drain goroutine writing the output back to
// the Telegram chat. Errors are logged but never fatal: a failed pre-warm just
// means that chat's pump will be created lazily on first user message.
func (t *Transport) prewarmPumps(ctx context.Context) {
	for chatID := range t.store.allSessions() {
		if _, err := t.pumpFor(ctx, chatID); err != nil {
			t.logger.Warn("telegram", "prewarm", "chat", chatID, "error", err.Error())
		}
	}
}

// resolveModel picks the model: the --model override if active, else the
// persisted default, else the first active model.
func (t *Transport) resolveModel() error {
	models, err := t.api.ListModels()
	if err != nil {
		return fmt.Errorf("telegram: reach server: %w", err)
	}
	if len(models) == 0 {
		return fmt.Errorf("telegram: no active providers — connect one first (harness connect ...)")
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

// pollLoop long-polls getUpdates and dispatches each message. It advances the
// offset past processed updates so none repeat.
func (t *Transport) pollLoop(ctx context.Context) error {
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := t.bot.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			t.logger.Error("telegram", "get_updates", "error", err.Error(), "action", "retrying")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.CallbackQuery != nil {
				t.handleCallbackQuery(ctx, u.CallbackQuery)
				continue
			}
			// Skip updates with nothing we handle.
			if u.Message == nil || (u.Message.Text == "" &&
				len(u.Message.Photo) == 0 &&
				u.Message.Document == nil) {
				continue
			}
			t.handleMessage(ctx, u.Message)
		}
	}
}

// handleMessage turns one incoming chat message into a prompt (or a command).
func (t *Transport) handleMessage(ctx context.Context, msg *Message) {
	chatID := msg.Chat.ID
	if !t.authorize(ctx, chatID) {
		return
	}
	// Photos (single or album) — downloaded and sent as image prompts.
	if len(msg.Photo) > 0 {
		t.handlePhotoMessage(ctx, msg)
		return
	}

	// Document — text files become <tel:attach> tags; others silently ignored.
	if msg.Document != nil {
		caption := strings.TrimSpace(msg.Caption)
		t.handleDocument(ctx, msg.Chat.ID, caption, msg.Document)
		return
	}

	text := strings.TrimSpace(msg.Text)

	// Commands.
	if strings.HasPrefix(text, "/") {
		t.handleCommand(ctx, chatID, text)
		return
	}

	pump, err := t.pumpFor(ctx, chatID)
	if err != nil {
		t.replyError(ctx, chatID, err)
		return
	}
	t.logger.Info("telegram", "prompt", "chat", chatID, "text", oneLine(text, 200))
	// The typing indicator is driven by the SSE drain (turn_start→turn_end) so it
	// stays alive for the whole turn, not just Telegram's ~5s window.
	if _, err := t.api.SendPrompt(pump.sessionID, text); err != nil {
		t.replyError(ctx, chatID, err)
	}
}

// handleCallbackQuery processes an inline keyboard button tap. The callback
// data encodes the command and value as "command:value" (e.g. "thinking:high").
func (t *Transport) handleCallbackQuery(ctx context.Context, cb *CallbackQuery) {
	if cb.Message == nil {
		_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "")
		return
	}
	chatID := cb.Message.Chat.ID
	t.logger.Info("telegram", "callback", "chat", chatID, "data", cb.Data)

	p := t.pump(chatID)
	if p == nil {
		_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "No active session.")
		return
	}

	// Verify this callback matches the pending keyboard for this chat.
	p.pendingKbMu.Lock()
	pending := p.pendingKb
	p.pendingKb = nil
	p.pendingKbMu.Unlock()

	if pending == nil {
		_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "")
		return
	}

	// Parse "command:value".
	idx := strings.Index(cb.Data, ":")
	if idx < 0 {
		_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "Invalid selection.")
		return
	}
	command := cb.Data[:idx]
	value := cb.Data[idx+1:]

	switch command {
	case "noop":
		// Provider header button — just dismiss the spinner.
		_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "")
		// Restore pending so the user can still pick a model.
		p.pendingKbMu.Lock()
		p.pendingKb = pending
		p.pendingKbMu.Unlock()

	case "thinking":
		_, err := t.api.ExecCommand(p.sessionID, "thinking", map[string]any{"level": value})
		if err != nil {
			_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "Failed: "+err.Error())
			return
		}
		_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "✓ thinking → "+value)
		_ = t.bot.EditMessageText(ctx, chatID, pending.messageID,
			fmt.Sprintf("🧠 *Thinking level* set to: `%s`", value))
		t.logger.Info("telegram", "thinking_set", "chat", chatID, "level", value)

	case "model":
		_, err := t.api.ExecCommand(p.sessionID, "model", map[string]any{"model": value})
		if err != nil {
			_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "Failed: "+err.Error())
			return
		}
		// Short label for the confirmation.
		short := value
		if idx := strings.LastIndex(value, "/"); idx >= 0 {
			short = value[idx+1:]
		}
		_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "✓ model → "+short)
		_ = t.bot.EditMessageText(ctx, chatID, pending.messageID,
			fmt.Sprintf("🤖 *Model* set to: `%s`", short))
		t.logger.Info("telegram", "model_set", "chat", chatID, "model", value)

	default:
		_ = t.bot.AnswerCallbackQuery(ctx, cb.ID, "Unknown command.")
	}
}

// authorize reports whether a chat may use the bot. With --allow-unpair, an
// unknown chat is auto-paired (added to the allowlist) on first contact.
// Otherwise an un-paired chat is rejected: it's told how to pair, and the
// rejection is logged as a warning.
func (t *Transport) authorize(ctx context.Context, chatID int64) bool {
	if t.store.allowed(chatID) {
		return true
	}
	if t.opts.AllowUnpair {
		if added, err := t.store.pair(chatID); err == nil && added {
			t.logger.Info("telegram", "auto_paired", "chat", chatID)
		}
		return true
	}
	t.logger.Warn("telegram", "rejected", "chat", chatID, "reason", "not paired")
	t.reply(ctx, chatID, fmt.Sprintf(
		"You're not authorized to use this bot yet.\n\nTo pair this chat, run on the host:\n`harness telegram pair %d`",
		chatID))
	return false
}

// oneLine collapses text to a single line and truncates it to max runes, for
// tidy one-line log entries.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
