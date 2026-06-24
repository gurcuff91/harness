package components

import (
	"strings"

	"github.com/gurcuff91/harness/transport/tui-v3/ansi"
)

// RawBlock is a render.Component holding pre-styled ANSI text (already laid out)
// that only needs to be wrapped to the current width. Used for content that is
// not markdown source — user prompts, tool-call blocks, spinners-as-text, etc.
// On resize it re-wraps (preserving ANSI) but does not re-lay-out structurally.
type RawBlock struct {
	text string

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
	b.text = text
	b.cacheValid = false
}

// Text returns the raw content.
func (b *RawBlock) Text() string { return b.text }

// Invalidate clears the cache (on resize).
func (b *RawBlock) Invalidate() { b.cacheValid = false }

// Render wraps the pre-styled text to width, preserving ANSI across breaks.
// An empty block renders zero lines (no phantom blank line for unfilled slots).
func (b *RawBlock) Render(width int) []string {
	if b.cacheValid && b.cacheWidth == width {
		return b.cacheLines
	}
	if b.text == "" {
		b.cacheWidth, b.cacheLines, b.cacheValid = width, nil, true
		return nil
	}
	var lines []string
	for _, line := range strings.Split(b.text, "\n") {
		if ansi.VisibleWidth(line) <= width {
			lines = append(lines, line)
		} else {
			lines = append(lines, ansi.WrapTextWithAnsi(line, width)...)
		}
	}
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
