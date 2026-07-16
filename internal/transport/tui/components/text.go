// Package components provides the built-in widgets for tui.
//
// Each widget implements render.Component (and optionally render.InputHandler /
// render.Invalidator). Widgets are pure-Go ports of the pi-tui components,
// styled to match the harness's existing tview-based TUI (transport/tui).
package components

import (
	"strings"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
)

// Text displays multi-line text with word wrapping and optional padding.
// Port of PI's Text component. Output is cached until text or width changes.
type Text struct {
	text     string
	paddingX int
	paddingY int

	cacheText  string
	cacheWidth int
	cacheLines []string
	cacheValid bool
}

// NewText creates a Text component. paddingX/paddingY default to 1 in PI; pass
// 0 for flush-left, no vertical padding.
func NewText(text string, paddingX, paddingY int) *Text {
	return &Text{text: text, paddingX: paddingX, paddingY: paddingY}
}

// SetText updates the content and invalidates the cache.
func (t *Text) SetText(text string) {
	t.text = text
	t.cacheValid = false
}

// Invalidate clears the render cache.
func (t *Text) Invalidate() { t.cacheValid = false }

// Render wraps the text to width (minus padding) and pads each line to width.
func (t *Text) Render(width int) []string {
	if t.cacheValid && t.cacheText == t.text && t.cacheWidth == width {
		return t.cacheLines
	}

	if strings.TrimSpace(t.text) == "" {
		t.cacheText, t.cacheWidth, t.cacheLines, t.cacheValid = t.text, width, nil, true
		return nil
	}

	normalized := strings.ReplaceAll(t.text, "\t", "   ")
	contentWidth := max(1, width-t.paddingX*2)
	wrapped := ansi.WrapTextWithAnsi(normalized, contentWidth)

	margin := strings.Repeat(" ", t.paddingX)
	var content []string
	for _, line := range wrapped {
		withMargins := margin + line + margin
		content = append(content, padTo(withMargins, width))
	}

	var result []string
	empty := strings.Repeat(" ", width)
	for i := 0; i < t.paddingY; i++ {
		result = append(result, empty)
	}
	result = append(result, content...)
	for i := 0; i < t.paddingY; i++ {
		result = append(result, empty)
	}

	t.cacheText, t.cacheWidth, t.cacheLines, t.cacheValid = t.text, width, result, true
	return result
}

// padTo right-pads a line with spaces to exactly width visible columns.
func padTo(line string, width int) string {
	vw := ansi.VisibleWidth(line)
	if vw >= width {
		return line
	}
	return line + strings.Repeat(" ", width-vw)
}
