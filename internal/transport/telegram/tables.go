package telegram

import (
	"strings"
	"unicode/utf8"
)

// cellWidth is the visual width of a cell in monospace: the number of Unicode
// code points, not bytes. Using bytes (len) would under-pad cells containing
// multi-byte runes (accents, ñ, …) and misalign the columns.
func cellWidth(s string) int { return utf8.RuneCountInString(s) }

// tablesToCodeBlocks rewrites GitHub-style pipe tables into fenced code blocks
// with space-aligned columns. Telegram supports neither Markdown nor HTML
// tables, so a raw table renders as literal `| … |` lines; wrapping it in a code
// block makes Telegram show it monospaced, which preserves the column alignment.
//
// A table is a header row (contains '|'), a delimiter row (only |, -, :, space),
// and one or more body rows. Lines inside existing ``` fences are left alone.
func tablesToCodeBlocks(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	inFence := false
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			inFence = !inFence
			out = append(out, lines[i])
			continue
		}
		if !inFence && i+1 < len(lines) &&
			isTableRow(lines[i]) && isDelimiterRow(lines[i+1]) {
			block, next := collectTable(lines, i)
			out = append(out, renderTable(block)...)
			i = next - 1 // -1 because the loop will ++
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// isTableRow reports whether a line looks like a table row (has a pipe and some
// non-pipe content).
func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return strings.Contains(t, "|") && strings.Trim(t, "|-: ") != ""
}

// isDelimiterRow reports whether a line is a table delimiter (|---|:--:|), i.e.
// made up only of pipes, dashes, colons and spaces, and contains a dash.
func isDelimiterRow(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.Contains(t, "-") || !strings.Contains(t, "|") {
		return false
	}
	return strings.Trim(t, "|-: ") == ""
}

// collectTable gathers the table starting at start (header + delimiter + body
// rows) and returns its lines plus the index just past it.
func collectTable(lines []string, start int) ([]string, int) {
	block := []string{lines[start], lines[start+1]}
	i := start + 2
	for i < len(lines) && isTableRow(lines[i]) && !isDelimiterRow(lines[i]) {
		block = append(block, lines[i])
		i++
	}
	return block, i
}

// renderTable turns the collected table lines into a fenced code block that
// keeps the table's structure — pipe borders and a header separator — with every
// column padded to a uniform width so it stays aligned in Telegram's monospace
// rendering. Only the cell padding is added; the layout (rows over columns,
// bordered) is preserved as the model wrote it.
func renderTable(block []string) []string {
	var rows [][]string
	for idx, line := range block {
		if idx == 1 {
			continue // original delimiter row — we regenerate it, sized to the columns
		}
		rows = append(rows, splitCells(line))
	}
	if len(rows) == 0 {
		return block
	}

	// Column count = widest row; widths = max cell length per column.
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	widths := make([]int, cols)
	for _, r := range rows {
		for c, cell := range r {
			if w := cellWidth(cell); w > widths[c] {
				widths[c] = w
			}
		}
	}

	// "| a    | b   |" — each cell left-padded to its column width, pipe-bordered.
	row := func(cells []string) string {
		var sb strings.Builder
		sb.WriteByte('|')
		for c := 0; c < cols; c++ {
			cell := ""
			if c < len(cells) {
				cell = cells[c]
			}
			sb.WriteByte(' ')
			sb.WriteString(cell)
			sb.WriteString(strings.Repeat(" ", widths[c]-cellWidth(cell)))
			sb.WriteString(" |")
		}
		return sb.String()
	}

	// "|------|-----|" separator sized to each column (dashes span the padded cell).
	var sep strings.Builder
	sep.WriteByte('|')
	for c := 0; c < cols; c++ {
		sep.WriteString(strings.Repeat("-", widths[c]+2))
		sep.WriteByte('|')
	}

	out := []string{"```", row(rows[0]), sep.String()}
	for _, r := range rows[1:] {
		out = append(out, row(r))
	}
	out = append(out, "```")
	return out
}

// splitCells splits a pipe table row into trimmed cell values, dropping the
// leading/trailing empty cells from the outer pipes.
func splitCells(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}
