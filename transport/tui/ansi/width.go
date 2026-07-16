// Package ansi provides ANSI-aware string utilities for terminal rendering.
//
// This is a pure-Go port of the core utilities from the pi-tui library
// (@earendil-works/pi-tui). The algorithms mirror PI's implementation:
//   - VisibleWidth: width of a string in terminal columns, ignoring ANSI codes
//   - TruncateToWidth: truncate to a column budget, preserving ANSI codes
//   - WrapTextWithAnsi: word-wrap while carrying ANSI state across lines
//
// PI delegates East Asian Width to the `get-east-asian-width` npm package and
// grapheme segmentation to the native `Intl.Segmenter`. The Go equivalent uses
// github.com/rivo/uniseg, which provides full UAX-29 grapheme clustering AND
// monospace width calculation (EAW, emoji, combining marks) in one library —
// the closest equivalent to PI's Intl.Segmenter + get-east-asian-width pair.
package ansi

import (
	"strings"

	"github.com/rivo/uniseg"
)

const (
	// Tab renders as 3 spaces — matches PI's graphemeWidth("\t") == 3.
	tabWidth = 3
	// Reset closes all SGR styling. Used when truncating mid-style.
	reset = "\x1b[0m"
)

// widthCache mirrors PI's bounded width cache for non-ASCII strings.
const widthCacheSize = 512

var widthCache = make(map[string]int, widthCacheSize)

// grapheme is a single cluster paired with its terminal column width.
type grapheme struct {
	seg   string
	width int
}

// VisibleWidth returns the width of str in terminal columns, ignoring ANSI/OSC/APC
// escape sequences. Tabs count as 3 columns. Port of PI's visibleWidth().
func VisibleWidth(str string) int {
	if len(str) == 0 {
		return 0
	}
	// Fast path: pure ASCII printable.
	if isPrintableASCII(str) {
		return len(str)
	}
	if w, ok := widthCache[str]; ok {
		return w
	}

	clean := str
	if strings.ContainsRune(clean, '\t') {
		clean = strings.ReplaceAll(clean, "\t", "   ")
	}
	if strings.ContainsRune(clean, 0x1b) {
		clean = stripAnsi(clean)
	}

	// Sum per-cluster widths so the pictograph override (clusterWidth) applies
	// consistently with graphemes(). uniseg.StringWidth alone would under-count
	// text-presentation pictographs that terminals render double-width.
	w := 0
	state := -1
	rest := clean
	for len(rest) > 0 {
		var cluster string
		var cw int
		cluster, rest, cw, state = uniseg.FirstGraphemeClusterInString(rest, state)
		w += clusterWidth(cluster, cw)
	}

	if len(widthCache) >= widthCacheSize {
		// Evict an arbitrary entry to bound memory (Go map order is random).
		for k := range widthCache {
			delete(widthCache, k)
			break
		}
	}
	widthCache[str] = w
	return w
}

// stripAnsi removes all supported escape sequences (CSI/OSC/APC) in one pass.
func stripAnsi(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if _, length := ExtractAnsiCode(s, i); length > 0 {
			i += length
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ExtractAnsiCode detects an escape sequence starting at byte position pos.
// Returns the sequence and its byte length, or ("", 0) if none. Port of PI's
// extractAnsiCode — handles CSI (ESC [ ... final), OSC (ESC ] ... BEL/ST), and
// APC (ESC _ ... BEL/ST).
func ExtractAnsiCode(s string, pos int) (string, int) {
	if pos >= len(s) || s[pos] != 0x1b {
		return "", 0
	}
	if pos+1 >= len(s) {
		return "", 0
	}
	next := s[pos+1]

	// CSI sequence: ESC [ ... final byte in {m,G,K,H,J}
	if next == '[' {
		j := pos + 2
		for j < len(s) && !isCSIFinal(s[j]) {
			j++
		}
		if j < len(s) {
			return s[pos : j+1], j + 1 - pos
		}
		return "", 0
	}

	// OSC sequence: ESC ] ... (BEL | ESC \)
	if next == ']' {
		j := pos + 2
		for j < len(s) {
			if s[j] == 0x07 {
				return s[pos : j+1], j + 1 - pos
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return s[pos : j+2], j + 2 - pos
			}
			j++
		}
		return "", 0
	}

	// APC sequence: ESC _ ... (BEL | ESC \) — used for cursor marker.
	if next == '_' {
		j := pos + 2
		for j < len(s) {
			if s[j] == 0x07 {
				return s[pos : j+1], j + 1 - pos
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return s[pos : j+2], j + 2 - pos
			}
			j++
		}
		return "", 0
	}

	return "", 0
}

func isCSIFinal(b byte) bool {
	switch b {
	case 'm', 'G', 'K', 'H', 'J':
		return true
	}
	return false
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// ── Grapheme segmentation ────────────────────────────────────────────────

// graphemes splits s into grapheme clusters paired with their terminal width,
// using uniseg's full UAX-29 segmentation. Tabs are normalized to width 3 to
// match PI's behavior (uniseg treats control chars as width 0).
func graphemes(s string) []grapheme {
	if isPrintableASCII(s) {
		out := make([]grapheme, len(s))
		for i := 0; i < len(s); i++ {
			out[i] = grapheme{seg: string(s[i]), width: 1}
		}
		return out
	}

	var out []grapheme
	state := -1
	rest := s
	for len(rest) > 0 {
		var cluster string
		var w int
		cluster, rest, w, state = uniseg.FirstGraphemeClusterInString(rest, state)
		if cluster == "\t" {
			w = tabWidth
		} else {
			w = clusterWidth(cluster, w)
		}
		out = append(out, grapheme{seg: cluster, width: w})
	}
	return out
}

// clusterWidth adjusts uniseg's width for a grapheme cluster. uniseg follows
// the Unicode standard strictly: an emoji pictograph WITHOUT a VS16 selector
// defaults to text presentation (width 1). However, the vast majority of
// terminals render these as emoji (width 2), which causes the visible text to
// be "eaten" if we trust width 1. So we conservatively force width 2 for
// single-rune emoji pictographs that uniseg reported as width 1 — matching the
// behavior PI relies on to avoid drift.
func clusterWidth(cluster string, w int) int {
	if w != 1 {
		return w
	}
	r := []rune(cluster)
	if len(r) != 1 {
		return w
	}
	if isEmojiPictograph(r[0]) {
		return 2
	}
	return w
}

// isEmojiPictograph reports whether cp is in an emoji pictograph block that
// terminals typically render double-width even without a VS16 selector.
func isEmojiPictograph(cp rune) bool {
	switch {
	case cp >= 0x2600 && cp <= 0x27bf: // Misc symbols, Dingbats (✈ ☔ ✨ ❤ ✅ etc.)
		return true
	case cp >= 0x2b00 && cp <= 0x2bff: // Misc symbols and arrows (⭐ ⭕)
		return true
	case cp >= 0x1f300 && cp <= 0x1faff: // Emoji & pictographs (🌀 🌡 🚗 🛰 etc.)
		return true
	case cp >= 0x2190 && cp <= 0x21ff: // Arrows that often render emoji-wide
		return false // keep arrows narrow (commonly used as text)
	}
	return false
}
