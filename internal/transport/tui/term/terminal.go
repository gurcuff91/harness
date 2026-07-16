// Package term provides terminal control for the tui renderer.
//
// It mirrors PI's Terminal interface / ProcessTerminal: raw mode, bracketed
// paste, resize notification, and the low-level cursor/clear primitives the
// differential renderer needs. Pure Go — the only dependency is
// golang.org/x/term for raw mode (already used elsewhere in the harness).
package term

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
)

// Terminal is the surface the renderer draws to. Mirrors PI's Terminal
// interface so the render engine stays decoupled from the concrete I/O.
type Terminal interface {
	Start(onInput func(string), onResize func()) error
	Stop()
	Write(data string)
	Columns() int
	Rows() int
	MoveBy(lines int)
	HideCursor()
	ShowCursor()
	ClearLine()
	ClearFromCursor()
	ClearScreen()
}

// ProcessTerminal is the real terminal backed by os.Stdin/os.Stdout.
type ProcessTerminal struct {
	in       *os.File
	out      *os.File
	oldState *term.State
	buffer   *stdinBuffer

	onInput  func(string)
	onResize func()

	resizeCh chan os.Signal
	stopCh   chan struct{}
	readErr  error
	started  bool
}

// NewProcessTerminal creates a terminal using stdin/stdout.
func NewProcessTerminal() *ProcessTerminal {
	return &ProcessTerminal{
		in:  os.Stdin,
		out: os.Stdout,
	}
}

// Start enters raw mode, enables bracketed paste, and begins reading input.
// onInput receives one complete sequence at a time. onResize fires on SIGWINCH.
//
// TODO(kitty-keyboard): PI negotiates the Kitty keyboard protocol here
// (CSI > 7 u + DA query, with a modifyOtherKeys fallback) so it can
// disambiguate Shift+Enter / Ctrl+Enter / Ctrl+I from their VT100-collapsed
// forms. We intentionally omit it for now — standard VT100 input (arrows,
// Enter, Backspace, Ctrl+C, Tab, bracketed paste) works without it. Revisit
// when building the multi-line Editor (Phase 4): either implement Kitty
// negotiation for clean Shift+Enter, or use Alt+Enter for newline (which PI's
// own README calls "most reliable" and needs no protocol negotiation).
func (t *ProcessTerminal) Start(onInput func(string), onResize func()) error {
	t.onInput = onInput
	t.onResize = onResize

	fd := int(t.in.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("stdin is not a terminal")
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enter raw mode: %w", err)
	}
	t.oldState = old
	t.started = true

	// Enable bracketed paste — pastes wrap in ESC[200~ ... ESC[201~.
	t.Write(ansi.BracketedPasteOn)

	// stdin sequence buffer: reassembles split escape sequences and
	// surfaces pastes as a single bracketed blob (matches PI's StdinBuffer).
	t.buffer = newStdinBuffer(func(seq string) {
		if t.onInput != nil {
			t.onInput(seq)
		}
	}, func(content string) {
		if t.onInput != nil {
			t.onInput(ansi.PasteStart + content + ansi.PasteEnd)
		}
	})

	t.stopCh = make(chan struct{})

	// Resize handling via SIGWINCH (Unix).
	t.resizeCh = make(chan os.Signal, 1)
	signal.Notify(t.resizeCh, syscall.SIGWINCH)
	go t.resizeLoop()

	// Read loop.
	go t.readLoop()

	return nil
}

func (t *ProcessTerminal) readLoop() {
	buf := make([]byte, 4096)
	for {
		select {
		case <-t.stopCh:
			return
		default:
		}
		n, err := t.in.Read(buf)
		if n > 0 {
			t.buffer.process(string(buf[:n]))
		}
		if err != nil {
			t.readErr = err
			return
		}
	}
}

func (t *ProcessTerminal) resizeLoop() {
	for {
		select {
		case <-t.stopCh:
			return
		case <-t.resizeCh:
			if t.onResize != nil {
				t.onResize()
			}
		}
	}
}

// Stop restores the terminal: disables bracketed paste, exits raw mode.
func (t *ProcessTerminal) Stop() {
	if !t.started {
		return
	}
	t.started = false

	if t.stopCh != nil {
		close(t.stopCh)
	}
	if t.resizeCh != nil {
		signal.Stop(t.resizeCh)
	}
	if t.buffer != nil {
		t.buffer.destroy()
	}

	t.Write(ansi.BracketedPasteOff)
	t.Write(ansi.ShowCursor)

	if t.oldState != nil {
		_ = term.Restore(int(t.in.Fd()), t.oldState)
		t.oldState = nil
	}
}

// Write emits raw bytes to stdout.
func (t *ProcessTerminal) Write(data string) {
	_, _ = t.out.WriteString(data)
}

// Columns returns the terminal width, falling back to $COLUMNS or 80.
func (t *ProcessTerminal) Columns() int {
	if w, _, err := term.GetSize(int(t.out.Fd())); err == nil && w > 0 {
		return w
	}
	if c := envInt("COLUMNS"); c > 0 {
		return c
	}
	return 80
}

// Rows returns the terminal height, falling back to $LINES or 24.
func (t *ProcessTerminal) Rows() int {
	if _, h, err := term.GetSize(int(t.out.Fd())); err == nil && h > 0 {
		return h
	}
	if r := envInt("LINES"); r > 0 {
		return r
	}
	return 24
}

// MoveBy moves the cursor down (positive) or up (negative) by lines rows.
func (t *ProcessTerminal) MoveBy(lines int) {
	if lines > 0 {
		t.Write(ansi.MoveDown(lines))
	} else if lines < 0 {
		t.Write(ansi.MoveUp(-lines))
	}
}

func (t *ProcessTerminal) HideCursor()      { t.Write(ansi.HideCursor) }
func (t *ProcessTerminal) ShowCursor()      { t.Write(ansi.ShowCursor) }
func (t *ProcessTerminal) ClearLine()       { t.Write(ansi.ClearToLineEnd) }
func (t *ProcessTerminal) ClearFromCursor() { t.Write(ansi.ClearFromCursor) }
func (t *ProcessTerminal) ClearScreen()     { t.Write(ansi.ClearScreenHome) }

func envInt(key string) int {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
