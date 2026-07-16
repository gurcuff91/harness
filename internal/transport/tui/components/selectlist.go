package components

import (
	"strings"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
	"github.com/gurcuff91/harness/internal/transport/tui/keys"
)

const (
	defaultPrimaryColWidth = 32
	primaryColGap          = 2
	minDescriptionWidth    = 10
)

// SelectItem is one entry in a SelectList.
type SelectItem struct {
	Value       string // internal key (e.g. command name or value token)
	Label       string // displayed primary text (falls back to Value)
	Description string // optional right-column hint
	ID          string // hidden internal id (e.g. session id); not displayed
	Flag        bool   // generic per-item flag (e.g. provider is a subscription)
}

// SelectList is an interactive, filterable selection list with keyboard
// navigation. Port of PI's SelectList, styled to match the harness palette
// (selected = primary arrow + text, description dimmed).
type SelectList struct {
	items    []SelectItem
	filtered []SelectItem
	selected int
	maxVis   int

	OnSelect          func(SelectItem)
	OnCancel          func()
	OnSelectionChange func(SelectItem)
}

// NewSelectList creates a list showing up to maxVisible items at once.
func NewSelectList(items []SelectItem, maxVisible int) *SelectList {
	return &SelectList{items: items, filtered: items, maxVis: maxVisible}
}

// SetItems replaces the item set and resets selection/filter.
func (s *SelectList) SetItems(items []SelectItem) {
	s.items = items
	s.filtered = items
	s.selected = 0
}

// SetFilter filters items whose Label/Value contain the (lowercased) filter.
func (s *SelectList) SetFilter(filter string) {
	if filter == "" {
		s.filtered = s.items
		s.selected = 0
		return
	}
	f := strings.ToLower(filter)
	var out []SelectItem
	for _, it := range s.items {
		if strings.Contains(strings.ToLower(it.Label), f) ||
			strings.Contains(strings.ToLower(it.Value), f) ||
			strings.Contains(strings.ToLower(it.Description), f) {
			out = append(out, it)
		}
	}
	s.filtered = out
	s.selected = 0
}

// Selected returns the highlighted item and true, or zero/false if empty.
func (s *SelectList) Selected() (SelectItem, bool) {
	if s.selected < 0 || s.selected >= len(s.filtered) {
		return SelectItem{}, false
	}
	return s.filtered[s.selected], true
}

// Count returns the number of filtered items.
func (s *SelectList) Count() int { return len(s.filtered) }

// HandleInput processes navigation/confirm/cancel keys.
func (s *SelectList) HandleInput(data string) {
	switch {
	case keys.Match(data, keys.Up):
		if len(s.filtered) > 0 {
			if s.selected == 0 {
				s.selected = len(s.filtered) - 1
			} else {
				s.selected--
			}
			s.notifyChange()
		}
	case keys.Match(data, keys.Down):
		if len(s.filtered) > 0 {
			if s.selected == len(s.filtered)-1 {
				s.selected = 0
			} else {
				s.selected++
			}
			s.notifyChange()
		}
	case keys.Match(data, keys.Enter):
		if it, ok := s.Selected(); ok && s.OnSelect != nil {
			s.OnSelect(it)
		}
	case keys.Match(data, keys.Escape), keys.Match(data, keys.CtrlC):
		if s.OnCancel != nil {
			s.OnCancel()
		}
	}
}

// Render produces the visible window of items with a scroll indicator.
func (s *SelectList) Render(width int) []string {
	if len(s.filtered) == 0 {
		return []string{ansi.Dimmed("  No matches")}
	}

	primaryW := s.primaryColumnWidth()
	start := s.selected - s.maxVis/2
	if start < 0 {
		start = 0
	}
	if start > len(s.filtered)-s.maxVis {
		start = len(s.filtered) - s.maxVis
	}
	if start < 0 {
		start = 0
	}
	end := start + s.maxVis
	if end > len(s.filtered) {
		end = len(s.filtered)
	}

	var lines []string
	for i := start; i < end; i++ {
		lines = append(lines, s.renderItem(s.filtered[i], i == s.selected, width, primaryW))
	}
	if start > 0 || end < len(s.filtered) {
		info := "  (" + itoa(s.selected+1) + "/" + itoa(len(s.filtered)) + ")"
		lines = append(lines, ansi.Dimmed(ansi.TruncateToWidth(info, width-2, "", false)))
	}
	return lines
}

func (s *SelectList) renderItem(item SelectItem, selected bool, width, primaryW int) string {
	prefix := "  "
	if selected {
		prefix = "→ "
	}
	prefixW := 2
	display := item.Label
	if display == "" {
		display = item.Value
	}
	desc := normalizeSingleLine(item.Description)

	if desc != "" && width > 40 {
		effW := min(primaryW, width-prefixW-4)
		if effW < 1 {
			effW = 1
		}
		maxPrimary := max(1, effW-primaryColGap)
		val := ansi.TruncateToWidth(display, maxPrimary, "", false)
		valW := ansi.VisibleWidth(val)
		spacing := strings.Repeat(" ", max(1, effW-valW))
		descStart := prefixW + valW + len(spacing)
		remaining := width - descStart - 2
		if remaining > minDescriptionWidth {
			td := ansi.TruncateToWidth(desc, remaining, "", false)
			if selected {
				return ansi.Primary(prefix + val + spacing + td)
			}
			return prefix + val + spacing + ansi.Dimmed(td)
		}
	}

	maxW := width - prefixW - 2
	val := ansi.TruncateToWidth(display, maxW, "", false)
	if selected {
		return ansi.Primary(prefix + val)
	}
	return prefix + val
}

func (s *SelectList) primaryColumnWidth() int {
	widest := 0
	for _, it := range s.filtered {
		d := it.Label
		if d == "" {
			d = it.Value
		}
		if w := ansi.VisibleWidth(d) + primaryColGap; w > widest {
			widest = w
		}
	}
	if widest > defaultPrimaryColWidth {
		return defaultPrimaryColWidth
	}
	if widest < 1 {
		return 1
	}
	return widest
}

func (s *SelectList) notifyChange() {
	if s.OnSelectionChange != nil {
		if it, ok := s.Selected(); ok {
			s.OnSelectionChange(it)
		}
	}
}

func normalizeSingleLine(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.TrimSpace(text)
}
