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

// AddStream appends text with word-wrap at the configured width.
func (o *Output) AddStream(text string) {
	if o.width <= 0 {
		// No wrapping configured — just append
		if len(o.lines) == 0 {
			o.lines = append(o.lines, text)
		} else {
			o.lines[len(o.lines)-1] += text
		}
		return
	}
	// Word-wrap at configured width
	for len(text) > 0 {
		if len(o.lines) == 0 {
			o.lines = append(o.lines, "")
		}
		last := &o.lines[len(o.lines)-1]
		curLen := visibleLen(*last)
		if curLen >= o.width {
			o.lines = append(o.lines, o.indent)
			last = &o.lines[len(o.lines)-1]
			curLen = visibleLen(*last)
		}
		remaining := o.width - curLen
		if remaining <= 0 {
			o.lines = append(o.lines, o.indent+text)
			return
		}
		take := len(text)
		if take > remaining {
			take = remaining
			// Don't break mid-word — find last space within remaining
			if idx := strings.LastIndexByte(text[:take], ' '); idx > 0 {
				take = idx + 1
			}
			// If no space found, break at remaining (word too long)
		}
		*last += text[:take]
		text = text[take:]
	}
}

func visibleLen(s string) int {
	n := 0
	esc := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' { esc = true; continue }
		if esc {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') { esc = false }
			continue
		}
		n++
	}
	return n
}

func (o *Output) Lines() []string { return o.lines }
func (o *Output) Clear()          { o.lines = o.lines[:0] }
func (o *Output) Render() string  { return strings.Join(o.lines, "\n") }
