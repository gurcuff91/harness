package ansi

import "strings"

// WrapTextWithAnsi word-wraps text to width visible columns per line, preserving
// ANSI styling across line breaks. Newlines in the input are honored. Lines are
// NOT padded. Port of PI's wrapTextWithAnsi.
func WrapTextWithAnsi(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	inputLines := strings.Split(text, "\n")
	var result []string
	tracker := &codeTracker{}
	for _, inputLine := range inputLines {
		prefix := ""
		if len(result) > 0 {
			prefix = tracker.activeCodes()
		}
		for _, wl := range wrapSingleLine(prefix+inputLine, width) {
			result = append(result, wl)
		}
		tracker.feed(inputLine)
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func wrapSingleLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	if VisibleWidth(line) <= width {
		return []string{line}
	}

	var wrapped []string
	tracker := &codeTracker{}
	tokens := splitTokens(line)
	current := ""
	currentWidth := 0

	for _, token := range tokens {
		tw := VisibleWidth(token)
		isWS := strings.TrimSpace(token) == ""

		// Token alone exceeds width — break it grapheme by grapheme.
		if tw > width && !isWS {
			if current != "" {
				if r := tracker.lineEndReset(); r != "" {
					current += r
				}
				wrapped = append(wrapped, current)
				current = ""
				currentWidth = 0
			}
			broken := breakLongWord(token, width, tracker)
			for i := 0; i < len(broken)-1; i++ {
				wrapped = append(wrapped, broken[i])
			}
			current = broken[len(broken)-1]
			currentWidth = VisibleWidth(current)
			continue
		}

		if currentWidth+tw > width && currentWidth > 0 {
			lineToWrap := strings.TrimRight(current, " \t")
			if r := tracker.lineEndReset(); r != "" {
				lineToWrap += r
			}
			wrapped = append(wrapped, lineToWrap)
			if isWS {
				current = tracker.activeCodes()
				currentWidth = 0
			} else {
				current = tracker.activeCodes() + token
				currentWidth = tw
			}
		} else {
			current += token
			currentWidth += tw
		}
		tracker.feed(token)
	}

	if current != "" {
		wrapped = append(wrapped, current)
	}
	if len(wrapped) == 0 {
		return []string{""}
	}
	for i := range wrapped {
		wrapped[i] = strings.TrimRight(wrapped[i], " \t")
	}
	return wrapped
}

// breakLongWord splits a single token wider than width into multiple lines,
// re-applying active codes at each line start.
func breakLongWord(word string, width int, tracker *codeTracker) []string {
	var lines []string
	current := tracker.activeCodes()
	currentWidth := 0

	type seg struct {
		ansi bool
		val  string
	}
	var segs []seg
	i := 0
	for i < len(word) {
		if code, length := ExtractAnsiCode(word, i); length > 0 {
			segs = append(segs, seg{ansi: true, val: code})
			i += length
			continue
		}
		end := i
		for end < len(word) {
			if _, length := ExtractAnsiCode(word, end); length > 0 {
				break
			}
			end++
		}
		for _, g := range graphemes(word[i:end]) {
			segs = append(segs, seg{ansi: false, val: g.seg})
		}
		i = end
	}

	for _, s := range segs {
		if s.ansi {
			current += s.val
			tracker.process(s.val)
			continue
		}
		if s.val == "" {
			continue
		}
		gw := VisibleWidth(s.val)
		if currentWidth+gw > width {
			if r := tracker.lineEndReset(); r != "" {
				current += r
			}
			lines = append(lines, current)
			current = tracker.activeCodes()
			currentWidth = 0
		}
		current += s.val
		currentWidth += gw
	}

	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// splitTokens splits text into word/space tokens, keeping ANSI codes attached to
// the following visible content. CJK runes become individual break tokens.
// Port of PI's splitIntoTokensWithAnsi.
func splitTokens(text string) []string {
	var tokens []string
	current := ""
	pendingAnsi := ""
	currentKind := "" // "word" | "space"

	flush := func() {
		if current != "" {
			tokens = append(tokens, current)
			current = ""
			currentKind = ""
		}
	}

	i := 0
	for i < len(text) {
		if code, length := ExtractAnsiCode(text, i); length > 0 {
			pendingAnsi += code
			i += length
			continue
		}
		end := i
		for end < len(text) {
			if _, length := ExtractAnsiCode(text, end); length > 0 {
				break
			}
			end++
		}
		for _, gr := range graphemes(text[i:end]) {
			g := gr.seg
			isSpace := g == " "
			if !isSpace && isCJK(g) {
				flush()
				tok := pendingAnsi + g
				pendingAnsi = ""
				tokens = append(tokens, tok)
				continue
			}
			kind := "word"
			if isSpace {
				kind = "space"
			}
			if current != "" && currentKind != kind {
				flush()
			}
			if pendingAnsi != "" {
				current += pendingAnsi
				pendingAnsi = ""
			}
			currentKind = kind
			current += g
		}
		i = end
	}

	if pendingAnsi != "" {
		if current != "" {
			current += pendingAnsi
		} else if len(tokens) > 0 {
			tokens[len(tokens)-1] += pendingAnsi
		} else {
			current = pendingAnsi
		}
	}
	if current != "" {
		tokens = append(tokens, current)
	}
	return tokens
}

// isCJK reports whether the grapheme's first rune is a CJK break character
// (Han, Hiragana, Katakana, Hangul, Bopomofo) — these break per-character.
func isCJK(g string) bool {
	for _, r := range g {
		switch {
		case r >= 0x4e00 && r <= 0x9fff: // CJK Unified Ideographs (Han)
			return true
		case r >= 0x3040 && r <= 0x309f: // Hiragana
			return true
		case r >= 0x30a0 && r <= 0x30ff: // Katakana
			return true
		case r >= 0xac00 && r <= 0xd7af: // Hangul Syllables
			return true
		case r >= 0x3100 && r <= 0x312f: // Bopomofo
			return true
		case r >= 0x3400 && r <= 0x4dbf: // CJK Ext A
			return true
		}
		return false
	}
	return false
}
