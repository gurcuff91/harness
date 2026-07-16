// Package tui is a from-scratch, pure-Go terminal frontend for the harness.
//
// It is a thin client over the same HTTP/SSE backend the other transports use
// (transport/http): it starts an in-process server, creates/resumes a session,
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
	"syscall"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
	"github.com/gurcuff91/harness/internal/transport/tui/components"
	"github.com/gurcuff91/harness/internal/transport/tui/render"
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
	client *Client
	addr   string

	// Flags.
	overrideModel    string
	overrideThinking string
	resumeID         string

	// Session state.
	sessionID      string
	sessionName    string
	model          string
	thinking       string
	isSubscription bool
	sessionCmds    []CommandDef
	lastSessionID  string

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
	baseCtx   context.Context // root context for (re)starting SSE on in-place resume

	// quitCh is closed when the user requests exit.
	quitCh   chan struct{}
	quitOnce sync.Once

	// Stats + flow.
	stats        tokensInfo
	spinning     bool
	queueCount   int
	compactStart time.Time
	lastTurnText strings.Builder

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

// SetFlags applies CLI overrides.
func (t *TUI) SetFlags(model, thinking, resumeID string) {
	t.overrideModel = model
	t.overrideThinking = thinking
	t.resumeID = resumeID
}

// Run starts the internal server, builds the UI, and runs the render loop until
// the context is cancelled or the user quits.
func (t *TUI) Run(ctx context.Context) error {
	srv, addr, err := startInternalServer(t.agent)
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	t.addr = addr
	defer srv.Close()
	t.client = NewClient(addr)

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	t.baseCtx = ctx

	t.buildUI()

	// Connect BEFORE the first render so the opening frame already shows the
	// banner, session and footer — no flash of an empty editor. The internal
	// server is guaranteed ready (startInternalServer waits for the listener),
	// so no artificial sleep is needed; the startup just takes as long as the
	// initial API calls, then paints everything at once.
	t.autoConnect(ctx)

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

	// Stop() already emitted one CRLF below the TUI content, so a single \r\n
	// here yields exactly one blank line above the farewell. The trailing \r\n
	// terminates the output cleanly so zsh doesn't print its "no final newline"
	// marker (%) and the shell prompt lands on its own line.
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
