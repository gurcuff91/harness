package components

import "github.com/gurcuff91/harness/transport/tui-v3/ansi"

// TruncatedText is a single-line component that truncates to fit the viewport
// width. Useful for status lines, headers, and footers. Port of PI's
// TruncatedText.
type TruncatedText struct {
	text     string
	paddingX int
}

// NewTruncatedText creates a single-line truncating text component.
func NewTruncatedText(text string, paddingX int) *TruncatedText {
	return &TruncatedText{text: text, paddingX: paddingX}
}

// SetText updates the content.
func (t *TruncatedText) SetText(text string) { t.text = text }

// Render returns exactly one line, truncated (with ellipsis) to width.
func (t *TruncatedText) Render(width int) []string {
	if t.text == "" {
		return []string{""}
	}
	avail := width - t.paddingX*2
	if avail < 1 {
		avail = 1
	}
	line := ansi.TruncateToWidth(t.text, avail, "…", false)
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
