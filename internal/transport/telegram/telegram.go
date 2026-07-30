package telegram

import (
	"context"
	"fmt"
	"github.com/gurcuff91/harness/internal/logx"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/client"
	"github.com/gurcuff91/harness/internal/server"
)

// Options configures the Telegram transport.
type Options struct {
	Token       string // bot token (required)
	Model       string // model override; empty = server default
	Thinking    string // thinking level override; empty = server default
	Scheduler   bool   // run the cron engine (schedules fire into their owner chat)
	AllowUnpair bool   // auto-pair: accept any chat, adding it to the allowlist on first contact
}

// Transport is the running Telegram bot: it owns the agent, the in-process
// server, the bot API client, and one live SSE pump per active chat.
type Transport struct {
	opts  Options
	agent *agent.Agent
	api   *client.Client
	bot   *Bot
	store *store
	model string
	cwd   string

	mu    sync.Mutex
	pumps map[int64]*chatPump // chat id → its live session pump

	pendingAlbums *albums // in-flight photo albums, keyed by media_group_id
}

// Run starts the bot and blocks until ctx is cancelled. It builds the agent,
// launches the internal server, verifies the token, then long-polls for
// messages — each becoming a prompt for that chat's session.
func Run(ctx context.Context, a *agent.Agent, opts Options) error {
	if opts.Token == "" {
		return fmt.Errorf("telegram: a bot token is required (--token or TELEGRAM_BOT_TOKEN)")
	}

	st, err := openStore("")
	if err != nil {
		return err
	}

	// In-process server — the transport talks to it over HTTP/SSE, exactly like
	// the TUI, keeping the frontend/backend split clean.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("telegram: bind server: %w", err)
	}
	srv := server.NewServer(a, server.ServerOptions{Verbose: false})
	go srv.Serve(listener) //nolint:errcheck

	cwd, _ := os.Getwd()
	t := &Transport{
		opts:          opts,
		agent:         a,
		api:           client.New(listener.Addr().String()),
		bot:           NewBot(opts.Token),
		store:         st,
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
	logx.Info("telegram", "connected",
		"bot", "@"+me.Username, "default_model", t.model,
		"scheduler", opts.Scheduler, "paired", len(st.allowlist()), "allow_unpair", opts.AllowUnpair)
	if len(st.allowlist()) == 0 && !opts.AllowUnpair {
		logx.Warn("telegram", "no_paired_chats", "hint", "run 'harness telegram pair <chat_id>' or use --allow-unpair")
	}

	// Register the command menu so Telegram suggests commands on "/". Best effort.
	if err := t.bot.SetMyCommands(ctx, botCommands); err != nil {
		logx.Warn("telegram", "set_commands", "error", err.Error())
	}

	t.prewarmPumps(ctx)

	return t.pollLoop(ctx)
}

// prewarmPumps opens a pump (SSE consumer) for every stored chat mapping at
// startup so scheduled prompts are never lost — when the scheduler fires into
// a session, there is already a live drain goroutine writing the output back to
// the Telegram chat. Errors are logged but never fatal: a failed pre-warm just
// means that chat's pump will be created lazily on first user message.
func (t *Transport) prewarmPumps(ctx context.Context) {
	for chatID := range t.store.allSessions() {
		if _, err := t.pumpFor(ctx, chatID); err != nil {
			logx.Warn("telegram", "prewarm", "chat", chatID, "error", err.Error())
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
			logx.Error("telegram", "get_updates", "error", err.Error(), "action", "retrying")
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
	logx.Info("telegram", "prompt", "chat", chatID, "text", oneLine(text, 200))
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
	logx.Info("telegram", "callback", "chat", chatID, "data", cb.Data)

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
		logx.Info("telegram", "thinking_set", "chat", chatID, "level", value)

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
		logx.Info("telegram", "model_set", "chat", chatID, "model", value)

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
			logx.Info("telegram", "auto_paired", "chat", chatID)
		}
		return true
	}
	logx.Warn("telegram", "rejected", "chat", chatID, "reason", "not paired")
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
