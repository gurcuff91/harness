package tui

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/providers/authflow"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/components"
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
	t.bashMode = false // reset on any submit

	if strings.HasPrefix(text, "/") {
		fields := strings.Fields(text)
		cmd := strings.TrimPrefix(fields[0], "/")
		args := fields[1:]
		t.runCommand(cmd, args)
		return
	}

	// "!" prefix — direct bash execution, bypasses the agent entirely.
	if strings.HasPrefix(text, "!") {
		cmd := strings.TrimSpace(text[1:])
		if cmd == "" {
			return // bare "!" with no command — no-op
		}
		t.execBashCommand(cmd)
		return
	}

	t.submitPrompt(text)
}

// cmdFork forks the current session: creates an exact copy with a new ID,
// switches the TUI to the fork in-place, and prints a one-line notice.
// The history stays on screen unchanged — the fork shares the same content.
func (t *TUI) cmdFork() {
	if t.sessionID == "" {
		t.showWarn("No active session.")
		return
	}
	parentName := t.sessionName

	fork, err := t.client.ForkSession(t.sessionID)
	if err != nil {
		t.showWarn("fork: " + err.Error())
		return
	}

	// Stop the SSE stream for the parent session. The fork is already active on
	// the server (ForkSession registered it); we just need to reconnect the
	// stream to the new session ID so events (turn_start, text_delta, etc.)
	// arrive for the fork, not the parent.
	if t.sseCancel != nil {
		t.sseCancel()
		t.sseCancel = nil
	}
	// Close the parent session on the server (flush to disk). The fork is a
	// separate session — it stays alive independently.
	t.client.CloseSession(t.sessionID) //nolint:errcheck

	// Switch TUI state to the fork.
	t.sessionID = fork.ID
	t.sessionName = fork.Name
	t.model = fork.Model
	t.thinking = fork.Thinking
	t.maxIterations = fork.MaxIterations
	t.refreshSubscriptionFlag()
	t.loadStatsFromSession(fork)
	t.loadSessionCommands()

	// Print notice into the existing history (do NOT clear scrollback — the
	// fork shares the same history content as the parent at this point).
	t.addRaw(ansi.Accent("⑂ ") + ansi.Dimmed("forked from "+parentName))
	t.updateInfo()

	// Reconnect SSE to the fork so prompts and events flow correctly.
	t.startSSE(t.baseCtx)
}

// bashCmdTimeout caps how long a direct "!" bash command may run.
const bashCmdTimeout = 30 * time.Second

// execBashCommand runs a shell command directly (bypassing the agent) and
// renders it like a tool call: header styled as Err(bold "$ cmd"), output Dimmed.
func (t *TUI) execBashCommand(cmd string) {
	header := ansi.Warn(ansi.Bold + "$ " + cmd)
	t.addRaw(header)

	go func() {
		ctx, cancel := context.WithTimeout(t.baseCtx, bashCmdTimeout)
		defer cancel()

		c := exec.CommandContext(ctx, "sh", "-c", cmd) //nolint:gosec — intentional user-driven shell
		var out bytes.Buffer
		c.Stdout = &out
		c.Stderr = &out
		runErr := c.Run()

		output := strings.TrimRight(out.String(), "\n")

		var result string
		if ctx.Err() == context.DeadlineExceeded {
			result = ansi.Err("✘ timed out after " + bashCmdTimeout.String())
		} else if runErr != nil {
			if output == "" {
				output = runErr.Error()
			}
			result = ansi.Dimmed(output) + "\n" + ansi.Err("✘ exit: "+runErr.Error())
		} else {
			if output == "" {
				result = ansi.Dimmed("(no output)")
			} else {
				result = ansi.Dimmed(output)
			}
		}

		t.addRaw(result + "\n") // trailing \n keeps next history entry separated
	}()
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
// scheduleIcon marks a prompt fired by the scheduler (a clock); userIcon marks a
// user prompt. The transport picks the icon from the prompt's origin, carried by
// the received_prompt / follow_up_start events.
const (
	scheduleIcon = "◷"
	userIcon     = "❯"
)

// promptIcon returns the echo icon for a prompt origin.
func promptIcon(origin string) string {
	if origin == "scheduled" {
		return scheduleIcon
	}
	return userIcon
}

func (t *TUI) submitPrompt(text string) {
	if t.sessionID == "" {
		t.showWarn("No active session.")
		return
	}

	// The backend is the single source of truth for echoing. The TUI no longer
	// paints the prompt itself: it sends to the server and waits for the
	// received_prompt (immediate) or follow_up_start (queued) event, then echoes
	// with the icon matching the prompt's origin. This unifies user and scheduled
	// prompts through one path (the ~ms round-trip is imperceptible).
	if t.isSpinning() {
		t.queueCount++
		t.updateInfo()
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

	case "fork":
		go t.cmdFork()
		return

	case "info":
		go t.showInfo()
		return

	case "context":
		go t.showContext()
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
			if err := t.client.DeleteSession(args[0]); err != nil {
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

	// Subscription/OAuth providers (e.g. claude-oauth) authenticate via the
	// OAuth flow, not a typed key: run it locally and send the obtained tokens.
	if t.providerIsSubscription(provider) {
		go func() {
			creds, err := authflow.ObtainOAuthCredentials(provider)
			if err != nil {
				t.showWarn(fmt.Sprintf("connect %s: %s", provider, err.Error()))
				return
			}
			if _, err := t.client.ConnectProviderWithCreds(provider, creds); err != nil {
				t.showWarn(fmt.Sprintf("connect %s: %s", provider, err.Error()))
				return
			}
			t.loadSessionCommands()
			t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("connected "+provider))
		}()
		return
	}

	// API-key providers: capture the key if not supplied.
	if apiKey == "" {
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
	providers, err := t.client.GetProviders()
	if err != nil {
		return false
	}
	for _, p := range providers {
		if p.Name == name {
			return p.IsSubscription
		}
	}
	return false
}

// refreshSubscriptionFlag updates t.isSubscription for the current model so the
// footer's "(sub)" tag stays accurate after a /model change.
func (t *TUI) refreshSubscriptionFlag() {
	models, err := t.client.ListModels()
	if err != nil {
		return
	}
	for _, m := range models {
		if m.Model == t.model {
			t.isSubscription = m.IsSubscription
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
		status, err := t.client.ExecCommand(t.sessionID, cmd, params)
		if err != nil {
			t.showWarn(err.Error())
			return
		}
		t.applyCommandResult(cmd, args, status)
	}()
}

// applyCommandResult refreshes local state after a command (e.g. model change)
// and prints a confirmation line so the user sees the command took effect. The
// exec endpoint returns only a {"status": {code, message}} envelope (never the
// changed value), so the new value is taken from the typed arg — which is what
// the user just supplied, and what the server applied.
func (t *TUI) applyCommandResult(cmd string, args []string, status *client.Status) {
	argVal := strings.Join(args, " ")
	confirm := ""
	switch cmd {
	case "model":
		if argVal != "" {
			t.model = argVal
			t.refreshSubscriptionFlag()
			confirm = "model → " + argVal
		}
	case "thinking":
		if argVal != "" {
			t.thinking = argVal
			confirm = "thinking → " + argVal
		}
	case "rename":
		if argVal != "" {
			t.sessionName = argVal
			confirm = "renamed → " + argVal
		}
	}
	// Reset: wipe the TUI's visual history and accumulated stats so the screen
	// matches the now-empty session store.
	if cmd == "reset" {
		t.history.Clear()
		t.stats = tokensInfo{}
		t.currTurn = 0
		t.queueCount = 0
		t.liveMD = nil
		t.mu.Lock()
		t.toolBlk = make(map[string]*components.RawBlock)
		t.toolArgs = make(map[string]*components.RawBlock)
		t.lastKind = ""
		t.mu.Unlock()
		t.addRaw(ansi.Accent("✔") + " " + ansi.Dimmed("session reset — history cleared"))
		t.updateInfo()
		t.tui.RequestRender(false)
		return
	}
	// Commands that trigger agent streaming show the spinner instead of a
	// static confirmation (the stream itself is the feedback).
	if cmd == "compact" || strings.HasPrefix(cmd, "skill:") {
		t.setSpinning(true)
		t.updateInfo()
		return
	}
	// Fallback: echo the status message, or the command + args, when the
	// command isn't one we specially confirm — so there's always feedback.
	if confirm == "" {
		if status != nil && status.Message != "" {
			confirm = status.Message
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

// showInfo fetches the session info snapshot and renders it in the scrollback.
// Matches the same data the TUI footer shows live, but as a readable panel —
// useful when the footer is too compressed to read, or to capture a snapshot.
func (t *TUI) showInfo() {
	if t.sessionID == "" {
		t.showWarn("No active session.")
		return
	}
	info, err := t.client.GetSessionInfo(t.sessionID)
	if err != nil {
		t.showWarn("info: " + err.Error())
		return
	}

	sess := info.Session
	stats := sess.Stats

	var b strings.Builder

	// label helpers — 2-space indent aligns labels with the "S" of "◉ Session info".
	// Labels padded to 10 chars so values align in one column.
	dim := func(label, value string) string {
		return "  " + ansi.Dimmed(fmt.Sprintf("%-10s", label)) + value + "\n"
	}
	mut := func(label, value string) string {
		return "  " + ansi.Muted(fmt.Sprintf("%-10s", label)) + value + "\n"
	}

	// Header + blank line
	b.WriteString(ansi.Accent(ansi.Bold+"◉ Session info") + "\n\n")

	// Identity — same 10-char label padding as the rest of the panel
	b.WriteString(dim("harness", info.Version))
	name := sess.Name
	if name == "" {
		name = sess.ID[:8]
	}
	b.WriteString(dim("session", name))

	// Model + runtime config
	b.WriteString("\n")
	b.WriteString(mut("model", sess.Model))
	thinking := sess.Thinking
	if thinking == "" {
		thinking = "off"
	}
	b.WriteString(mut("thinking", thinking))
	b.WriteString(mut("iters", fmt.Sprintf("max %d", sess.MaxIterations)))
	if stats.ContextWindow > 0 {
		b.WriteString(mut("context",
			fmt.Sprintf("%.1f%% of %s tokens", stats.ContextUsage*100, compactNum(stats.ContextWindow))))
	}

	// Token / cache / cost
	b.WriteString("\n")
	b.WriteString(mut("tokens",
		fmt.Sprintf("↑%s ↓%s", compactNum(stats.InputTokens), compactNum(stats.OutputTokens))))
	if stats.CacheRead > 0 || stats.CacheWrite > 0 {
		b.WriteString(mut("cache",
			fmt.Sprintf("R%s W%s", compactNum(stats.CacheRead), compactNum(stats.CacheWrite))))
	}
	b.WriteString(mut("cost", fmt.Sprintf("$%.4f", stats.CostUSD)))

	// Environment
	b.WriteString("\n")
	b.WriteString(mut("mcps", fmt.Sprintf("%d connected", info.MCPConnected)))
	b.WriteString(mut("schedules", fmt.Sprintf("%d", info.ScheduleCount)))

	// Runtime state
	if info.Busy {
		queued := ""
		if info.QueueDepth > 0 {
			queued = fmt.Sprintf(" (%d queued)", info.QueueDepth)
		}
		b.WriteString("\n  " + ansi.Warn("⚙ busy"+queued) + "\n")
	}

	// Trailing blank line so the next message in the scrollback has breathing room.
	t.addRaw(strings.TrimRight(b.String(), "\n") + "\n")
}

// showContext fetches the context breakdown and renders it in the scrollback
// as an 8×8 grid where each cell represents 1/64th of the model's context
// window. Components fill cells using ceiling division (any fraction of a cell
// counts as a full cell), so even a tiny component always shows at least 1 cell.
// Order: system prompt → tools → conversation → free.
func (t *TUI) showContext() {
	if t.sessionID == "" {
		t.showWarn("No active session.")
		return
	}
	bd, err := t.client.GetSessionContext(t.sessionID)
	if err != nil {
		t.showWarn("context: " + err.Error())
		return
	}

	win := bd.ContextWindow
	if win == 0 {
		t.showWarn("context: context window size unknown (no model metadata yet).")
		return
	}

	const gridCells = 64                 // 8×8
	cellSize := float64(win) / gridCells // tokens per cell (15625 for 1M window)

	// Each component gets ceil(tokens/cellSize) cells — minimum 1 if tokens > 0.
	// S and T are static (their token counts don't change turn-to-turn), so
	// their cell counts are always fixed. C grows as the conversation grows.
	// free = 64 - sC - tC - cC (may differ from 64-round(actual/win*64),
	// but the actual bar below is always the honest representation of window%).
	cellsFor := func(n int) int {
		if n <= 0 {
			return 0
		}
		c := int(math.Round(float64(n) / cellSize))
		if c < 1 {
			c = 1
		}
		if c > gridCells {
			c = gridCells
		}
		return c
	}

	sC := cellsFor(bd.System)
	tC := cellsFor(bd.Tools)
	cC := cellsFor(bd.Conversation)
	fC := gridCells - sC - tC - cC
	if fC < 0 {
		fC = 0
	}

	// Build the 64-char sequence: S T C ░
	seq := strings.Repeat("S", sC) +
		strings.Repeat("T", tC) +
		strings.Repeat("C", cC) +
		strings.Repeat("░", fC)
	// Pad to exactly 64 in case of rounding edge cases.
	for len(seq) < gridCells {
		seq += "░"
	}
	seq = seq[:gridCells]

	// Build all grid lines first (17 lines: header + 8×(cell+sep) - last sep + footer).
	var gridLines []string
	gridLines = append(gridLines, ansi.Dimmed("  ┌─┬─┬─┬─┬─┬─┬─┬─┐"))
	for row := 0; row < 8; row++ {
		// Cell row.
		var cellLine strings.Builder
		cellLine.WriteString(ansi.Dimmed("  │"))
		for col := 0; col < 8; col++ {
			ch := string(seq[row*8+col])
			switch ch {
			case "S":
				cellLine.WriteString(ansi.Primary("S"))
			case "T":
				cellLine.WriteString(ansi.Warn("T"))
			case "C":
				cellLine.WriteString(ansi.Accent("C"))
			default:
				cellLine.WriteString(ansi.Dimmed("░"))
			}
			if col < 7 {
				cellLine.WriteString(ansi.Dimmed("│"))
			}
		}
		cellLine.WriteString(ansi.Dimmed("│"))
		gridLines = append(gridLines, cellLine.String())
		// Separator (not after last row).
		if row < 7 {
			gridLines = append(gridLines, ansi.Dimmed("  ├─┼─┼─┼─┼─┼─┼─┼─┤"))
		}
	}
	gridLines = append(gridLines, ansi.Dimmed("  └─┴─┴─┴─┴─┴─┴─┴─┘"))

	// Legend lines — dense (no gaps between items), one blank before cell note.
	// Labels padded to 6 chars, values right-aligned to 6 chars → columns align.
	cellK := float64(win) / gridCells / 1000
	legendLines := []string{
		ansi.Primary("S") + ansi.Muted(fmt.Sprintf(" %-6s  %6s", "system", compactNum(bd.System))),
		ansi.Warn("T") + ansi.Muted(fmt.Sprintf(" %-6s  %6s", "tools", compactNum(bd.Tools))),
		ansi.Accent("C") + ansi.Muted(fmt.Sprintf(" %-6s  %6s", "conv", compactNum(bd.Conversation))),
		ansi.Dimmed(fmt.Sprintf("░ %-6s  %6s", "free", compactNum(bd.FreeSpace))),
		"",
		ansi.Dimmed(fmt.Sprintf("cell ≈ %.1fk tokens", cellK)),
	}

	// Zip grid lines + legend lines — legend items appear on consecutive lines
	// (including separator lines) so no visual gaps between them.
	var b strings.Builder
	b.WriteString(ansi.Accent(ansi.Bold+"◉ Context breakdown") + "\n\n")

	for i, gl := range gridLines {
		b.WriteString(gl)
		if i < len(legendLines) {
			b.WriteString("   " + legendLines[i])
		}
		b.WriteByte('\n')
	}

	// Separator + actual bar — same 2-space indent as the grid.
	b.WriteString("\n  " + ansi.Dimmed(strings.Repeat("─", 52)) + "\n")

	if bd.LastRealTotal > 0 && win > 0 {
		usedPct := float64(bd.LastRealTotal) / float64(win) * 100
		uf := int(usedPct / 100 * 36)
		if uf < 1 {
			uf = 1
		}
		bigBar := ansi.Primary(strings.Repeat("█", uf)) + ansi.Dimmed(strings.Repeat("░", 36-uf))
		b.WriteString("  " + ansi.Muted("actual") + fmt.Sprintf("  [%s]  ", bigBar) +
			ansi.Primary(fmt.Sprintf("%.1f%%", usedPct)) + " used\n")
		b.WriteString(fmt.Sprintf("          %s used · %s free · %s total\n",
			ansi.Primary(compactNum(bd.LastRealTotal)),
			ansi.Dimmed(compactNum(bd.FreeSpace)),
			ansi.Dimmed(compactNum(win))))
	} else {
		b.WriteString("  " + ansi.Muted("estimated") + "  ~" +
			ansi.Primary(compactNum(bd.EstimatedTotal)) + " tokens\n")
		b.WriteString(ansi.Dimmed("  (no turn yet — actual count unavailable)\n"))
	}

	t.addRaw(strings.TrimRight(b.String(), "\n") + "\n")
}
