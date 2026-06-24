// Package tuiv3 is a from-scratch, pure-Go terminal frontend for the harness.
//
// It is a thin client over the same HTTP/SSE backend the other transports use
// (transport/http): it starts an in-process server, creates/resumes a session,
// streams events over SSE, and renders everything with the custom differential
// renderer in transport/tui-v3/render. No external TUI library — only
// golang.org/x/term (raw mode) and rivo/uniseg (grapheme width).
//
// Styling and behavior mirror the original tview-based TUI (transport/tui):
// the Kaiban palette, streaming markdown, tool-call rendering, command palette,
// spinner, and stats footer.
package tuiv3

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/transport/tui-v3/components"
	"github.com/gurcuff91/harness/transport/tui-v3/render"
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
	sessionID     string
	sessionName   string
	model         string
	thinking      string
	isSubscription bool
	sessionCmds   []CommandDef
	lastSessionID string

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
	lastKind string                         // last history section kind (for spacing)

	// SSE.
	sseCancel context.CancelFunc

	// quitCh is closed when the user requests exit.
	quitCh   chan struct{}
	quitOnce sync.Once

	// Stats + flow.
	stats        tokensInfo
	spinning     bool
	queueCount   int
	localQueue   []string
	compactStart time.Time
	lastTurnText strings.Builder
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

// quit signals the run loop to exit (idempotent).
func (t *TUI) quit() {
	t.quitOnce.Do(func() { close(t.quitCh) })
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

	t.buildUI()

	if err := t.tui.Start(); err != nil {
		return fmt.Errorf("start tui: %w", err)
	}
	defer t.tui.Stop()

	// Quit on context cancel.
	go func() {
		<-ctx.Done()
		t.tui.Stop()
	}()

	// autoConnect once the server is ready.
	go func() {
		time.Sleep(150 * time.Millisecond)
		t.autoConnect(ctx)
	}()

	// Block until the render loop signals quit.
	select {
	case <-t.quitCh:
	case <-ctx.Done():
	}

	if t.lastSessionID != "" {
		fmt.Printf("\n  Resume: harness --resume %s\n\n", t.lastSessionID)
	}
	return nil
}
