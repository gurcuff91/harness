package components

import (
	"strings"

	"github.com/gurcuff91/harness/transport/tui-v3/ansi"
)

// Markdown is a render.Component that holds RAW markdown source and renders it
// to styled ANSI lines at the CURRENT terminal width. Because it keeps the
// source (not the rendered output), it re-lays-out correctly on resize —
// tables in particular recompute their column widths to the new width.
//
// This mirrors PI's Markdown component: the chat keeps a tree of source-backed
// blocks, so a width change just re-renders every block.
type Markdown struct {
	source string

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
	b.source = source
	b.cacheValid = false
}

// Source returns the raw markdown.
func (b *Markdown) Source() string { return b.source }

// Append adds a delta to the raw markdown.
func (b *Markdown) Append(delta string) {
	b.source += delta
	b.cacheValid = false
}

// Invalidate clears the render cache (called on resize).
func (b *Markdown) Invalidate() { b.cacheValid = false }

// Render lays the raw markdown out at the given width, caching by width.
func (b *Markdown) Render(width int) []string {
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

	// Split into physical lines. The differential renderer requires each line
	// to fit the width; long unwrapped lines are wrapped here as a safety net.
	var lines []string
	for _, line := range strings.Split(rendered, "\n") {
		if ansi.VisibleWidth(line) <= width {
			lines = append(lines, line)
		} else {
			lines = append(lines, ansi.WrapTextWithAnsi(line, width)...)
		}
	}

	b.cacheWidth, b.cacheLines, b.cacheValid = width, lines, true
	return lines
}
