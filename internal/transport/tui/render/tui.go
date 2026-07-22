package render

import (
	"sync"
	"time"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
	"github.com/gurcuff91/harness/internal/transport/tui/term"
)

// minRenderInterval throttles renders to ~60fps, matching PI's 16ms cap.
const minRenderInterval = 16 * time.Millisecond

// TUI is the root container and differential renderer. It owns the terminal,
// schedules renders, and dispatches input to the focused component.
type TUI struct {
	Container
	terminal term.Terminal

	mu sync.Mutex

	// Differential render state (mirrors PI's TUI fields).
	previousLines       []string
	previousWidth       int
	previousHeight      int
	cursorRow           int // end-of-content row (for viewport math)
	hardwareCursorRow   int // actual terminal cursor row
	maxLinesRendered    int
	previousViewportTop int
	previousScrollOffset int // tracks last render's scroll offset

	// userViewportTop freezes the viewport top at the line the user is
	// reading while scrollOffset > 0. Without this, the renderer would
	// recompute the viewport top relative to the end of the buffer on every
	// stream tick (contentHeight grows as the agent streams) and the user's
	// reading position would slide down with the new content — a surprising
	// jump-to-the-bottom behavior. We pin the viewport to userViewportTop
	// instead so the user can keep reading while the agent streams below.
	// Cleared (set to -1) when scrollOffset returns to 0.
	userViewportTop int

	// scrollOffset is the number of content lines the user has scrolled up
	// from the bottom. 0 means stick to the bottom.
	scrollOffset int

	// Scheduling.
	stopped         bool
	renderRequested bool
	renderTimer     *time.Timer
	lastRenderAt    time.Time

	// Input.
	focused        Component
	inputListeners []func(string) bool // return true to consume

	// OnDebug fires on Shift+Ctrl+D if set.
	OnDebug func()
}

// New creates a TUI bound to the given terminal.
func New(t term.Terminal) *TUI {
	return &TUI{terminal: t, userViewportTop: -1}
}

// SetFocus directs keyboard input to component (nil clears focus).
func (t *TUI) SetFocus(component Component) {
	t.focused = component
}

// Focused returns the currently focused component.
func (t *TUI) Focused() Component { return t.focused }

// SetScrollOffset sets how many lines above the bottom of the content are
// visible. 0 sticks to the bottom. Negative values are treated as 0; values
// larger than the content are clamped by doRender.
func (t *TUI) SetScrollOffset(offset int) {
	t.mu.Lock()
	if offset < 0 {
		offset = 0
	}
	if offset == t.scrollOffset {
		t.mu.Unlock()
		return
	}
	// Returning to bottom: clear any pinned viewport so future renders snap
	// to the end and a fresh scroll-up can re-pick a (possibly different)
	// natural viewport top.
	if offset == 0 {
		t.userViewportTop = -1
	}
	t.scrollOffset = offset
	t.mu.Unlock()
	t.RequestRender(false)
}

// ScrollOffset returns the current scroll offset.
func (t *TUI) ScrollOffset() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scrollOffset
}

// Width returns the current terminal width in columns.
func (t *TUI) Width() int { return t.terminal.Columns() }

// Rows returns the current terminal height in rows.
func (t *TUI) Rows() int { return t.terminal.Rows() }

// AddInputListener registers a pre-dispatch input hook. The listener returns
// true to consume the input (stopping further dispatch). Returns an unsubscribe
// function.
func (t *TUI) AddInputListener(fn func(string) bool) func() {
	t.inputListeners = append(t.inputListeners, fn)
	idx := len(t.inputListeners) - 1
	return func() {
		if idx < len(t.inputListeners) {
			t.inputListeners[idx] = nil
		}
	}
}

// Start enters raw mode, hides the cursor, and triggers the first render.
func (t *TUI) Start() error {
	t.stopped = false
	onResize := func() {
		// On resize, invalidate every component so source-backed blocks (e.g.
		// markdown tables) re-lay-out to the new width, then force a full redraw.
		t.Invalidate()
		t.RequestRender(true)
	}
	if err := t.terminal.Start(t.handleInput, onResize); err != nil {
		return err
	}
	t.terminal.HideCursor()
	t.RequestRender(false)
	return nil
}

// Stop moves the cursor below the content, restores it, and exits raw mode.
func (t *TUI) Stop() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return // idempotent: safe against double Stop (explicit + ctx goroutine)
	}
	t.stopped = true
	if t.renderTimer != nil {
		t.renderTimer.Stop()
		t.renderTimer = nil
	}
	prevLen := len(t.previousLines)
	hwRow := t.hardwareCursorRow
	t.mu.Unlock()

	// Move cursor to the line after the content to avoid clobbering it.
	if prevLen > 0 {
		lineDiff := prevLen - hwRow
		if lineDiff > 0 {
			t.terminal.Write(ansi.MoveDown(lineDiff))
		} else if lineDiff < 0 {
			t.terminal.Write(ansi.MoveUp(-lineDiff))
		}
		t.terminal.Write(ansi.CRLF)
	}
	t.terminal.ShowCursor()
	t.terminal.Stop()
}

// RequestRender schedules a render. When force is true, all diff state is reset
// so the next render is a full clear+redraw (used on resize).
func (t *TUI) RequestRender(force bool) {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	if force {
		t.previousLines = nil
		t.previousWidth = -1
		t.previousHeight = -1
		t.cursorRow = 0
		t.hardwareCursorRow = 0
		t.maxLinesRendered = 0
		t.previousViewportTop = 0
		if t.renderTimer != nil {
			t.renderTimer.Stop()
			t.renderTimer = nil
		}
	}
	if t.renderRequested && !force {
		t.mu.Unlock()
		return
	}
	t.renderRequested = true
	t.mu.Unlock()
	t.scheduleRender()
}

// scheduleRender coalesces render requests and enforces minRenderInterval.
func (t *TUI) scheduleRender() {
	t.mu.Lock()
	if t.stopped || t.renderTimer != nil || !t.renderRequested {
		t.mu.Unlock()
		return
	}
	elapsed := time.Since(t.lastRenderAt)
	delay := minRenderInterval - elapsed
	if delay < 0 {
		delay = 0
	}
	t.renderTimer = time.AfterFunc(delay, func() {
		t.mu.Lock()
		t.renderTimer = nil
		if t.stopped || !t.renderRequested {
			t.mu.Unlock()
			return
		}
		t.renderRequested = false
		t.lastRenderAt = time.Now()
		t.mu.Unlock()

		t.doRender()

		t.mu.Lock()
		again := t.renderRequested
		t.mu.Unlock()
		if again {
			t.scheduleRender()
		}
	})
	t.mu.Unlock()
}

// handleInput dispatches one input sequence: listeners first, then the focused
// component.
func (t *TUI) handleInput(data string) {
	for _, fn := range t.inputListeners {
		if fn == nil {
			continue
		}
		if fn(data) {
			return
		}
	}
	if t.focused != nil {
		if h, ok := t.focused.(InputHandler); ok {
			h.HandleInput(data)
		}
	}
}

