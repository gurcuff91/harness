package tools

import (
	"os"
	"strings"
	"testing"
)

func makeLines(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("line ")
		sb.WriteByte(byte('0' + i%10))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestTruncateHeadByLines(t *testing.T) {
	r := TruncateHead(makeLines(5000), 2000, 1<<30)
	if !r.Truncated || r.TruncatedBy != "lines" {
		t.Fatalf("expected line truncation, got %+v", r)
	}
	if r.OutputLines != 2000 || r.TotalLines != 5000 {
		t.Errorf("counts wrong: shown=%d total=%d", r.OutputLines, r.TotalLines)
	}
	// Head keeps the FIRST lines.
	if !strings.HasPrefix(r.Content, "line 0") {
		t.Errorf("head should start at first line: %q", r.Content[:20])
	}
}

func TestTruncateTailByLines(t *testing.T) {
	r := TruncateTail(makeLines(5000), 2000, 1<<30)
	if !r.Truncated || r.OutputLines != 2000 {
		t.Fatalf("expected 2000 tail lines, got %+v", r)
	}
	// Tail keeps the LAST lines; line 5000 is index 4999 → "line 9".
	lines := strings.Split(r.Content, "\n")
	if len(lines) != 2000 {
		t.Errorf("expected 2000 lines, got %d", len(lines))
	}
}

func TestTruncateByBytes(t *testing.T) {
	big := strings.Repeat("x", 60*1024) // one huge line, no newlines
	r := TruncateHead(big, 2000, 50*1024)
	if !r.Truncated || r.TruncatedBy != "bytes" {
		t.Errorf("expected byte truncation, got %+v", r)
	}
}

func TestTruncateNoOp(t *testing.T) {
	r := TruncateHead("small\noutput\nhere", 2000, 50*1024)
	if r.Truncated {
		t.Errorf("small content should not truncate")
	}
	if r.Content != "small\noutput\nhere" {
		t.Errorf("content altered: %q", r.Content)
	}
}

func TestApplyTruncationSavesFullOutputAndFooter(t *testing.T) {
	content := makeLines(5000)
	out := ApplyTruncation("test", content, true)
	if !strings.Contains(out, "Truncated: showing 2000 of 5000 lines") {
		t.Errorf("missing/incorrect footer: %q", out[len(out)-120:])
	}
	// Extract the temp path from the footer and verify it holds the FULL output.
	idx := strings.Index(out, "Full output: ")
	if idx < 0 {
		t.Fatal("footer missing full output path")
	}
	rest := out[idx+len("Full output: "):]
	path := rest[:strings.Index(rest, ".txt")+len(".txt")]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("temp file unreadable: %v", err)
	}
	if string(data) != content {
		t.Errorf("temp file does not hold full output (%d vs %d bytes)", len(data), len(content))
	}
	os.Remove(path)
}

func TestApplyTruncationNoOpNoFooter(t *testing.T) {
	out := ApplyTruncation("test", "tiny output", true)
	if strings.Contains(out, "Truncated") || strings.Contains(out, "Full output") {
		t.Errorf("small output should have no footer: %q", out)
	}
}
