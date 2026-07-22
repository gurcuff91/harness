package components

import (
	"sync"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
)

// RawBlock is a render.Component holding pre-styled ANSI text (already laid out)
// that only needs to be wrapped to the current width. Used for content that is
// not markdown source — user prompts, tool-call blocks, spinners-as-text, etc.
// On resize it re-wraps (preserving ANSI) but does not re-lay-out structurally.
//
// Thread-safe: SetText runs on the SSE-consuming goroutine while Render runs on
// the render-loop goroutine, so all field access is guarded by mu. Without this,
// a fast tool call could SetText the real header while the render goroutine was
// mid-Render on the stale "…" placeholder — leaving the old text on screen.
type RawBlock struct {
	mu         sync.Mutex
	text       string
	cacheWidth int
	cacheLines []string
	cacheValid bool
}

// NewRawBlock creates a raw (pre-styled) block.
func NewRawBlock(text string) *RawBlock {
	return &RawBlock{text: text}
}

// SetText replaces the content.
func (b *RawBlock) SetText(text string) {
	b.mu.Lock()
	b.text = text
	b.cacheValid = false
	b.mu.Unlock()
}

// Text returns the raw content.
func (b *RawBlock) Text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text
}

// Invalidate clears the cache (on resize).
func (b *RawBlock) Invalidate() {
	b.mu.Lock()
	b.cacheValid = false
	b.mu.Unlock()
}

// Render wraps the pre-styled text to width, preserving ANSI across breaks.
// An empty block renders zero lines (no phantom blank line for unfilled slots).
//
// WrapTextWithAnsi handles BOTH newline splitting and width wrapping, and
// crucially re-applies the active SGR state (dim, italic, color) at the start
// of every produced line. A naive strings.Split would drop styling on the 2nd+
// line of a multi-line block (e.g. a dim/italic thinking block would render its
// later lines in default white — the bug this fixes).
func (b *RawBlock) Render(width int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cacheValid && b.cacheWidth == width {
		return b.cacheLines
	}
	if b.text == "" {
		b.cacheWidth, b.cacheLines, b.cacheValid = width, nil, true
		return nil
	}
	lines := ansi.WrapTextWithAnsi(b.text, width)
	b.cacheWidth, b.cacheLines, b.cacheValid = width, lines, true
	return lines
}

// History is the scrollback: an ordered list of source-backed blocks. Each
// block re-renders itself at the current width, so a terminal resize re-lays
// out the whole conversation correctly (tables included). Mirrors PI's
// chatContainer of Markdown/Text blocks.
type History struct {
	blocks []Block
}

// Block is a history entry that can render itself and be invalidated on resize.
type Block interface {
	Render(width int) []string
	Invalidate()
}

// NewHistory creates an empty history container.
func NewHistory() *History { return &History{} }

// Add appends a block.
func (h *History) Add(b Block) { h.blocks = append(h.blocks, b) }

// Blocks returns a snapshot of the current block list. The slice is a copy;
// callers can iterate without locking concerns.
func (h *History) Blocks() []Block {
	out := make([]Block, len(h.blocks))
	copy(out, h.blocks)
	return out
}

// Len returns the number of blocks.
func (h *History) Len() int { return len(h.blocks) }

// Last returns the most recently added block, or nil.
func (h *History) Last() Block {
	if len(h.blocks) == 0 {
		return nil
	}
	return h.blocks[len(h.blocks)-1]
}

// Clear removes all blocks.
func (h *History) Clear() { h.blocks = nil }

// Invalidate clears every block's cache (on resize).
func (h *History) Invalidate() {
	for _, b := range h.blocks {
		b.Invalidate()
	}
}

// Render composes all blocks top-to-bottom.
func (h *History) Render(width int) []string {
	var lines []string
	for _, b := range h.blocks {
		lines = append(lines, b.Render(width)...)
	}
	return lines
}
