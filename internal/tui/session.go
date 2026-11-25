package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/components"
	"github.com/gurcuff91/harness/types"
)

// autoConnect resolves the model, creates or resumes a session, and opens SSE.
// Port of transport/tui's autoConnect, adapted to the v3 render model.
//
// The banner is harness's identity and is ALWAYS shown exactly once, even
// when startup can't fully succeed (server unreachable, no active
// providers, a requested resume failing, …) — every problem along the way
// is collected into `warnings` and surfaces INSIDE the single banner block
// (see welcomeBanner) instead of a bare warning printed on its own with no
// banner above it, or the banner being skipped entirely.
//
// An explicit --model, --thinking or --resume is a REQUIREMENT, not a hint. Either the session runs
// exactly as asked, or autoConnect returns an error and Run exits 1 before the TUI opens (never a
// fallback, never ignored): a task must not run on a model that was not chosen, nor in a
// conversation that was not asked for. Fallbacks apply only to what the person did not pass — the
// settings model, then the first active one. A startup that fails WITHOUT any flag being violated
// (no providers, the server refuses to create a session) is not an error: the banner or a warning
// explains it and the prompt stays in the editor.
func (t *TUI) autoConnect(ctx context.Context) error {
	var warnings []string

	// What was CHOSEN. Nothing is stored on the TUI (t.model, t.thinking…) until the server has
	// confirmed it: on a failure the TUI must not look as if it had applied a model or a thinking
	// level that never took effect (the footer reads these fields).
	var model, thinking string
	var subscription bool

	models, err := t.client.ListModels()
	switch {
	case err != nil:
		warnings = append(warnings, "Failed to reach server. Is harness running?")
	case len(models) == 0:
		warnings = append(warnings, "No active providers. Use /connect to add one.")
	default:
		available := map[string]bool{}
		for _, m := range models {
			if m.Model != "" {
				available[m.Model] = true
			}
		}

		var settingsModel, settingsThinking string
		if s, err := t.client.GetSettings(); err == nil {
			settingsModel = s.ActiveModel
			settingsThinking = s.ThinkingLevel
		}

		switch {
		case settingsModel != "" && available[settingsModel]:
			model = settingsModel
		default:
			if settingsModel != "" {
				warnings = append(warnings, fmt.Sprintf("Model '%s' not available. Using first active model.", settingsModel))
			}
			model = models[0].Model
		}
		thinking = settingsThinking

	}

	// An explicit --model is THE model, used exactly as given and decided in exactly one place —
	// here, after the settings/first-active choice above, so nothing above can ever win over it, and
	// also when the models list was empty or could not be read. It is deliberately not checked against
	// that list: the server is the one source of truth for whether it exists, and the error it returns
	// when creating the session is the very text -p prints (provider not found / model not found in
	// provider). Falling back to another model is not an option.
	if t.overrideModel != "" {
		model = t.overrideModel
	}

	for _, m := range models {
		if m.Model == model {
			subscription = m.IsSubscription
			break
		}
	}

	cwd, _ := os.Getwd()

	// Resume path. A --resume is explicit, so it is attempted whenever it was asked for — and when it
	// cannot be honored the process fails (see above): a task meant for THAT conversation must not
	// run in a fresh one.
	if t.resumeID != "" {
		sess, err := t.client.ResumeSession(t.resumeID, t.channelOptions()...)
		if err != nil {
			return fmt.Errorf("resume session %s: %w", t.resumeID, err)
		}
		// What the session really runs on, never what was asked: nothing below is assumed to have
		// worked until the server said so.
		model, thinking = sess.Model, sess.Thinking
		if t.overrideModel != "" && t.overrideModel != model {
			if _, err := t.client.ExecCommand(sess.ID, "model", map[string]any{"model": t.overrideModel}); err != nil {
				t.client.CloseSession(sess.ID) //nolint:errcheck — release it untouched; the error below is the point
				return fmt.Errorf("switch session %s to model %s: %w", sess.ID, t.overrideModel, err)
			}
			model = t.overrideModel
		}
		if t.overrideThinking != "" && t.overrideThinking != thinking {
			if _, err := t.client.ExecCommand(sess.ID, "thinking", map[string]any{"level": t.overrideThinking}); err != nil {
				t.client.CloseSession(sess.ID) //nolint:errcheck
				return fmt.Errorf("set thinking %s on session %s: %w", t.overrideThinking, sess.ID, err)
			}
			thinking = t.overrideThinking
		}
		t.switchView(sess.ID, model, thinking, sess.MaxIterations, sess.Channels)
		t.setSessionName(sess.Name)
		t.refreshSubscriptionFlag() // the model may differ from the one resolved above (fallback or --model)
		t.loadStatsFromSession(sess)
		t.loadSessionCommands()
		t.addRaw(ansi.Dimmed(fmt.Sprintf("── resumed: %s ──", t.view().sessionName)))
		t.noteModelFallback(sess)
		t.renderHistory()
		t.updateInfo()
		t.startSSE(ctx)
		t.sendInitialPrompt()
		return nil
	}

	// Nothing may reach the terminal until we know startup succeeds or fails SOFTLY: adding a block
	// to the history schedules a render, and a render writes to the terminal before the TUI has
	// even started. So the banner is added only after the outcome of creating the session — a hard
	// failure (an explicit flag that cannot be honored) leaves stdout and stderr untouched.
	if model == "" {
		// No model could be resolved (server unreachable / no active providers) — the banner
		// explains why; nothing left to do without one.
		t.withView(func() { t.thinking = thinking })
		t.addRaw(t.welcomeBanner(warnings...))
		t.keepInitialPrompt()
		return nil
	}

	// Create new session.
	sess, err := t.client.CreateSession(model, cwd, "", t.channelOptions()...)
	if err != nil {
		if t.overrideModel != "" || len(t.channels) > 0 {
			// The person chose this model and it cannot be used: not a warning, not a different
			// model. The wording is the server's, the same as `-p` prints.
			return fmt.Errorf("create session: %w", err)
		}
		t.withView(func() { t.model, t.thinking, t.isSubscription = model, thinking, subscription })
		t.addRaw(t.welcomeBanner(warnings...))
		t.showWarn(fmt.Sprintf("Failed to create session: %s", err.Error()))
		t.keepInitialPrompt()
		return nil
	}
	// An explicit --thinking is applied to the new session and checked, like --model: a fresh
	// session starts on the settings level, and ignoring the flag would run the task at a level
	// that was not chosen.
	if sess.Thinking != "" {
		thinking = sess.Thinking
	}
	if t.overrideThinking != "" && t.overrideThinking != thinking {
		if _, err := t.client.ExecCommand(sess.ID, "thinking", map[string]any{"level": t.overrideThinking}); err != nil {
			t.client.CloseSession(sess.ID) //nolint:errcheck
			return fmt.Errorf("set thinking %s on session %s: %w", t.overrideThinking, sess.ID, err)
		}
		thinking = t.overrideThinking
	}
	t.withView(func() { t.isSubscription = subscription })
	t.switchView(sess.ID, model, thinking, sess.MaxIterations, sess.Channels)
	t.addRaw(t.welcomeBanner(warnings...))
	t.setSessionName(sess.Name)
	t.loadStatsFromSession(sess)
	t.loadSessionCommands()
	t.updateInfo()
	t.startSSE(ctx)
	t.sendInitialPrompt()
	return nil
}

// sendInitialPrompt submits the command-line prompt (`harness "task"`) through the normal submit
// path — the echo, the spinner, the queue and the status reports behave exactly as for a typed
// message — and only ONCE: the field is consumed, so nothing that reconnects a stream or switches
// session (/resume, /new, /fork) can replay it.
//
// It is called right after startSSE, and that order is the point: the echo of a prompt is an event
// on the session's stream, so a prompt sent before the stream is subscribed loses its echo (what
// TestTUIEchoesAColleaguePromptWithItsIcon learned). startSSE opens the stream in a goroutine, so
// the send itself waits for the subscription (awaitStream).
//
// Like a typed message it goes to the agent as TEXT: it is deliberately not run through
// handleSubmit, so a prompt that starts with "/" or "!" is not taken for a command (harness "/quit"
// must not exit) — it is what the caller asked the agent, verbatim.
func (t *TUI) sendInitialPrompt() {
	text := t.initialPrompt
	t.initialPrompt = ""
	if text == "" || t.view().sessionID == "" {
		return
	}
	t.awaitStream()
	t.submitPrompt(text)
}

// keepInitialPrompt is for a start that ended without a usable session: nothing is sent, and the
// message is put in the editor so it is not lost — one Enter sends it once the user has fixed what
// the banner says (/connect a provider, say).
func (t *TUI) keepInitialPrompt() {
	text := t.initialPrompt
	t.initialPrompt = ""
	if text == "" || t.editor == nil {
		return
	}
	t.editor.SetValue(text)
	t.tui.RequestRender(false)
}

// resumeInPlace switches the running TUI to a different session without a
// restart: it closes the current session (flushing it to disk), stops the SSE
// stream, clears the scrollback, loads the target session + its history, and
// reopens the stream. Mirrors v1's /resume behavior.
func (t *TUI) resumeInPlace(sessID string) {
	// Stop the current stream and close the active session (flush to disk).
	t.stopSSE()
	if t.view().sessionID != "" && t.view().sessionID != sessID {
		t.client.CloseSession(t.view().sessionID) //nolint:errcheck
	}

	sess, err := t.client.ResumeSession(sessID)
	if err != nil {
		t.showWarn(fmt.Sprintf("Failed to resume: %s", err.Error()))
		return
	}

	// Reset state + scrollback for the incoming session.
	t.resetForNewSession()

	t.switchView(sess.ID, sess.Model, sess.Thinking, sess.MaxIterations, sess.Channels)
	t.setSessionName(sess.Name)
	t.refreshSubscriptionFlag()
	t.loadStatsFromSession(sess)
	t.loadSessionCommands()

	t.addRaw(ansi.Dimmed(fmt.Sprintf("── resumed: %s ──", t.view().sessionName)))
	t.noteModelFallback(sess)
	t.renderHistory()
	t.updateInfo()
	t.startSSE(t.baseCtx)
}

// noteModelFallback tells the user when a resumed session was reopened on a
// different model than the one it was saved with — its provider was gone, so the
// server fell back to the harness default. Never silent: the new model changes
// cost, capabilities and context window.
func (t *TUI) noteModelFallback(sess *client.Session) {
	if sess == nil {
		return
	}
	if len(sess.MissingProfiles) > 0 {
		t.addRaw(ansi.Warn(fmt.Sprintf("⚠ profiles not registered here, skipped: %s", strings.Join(sess.MissingProfiles, ", "))))
	}
	if sess.ModelFallback == nil {
		return
	}
	f := sess.ModelFallback
	t.addRaw(ansi.Warn(fmt.Sprintf("⚠ %s is not available — resumed on %s (saved as this session's model now)", f.From, f.To)))
}

// newSessionInPlace closes the currently active session (if any, flushing
// it to disk) and starts a brand new, empty one with the same model/cwd —
// distinct from /reset, which reuses the SAME session ID and only wipes its
// history. Mirrors resumeInPlace's shape (stop SSE, close old session,
// clear scrollback, wire up the new one, reconnect SSE), but creates rather
// than resumes. No warning when there was no active session to close — a
// missing t.sessionID (e.g. startup couldn't create one) is not an error
// here, it's just nothing to close before creating the new one.
func (t *TUI) newSessionInPlace() {
	t.stopSSE()
	if t.view().sessionID != "" {
		t.client.CloseSession(t.view().sessionID) //nolint:errcheck
	}

	cwd, _ := os.Getwd()
	sess, err := t.client.CreateSession(t.view().model, cwd, "")
	if err != nil {
		t.showWarn(fmt.Sprintf("Failed to create session: %s", err.Error()))
		return
	}

	// Reset state + scrollback for the incoming session.
	t.resetForNewSession()

	thinking := t.view().thinking
	if sess.Thinking != "" {
		thinking = sess.Thinking
	}
	t.switchView(sess.ID, t.view().model, thinking, sess.MaxIterations, sess.Channels)
	t.setSessionName(sess.Name)
	t.refreshSubscriptionFlag()
	t.loadStatsFromSession(sess)
	t.loadSessionCommands()

	t.addRaw(ansi.Dimmed(fmt.Sprintf("── new session: %s ──", t.view().sessionName)))
	t.updateInfo()
	t.startSSE(t.baseCtx)
}

// stopSSE cancels the active session's event stream, for a switch to another session.
//
// A turn that was running on it will never deliver its turn_end here, so the terminal is told idle
// at once rather than left on "working". It is done synchronously on purpose: the consumer of the
// cancelled stream notices only later (its ctx.Done), and by then the new stream may already be
// reporting — a late idle would then overwrite the new session's working.
func (t *TUI) stopSSE() {
	if cancel := t.takeSSECancel(); cancel != nil {
		cancel()
	}
	t.status.IdleIfWorking()
}

// setSSECancel records the current stream's cancel func (sseMu).
func (t *TUI) setSSECancel(c context.CancelFunc) {
	t.sseMu.Lock()
	t.sseCancel = c
	t.sseMu.Unlock()
}

// takeSSECancel removes and returns the current stream's cancel func (nil if
// none), so exactly one caller cancels it.
func (t *TUI) takeSSECancel() context.CancelFunc {
	t.sseMu.Lock()
	defer t.sseMu.Unlock()
	c := t.sseCancel
	t.sseCancel = nil
	return c
}

// setSessionName records the session's name and publishes it as the terminal title. Every place
// that changes the name goes through here, so the title cannot fall behind the footer.
func (t *TUI) setSessionName(name string) {
	t.withView(func() { t.sessionName = name })
	t.status.SetTitle(name)
}

// startSSE opens a persistent SSE stream for the active session.
//
// It is also where the terminal is told the session is ready: every path that establishes a
// session (startup, resume, a new session, a fork) ends here, and a session with nothing running is
// idle. That is what makes a fresh instance an agent in the terminal's list — with a status
// hosts such as `hc send --if idle` can target — before any turn, and what keeps a switch from
// leaving the previous session's state behind. It must come BEFORE the stream opens: a resumed
// session may already have a turn in flight, whose working would otherwise be overwritten by a
// late idle. No session (startup could not create one) means nothing is reported.
func (t *TUI) startSSE(ctx context.Context) {
	if t.view().sessionID == "" {
		return
	}
	t.status.Set(ansi.StatusIdle)
	sseCtx, cancel := context.WithCancel(ctx)
	t.setSSECancel(cancel)
	ready := make(chan struct{})
	t.sseReady = ready
	go t.streamEventsReady(sseCtx, ready)
}

// streamReadyTimeout (a variable so a test can shorten it) bounds how long sendInitialPrompt waits for the event stream to open. Opening
// it is a loopback request to the in-process server — milliseconds; this only keeps a stuck
// connection from holding the first message forever.
var streamReadyTimeout = 5 * time.Second

// awaitStream blocks until the stream startSSE opened is subscribed (or failed to open, or the
// timeout passed). The echo of a prompt is an event on that stream, so a prompt sent first loses it.
func (t *TUI) awaitStream() {
	if t.sseReady == nil {
		return
	}
	select {
	case <-t.sseReady:
	case <-time.After(streamReadyTimeout):
	}
}

// resetForNewSession wipes the scrollback and per-session state in preparation
// for loading a different session (resume).
func (t *TUI) resetForNewSession() {
	t.mu.Lock()
	t.history.Clear()
	t.liveMD = nil
	t.lastKind = ""
	t.stats = tokensInfo{}
	t.mu.Unlock()
}

// loadSessionCommands fetches the dynamic command list for the session.
func (t *TUI) loadSessionCommands() {
	if t.view().sessionID == "" {
		return
	}
	t.refreshBadges() // footer status counts (MCP connected, schedule jobs)
	cmds, err := t.client.ListCommands(t.view().sessionID)
	if err != nil {
		return
	}
	t.sessionCmds = cmds
}

// loadStatsFromSession populates footer stats from a session's persisted
// stats. Only non-zero fields overwrite (a fresh session's zero stats
// shouldn't clobber anything already shown).
func (t *TUI) loadStatsFromSession(sess *client.Session) {
	stats := sess.Stats
	t.mu.Lock()
	defer t.mu.Unlock()
	if stats.InputTokens > 0 {
		t.stats.input = stats.InputTokens
	}
	if stats.OutputTokens > 0 {
		t.stats.output = stats.OutputTokens
	}
	if stats.CacheRead > 0 {
		t.stats.cacheRead = stats.CacheRead
	}
	if stats.CacheWrite > 0 {
		t.stats.cacheWrite = stats.CacheWrite
	}
	if stats.CostUSD > 0 {
		t.stats.cost = stats.CostUSD
	}
	if stats.ContextUsage > 0 {
		t.stats.contextPct = stats.ContextUsage
	}
	if stats.ContextWindow > 0 {
		t.stats.contextWin = stats.ContextWindow
	}
}

// resumeCompactions is how many compaction checkpoints back the TUI reaches
// when it replays a resumed session: it renders from just AFTER the
// SECOND-TO-LAST checkpoint onward. That yields the last pre-compaction block
// plus the current working set — enough recent context that a freshly-compacted
// session isn't visually empty, without paying to fetch, parse and render
// thousands of stale messages (rendering through the markdown engine is the
// bottleneck, and nobody scrolls back through a long, many-times-compacted
// conversation).
//
// The cut is one past the checkpoint (not on it), so the replay doesn't OPEN
// with a bare "◎ Compacting" marker right under the "N earlier messages
// hidden" hint; the LAST checkpoint still falls inside the range and shows
// normally. A session compacted fewer than twice is replayed whole.
//
// The server does the cutting (GET /messages?compactions=2), so the skipped
// part of the log is never even sent, let alone parsed.
const resumeCompactions = 2

// renderHistory fetches and replays prior messages on resume. Messages carry a
// parts[] array of typed blocks (text, tool_call, tool_result) — the same
// shape the live stream produces — so we render each into the block history.
// Thinking is intentionally skipped (not persisted). Mirrors v1's renderHistory.
func (t *TUI) renderHistory() {
	messages, hidden, _, err := t.client.GetMessagesWith(t.view().sessionID, client.MessagesOptions{Compactions: resumeCompactions})
	if err != nil {
		return
	}
	t.replayMessages(messages, hidden)
}

// replayMessages renders messages into the block history; hidden is how many
// earlier messages were left out ahead of them (0 = none), shown as a hint.
func (t *TUI) replayMessages(messages []types.Message, hidden int) {
	if hidden > 0 {
		t.addRaw(ansi.Dimmed(fmt.Sprintf("↑ %d earlier messages hidden", hidden)))
	}

	// Tool calls live in an assistant message; their results arrive in the NEXT
	// (user/tool) message. Rendering linearly would group every call together,
	// then every result — visually orphaning each result from its call. Instead
	// we pre-index all results by tool-call id so each call can render its own
	// result immediately below it (matching the live stream's pairing).
	resultByID := map[string]*types.ToolResult{}
	for _, msg := range messages {
		for i := range msg.Parts {
			if tr := msg.Parts[i].ToolResult; tr != nil && tr.ID != "" {
				resultByID[tr.ID] = tr
			}
		}
	}

	for _, msg := range messages {
		// Compaction marker.
		if msg.Meta != nil {
			if msg.Meta.IsCompaction {
				t.addSection("notice", ansi.Accent(ansi.Bold+"◎ Compacting"))
				t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("(history)"))
				continue
			}
			if msg.Meta.IsSystemGenerated {
				t.addSection("notice", ansi.Dimmed("◎ progress summary requested"))
				continue
			}
		}
		for i := range msg.Parts {
			part := msg.Parts[i]
			switch {
			case part.Text != "":
				if msg.Role == types.RoleUser {
					// The persisted origin picks the icon, exactly as the live echo does:
					// a scheduled or colleague prompt must not read as typed by the user
					// once the session is reloaded.
					origin := ""
					if msg.Meta != nil {
						origin = msg.Meta.Origin
					}
					t.addSection("user", ansi.Primary(promptIcon(origin, part.Text)+" "+part.Text))
				} else {
					t.mu.Lock()
					t.beginSection("text")
					t.history.Add(components.NewMarkdown(part.Text))
					t.mu.Unlock()
				}
			case part.ToolCall != nil:
				tc := part.ToolCall
				// The tool_call's Input is the COMPLETE args JSON; toolHeader parses
				// it into a readable header (same path as the live tool_call event).
				t.mu.Lock()
				if t.lastKind == "tool" && t.history.Len() > 0 {
					t.history.Add(components.NewSpacer(1))
				}
				t.beginSection("tool")
				t.history.Add(components.NewRawBlock(t.toolHeader(tc.Name, string(tc.Input))))
				// Pair this call with its result (looked up by id) and render the
				// result immediately below, so calls and results never drift apart.
				if tr := resultByID[tc.ID]; tr != nil {
					t.history.Add(components.NewRawBlock(t.formatToolResult(tr.Output, 0, tr.IsErr)))
				}
				t.mu.Unlock()
			}
		}
	}
	t.tui.RequestRender(false)
}

// rootCommandItems builds the palette's top-level command list.
func (t *TUI) rootCommandItems() []components.SelectItem {
	items := []components.SelectItem{
		{Value: "connect", Label: "connect", Description: "Connect a provider"},
		{Value: "disconnect", Label: "disconnect", Description: "Disconnect a provider"},
		{Value: "resume", Label: "resume", Description: "Resume a previous session"},
		{Value: "delete", Label: "delete", Description: "Delete a session"},
		{Value: "info", Label: "info", Description: "Show session info snapshot"},
		{Value: "context", Label: "context", Description: "Show context window breakdown"},
		{Value: "fork", Label: "fork", Description: "Fork this session — exact copy with a new ID"},
		{Value: "new", Label: "new", Description: "Close the active session (if any) and start a brand new, empty one"},
	}
	for _, cmd := range t.sessionCmds {
		items = append(items, components.SelectItem{
			Value:       cmd.Name,
			Label:       cmd.Name,
			Description: cmd.Description,
		})
	}
	items = append(items, components.SelectItem{Value: "quit", Label: "quit", Description: "Exit harness"})
	return items
}

// channelOptions binds the session opened at startup to the --slack-channel
// conversations. Only that session: /new, /resume and /fork open sessions that
// listen to nothing (channels are explicit, per session, never inherited).
func (t *TUI) channelOptions() []client.SessionOption {
	if len(t.channels) == 0 {
		return nil
	}
	return []client.SessionOption{client.SessionWithChannels(t.channels...)}
}
