package components

import (
	"sync"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
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
	paddingX int
}

// NewTruncatedText creates a single-line truncating text component.
func NewTruncatedText(text string, paddingX int) *TruncatedText {
	return &TruncatedText{text: text, paddingX: paddingX}
}

// SetText updates the content.
func (t *TruncatedText) SetText(text string) {
	t.mu.Lock()
	t.text = text
	t.mu.Unlock()
}

// Render returns exactly one line, truncated (with ellipsis) to width.
func (t *TruncatedText) Render(width int) []string {
	t.mu.Lock()
	text := t.text
	t.mu.Unlock()

	if text == "" {
		return []string{""}
	}
	avail := width - t.paddingX*2
	if avail < 1 {
		avail = 1
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
