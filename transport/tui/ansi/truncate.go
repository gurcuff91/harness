package ansi

import "strings"

// TruncateToWidth truncates text to maxWidth visible columns, preserving ANSI
// codes and closing them at the cut point. When the text fits, it is returned
// unchanged (optionally padded). Port of PI's truncateToWidth.
//
// ellipsis is appended when truncation occurs (default "..."). pad right-fills
// with spaces to exactly maxWidth.
func TruncateToWidth(text string, maxWidth int, ellipsis string, pad bool) string {
	if maxWidth <= 0 {
		return ""
	}
	if len(text) == 0 {
		if pad {
			return strings.Repeat(" ", maxWidth)
		}
		return ""
	}

	ellipsisWidth := VisibleWidth(ellipsis)

	// Ellipsis alone doesn't fit — degrade gracefully.
	if ellipsisWidth >= maxWidth {
		tw := VisibleWidth(text)
		if tw <= maxWidth {
			if pad {
				return text + strings.Repeat(" ", maxWidth-tw)
			}
			return text
		}
		clip, clipW := truncateFragment(ellipsis, maxWidth)
		if clipW == 0 {
			if pad {
				return strings.Repeat(" ", maxWidth)
			}
			return ""
		}
		return finalize("", 0, clip, clipW, maxWidth, pad)
	}

	targetWidth := maxWidth - ellipsisWidth

	// Fast path: pure ASCII.
	if isPrintableASCII(text) {
		if len(text) <= maxWidth {
			if pad {
				return text + strings.Repeat(" ", maxWidth-len(text))
			}
			return text
		}
		return finalize(text[:targetWidth], targetWidth, ellipsis, ellipsisWidth, maxWidth, pad)
	}

	var result strings.Builder
	var pendingAnsi string
	visibleSoFar := 0
	keptWidth := 0
	keepPrefix := true
	overflowed := false

	hasAnsi := strings.ContainsRune(text, 0x1b)
	hasTabs := strings.ContainsRune(text, '\t')

	if !hasAnsi && !hasTabs {
		for _, g := range graphemes(text) {
			w := g.width
			if keepPrefix && keptWidth+w <= targetWidth {
				result.WriteString(g.seg)
				keptWidth += w
			} else {
				keepPrefix = false
			}
			visibleSoFar += w
			if visibleSoFar > maxWidth {
				overflowed = true
				break
			}
		}
		if !overflowed {
			if pad {
				return text + strings.Repeat(" ", max(0, maxWidth-visibleSoFar))
			}
			return text
		}
		return finalize(result.String(), keptWidth, ellipsis, ellipsisWidth, maxWidth, pad)
	}

	i := 0
	exhausted := false
	for i < len(text) {
		if code, length := ExtractAnsiCode(text, i); length > 0 {
			pendingAnsi += code
			i += length
			continue
		}
		if text[i] == '\t' {
			if keepPrefix && keptWidth+tabWidth <= targetWidth {
				if pendingAnsi != "" {
					result.WriteString(pendingAnsi)
					pendingAnsi = ""
				}
				result.WriteByte('\t')
				keptWidth += tabWidth
			} else {
				keepPrefix = false
				pendingAnsi = ""
			}
			visibleSoFar += tabWidth
			if visibleSoFar > maxWidth {
				overflowed = true
				break
			}
			i++
			continue
		}
		// Collect a run of non-ANSI, non-tab bytes.
		end := i
		for end < len(text) && text[end] != '\t' {
			if _, length := ExtractAnsiCode(text, end); length > 0 {
				break
			}
			end++
		}
		for _, g := range graphemes(text[i:end]) {
			w := g.width
			if keepPrefix && keptWidth+w <= targetWidth {
				if pendingAnsi != "" {
					result.WriteString(pendingAnsi)
					pendingAnsi = ""
				}
				result.WriteString(g.seg)
				keptWidth += w
			} else {
				keepPrefix = false
				pendingAnsi = ""
			}
			visibleSoFar += w
			if visibleSoFar > maxWidth {
				overflowed = true
				break
			}
		}
		if overflowed {
			break
		}
		i = end
	}
	exhausted = i >= len(text)

	if !overflowed && exhausted {
		if pad {
			return text + strings.Repeat(" ", max(0, maxWidth-visibleSoFar))
		}
		return text
	}
	return finalize(result.String(), keptWidth, ellipsis, ellipsisWidth, maxWidth, pad)
}

// Truncate is a convenience wrapper with the default "..." ellipsis, no padding.
func Truncate(text string, maxWidth int) string {
	return TruncateToWidth(text, maxWidth, "...", false)
}

func finalize(prefix string, prefixWidth int, ellipsis string, ellipsisWidth, maxWidth int, pad bool) string {
	visible := prefixWidth + ellipsisWidth
	var result string
	if len(ellipsis) > 0 {
		result = prefix + reset + ellipsis + reset
	} else {
		result = prefix + reset
	}
	if pad {
		return result + strings.Repeat(" ", max(0, maxWidth-visible))
	}
	return result
}

// truncateFragment clips text to maxWidth without ellipsis, returning the
// clipped string and its visible width.
func truncateFragment(text string, maxWidth int) (string, int) {
	if maxWidth <= 0 || len(text) == 0 {
		return "", 0
	}
	if isPrintableASCII(text) {
		if len(text) <= maxWidth {
			return text, len(text)
		}
		return text[:maxWidth], maxWidth
	}
	var b strings.Builder
	w := 0
	for _, g := range graphemes(text) {
		gw := g.width
		if w+gw > maxWidth {
			break
		}
		b.WriteString(g.seg)
		w += gw
	}
	return b.String(), w
}
