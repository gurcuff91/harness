package ansi

import (
	"strconv"
	"strings"
)

// codeTracker tracks active SGR styling so it can be re-applied at the start of
// wrapped lines and selectively closed at line ends. Port of PI's
// AnsiCodeTracker. Hyperlink (OSC 8) tracking is intentionally omitted — the
// harness output does not emit OSC 8 links.
type codeTracker struct {
	bold, dim, italic, underline       bool
	blink, inverse, hidden, strike     bool
	fgColor, bgColor                   string // full code, e.g. "31" or "38;5;240"
}

func (t *codeTracker) reset() {
	t.bold, t.dim, t.italic, t.underline = false, false, false, false
	t.blink, t.inverse, t.hidden, t.strike = false, false, false, false
	t.fgColor, t.bgColor = "", ""
}

// process updates tracker state from a single ANSI code (only SGR "m" matters).
func (t *codeTracker) process(code string) {
	if !strings.HasSuffix(code, "m") {
		return
	}
	// Extract params between ESC[ and m.
	if !strings.HasPrefix(code, "\x1b[") {
		return
	}
	params := code[2 : len(code)-1]
	if params == "" || params == "0" {
		t.reset()
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		// 256-color / RGB consume multiple params.
		if n == 38 || n == 48 {
			if i+2 < len(parts) && parts[i+1] == "5" {
				cc := parts[i] + ";" + parts[i+1] + ";" + parts[i+2]
				if n == 38 {
					t.fgColor = cc
				} else {
					t.bgColor = cc
				}
				i += 2
				continue
			}
			if i+4 < len(parts) && parts[i+1] == "2" {
				cc := parts[i] + ";" + parts[i+1] + ";" + parts[i+2] + ";" + parts[i+3] + ";" + parts[i+4]
				if n == 38 {
					t.fgColor = cc
				} else {
					t.bgColor = cc
				}
				i += 4
				continue
			}
		}
		switch n {
		case 0:
			t.reset()
		case 1:
			t.bold = true
		case 2:
			t.dim = true
		case 3:
			t.italic = true
		case 4:
			t.underline = true
		case 5:
			t.blink = true
		case 7:
			t.inverse = true
		case 8:
			t.hidden = true
		case 9:
			t.strike = true
		case 21, 22:
			t.bold = false
			if n == 22 {
				t.dim = false
			}
		case 23:
			t.italic = false
		case 24:
			t.underline = false
		case 25:
			t.blink = false
		case 27:
			t.inverse = false
		case 28:
			t.hidden = false
		case 29:
			t.strike = false
		case 39:
			t.fgColor = ""
		case 49:
			t.bgColor = ""
		default:
			if (n >= 30 && n <= 37) || (n >= 90 && n <= 97) {
				t.fgColor = strconv.Itoa(n)
			} else if (n >= 40 && n <= 47) || (n >= 100 && n <= 107) {
				t.bgColor = strconv.Itoa(n)
			}
		}
	}
}

// activeCodes returns the SGR sequence that re-applies the current state, or "".
func (t *codeTracker) activeCodes() string {
	var codes []string
	if t.bold {
		codes = append(codes, "1")
	}
	if t.dim {
		codes = append(codes, "2")
	}
	if t.italic {
		codes = append(codes, "3")
	}
	if t.underline {
		codes = append(codes, "4")
	}
	if t.blink {
		codes = append(codes, "5")
	}
	if t.inverse {
		codes = append(codes, "7")
	}
	if t.hidden {
		codes = append(codes, "8")
	}
	if t.strike {
		codes = append(codes, "9")
	}
	if t.fgColor != "" {
		codes = append(codes, t.fgColor)
	}
	if t.bgColor != "" {
		codes = append(codes, t.bgColor)
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

// lineEndReset returns codes that must be closed at line end to avoid bleeding
// into padding (underline only — background is preserved intentionally).
func (t *codeTracker) lineEndReset() string {
	if t.underline {
		return "\x1b[24m"
	}
	return ""
}

// feed advances the tracker over all ANSI codes embedded in text.
func (t *codeTracker) feed(text string) {
	i := 0
	for i < len(text) {
		if code, length := ExtractAnsiCode(text, i); length > 0 {
			t.process(code)
			i += length
			continue
		}
		i++
	}
}
