package tools

import (
	"fmt"
	"os"
	"strings"
)

// Tool-output truncation, ported from PI. Truncation uses two independent
// limits — whichever is hit first wins — and NEVER cuts a line in half (so JSON
// and other structured output stays parseable). When output is truncated the
// full text is saved to a temp file and the model is told where to find it, so
// it can Read the rest with offset/limit.
const (
	DefaultMaxLines = 2000
	DefaultMaxBytes = 50 * 1024 // 50KB
)

// TruncateResult describes the outcome of truncating tool output.
type TruncateResult struct {
	Content     string
	Truncated   bool
	TruncatedBy string // "lines" | "bytes" | ""
	TotalLines  int
	OutputLines int
	MaxBytes    int
}

// splitLines splits content into lines for counting, dropping a single trailing
// newline (so "a\n" is one line, not two).
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TruncateHead keeps the FIRST maxLines/maxBytes (whichever hits first).
// Suitable for file reads and fetches where the beginning matters.
func TruncateHead(content string, maxLines, maxBytes int) TruncateResult {
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	lines := splitLines(content)
	total := len(lines)
	if total <= maxLines && len(content) <= maxBytes {
		return TruncateResult{Content: content, TotalLines: total, OutputLines: total, MaxBytes: maxBytes}
	}

	var out []string
	bytes := 0
	by := "lines"
	for i := 0; i < len(lines) && i < maxLines; i++ {
		lb := len(lines[i])
		if i > 0 {
			lb++ // newline
		}
		if bytes+lb > maxBytes {
			by = "bytes"
			break
		}
		out = append(out, lines[i])
		bytes += lb
	}
	return TruncateResult{
		Content:     strings.Join(out, "\n"),
		Truncated:   true,
		TruncatedBy: by,
		TotalLines:  total,
		OutputLines: len(out),
		MaxBytes:    maxBytes,
	}
}

// TruncateTail keeps the LAST maxLines/maxBytes (whichever hits first).
// Suitable for bash output where the end matters (errors, final results).
func TruncateTail(content string, maxLines, maxBytes int) TruncateResult {
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	lines := splitLines(content)
	total := len(lines)
	if total <= maxLines && len(content) <= maxBytes {
		return TruncateResult{Content: content, TotalLines: total, OutputLines: total, MaxBytes: maxBytes}
	}

	var out []string // built in reverse then flipped
	bytes := 0
	by := "lines"
	for i := len(lines) - 1; i >= 0 && len(out) < maxLines; i-- {
		lb := len(lines[i])
		if len(out) > 0 {
			lb++
		}
		if bytes+lb > maxBytes {
			by = "bytes"
			break
		}
		out = append(out, lines[i])
		bytes += lb
	}
	// reverse
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return TruncateResult{
		Content:     strings.Join(out, "\n"),
		Truncated:   true,
		TruncatedBy: by,
		TotalLines:  total,
		OutputLines: len(out),
		MaxBytes:    maxBytes,
	}
}

// saveFullOutput writes the full (untruncated) content to a temp file and
// returns its path. label is a short tool identifier used in the filename.
func saveFullOutput(label, content string) (string, error) {
	f, err := os.CreateTemp("", "harness-"+label+"-*.txt")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// ApplyTruncation truncates content per the given strategy, saves the full
// output to a temp file when truncated, and appends a footer telling the model
// where the full output lives and how much was shown. label names the tool
// (used in the temp filename). If head is true, keeps the beginning; otherwise
// the end.
func ApplyTruncation(label, content string, head bool) string {
	var r TruncateResult
	if head {
		r = TruncateHead(content, DefaultMaxLines, DefaultMaxBytes)
	} else {
		r = TruncateTail(content, DefaultMaxLines, DefaultMaxBytes)
	}
	if !r.Truncated {
		return content
	}

	footer := ""
	if path, err := saveFullOutput(label, content); err == nil {
		footer = fmt.Sprintf("\n\n[Full output: %s. ", path)
	} else {
		footer = "\n\n["
	}
	if r.TruncatedBy == "lines" {
		footer += fmt.Sprintf("Truncated: showing %d of %d lines]", r.OutputLines, r.TotalLines)
	} else {
		footer += fmt.Sprintf("Truncated: %d lines shown (%s limit)]", r.OutputLines, formatSize(r.MaxBytes))
	}
	return r.Content + footer
}

// formatSize renders a byte count as a human-readable size.
func formatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}
