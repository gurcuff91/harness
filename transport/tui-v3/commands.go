package tuiv3

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/transport/tui-v3/ansi"
)

// defaultPlaceholder is the editor's idle hint, restored after a value capture.
const defaultPlaceholder = "Type a message or / for commands..."

// handleSubmit processes editor submission: a captured required value, a slash
// command, or a prompt.
func (t *TUI) handleSubmit(text string) {
	// Capturing a required value (e.g. an API key for /connect). The whole line
	// is the value — don't trim or parse it as a command.
	if t.pending != nil {
		t.captureValue(text)
		return
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	t.editor.Clear()

	if strings.HasPrefix(text, "/") {
		fields := strings.Fields(text)
		cmd := strings.TrimPrefix(fields[0], "/")
		args := fields[1:]
		t.runCommand(cmd, args)
		return
	}

	t.submitPrompt(text)
}

// beginValueCapture clears the editor and shows a guiding placeholder so the
// user types a required value (e.g. an API key) into a clean input. The next
// submission is captured by handleSubmit instead of being run as a command.
func (t *TUI) beginValueCapture(cmd string, args []string, placeholder string) {
	t.pending = &pendingValue{cmd: cmd, args: args}
	t.editor.Clear()
	t.editor.SetPlaceholder(placeholder)
	t.tui.RequestRender(false)
}

// captureValue completes a pending command with the typed value, restores the
// default placeholder, and runs the command. Empty input cancels the capture.
func (t *TUI) captureValue(value string) {
	p := t.pending
	t.pending = nil
	t.editor.Clear()
	t.editor.SetPlaceholder(defaultPlaceholder)

	value = strings.TrimSpace(value)
	if value == "" {
		t.showWarn("Cancelled: " + p.cmd)
		t.tui.RequestRender(false)
		return
	}
	t.runCommand(p.cmd, append(p.args, value))
}

// submitPrompt sends a prompt to the session, queueing it locally if a turn is
// already in flight.
func (t *TUI) submitPrompt(text string) {
	if t.sessionID == "" {
		t.showWarn("No active session.")
		return
	}

	// The backend is the single source of truth for queueing. When a turn is in
	// flight it queues the prompt and, when that queued turn starts, emits a
	// follow_up_start event carrying the text — which is when the TUI echoes it
	// (see events.go). So:
	//   - busy → don't echo now; just bump the footer counter. The echo arrives
	//            via follow_up_start when the backend actually starts it.
	//   - idle → this is the turn that starts immediately (no follow_up_start is
	//            emitted for it), so echo it now.
	if t.spinning {
		t.queueCount++
		t.updateInfo()
	} else {
		t.addRaw(ansi.Primary("❯ " + text))
		t.setSpinning(true)
	}

	go func() {
		if _, err := t.client.SendPrompt(t.sessionID, text); err != nil {
			t.addRaw(ansi.Err("✘ " + err.Error()))
			t.setSpinning(false)
		}
	}()
}

// runCommand executes a palette/slash command. It is the SINGLE funnel for
// every entry path (palette Enter, Tab+Enter, hand-typed) and the one place
// that enforces required-parameter completeness: if a command needs a value
// that wasn't supplied, it switches the editor into value-capture mode instead
// of running incomplete. Optional params (e.g. a skill's prompt) never block.
func (t *TUI) runCommand(cmd string, args []string) {
	// Completeness gate for session commands with a REQUIRED first param.
	// connect/resume/delete have their own handling below; quit takes none.
	if t.needsRequiredValue(cmd, args) {
		def := t.sessionCommand(cmd)
		paramName := "value"
		if def != nil && len(def.Params) > 0 {
			paramName = def.Params[0].Name
		}
		t.beginValueCapture(cmd, nil,
			fmt.Sprintf("Enter %s for /%s and press Enter (Esc to cancel)", paramName, cmd))
		return
	}

	switch cmd {
	case "quit", "exit":
		t.quit() // closes the session (flush to disk) + exits
		return

	case "connect":
		if len(args) < 1 {
			t.showWarn("Usage: /connect <provider> [api_key]")
			return
		}
		t.cmdConnect(args)
		return

	case "disconnect":
		if len(args) < 1 {
			t.showWarn("Usage: /disconnect <provider>")
			return
		}
		go func() {
			if _, err := t.client.DisconnectProvider(args[0]); err != nil {
				t.showWarn(err.Error())
				return
			}
			// Refresh so /model drops the now-unavailable provider's models.
			t.loadSessionCommands()
			t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("disconnected "+args[0]))
		}()
		return

	case "resume":
		if len(args) < 1 {
			t.showWarn("Usage: /resume <session_id>")
			return
		}
		t.resumeInPlace(args[0])
		return

	case "delete":
		if len(args) < 1 {
			t.showWarn("Usage: /delete <session_id>")
			return
		}
		go func() {
			if _, err := t.client.DeleteSession(args[0]); err != nil {
				t.showWarn(err.Error())
				return
			}
			t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("deleted session"))
		}()
		return
	}

	// Session-scoped command → exec via API.
	t.execSessionCommand(cmd, args)
}

// cmdConnect connects a provider. OAuth/subscription providers connect directly
// (no key). API-key providers need a key: if none was supplied, drop into
// value-capture mode (clean editor + guiding placeholder) so the key can be
// typed. This is the single funnel — it works whether the command arrived from
// the palette (Enter), from Tab autocomplete + Enter, or typed by hand.
func (t *TUI) cmdConnect(args []string) {
	provider := args[0]
	apiKey := ""
	if len(args) > 1 {
		apiKey = strings.Join(args[1:], " ")
	}

	// No key yet and the provider isn't OAuth → capture the key.
	if apiKey == "" && !t.providerIsSubscription(provider) {
		t.beginValueCapture("connect", []string{provider},
			"Paste the API key for "+provider+" and press Enter (Esc to cancel)")
		return
	}

	go func() {
		if _, err := t.client.ConnectProvider(provider, apiKey); err != nil {
			t.showWarn(fmt.Sprintf("connect %s: %s", provider, err.Error()))
			return
		}
		// Refresh the command list so /model picks up the newly available models.
		t.loadSessionCommands()
		t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("connected "+provider))
	}()
}

// providerIsSubscription reports whether a provider authenticates via OAuth /
// subscription (and so connects without a typed API key).
func (t *TUI) providerIsSubscription(name string) bool {
	data, err := t.client.GetProviders()
	if err != nil {
		return false
	}
	var providers []map[string]any
	json.Unmarshal(data, &providers)
	for _, p := range providers {
		if n, _ := p["name"].(string); n == name {
			sub, _ := p["is_subscription"].(bool)
			return sub
		}
	}
	return false
}

// refreshSubscriptionFlag updates t.isSubscription for the current model so the
// footer's "(sub)" tag stays accurate after a /model change.
func (t *TUI) refreshSubscriptionFlag() {
	data, err := t.client.ListModels()
	if err != nil {
		return
	}
	var models []map[string]any
	json.Unmarshal(data, &models)
	for _, m := range models {
		if id, _ := m["model"].(string); id == t.model {
			t.isSubscription, _ = m["is_subscription"].(bool)
			return
		}
	}
}

// execSessionCommand maps a session command to its API exec call.
func (t *TUI) execSessionCommand(cmd string, args []string) {
	if t.sessionID == "" {
		t.showWarn("No active session.")
		return
	}

	var def *CommandDef
	for i := range t.sessionCmds {
		if t.sessionCmds[i].Name == cmd {
			def = &t.sessionCmds[i]
			break
		}
	}
	if def == nil {
		t.showWarn("Unknown command: " + cmd)
		return
	}

	params := map[string]any{}
	if len(def.Params) > 0 && len(args) > 0 {
		params[def.Params[0].Name] = strings.Join(args, " ")
	}

	go func() {
		data, err := t.client.ExecCommand(t.sessionID, cmd, params)
		if err != nil {
			t.showWarn(err.Error())
			return
		}
		t.applyCommandResult(cmd, args, data)
	}()
}

// applyCommandResult refreshes local state after a command (e.g. model change)
// and prints a confirmation line so the user sees the command took effect.
func (t *TUI) applyCommandResult(cmd string, args []string, data []byte) {
	var resp map[string]any
	json.Unmarshal(data, &resp)

	// Prefer the value echoed back by the API; fall back to the typed arg so
	// the footer always reflects the change even if the response omits it.
	argVal := strings.Join(args, " ")
	confirm := ""
	switch cmd {
	case "model":
		m, _ := resp["model"].(string)
		if m == "" {
			m = argVal
		}
		if m != "" {
			t.model = m
			t.refreshSubscriptionFlag()
			confirm = "model → " + m
		}
	case "thinking":
		l, _ := resp["level"].(string)
		if l == "" {
			l = argVal
		}
		if l != "" {
			t.thinking = l
			confirm = "thinking → " + l
		}
	case "rename":
		n, _ := resp["name"].(string)
		if n == "" {
			n = argVal
		}
		if n != "" {
			t.sessionName = n
			confirm = "renamed → " + n
		}
	}
	// Commands that trigger agent streaming show the spinner instead of a
	// static confirmation (the stream itself is the feedback).
	if cmd == "compact" || strings.HasPrefix(cmd, "skill:") {
		t.setSpinning(true)
		t.updateInfo()
		return
	}
	// Fallback: echo the command + args when the response carries no field we
	// recognize (e.g. a custom session command), so there's always feedback.
	if confirm == "" {
		if msg, _ := resp["message"].(string); msg != "" {
			confirm = msg
		} else {
			confirm = "/" + cmd
			if len(args) > 0 {
				confirm += " " + strings.Join(args, " ")
			}
		}
	}
	t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed(confirm))
	t.updateInfo()
}
