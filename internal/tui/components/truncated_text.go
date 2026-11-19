package components

import (
	"sync"

	"github.com/gurcuff91/harness/internal/tui/ansi"
)

// TruncatedText is a single-line component that truncates to fit the viewport
// width. Useful for status lines, headers, and footers. Port of PI's
// TruncatedText.
//
// Guarded by mu like RawBlock/History: SetText is called from the SSE
// event-consumer goroutine (e.g. TUI.updateInfo, on every turn/loop/tokens
// event) while Render runs from the render-scheduler goroutine — without a
// lock those race on t.text.
type TruncatedText struct {
	mu       sync.Mutex
	text     string
	short    string // shown instead of text when text does not fit ("" = none)
	paddingX int
	// collapse: with no text, render NO line at all (instead of an empty
	// one), so an optional status line takes no room while it has nothing.
	collapse bool
}

// NewCollapsibleText is a TruncatedText that renders no line at all while its
// text is empty.
func NewCollapsibleText(paddingX int) *TruncatedText {
	return &TruncatedText{paddingX: paddingX, collapse: true}
}

// NewTruncatedText creates a single-line truncating text component.
func NewTruncatedText(text string, paddingX int) *TruncatedText {
	return &TruncatedText{text: text, paddingX: paddingX}
}

// SetText updates the content (and drops any shorter alternative).
func (t *TruncatedText) SetText(text string) { t.SetTexts(text, "") }

// SetTexts sets the content and a shorter alternative drawn instead when the
// content does not fit the width (the alternative is then truncated as
// usual). An empty short means none.
func (t *TruncatedText) SetTexts(text, short string) {
	t.mu.Lock()
	t.text, t.short = text, short
	t.mu.Unlock()
}

// Render returns exactly one line, truncated (with ellipsis) to width.
func (t *TruncatedText) Render(width int) []string {
	t.mu.Lock()
	text, short := t.text, t.short
	t.mu.Unlock()

	if text == "" {
		if t.collapse {
			return nil
		}
		return []string{""}
	}
	avail := width - t.paddingX*2
	if avail < 1 {
		avail = 1
	}
	if short != "" && ansi.VisibleWidth(text) > avail {
		text = short
	}
	line := ansi.TruncateToWidth(text, avail, "…", false)
	if t.paddingX > 0 {
		pad := spaces(t.paddingX)
		line = pad + line + pad
	}
	return []string{line}
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
