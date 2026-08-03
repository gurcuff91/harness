package tui

import (
	"context"
	"fmt"
	"os"

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
func (t *TUI) autoConnect(ctx context.Context) {
	var warnings []string

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
		case t.overrideModel != "" && available[t.overrideModel]:
			t.model = t.overrideModel
		case settingsModel != "" && available[settingsModel]:
			t.model = settingsModel
		default:
			if settingsModel != "" {
				warnings = append(warnings, fmt.Sprintf("Model '%s' not available. Using first active model.", settingsModel))
			}
			t.model = models[0].Model
		}
		t.thinking = settingsThinking
		if t.overrideThinking != "" {
			t.thinking = t.overrideThinking
		}

		for _, m := range models {
			if m.Model == t.model {
				t.isSubscription = m.IsSubscription
				break
			}
		}
	}

	cwd, _ := os.Getwd()

	// Resume path — only attempted when a model was actually resolved above
	// (t.model != "" implies the "no providers"/"server unreachable" cases
	// didn't happen, since those never assign it).
	if t.resumeID != "" && t.model != "" {
		if sess, err := t.client.ResumeSession(t.resumeID); err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to resume: %s", err.Error()))
		} else {
			t.sessionID = sess.ID
			t.sessionName = sess.Name
			t.model = sess.Model
			t.thinking = sess.Thinking
			t.maxIterations = sess.MaxIterations
			if t.overrideModel != "" && t.overrideModel != t.model {
				t.client.ExecCommand(t.sessionID, "model", map[string]any{"model": t.overrideModel}) //nolint:errcheck
				t.model = t.overrideModel
			}
			if t.overrideThinking != "" && t.overrideThinking != t.thinking {
				t.client.ExecCommand(t.sessionID, "thinking", map[string]any{"level": t.overrideThinking}) //nolint:errcheck
				t.thinking = t.overrideThinking
			}
			t.loadStatsFromSession(sess)
			t.loadSessionCommands()
			t.addRaw(ansi.Dimmed(fmt.Sprintf("── resumed: %s ──", t.sessionName)))
			t.renderHistory()
			t.updateInfo()
			t.startSSE(ctx)
			return
		}
	}

	// The banner — shown exactly once here, whether or not everything above
	// succeeded. Never shown on a successful resume (which already replayed
	// history above and returned) — only on a fresh session, or when startup
	// couldn't get far enough to create one at all.
	t.addRaw(t.welcomeBanner(warnings...))

	if t.model == "" {
		// No model could be resolved (server unreachable / no active
		// providers) — the banner above already explains why; nothing left
		// to do without one.
		return
	}

	// Create new session.
	sess, err := t.client.CreateSession(t.model, cwd, "")
	if err != nil {
		t.showWarn(fmt.Sprintf("Failed to create session: %s", err.Error()))
		return
	}
	t.sessionID = sess.ID
	t.sessionName = sess.Name
	if sess.Thinking != "" {
		t.thinking = sess.Thinking
	}
	t.maxIterations = sess.MaxIterations
	t.loadStatsFromSession(sess)
	t.loadSessionCommands()
	t.updateInfo()
	t.startSSE(ctx)
}

// resumeInPlace switches the running TUI to a different session without a
// restart: it closes the current session (flushing it to disk), stops the SSE
// stream, clears the scrollback, loads the target session + its history, and
// reopens the stream. Mirrors v1's /resume behavior.
func (t *TUI) resumeInPlace(sessID string) {
	// Stop the current stream and close the active session (flush to disk).
	if t.sseCancel != nil {
		t.sseCancel()
		t.sseCancel = nil
	}
	if t.sessionID != "" && t.sessionID != sessID {
		t.client.CloseSession(t.sessionID) //nolint:errcheck
	}

	sess, err := t.client.ResumeSession(sessID)
	if err != nil {
		t.showWarn(fmt.Sprintf("Failed to resume: %s", err.Error()))
		return
	}

	// Reset state + scrollback for the incoming session.
	t.resetForNewSession()

	t.sessionID = sess.ID
	t.sessionName = sess.Name
	t.model = sess.Model
	t.thinking = sess.Thinking
	t.maxIterations = sess.MaxIterations
	t.refreshSubscriptionFlag()
	t.loadStatsFromSession(sess)
	t.loadSessionCommands()

	t.addRaw(ansi.Dimmed(fmt.Sprintf("── resumed: %s ──", t.sessionName)))
	t.renderHistory()
	t.updateInfo()
	t.startSSE(t.baseCtx)
}

// startSSE opens a persistent SSE stream for the active session.
func (t *TUI) startSSE(ctx context.Context) {
	if t.sessionID == "" {
		return
	}
	sseCtx, cancel := context.WithCancel(ctx)
	t.sseCancel = cancel
	go t.streamEvents(sseCtx)
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
	if t.sessionID == "" {
		return
	}
	t.refreshBadges() // footer status counts (MCP connected, schedule jobs)
	cmds, err := t.client.ListCommands(t.sessionID)
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

// renderHistory fetches and replays prior messages on resume. Messages carry a
// parts[] array of typed blocks (text, tool_call, tool_result) — the same
// shape the live stream produces — so we render each into the block history.
// Thinking is intentionally skipped (not persisted). Mirrors v1's renderHistory.
func (t *TUI) renderHistory() {
	messages, err := t.client.GetMessages(t.sessionID)
	if err != nil {
		return
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
					t.addSection("user", ansi.Primary("❯ "+part.Text))
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
