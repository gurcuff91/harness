package render

import (
	"fmt"
	"strings"
	"testing"
)

// bigTranscript simulates a long-lived TUI: n rendered lines of styled,
// mixed-width text (ANSI colors, accents, emoji, CJK) — the shape that made
// per-frame width measurement dominate CPU on a multi-day session.
func bigTranscript(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("\x1b[36m│\x1b[0m línea %d — análisis de código ⚔️ 漢字 %s", i, strings.Repeat("x", i%60))
	}
	return lines
}

// BenchmarkSanitizeSteadyState is one streaming frame on a long transcript:
// only the LAST line changed since the previous frame.
func BenchmarkSanitizeSteadyState(b *testing.B) {
	const width = 120
	base := bigTranscript(50000)
	var s lineSanitizer
	s.sanitize(append([]string(nil), base...), width) // warm: first frame
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frame := append([]string(nil), base...)
		frame[len(frame)-1] = fmt.Sprintf("streaming tick %d", i)
		s.sanitize(frame, width)
	}
}

// BenchmarkSanitizeStatelessBaseline is the pre-fix behavior (re-measure every
// line every frame), kept for comparison.
func BenchmarkSanitizeStatelessBaseline(b *testing.B) {
	const width = 120
	base := bigTranscript(50000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frame := append([]string(nil), base...)
		frame[len(frame)-1] = fmt.Sprintf("streaming tick %d", i)
		sanitizeLines(frame, width)
	}
}
