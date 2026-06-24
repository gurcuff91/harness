package tuiv3

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gurcuff91/harness/transport/tui-v3/ansi"
	"github.com/gurcuff91/harness/transport/tui-v3/components"
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
	t.loadStatsFromSession(sess)
	t.loadSessionCommands()
	t.updateInfo()
	t.startSSE(ctx)
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

// loadSessionCommands fetches the dynamic command list for the session.
func (t *TUI) loadSessionCommands() {
	if t.sessionID == "" {
		return
	}
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

// renderHistory fetches and renders prior messages on resume.
func (t *TUI) renderHistory() {
	data, err := t.client.GetMessages(t.sessionID)
	if err != nil {
		return
	}
	var messages []map[string]any
	if err := json.Unmarshal(data, &messages); err != nil {
		return
	}
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if content == "" {
			continue
		}
		switch role {
		case "user":
			t.addRaw(ansi.Primary("❯ " + content))
		case "assistant":
			t.addMarkdown(content)
		}
	}
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
