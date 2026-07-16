package render

import (
	"strings"

	"github.com/gurcuff91/harness/transport/tui/ansi"
)

// doRender is the differential renderer — a faithful 1:1 port of PI's doRender
// (scoped to the harness: no Kitty images, no overlays, no IME cursor).
//
// The key to correctness is tracking two row positions precisely:
//   - cursorRow: end-of-content row (drives viewport math)
//   - hardwareCursorRow: where the terminal cursor physically is
//
// and converting between content rows and SCREEN rows via the viewport top.
// Cursor movement sequences (CSI A/B) operate in screen rows, so every move is
// computed as (targetScreenRow - currentScreenRow). Collapsing this conversion
// (as an earlier version did) corrupts positioning once the viewport scrolls,
// leaving stale duplicate lines in scrollback.
func (t *TUI) doRender() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}

	width := t.terminal.Columns()
	height := t.terminal.Rows()

	newLines := t.Render(width)
	newLines = sanitizeLines(newLines, width)

	widthChanged := t.previousWidth != 0 && t.previousWidth != width
	heightChanged := t.previousHeight != 0 && t.previousHeight != height

	previousBufferLength := height
	if t.previousHeight > 0 {
		previousBufferLength = t.previousViewportTop + t.previousHeight
	}
	prevViewportTop := t.previousViewportTop
	if heightChanged {
		prevViewportTop = max(0, previousBufferLength-height)
	}
	viewportTop := prevViewportTop
	hardwareCursorRow := t.hardwareCursorRow

	// computeLineDiff converts a target CONTENT row into the number of screen
	// rows to move from the current hardware cursor position.
	computeLineDiff := func(targetRow int) int {
		currentScreenRow := hardwareCursorRow - prevViewportTop
		targetScreenRow := targetRow - viewportTop
		return targetScreenRow - currentScreenRow
	}

	// ── Strategy 1: first render (assume clean screen) ──────────────────
	if len(t.previousLines) == 0 && !widthChanged && !heightChanged {
		t.fullRender(newLines, width, height, clearNone)
		return
	}

	// ── Strategy 2: width/height change → full ABSOLUTE redraw ──────────
	// On resize, line wrapping and viewport geometry both change, so relative
	// cursor math (used for scrollback-safe shrink) is unreliable. Clear the
	// whole screen + scrollback and repaint from the top, exactly like PI. This
	// is what prevents the "resize leaves a growing scroll of stale frames".
	if widthChanged || heightChanged {
		t.fullRender(newLines, width, height, clearAbsolute)
		return
	}

	// Content shrank below the working area → clear stale rows. Here a relative
	// clear is safe (geometry unchanged) and preserves the shell's scrollback.
	if len(newLines) < t.maxLinesRendered {
		t.fullRender(newLines, width, height, clearRelative)
		return
	}

	// ── Strategy 3: normal update ───────────────────────────────────────
	firstChanged, lastChanged := diffRange(t.previousLines, newLines)

	appended := len(newLines) > len(t.previousLines)
	if appended {
		if firstChanged == -1 {
			firstChanged = len(t.previousLines)
		}
		lastChanged = len(newLines) - 1
	}

	appendStart := appended && firstChanged == len(t.previousLines) && firstChanged > 0

	// No changes — nothing to do.
	if firstChanged == -1 {
		t.previousViewportTop = prevViewportTop
		t.previousHeight = height
		return
	}

	// Mixed change: content grew AND a line strictly before the old end changed
	// (not a clean append). This happens when a streaming markdown block flushes
	// a buffered table — a previously-blank separator line becomes the table's
	// top border while new rows are appended below. The incremental cursor math
	// can't reposition cleanly here, so repaint from the first change with a
	// relative (scrollback-safe) clear, which is always correct.
	if appended && firstChanged < len(t.previousLines) {
		t.fullRender(newLines, width, height, clearRelative)
		return
	}

	// All changes are in deleted lines (new content is shorter).
	if firstChanged >= len(newLines) {
		t.renderDeletedTail(newLines, height, firstChanged, prevViewportTop, hardwareCursorRow, viewportTop)
		return
	}

	// First changed line is above the previous viewport → full redraw
	// (we cannot touch lines that scrolled into terminal scrollback).
	if firstChanged < prevViewportTop {
		t.fullRender(newLines, width, height, clearRelative)
		return
	}

	var buf strings.Builder
	buf.WriteString(ansi.SyncBegin)

	prevViewportBottom := prevViewportTop + height - 1
	moveTargetRow := firstChanged
	if appendStart {
		moveTargetRow = firstChanged - 1
	}

	// If the target is below the visible bottom, drop to the last screen row
	// and scroll new lines in with \r\n, advancing the viewport.
	if moveTargetRow > prevViewportBottom {
		currentScreenRow := clamp(hardwareCursorRow-prevViewportTop, 0, height-1)
		if moveToBottom := height - 1 - currentScreenRow; moveToBottom > 0 {
			buf.WriteString(ansi.MoveDown(moveToBottom))
		}
		scroll := moveTargetRow - prevViewportBottom
		buf.WriteString(strings.Repeat(ansi.CRLF, scroll))
		prevViewportTop += scroll
		viewportTop += scroll
		hardwareCursorRow = moveTargetRow
	}

	// Move the cursor to the first changed line (screen-row aware).
	lineDiff := computeLineDiff(moveTargetRow)
	if lineDiff > 0 {
		buf.WriteString(ansi.MoveDown(lineDiff))
	} else if lineDiff < 0 {
		buf.WriteString(ansi.MoveUp(-lineDiff))
	}
	if appendStart {
		buf.WriteString(ansi.CRLF)
	} else {
		buf.WriteString(ansi.CR)
	}

	// Rewrite only the changed range.
	renderEnd := min(lastChanged, len(newLines)-1)
	for i := firstChanged; i <= renderEnd; i++ {
		if i > firstChanged {
			buf.WriteString(ansi.CRLF)
		}
		buf.WriteString(ansi.ClearLine)
		buf.WriteString(newLines[i])
	}

	finalCursorRow := renderEnd

	// Clear trailing lines that were removed (content got shorter mid-buffer).
	if len(t.previousLines) > len(newLines) {
		if renderEnd < len(newLines)-1 {
			down := len(newLines) - 1 - renderEnd
			buf.WriteString(ansi.MoveDown(down))
			finalCursorRow = len(newLines) - 1
		}
		extra := len(t.previousLines) - len(newLines)
		for i := 0; i < extra; i++ {
			buf.WriteString(ansi.CRLF)
			buf.WriteString(ansi.ClearLine)
		}
		buf.WriteString(ansi.MoveUp(extra))
	}

	buf.WriteString(ansi.SyncEnd)
	t.terminal.Write(buf.String())

	t.cursorRow = max(0, len(newLines)-1)
	t.hardwareCursorRow = finalCursorRow
	t.maxLinesRendered = max(t.maxLinesRendered, len(newLines))
	t.previousViewportTop = max(prevViewportTop, finalCursorRow-height+1)
	t.previousLines = newLines
	t.previousWidth = width
	t.previousHeight = height
}

// clearMode selects how fullRender wipes the screen before repainting.
type clearMode int

const (
	clearNone     clearMode = iota // first render — assume a clean screen
	clearRelative                  // move up + \x1b[J — scrollback-safe, geometry unchanged
	clearAbsolute                  // \x1b[2J\x1b[H\x1b[3J — resize: full reset like PI
)

// fullRender writes all lines, clearing per the given mode.
//
// Clearing strategy matters because this TUI is INLINE (no alternate screen):
// the shell prompt and prior output live in the scrollback ABOVE our region.
//
//   - clearRelative: move up to our block's first row and clear to end-of-screen
//     (\x1b[J never touches scrollback). Used for shrink when geometry is stable.
//   - clearAbsolute: \x1b[2J\x1b[H\x1b[3J — full screen + scrollback reset, homing
//     to (0,0). Used on RESIZE, where wrapping and viewport geometry change and
//     relative cursor math would desync (leaving stale, scrolling frames).
//     This matches PI's resize behavior.
func (t *TUI) fullRender(newLines []string, width, height int, mode clearMode) {
	var buf strings.Builder
	buf.WriteString(ansi.SyncBegin)
	switch mode {
	case clearRelative:
		if t.previousViewportTop == 0 && t.hardwareCursorRow >= 0 {
			if t.hardwareCursorRow > 0 {
				buf.WriteString(ansi.MoveUp(t.hardwareCursorRow))
			}
			buf.WriteString(ansi.CR)
			buf.WriteString(ansi.ClearFromCursor) // \x1b[J — preserves scrollback
		} else {
			buf.WriteString(ansi.FullClear)
		}
	case clearAbsolute:
		buf.WriteString(ansi.FullClear) // \x1b[2J\x1b[H\x1b[3J
	}
	for i, line := range newLines {
		if i > 0 {
			buf.WriteString(ansi.CRLF)
		}
		buf.WriteString(line)
	}
	buf.WriteString(ansi.SyncEnd)
	t.terminal.Write(buf.String())

	t.cursorRow = max(0, len(newLines)-1)
	t.hardwareCursorRow = t.cursorRow
	if mode != clearNone {
		t.maxLinesRendered = len(newLines)
	} else {
		t.maxLinesRendered = max(t.maxLinesRendered, len(newLines))
	}
	bufLen := max(height, len(newLines))
	t.previousViewportTop = max(0, bufLen-height)
	t.previousLines = newLines
	t.previousWidth = width
	t.previousHeight = height
}

// renderDeletedTail handles the case where all changed lines are deletions:
// move to the end of the new content and clear the extra rows below it.
func (t *TUI) renderDeletedTail(newLines []string, height, firstChanged, prevViewportTop, hardwareCursorRow, viewportTop int) {
	if len(t.previousLines) <= len(newLines) {
		t.previousLines = newLines
		return
	}
	var buf strings.Builder
	buf.WriteString(ansi.SyncBegin)

	targetRow := max(0, len(newLines)-1)
	// Screen-row aware move.
	currentScreenRow := hardwareCursorRow - prevViewportTop
	targetScreenRow := targetRow - viewportTop
	lineDiff := targetScreenRow - currentScreenRow
	if lineDiff > 0 {
		buf.WriteString(ansi.MoveDown(lineDiff))
	} else if lineDiff < 0 {
		buf.WriteString(ansi.MoveUp(-lineDiff))
	}
	buf.WriteString(ansi.CR)

	extra := len(t.previousLines) - len(newLines)
	clearStart := 1
	if len(newLines) == 0 {
		clearStart = 0
	}
	if extra > 0 && clearStart > 0 {
		buf.WriteString(ansi.MoveDown(clearStart))
	}
	for i := 0; i < extra; i++ {
		buf.WriteString(ansi.CR)
		buf.WriteString(ansi.ClearLine)
		if i < extra-1 {
			buf.WriteString(ansi.MoveDown(1))
		}
	}
	if moveBack := extra - 1 + clearStart; moveBack > 0 {
		buf.WriteString(ansi.MoveUp(moveBack))
	}
	buf.WriteString(ansi.SyncEnd)
	t.terminal.Write(buf.String())

	t.cursorRow = targetRow
	t.hardwareCursorRow = targetRow
	t.previousLines = newLines
	t.previousViewportTop = prevViewportTop
}

// diffRange returns the first and last line indices that differ. Returns
// (-1, -1) when identical up to the shorter length.
func diffRange(old, new []string) (first, last int) {
	first, last = -1, -1
	n := max(len(old), len(new))
	for i := 0; i < n; i++ {
		var o, nw string
		if i < len(old) {
			o = old[i]
		}
		if i < len(new) {
			nw = new[i]
		}
		if o != nw {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	return first, last
}

// sanitizeLines clips any line wider than width to protect the diff state.
func sanitizeLines(lines []string, width int) []string {
	for i, line := range lines {
		if ansi.VisibleWidth(line) > width {
			lines[i] = ansi.TruncateToWidth(line, width, "", false)
		}
	}
	return lines
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
