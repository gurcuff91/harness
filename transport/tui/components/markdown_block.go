package components

import (
	"sync"

	"github.com/gurcuff91/harness/transport/tui/ansi"
)

// Markdown is a render.Component that holds RAW markdown source and renders it
// to styled ANSI lines at the CURRENT terminal width. Because it keeps the
// source (not the rendered output), it re-lays-out correctly on resize —
// tables in particular recompute their column widths to the new width.
//
// This mirrors PI's Markdown component: the chat keeps a tree of source-backed
// blocks, so a width change just re-renders every block.
// Thread-safe: Append/SetSource run on the SSE goroutine while Render runs on
// the render loop; mu guards all field access to avoid a torn read of a growing
// live block.
type Markdown struct {
	mu         sync.Mutex
	source     string
	cacheWidth int
	cacheLines []string
	cacheValid bool
}

// NewMarkdown creates a markdown block from raw source.
func NewMarkdown(source string) *Markdown {
	return &Markdown{source: source}
}

// SetSource replaces the raw markdown and invalidates the cache. Used by the
// streaming path to grow the live block as deltas arrive.
func (b *Markdown) SetSource(source string) {
	b.mu.Lock()
	b.source = source
	b.cacheValid = false
	b.mu.Unlock()
}

// Source returns the raw markdown.
func (b *Markdown) Source() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.source
}

// Append adds a delta to the raw markdown.
func (b *Markdown) Append(delta string) {
	b.mu.Lock()
	b.source += delta
	b.cacheValid = false
	b.mu.Unlock()
}

// Invalidate clears the render cache (called on resize).
func (b *Markdown) Invalidate() {
	b.mu.Lock()
	b.cacheValid = false
	b.mu.Unlock()
}

// Render lays the raw markdown out at the given width, caching by width.
func (b *Markdown) Render(width int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cacheValid && b.cacheWidth == width {
		return b.cacheLines
	}
	if b.source == "" {
		b.cacheWidth, b.cacheLines, b.cacheValid = width, nil, true
		return nil
	}

	md := NewMarkdownStream()
	md.SetWidth(width)
	rendered := md.Feed(b.source) + md.Flush()

	// WrapTextWithAnsi splits on newlines, wraps over-wide lines, AND re-applies
	// the active SGR state at the start of each produced line — so multi-line
	// styled content (dim thinking, colored blocks) keeps its styling on every
	// line instead of reverting to default after the first newline.
	lines := ansi.WrapTextWithAnsi(rendered, width)

	b.cacheWidth, b.cacheLines, b.cacheValid = width, lines, true
	return lines
}
