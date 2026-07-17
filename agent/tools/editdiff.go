package tools

import (
	"fmt"
	"strings"
)

// editReplacement is one exact-text replacement request.
type editReplacement struct {
	OldText string
	NewText string
}

// matchedEdit is a resolved replacement with its position in the base content.
type matchedEdit struct {
	editIndex   int
	matchIndex  int // byte offset in base content
	matchLength int
	newText     string
}

// ── Line endings & BOM ──────────────────────────────────────────────────────

const bomPrefix = "\uFEFF"

// stripBOM splits off a leading UTF-8 BOM so matching ignores the invisible
// marker (the model never includes it in oldText).
func stripBOM(content string) (bom, text string) {
	if strings.HasPrefix(content, bomPrefix) {
		return bomPrefix, content[len(bomPrefix):]
	}
	return "", content
}

// detectLineEnding reports the file's dominant line ending: CRLF if the first
// newline is preceded by CR, else LF.
func detectLineEnding(content string) string {
	crlf := strings.Index(content, "\r\n")
	lf := strings.Index(content, "\n")
	if crlf != -1 && crlf == lf-1 {
		return "\r\n"
	}
	return "\n"
}

// normalizeToLF converts CRLF and lone CR to LF for uniform matching.
func normalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// restoreLineEndings converts LF back to the original ending before writing.
func restoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// ── Fuzzy matching ──────────────────────────────────────────────────────────

// fuzzyReplacer normalizes typographic variants that models often introduce so
// a near-miss still matches: smart quotes → ASCII, various dashes → "-", exotic
// spaces → " ". Trailing per-line whitespace is stripped separately.
var fuzzyReplacer = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'", // single quotes
	"\u201C", `"`, "\u201D", `"`, "\u201E", `"`, "\u201F", `"`, // double quotes
	"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", // dashes
	"\u2014", "-", "\u2015", "-", "\u2212", "-",
	"\u00A0", " ", "\u2002", " ", "\u2003", " ", "\u2004", " ", // spaces
	"\u2005", " ", "\u2006", " ", "\u2007", " ", "\u2008", " ",
	"\u2009", " ", "\u200A", " ", "\u202F", " ", "\u205F", " ", "\u3000", " ",
)

// normalizeForFuzzyMatch strips trailing whitespace per line and folds Unicode
// punctuation/space variants to ASCII. Character count is preserved (each
// replacement is 1→1 rune) EXCEPT trailing-whitespace stripping, so fuzzy
// matches use line-level preservation when writing back.
func normalizeForFuzzyMatch(text string) string {
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return fuzzyReplacer.Replace(strings.Join(lines, "\n"))
}

// countOccurrences counts fuzzy occurrences of oldText in content.
func countOccurrences(content, oldText string) int {
	return strings.Count(normalizeForFuzzyMatch(content), normalizeForFuzzyMatch(oldText))
}

type findResult struct {
	found       bool
	index       int
	matchLength int
	usedFuzzy   bool
}

// fuzzyFindText finds oldText in content: exact first, then fuzzy-normalized.
func fuzzyFindText(content, oldText string) findResult {
	if idx := strings.Index(content, oldText); idx != -1 {
		return findResult{found: true, index: idx, matchLength: len(oldText)}
	}
	fc := normalizeForFuzzyMatch(content)
	fo := normalizeForFuzzyMatch(oldText)
	if idx := strings.Index(fc, fo); idx != -1 {
		return findResult{found: true, index: idx, matchLength: len(fo), usedFuzzy: true}
	}
	return findResult{}
}

// ── Applying replacements ───────────────────────────────────────────────────

// applyReplacements applies matched edits to content in reverse order (so byte
// offsets stay stable). offset shifts match indices for slice-relative content.
func applyReplacements(content string, edits []matchedEdit, offset int) string {
	result := content
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		at := e.matchIndex - offset
		result = result[:at] + e.newText + result[at+e.matchLength:]
	}
	return result
}

// splitLinesWithEndings splits content keeping each line's trailing newline.
func splitLinesWithEndings(content string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i+1])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

type lineSpan struct{ start, end int }

func getLineSpans(content string) []lineSpan {
	var spans []lineSpan
	offset := 0
	for _, ln := range splitLinesWithEndings(content) {
		spans = append(spans, lineSpan{offset, offset + len(ln)})
		offset += len(ln)
	}
	return spans
}

func getReplacementLineRange(spans []lineSpan, e matchedEdit) (startLine, endLine int, err error) {
	start := e.matchIndex
	end := e.matchIndex + e.matchLength
	startLine = -1
	for i, s := range spans {
		if start >= s.start && start < s.end {
			startLine = i
			break
		}
	}
	if startLine == -1 {
		return 0, 0, fmt.Errorf("replacement range is outside the base content")
	}
	endLine = startLine
	for endLine < len(spans) && spans[endLine].end < end {
		endLine++
	}
	if endLine >= len(spans) {
		return 0, 0, fmt.Errorf("replacement range is outside the base content")
	}
	return startLine, endLine + 1, nil
}

// applyReplacementsPreservingUnchangedLines applies edits matched against a
// normalized (fuzzy) base back onto the ORIGINAL content, rewriting only the
// touched line blocks and copying every other line verbatim from the original.
// This keeps the file's original bytes on unchanged lines even when fuzzy
// matching altered the matching view.
func applyReplacementsPreservingUnchangedLines(original, base string, edits []matchedEdit) (string, error) {
	originalLines := splitLinesWithEndings(original)
	baseSpans := getLineSpans(base)
	if len(originalLines) != len(baseSpans) {
		return "", fmt.Errorf("cannot preserve unchanged lines: base content has a different line count")
	}

	type group struct {
		startLine, endLine int
		edits              []matchedEdit
	}
	// edits are already sorted by matchIndex by the caller.
	var groups []group
	for _, e := range edits {
		sl, el, err := getReplacementLineRange(baseSpans, e)
		if err != nil {
			return "", err
		}
		if n := len(groups); n > 0 && sl < groups[n-1].endLine {
			if el > groups[n-1].endLine {
				groups[n-1].endLine = el
			}
			groups[n-1].edits = append(groups[n-1].edits, e)
			continue
		}
		groups = append(groups, group{sl, el, []matchedEdit{e}})
	}

	var b strings.Builder
	line := 0
	for _, g := range groups {
		for _, l := range originalLines[line:g.startLine] {
			b.WriteString(l)
		}
		gStart := baseSpans[g.startLine].start
		gEnd := baseSpans[g.endLine-1].end
		b.WriteString(applyReplacements(base[gStart:gEnd], g.edits, gStart))
		line = g.endLine
	}
	for _, l := range originalLines[line:] {
		b.WriteString(l)
	}
	return b.String(), nil
}

// ── Orchestration ───────────────────────────────────────────────────────────

// applyEdits resolves and applies all edits against LF-normalized content,
// mirroring PI: exact-or-fuzzy match, uniqueness + overlap validation, then
// reverse-order application (line-preserving when fuzzy). Returns the new
// content or an actionable error.
func applyEdits(normalized string, edits []editReplacement, path string) (string, error) {
	n := len(edits)
	norm := make([]editReplacement, n)
	for i, e := range edits {
		norm[i] = editReplacement{OldText: normalizeToLF(e.OldText), NewText: normalizeToLF(e.NewText)}
		if norm[i].OldText == "" {
			return "", emptyOldTextErr(path, i, n)
		}
	}

	// If ANY edit needs fuzzy matching, run all matching in fuzzy space so
	// offsets are consistent, then write back with line preservation.
	usedFuzzy := false
	for _, e := range norm {
		if fuzzyFindText(normalized, e.OldText).usedFuzzy {
			usedFuzzy = true
			break
		}
	}
	base := normalized
	if usedFuzzy {
		base = normalizeForFuzzyMatch(normalized)
	}

	matched := make([]matchedEdit, 0, n)
	for i, e := range norm {
		res := fuzzyFindText(base, e.OldText)
		if !res.found {
			return "", notFoundErr(path, i, n)
		}
		if occ := countOccurrences(base, e.OldText); occ > 1 {
			return "", duplicateErr(path, i, n, occ)
		}
		matched = append(matched, matchedEdit{
			editIndex:   i,
			matchIndex:  res.index,
			matchLength: res.matchLength,
			newText:     e.NewText,
		})
	}

	// Sort by position, then reject overlapping edits.
	for i := 1; i < len(matched); i++ {
		for j := i; j > 0 && matched[j].matchIndex < matched[j-1].matchIndex; j-- {
			matched[j], matched[j-1] = matched[j-1], matched[j]
		}
	}
	for i := 1; i < len(matched); i++ {
		prev, cur := matched[i-1], matched[i]
		if prev.matchIndex+prev.matchLength > cur.matchIndex {
			return "", fmt.Errorf("edits[%d] and edits[%d] overlap in %s. Merge them into one edit or target disjoint regions", prev.editIndex, cur.editIndex, path)
		}
	}

	var newContent string
	var err error
	if usedFuzzy {
		newContent, err = applyReplacementsPreservingUnchangedLines(normalized, base, matched)
		if err != nil {
			return "", err
		}
	} else {
		newContent = applyReplacements(base, matched, 0)
	}
	if newContent == normalized {
		return "", noChangeErr(path, n)
	}
	return newContent, nil
}

// ── Errors (mirror PI's actionable messages) ────────────────────────────────

func notFoundErr(path string, i, total int) error {
	if total == 1 {
		return fmt.Errorf("could not find the exact text in %s. The old text must match exactly including all whitespace and newlines", path)
	}
	return fmt.Errorf("could not find edits[%d] in %s. The oldText must match exactly including all whitespace and newlines", i, path)
}

func duplicateErr(path string, i, total, occ int) error {
	if total == 1 {
		return fmt.Errorf("found %d occurrences of the text in %s. The text must be unique — provide more context to make it unique", occ, path)
	}
	return fmt.Errorf("found %d occurrences of edits[%d] in %s. Each oldText must be unique — provide more context", occ, i, path)
}

func emptyOldTextErr(path string, i, total int) error {
	if total == 1 {
		return fmt.Errorf("old_text must not be empty in %s", path)
	}
	return fmt.Errorf("edits[%d].oldText must not be empty in %s", i, path)
}

func noChangeErr(path string, total int) error {
	if total == 1 {
		return fmt.Errorf("no changes made to %s. The replacement produced identical content — check for special characters or that the text exists as expected", path)
	}
	return fmt.Errorf("no changes made to %s. The replacements produced identical content", path)
}
