package components

import (
	"strings"
	"sync"

	"github.com/gurcuff91/harness/internal/tui/ansi"
	"github.com/gurcuff91/harness/internal/tui/keys"
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
//
// It is safe for concurrent use, because it IS used from two goroutines: the input goroutine
// (typing a filter calls SetFilter/SetItems; Up/Down/Enter call HandleInput) and the render timer's
// goroutine (Render). Without a lock, Render read len(s.filtered) to bound its loop and then indexed
// s.filtered[i], so a SetFilter in between — filtering "/thinking" shrinks the list from dozens of
// entries to two — ran i past the new, shorter slice and crashed the whole TUI with
// "index out of range [5] with length 2". Three rules keep it correct:
//
//   - items, filtered and selected are guarded by mu;
//   - Render takes ONE snapshot under the lock and draws outside it, so the length it checks and the
//     entries it indexes can never come from two different slices;
//   - no callback runs while mu is held: OnSelect, OnCancel and OnSelectionChange re-enter the list
//     (the palette's onEnter calls SetItems; notifying a change calls Selected), and a mutex held
//     across them would deadlock.
type SelectList struct {
	mu       sync.Mutex
	items    []SelectItem
	filtered []SelectItem
	selected int
	maxVis   int

	// The callbacks are set once, before the list is shared, and read-only afterwards.
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
	s.filtered = items
	s.selected = 0
}

// SetFilter filters items whose Label/Value contain the (lowercased) filter.
func (s *SelectList) SetFilter(filter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedLocked()
}

// selectedLocked is Selected for a caller that already holds mu.
func (s *SelectList) selectedLocked() (SelectItem, bool) {
	if s.selected < 0 || s.selected >= len(s.filtered) {
		return SelectItem{}, false
	}
	return s.filtered[s.selected], true
}

// Count returns the number of filtered items.
func (s *SelectList) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.filtered)
}

// HandleInput processes navigation/confirm/cancel keys. The selection changes under the lock; the
// callbacks run only after it is released, because they re-enter the list (see SelectList).
func (s *SelectList) HandleInput(data string) {
	switch {
	case keys.Match(data, keys.Up):
		s.move(-1)
	case keys.Match(data, keys.Down):
		s.move(+1)
	case keys.Match(data, keys.Enter):
		if it, ok := s.Selected(); ok && s.OnSelect != nil {
			s.OnSelect(it) // lock released: onEnter calls SetItems
		}
	case keys.Match(data, keys.Escape), keys.Match(data, keys.CtrlC):
		if s.OnCancel != nil {
			s.OnCancel()
		}
	}
}

// move steps the selection by delta (-1 up, +1 down), wrapping at both ends, and notifies the
// change with the lock released. An empty list does nothing and notifies nothing.
func (s *SelectList) move(delta int) {
	s.mu.Lock()
	n := len(s.filtered)
	if n == 0 {
		s.mu.Unlock()
		return
	}
	s.selected = ((s.selected+delta)%n + n) % n
	it, ok := s.selectedLocked()
	s.mu.Unlock()
	if ok && s.OnSelectionChange != nil {
		s.OnSelectionChange(it)
	}
}

// Render produces the visible window of items with a scroll indicator.
//
// It takes one snapshot of the state under the lock and draws from that copy, outside it: the length
// that bounds the loop and the entries the loop indexes always belong to the same slice, whatever
// the input goroutine does meanwhile (it only ever REPLACES s.filtered, never edits it in place, so
// holding the slice header is a consistent view).
func (s *SelectList) Render(width int) []string {
	s.mu.Lock()
	filtered, selected := s.filtered, s.selected
	s.mu.Unlock()

	if len(filtered) == 0 {
		return []string{ansi.Dimmed("  No matches")}
	}

	primaryW := primaryColumnWidth(filtered)
	start := selected - s.maxVis/2
	if start < 0 {
		start = 0
	}
	if start > len(filtered)-s.maxVis {
		start = len(filtered) - s.maxVis
	}
	if start < 0 {
		start = 0
	}
	end := start + s.maxVis
	if end > len(filtered) {
		end = len(filtered)
	}

	var lines []string
	for i := start; i < end; i++ {
		lines = append(lines, s.renderItem(filtered[i], i == selected, width, primaryW))
	}
	if start > 0 || end < len(filtered) {
		info := "  (" + itoa(selected+1) + "/" + itoa(len(filtered)) + ")"
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

// primaryColumnWidth is the width of the label column for the snapshot it is given — never for
// s.filtered, which another goroutine may be replacing.
func primaryColumnWidth(filtered []SelectItem) int {
	widest := 0
	for _, it := range filtered {
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

func normalizeSingleLine(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.TrimSpace(text)
}
