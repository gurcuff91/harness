package ansi

import (
	"strings"
	"testing"
	"time"
)

// One very long token with no break point (a pasted base64 blob, a minified JSON line, a long URL)
// must wrap in time linear in its length. splitTokens used to append each grapheme to a string —
// a copy of everything so far each time — so the cost was quadratic: 100 000 characters took 0.4 s
// and 1 000 000 took 80 s, inside the render that holds the renderer's lock, freezing the keyboard.
//
// Linear scaling is measured, not a wall-clock budget (a budget is fragile on a loaded machine): a
// 10× longer input may cost at most 40× more (linear is ~10×, quadratic ~100×). The best of three
// runs of each size is compared, which filters out a scheduling hiccup.
func TestWrappingALongTokenCostsLinearTime(t *testing.T) {
	best := func(n int) time.Duration {
		s := strings.Repeat("x", n)
		var min time.Duration
		for i := 0; i < 3; i++ {
			t0 := time.Now()
			WrapTextWithAnsi(s, 80)
			if d := time.Since(t0); i == 0 || d < min {
				min = d
			}
		}
		return min
	}
	small, large := best(30_000), best(300_000)
	if small < 200*time.Microsecond {
		small = 200 * time.Microsecond // below timer noise
	}
	if ratio := float64(large) / float64(small); ratio > 20 {
		t.Fatalf("wrapping 300k characters took %v, 30k took %v: %.0fx for 10x the input — super-linear", large, small, ratio)
	}
}

// Splitting must still produce what it always did for the cases the wrapper relies on.
func TestSplitTokensKeepsItsContract(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"ab cd", []string{"ab", " ", "cd"}},
		{"a,b,c", []string{"a,", "b,", "c"}},
		{"a  b", []string{"a", "  ", "b"}},
		{"日本a", []string{"日", "本", "a"}},
		// an ANSI code is kept attached to the visible content that FOLLOWS it (so the reset after "red"
		// travels with the space), which is how the wrapper has always re-applies styles after a break
		{"\x1b[31mred\x1b[0m x", []string{"\x1b[31mred", "\x1b[0m ", "x"}},
		{"ab\x1b[0m", []string{"ab\x1b[0m"}},
		{"\x1b[0m", []string{"\x1b[0m"}},
		{"x \x1b[0m", []string{"x", " \x1b[0m"}},
	} {
		got := splitTokens(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(got) != len(tc.want) {
			t.Errorf("splitTokens(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
