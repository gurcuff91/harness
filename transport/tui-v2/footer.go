package tuiv2

import (
	"fmt"
	"strings"
)

// Footer renders the compact stats line (tokens, cost, model).
type Footer struct {
	text string // built externally and set here
}

func NewFooter() *Footer {
	return &Footer{}
}

func (f *Footer) Set(text string) {
	f.text = text
}

func (f *Footer) Render(width int) []string {
	if f.text == "" {
		return nil
	}
	display := f.text
	if len(display) > width-2 {
		display = display[:width-2]
	}
	return []string{" \033[90m" + display + "\033[0m"}
}

// Helpers for building the footer text (used by the harness TUI).
func BuildFooter(input, output, cacheR, cacheW int, cost float64, contextPct float64, contextWindow int, modelName string) string {
	var parts []string
	if input > 0 {
		parts = append(parts, "↑"+compactNum(input))
	}
	if output > 0 {
		parts = append(parts, "↓"+compactNum(output))
	}
	if cacheR > 0 {
		parts = append(parts, "R"+compactNum(cacheR))
	}
	if cacheW > 0 {
		parts = append(parts, "W"+compactNum(cacheW))
	}
	if cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.3f", cost))
	}
	if contextPct > 0 && contextWindow > 0 {
		parts = append(parts, fmt.Sprintf("%.1f%%/%s", contextPct*100, compactNum(contextWindow)))
	}
	if modelName != "" {
		parts = append(parts, modelName)
	}
	return strings.Join(parts, " ")
}

func compactNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
