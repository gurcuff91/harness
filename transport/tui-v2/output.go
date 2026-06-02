package tuiv2

import "strings"

// Output is the scrollable conversation output area.
type Output struct {
	lines  []string
	width  int
	indent string // continuation indent (e.g. "   ")
}

func NewOutput() *Output {
	return &Output{lines: make([]string, 0, 256)}
}

// SetWrap enables word-wrap at the given width with the given continuation indent.
func (o *Output) SetWrap(width int, indent string) {
	o.width = width
	o.indent = indent
}

// Add appends a line (already ANSI-styled, no wrapping).
func (o *Output) Add(line string) {
	o.lines = append(o.lines, line)
}

// AddWrapped appends a line with word-wrap at the configured width using a custom indent.
func (o *Output) AddWrapped(line string, indent string) {
	if o.width <= 0 || visibleLen(line) <= o.width {
		o.lines = append(o.lines, line)
		return
	}
	// First line is the full content, then wrap remainder
	o.lines = append(o.lines, line)
	// Let AddStream handle the wrapping by replacing indent temporarily
	// Actually, split manually respecting word boundaries
	last := &o.lines[len(o.lines)-1]
	for visibleLen(*last) > o.width {
		// Find a break point
		raw := *last
		cut := o.findBreak(raw, o.width)
		if cut <= 0 {
			break
		}
		*last = raw[:cut]
		o.lines = append(o.lines, indent+raw[cut:])
		last = &o.lines[len(o.lines)-1]
	}
}

func (o *Output) findBreak(s string, maxVis int) int {
	vis := 0
	esc := false
	lastSpace := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			esc = true
			continue
		}
		if esc {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				esc = false
			}
			continue
		}
		vis++
		if s[i] == ' ' {
			lastSpace = i + 1
		}
		if vis >= maxVis {
			if lastSpace > 0 {
				return lastSpace
			}
			return i + 1
		}
	}
	return -1
}

// AddStream appends text with word-wrap at the configured width.
func (o *Output) AddStream(text string) {
	if o.width <= 0 {
		if len(o.lines) == 0 {
			o.lines = append(o.lines, text)
		} else {
			o.lines[len(o.lines)-1] += text
		}
		return
	}
	for len(text) > 0 {
		if len(o.lines) == 0 {
			o.lines = append(o.lines, "")
		}
		last := &o.lines[len(o.lines)-1]
		curLen := visibleLen(*last)

		// Line already at width — need to wrap
		if curLen >= o.width {
			// Look for last space in current line to re-break cleanly
			raw := *last
			if idx := lastVisibleSpace(raw); idx > len(o.indent) {
				// Move text after last space to new line
				*last = raw[:idx]
				o.lines = append(o.lines, o.indent+raw[idx+1:])
			} else {
				o.lines = append(o.lines, o.indent)
			}
			continue
		}

		remaining := o.width - curLen
		take := len(text)
		if take > remaining {
			take = remaining
		}
		*last += text[:take]
		text = text[take:]
	}
}

// lastVisibleSpace finds the byte index of the last space in s,
// ignoring spaces inside ANSI escape sequences.
func lastVisibleSpace(s string) int {
	last := -1
	esc := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			esc = true
			continue
		}
		if esc {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				esc = false
			}
			continue
		}
		if s[i] == ' ' {
			last = i
		}
	}
	return last
}

func visibleLen(s string) int {
	n := 0
	esc := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			esc = true
			continue
		}
		if esc {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				esc = false
			}
			continue
		}
		n++
	}
	return n
}

func (o *Output) Lines() []string { return o.lines }
func (o *Output) Clear()          { o.lines = o.lines[:0] }
func (o *Output) Render() string  { return strings.Join(o.lines, "\n") }
