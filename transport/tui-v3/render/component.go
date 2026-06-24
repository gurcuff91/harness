// Package render implements the differential rendering engine for tui-v3.
//
// It is a pure-Go port of PI's TUI core. The model is simple:
//   - A Component renders itself to an array of lines for a given width.
//   - The TUI composes all components top-to-bottom into a frame.
//   - On each render, only the lines that changed since the previous frame
//     are rewritten, wrapped in synchronized-output markers for zero flicker.
//
// This mirrors @earendil-works/pi-tui's three-strategy renderer:
//  1. First render — output everything (assume clean screen).
//  2. Width/height change — full redraw (wrapping changes).
//  3. Normal update — move to first changed line, rewrite only the changed range.
package render

// Component is the unit of rendering. Every widget implements it.
//
// render returns one string per line. Each line MUST NOT exceed width visible
// columns (use ansi.TruncateToWidth / ansi.WrapTextWithAnsi to guarantee this);
// the renderer treats over-wide lines as a bug.
type Component interface {
	Render(width int) []string
}

// InputHandler is implemented by components that accept keyboard input when
// focused. data is one complete terminal sequence (see term.stdinBuffer).
type InputHandler interface {
	HandleInput(data string)
}

// Invalidator is implemented by components that cache render output and need to
// be told to recompute (e.g. on resize).
type Invalidator interface {
	Invalidate()
}

// Container groups child components and renders them in order. It is the base
// for the TUI itself and any composite widget.
type Container struct {
	children []Component
}

// AddChild appends a component to the container.
func (c *Container) AddChild(child Component) {
	c.children = append(c.children, child)
}

// RemoveChild removes a component if present.
func (c *Container) RemoveChild(child Component) {
	for i, ch := range c.children {
		if ch == child {
			c.children = append(c.children[:i], c.children[i+1:]...)
			return
		}
	}
}

// Children returns the current child slice (read-only use).
func (c *Container) Children() []Component {
	return c.children
}

// Clear removes all children.
func (c *Container) Clear() {
	c.children = nil
}

// Invalidate propagates invalidation to all children that support it.
func (c *Container) Invalidate() {
	for _, child := range c.children {
		if inv, ok := child.(Invalidator); ok {
			inv.Invalidate()
		}
	}
}

// Render composes all children top-to-bottom into a single line slice.
func (c *Container) Render(width int) []string {
	var lines []string
	for _, child := range c.children {
		lines = append(lines, child.Render(width)...)
	}
	return lines
}
