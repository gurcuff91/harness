package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
	"github.com/gurcuff91/harness/internal/transport/tui/components"
)

// autoConnect resolves the model, creates or resumes a session, and opens SSE.
// Port of transport/tui's autoConnect, adapted to the v3 render model.
func (t *TUI) autoConnect(ctx context.Context) {
	data, err := t.client.ListModels()
	if err != nil {
		t.showWarn("Failed to reach server. Is harness running?")
		return
	}
	var models []map[string]any
	json.Unmarshal(data, &models)
	if len(models) == 0 {
		t.showWarn("No active providers. Use /connect to add one.")
		return
	}

	available := map[string]bool{}
	for _, m := range models {
		if id, _ := m["model"].(string); id != "" {
			available[id] = true
		}
	}

	var settingsModel, settingsThinking string
	if d, err := t.client.GetSettings(); err == nil {
		var settings map[string]any
		json.Unmarshal(d, &settings)
		settingsModel, _ = settings["active_model"].(string)
		settingsThinking, _ = settings["thinking_level"].(string)
	}

	switch {
	case t.overrideModel != "" && available[t.overrideModel]:
		t.model = t.overrideModel
	case settingsModel != "" && available[settingsModel]:
		t.model = settingsModel
	default:
		if settingsModel != "" {
			t.showWarn(fmt.Sprintf("Model '%s' not available. Using first active model.", settingsModel))
		}
		t.model, _ = models[0]["model"].(string)
	}
	t.thinking = settingsThinking
	if t.overrideThinking != "" {
		t.thinking = t.overrideThinking
	}

	for _, m := range models {
		if id, _ := m["model"].(string); id == t.model {
			t.isSubscription, _ = m["is_subscription"].(bool)
			break
		}
	}

	cwd, _ := os.Getwd()
	var sess map[string]any

	// Resume path.
	if t.resumeID != "" {
		t.addRaw(ansi.Dimmed("── resuming session ──"))
		if d, err := t.client.ResumeSession(t.resumeID); err != nil {
			t.showWarn(fmt.Sprintf("Failed to resume: %s", err.Error()))
		} else {
			json.Unmarshal(d, &sess)
			t.sessionID, _ = sess["id"].(string)
			t.sessionName, _ = sess["name"].(string)
			t.model, _ = sess["model"].(string)
			t.thinking = ""
			if th, _ := sess["thinking"].(string); th != "" {
				t.thinking = th
			}
			if mt, ok := intFromMap(sess, "max_turns"); ok {
				t.maxTurns = mt
			}
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
			t.renderHistory()
			t.updateInfo()
			t.startSSE(ctx)
			return
		}
	}

	// New session: show the welcome banner (only here — never on resume, which
	// already replays history).
	t.addRaw(t.welcomeBanner())

	// Create new session.
	d, err := t.client.CreateSession(t.model, cwd)
	if err != nil {
		t.showWarn(fmt.Sprintf("Failed to create session: %s", err.Error()))
		return
	}
	json.Unmarshal(d, &sess)
	t.sessionID, _ = sess["id"].(string)
	t.sessionName, _ = sess["name"].(string)
	if th, _ := sess["thinking"].(string); th != "" {
		t.thinking = th
	}
	if mt, ok := intFromMap(sess, "max_turns"); ok {
		t.maxTurns = mt
	}
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

	d, err := t.client.ResumeSession(sessID)
	if err != nil {
		t.showWarn(fmt.Sprintf("Failed to resume: %s", err.Error()))
		return
	}
	var sess map[string]any
	json.Unmarshal(d, &sess)

	// Reset state + scrollback for the incoming session.
	t.resetForNewSession()

	t.sessionID, _ = sess["id"].(string)
	t.sessionName, _ = sess["name"].(string)
	t.model, _ = sess["model"].(string)
	t.thinking = ""
	if th, _ := sess["thinking"].(string); th != "" {
		t.thinking = th
	}
	if mt, ok := intFromMap(sess, "max_turns"); ok {
		t.maxTurns = mt
	}
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

// loadStatsFromSession populates footer stats from a session response.
func (t *TUI) loadStatsFromSession(sess map[string]any) {
	stats, ok := sess["stats"].(map[string]any)
	if !ok {
		return
	}
	if v, _ := stats["input_tokens"].(float64); v > 0 {
		t.stats.input = int(v)
	}
	if v, _ := stats["output_tokens"].(float64); v > 0 {
		t.stats.output = int(v)
	}
	if v, _ := stats["cache_read"].(float64); v > 0 {
		t.stats.cacheRead = int(v)
	}
	if v, _ := stats["cache_write"].(float64); v > 0 {
		t.stats.cacheWrite = int(v)
	}
	if v, _ := stats["cost_usd"].(float64); v > 0 {
		t.stats.cost = v
	}
	if v, _ := stats["context_usage"].(float64); v > 0 {
		t.stats.contextPct = v
	}
	if v, _ := stats["context_window"].(float64); v > 0 {
		t.stats.contextWin = int(v)
	}
}

// renderHistory fetches and replays prior messages on resume. Messages carry a
// parts[] array of typed blocks (text, tool_call, tool_result) — the same
// shape the live stream produces — so we render each into the block history.
// Thinking is intentionally skipped (not persisted). Mirrors v1's renderHistory.
func (t *TUI) renderHistory() {
	data, err := t.client.GetMessages(t.sessionID)
	if err != nil {
		return
	}
	var messages []map[string]any
	if err := json.Unmarshal(data, &messages); err != nil {
		return
	}

	// Tool calls live in an assistant message; their results arrive in the NEXT
	// (user/tool) message. Rendering linearly would group every call together,
	// then every result — visually orphaning each result from its call. Instead
	// we pre-index all results by tool-call id so each call can render its own
	// result immediately below it (matching the live stream's pairing).
	resultByID := map[string]map[string]any{}
	for _, msg := range messages {
		parts, _ := msg["parts"].([]any)
		for _, p := range parts {
			part, _ := p.(map[string]any)
			if part == nil {
				continue
			}
			if tr, ok := part["tool_result"].(map[string]any); ok && tr != nil {
				if id, _ := tr["id"].(string); id != "" {
					resultByID[id] = tr
				}
			}
		}
	}

	for _, msg := range messages {
		// Compaction marker.
		if meta, ok := msg["meta"].(map[string]any); ok {
			if isCompaction, _ := meta["is_compaction"].(bool); isCompaction {
				t.addSection("notice", ansi.Accent(ansi.Bold+"◎ Compacting"))
				t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("(history)"))
				continue
			}
			if isSystemGenerated, _ := meta["is_system_generated"].(bool); isSystemGenerated {
				t.addSection("notice", ansi.Dimmed("◎ progress summary requested"))
				continue
			}
		}
		role, _ := msg["role"].(string)
		parts, _ := msg["parts"].([]any)
		for _, p := range parts {
			part, _ := p.(map[string]any)
			if part == nil {
				continue
			}
			switch {
			case part["text"] != nil:
				text, _ := part["text"].(string)
				if text == "" {
					continue
				}
				if role == "user" {
					t.addSection("user", ansi.Primary("❯ "+text))
				} else {
					t.mu.Lock()
					t.beginSection("text")
					t.history.Add(components.NewMarkdown(text))
					t.mu.Unlock()
				}
			case part["tool_call"] != nil:
				tc, _ := part["tool_call"].(map[string]any)
				name, _ := tc["name"].(string)
				// Pass the COMPLETE args JSON to toolHeader, which parses it into a
				// readable header (same path as the live tool_call event).
				args := ""
				if input, ok := tc["input"].(map[string]any); ok {
					if b, err := json.Marshal(input); err == nil {
						args = string(b)
					}
				}
				callID, _ := tc["id"].(string)
				t.mu.Lock()
				if t.lastKind == "tool" && t.history.Len() > 0 {
					t.history.Add(components.NewSpacer(1))
				}
				t.beginSection("tool")
				t.history.Add(components.NewRawBlock(t.toolHeader(name, args)))
				// Pair this call with its result (looked up by id) and render the
				// result immediately below, so calls and results never drift apart.
				if tr := resultByID[callID]; tr != nil {
					t.history.Add(components.NewRawBlock(toolResultText(t, tr)))
				}
				t.mu.Unlock()
			}
		}
	}
	t.tui.RequestRender(false)
}

// toolResultText extracts a tool_result's output (handling both the flat
// "output" field and the structured content[].text fallback) and formats it
// the same way the live stream does. Duration is 0 on replay (not persisted).
func toolResultText(t *TUI, tr map[string]any) string {
	isErr, _ := tr["is_error"].(bool)
	output, _ := tr["output"].(string)
	if output == "" {
		if content, _ := tr["content"].([]any); len(content) > 0 {
			if c0, _ := content[0].(map[string]any); c0 != nil {
				output, _ = c0["text"].(string)
			}
		}
	}
	return t.formatToolResult(output, 0, isErr)
}

// rootCommandItems builds the palette's top-level command list.
func (t *TUI) rootCommandItems() []components.SelectItem {
	items := []components.SelectItem{
		{Value: "connect", Label: "connect", Description: "Connect a provider"},
		{Value: "disconnect", Label: "disconnect", Description: "Disconnect a provider"},
		{Value: "resume", Label: "resume", Description: "Resume a previous session"},
		{Value: "delete", Label: "delete", Description: "Delete a session"},
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
