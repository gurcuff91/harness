package tuiv3

import (
	"strings"

	"github.com/gurcuff91/harness/transport/tui-v3/components"
)

// paletteController is the command palette: a SelectList that opens when the
// editor begins with "/" and drives command execution. It is a render.Component
// that renders nothing while closed.
//
// This implements the core flow (open, filter, select, execute simple commands
// and value-cycling commands). Multi-step flows (connect → api key) are handled
// by delegating to the TUI's command handler.
type paletteController struct {
	tui  *TUI
	list *components.SelectList
	open bool

	// When awaiting a value for a selected command (e.g. /model <value>).
	pendingCmd string
}

func newPaletteController(t *TUI) *paletteController {
	p := &paletteController{tui: t}
	p.list = components.NewSelectList(nil, 8)
	p.list.OnSelect = p.onSelect
	p.list.OnCancel = p.close
	return p
}

// IsOpen reports whether the palette is currently showing.
func (p *paletteController) IsOpen() bool { return p.open }

// Render shows the list when open, nothing otherwise.
func (p *paletteController) Render(width int) []string {
	if !p.open {
		return nil
	}
	return p.list.Render(width)
}

// HandleInputConsumed forwards navigation keys to the list while open.
// Returns true to consume the input (stop further dispatch).
func (p *paletteController) HandleInputConsumed(data string) bool {
	if !p.open {
		return false
	}
	// Navigation / select / cancel keys go to the list. Printable characters
	// fall through to the editor so the user keeps typing the filter.
	switch data {
	case "\x1b[A", "\x1b[B", "\x1bOA", "\x1bOB", "\r", "\n", "\x1b":
		p.list.HandleInput(data)
		p.tui.tui.RequestRender(false)
		return true
	}
	return false
}

// onEditorChange opens/filters/closes the palette based on editor content.
func (p *paletteController) onEditorChange(text string) {
	if strings.HasPrefix(text, "/") {
		// Still typing the command word (no space yet) → filter root commands.
		if !strings.Contains(text, " ") {
			if !p.open {
				p.openRoot()
			}
			p.list.SetFilter(strings.TrimPrefix(text, "/"))
			p.tui.tui.RequestRender(false)
			return
		}
	}
	if p.open {
		p.close()
	}
}

func (p *paletteController) openRoot() {
	p.open = true
	p.pendingCmd = ""
	p.list.SetItems(p.tui.rootCommandItems())
	p.tui.tui.RequestRender(false)
}

func (p *paletteController) close() {
	if !p.open {
		return
	}
	p.open = false
	p.pendingCmd = ""
	p.tui.tui.RequestRender(false)
}

// onSelect runs when the user confirms a palette item.
func (p *paletteController) onSelect(item components.SelectItem) {
	cmd := item.Value
	p.close()
	p.tui.editor.Clear()
	p.tui.runCommand(cmd, nil)
}
