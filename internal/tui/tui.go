// Package tui is a from-scratch, pure-Go terminal frontend for the harness.
//
// It is a thin client over the same HTTP/SSE backend the other transports use
// (server): it starts an in-process server, creates/resumes a session,
// streams events over SSE, and renders everything with the custom differential
// renderer in transport/tui/render. No external TUI library — only
// golang.org/x/term (raw mode) and rivo/uniseg (grapheme width).
//
// Styling and behavior mirror the original tview-based TUI (transport/tui):
// the Kaiban palette, streaming markdown, tool-call rendering, command palette,
// spinner, and stats footer.
package tui

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/components"
	"github.com/gurcuff91/harness/internal/tui/render"
	"github.com/gurcuff91/harness/internal/tui/term"
	"github.com/gurcuff91/harness/types"
)

// CommandDef and ParamDef are the session command shapes the palette and
// command dispatch drive off of. They alias the client's decoded types (from
// GET /api/sessions/{id}/commands) so this package's existing references keep
// working while the decoding lives in client.
type (
	CommandDef = client.CommandDef
	ParamDef   = client.ParamDef
)

// tokensInfo holds the footer stats.
type tokensInfo struct {
	input, output         int
	cacheRead, cacheWrite int
	cost                  float64
	contextPct            float64
	contextWin            int
}

// TUI is the top-level frontend controller.
type TUI struct {
	agent  *agent.Agent
	client *client.Client
	addr   string

	// Flags.
	overrideModel    string
	channels         []types.InputChannel // --slack-channel, for the startup session only
	channelInfos     []client.ChannelInfo // what the current session listens to (status line)
	overrideThinking string
	resumeID         string
	schedulerOn      bool // --with-scheduler: the agent runs the engine

	// initialPrompt is the first message from the command line (`harness "task"`), submitted ONCE
	// when the first session is ready; see TUI.sendInitialPrompt.
	initialPrompt string

	// Status badges (footer): counts loaded at connect time.
	mcpConnected int // MCP servers currently connected
	scheduleJobs int // schedules configured (shown only when schedulerOn)

	// Session state.
	sessionID      string
	sessionName    string
	model          string
	thinking       string
	isSubscription bool
	sessionCmds    []CommandDef
	lastSessionID  string
	maxIterations  int // ReAct iteration cap per turn, from the session (footer "(turn/max)")

	// status reports the agent's state to the host terminal (OSC 7501); see statusReporter. Built
	// with the renderer, nil until buildUI runs (its methods tolerate that).
	status  *statusReporter
	outcome outcomeFlag // how the current turn is going; see turnOutcome

	// newTerminal builds the terminal buildUI draws on; nil = the real one (stdin/stdout). A seam
	// for tests, which run Run against a recording terminal.
	newTerminal func() term.Terminal

	// Render engine + components.
	tui     *render.TUI
	history *components.History // scrollback as source-backed blocks
	spinner *components.Spinner
	editor  *components.Editor
	info    *components.TruncatedText
	footer  *components.TruncatedText
	palette *paletteController

	// Streaming state (guarded by mu).
	mu       sync.Mutex
	liveMD   *components.Markdown            // current streaming assistant block
	toolBlk  map[string]*components.RawBlock // tool_id -> its result block
	toolArgs map[string]*components.RawBlock // tool_id -> its arg block
	lastKind string                          // last history section kind (for spacing)

	// SSE.
	sseCancel context.CancelFunc
	sseReady  <-chan struct{} // closed once the stream startSSE opened is subscribed (or failed)
	baseCtx   context.Context // root context for (re)starting SSE on in-place resume

	// quitCh is closed when the user requests exit.
	quitCh   chan struct{}
	quitOnce sync.Once

	// Stats + flow.
	stats        tokensInfo
	spinning     bool
	queueCount   int
	currTurn     int // 1-based ReAct iteration for the footer display, set from each loop_start event's own Loop index (+1) — see events.go
	compactStart time.Time
	lastTurnText strings.Builder

	// bashMode is true while the editor text starts with "!" — both separators
	// switch to rose to signal direct bash execution mode. Atomic: the input goroutine writes it
	// (editor OnChange, submit) while the render timer's goroutine reads it (the separators).
	bashMode atomic.Bool

	// pending is set when a command needs a required value typed into a clean
	// editor (e.g. /connect <provider> waiting for the API key). While active,
	// the next editor submission is captured as that value instead of being
	// treated as a prompt or command.
	pending *pendingValue
}

// pendingValue tracks a command awaiting a typed required value.
type pendingValue struct {
	cmd  string   // command to run once the value is captured
	args []string // args already collected (the value is appended)
	// oauthVerifier, when non-empty, means the captured value is an OAuth
	// authorization code to exchange via the server's stateless
	// /api/oauth/{provider} endpoint — not a plain command argument. Set by
	// cmdConnect for subscription providers right after client.StartOAuth
	// returns; consumed in captureValue via client.ExchangeOAuth. The TUI
	// never imports internal/oauthflow directly — see AGENTS.md's
	// backend/frontend separation and the OAuth API endpoint design doc.
	oauthVerifier string
}

// New creates a TUI for the given agent.
func New(a *agent.Agent) *TUI {
	return &TUI{
		agent:    a,
		toolBlk:  make(map[string]*components.RawBlock),
		toolArgs: make(map[string]*components.RawBlock),
		quitCh:   make(chan struct{}),
	}
}

// quit closes the active session (flushing its messages to disk) and signals
// the run loop to exit. Idempotent. Closing the session is critical: the file
// store only persists messages on Close(), so skipping it loses the whole
// conversation — which is exactly why resumed sessions came back empty.
func (t *TUI) quit() {
	t.quitOnce.Do(func() {
		if t.sessionID != "" {
			t.lastSessionID = t.sessionID
			t.client.CloseSession(t.sessionID) //nolint:errcheck
		}
		if t.sseCancel != nil {
			t.sseCancel()
			t.sseCancel = nil
		}
		close(t.quitCh)
	})
}

// SetChannels sets the external conversations the startup session listens to
// (--slack-channel). An explicit request: if one cannot be honored, startup
// fails before the TUI opens.
func (t *TUI) SetChannels(chs []types.InputChannel) { t.channels = chs }

// SetFlags applies CLI overrides.
func (t *TUI) SetFlags(model, thinking, resumeID string) {
	t.overrideModel = model
	t.overrideThinking = thinking
	t.resumeID = resumeID
}

// SetInitialPrompt sets the message to submit as soon as the first session is ready, as if the user
// had typed it — `harness "fix the login bug"`. Blank means none. It is sent once: switching
// session (/resume, /new, /fork) never replays it.
func (t *TUI) SetInitialPrompt(text string) { t.initialPrompt = strings.TrimSpace(text) }

// InitialPrompt returns the message SetInitialPrompt recorded and Run has not yet submitted.
func (t *TUI) InitialPrompt() string { return t.initialPrompt }

// SetScheduler records whether this TUI's agent runs the scheduler engine
// (--with-scheduler). Drives the footer's scheduler badge.
func (t *TUI) SetScheduler(on bool) { t.schedulerOn = on }

// SchedulerOn reports what SetScheduler recorded: whether the footer shows the scheduler's badge. The CLI's
// tests read it to check that the flag and the environment that built the agent are the ones the TUI shows.
func (t *TUI) SchedulerOn() bool { return t.schedulerOn }

// Run starts the internal server, builds the UI, and runs the render loop until
// the context is cancelled or the user quits.
func (t *TUI) Run(ctx context.Context) error {
	srv, addr, err := startInternalServer(t.agent)
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	t.addr = addr
	defer srv.Close()
	t.client = client.New(addr)

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	t.baseCtx = ctx

	t.buildUI()

	// Connect BEFORE the first render so the opening frame already shows the
	// banner, session and footer — no flash of an empty editor. The internal
	// server is guaranteed ready (startInternalServer waits for the listener),
	// so no artificial sleep is needed; the startup just takes as long as the
	// initial API calls, then paints everything at once.
	//
	// An explicit --model/--thinking/--resume that cannot be honored fails HERE, before the TUI
	// starts: raw mode is never entered, nothing is drawn, no OSC is written (the status reporter
	// stays silent until a usable session exists, and there is no record to clear), and Main prints
	// the error and exits 1.
	if err := t.autoConnect(ctx); err != nil {
		if t.sseCancel != nil {
			t.sseCancel()
		}
		return err
	}

	if err := t.tui.Start(); err != nil {
		return fmt.Errorf("start tui: %w", err)
	}

	// Quit on context cancel.
	go func() {
		<-ctx.Done()
		t.tui.Stop()
	}()

	// Block until the render loop signals quit.
	select {
	case <-t.quitCh:
	case <-ctx.Done():
	}

	// Stop the render loop FIRST so it parks the cursor below the TUI content
	// and restores the terminal. Printing the farewell after this means our
	// lines land cleanly with no extra blank rows injected by Stop().
	t.tui.Stop()

	// Drop the status record now that the terminal is back to normal and before anything else is
	// printed. Herdr clears working/blocked on its own when the shell returns, but done and error
	// persist after the process exits, so without this a finished session would leave its last
	// result in the sidebar.
	t.status.Clear()

	// Stop() parks the cursor exactly one line below the TUI content, so the
	// single leading \r\n here yields exactly one blank line above the
	// farewell. (Stop() used to overshoot by one — a MoveDown to just past the
	// content plus its CRLF — which made this render with two blank lines
	// instead; see render.TUI.Stop.) The trailing \r\n terminates the output
	// cleanly so zsh doesn't print its "no final newline" marker (%) and the
	// shell prompt lands on its own line.
	if t.lastSessionID != "" {
		fmt.Printf("\r\n%s %s\r\n  %s harness --resume %s\r\n\r\n",
			ansi.Primary("👋"),
			ansi.Bold+"Goodbye!"+ansi.Reset,
			ansi.Dimmed("To resume this session:"),
			t.lastSessionID)
	} else {
		fmt.Printf("\r\n%s %s\r\n\r\n", ansi.Primary("👋"), ansi.Bold+"Goodbye!"+ansi.Reset)
	}
	return nil
}
