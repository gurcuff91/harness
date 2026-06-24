package components

// Spacer renders a fixed number of blank lines for vertical spacing.
// Port of PI's Spacer.
type Spacer struct {
	lines int
}

// NewSpacer creates a spacer of n blank lines (n<1 clamps to 1).
func NewSpacer(n int) *Spacer {
	if n < 1 {
		n = 1
	}
	return &Spacer{lines: n}
}

// Render returns n empty lines.
func (s *Spacer) Render(width int) []string {
	out := make([]string, s.lines)
	return out
}

// Invalidate is a no-op (spacers have nothing to recompute) but satisfies the
// History Block interface.
func (s *Spacer) Invalidate() {}
