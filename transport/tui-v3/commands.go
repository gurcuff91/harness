package tuiv3

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/transport/tui-v3/ansi"
)

// handleSubmit processes editor submission: a slash command or a prompt.
func (t *TUI) handleSubmit(text string) {
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

// submitPrompt sends a prompt to the session, queueing it locally if a turn is
// already in flight.
func (t *TUI) submitPrompt(text string) {
	if t.sessionID == "" {
		t.showWarn("No active session.")
		return
	}
	t.addRaw(ansi.Primary("❯ " + text))

	if t.spinning {
		t.localQueue = append(t.localQueue, text)
		t.queueCount++
		t.updateInfo()
	} else {
		t.setSpinning(true)
	}

	go func() {
		if _, err := t.client.SendPrompt(t.sessionID, text); err != nil {
			t.addRaw(ansi.Err("✘ " + err.Error()))
			t.setSpinning(false)
		}
	}()
}

// runCommand executes a palette/slash command. Simple commands (quit, value
// commands) are handled directly; the rest delegate to the API exec endpoint.
func (t *TUI) runCommand(cmd string, args []string) {
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

// cmdConnect connects a provider (OAuth or API key).
func (t *TUI) cmdConnect(args []string) {
	provider := args[0]
	apiKey := ""
	if len(args) > 1 {
		apiKey = args[1]
	}
	go func() {
		var err error
		if apiKey != "" {
			_, err = t.client.ConnectProvider(provider, apiKey)
		} else {
			_, err = t.client.ConnectProvider(provider, "")
		}
		if err != nil {
			t.showWarn(fmt.Sprintf("connect %s: %s", provider, err.Error()))
			return
		}
		t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("connected "+provider))
	}()
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
