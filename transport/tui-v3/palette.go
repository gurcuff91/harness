package tuiv3

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/gurcuff91/harness/transport/tui-v3/components"
)

// paletteController is the command palette: a multi-level, filterable command
// menu that opens when the editor begins with "/". Level 1 shows root commands;
// selecting a command that needs a value pushes a level-2 sub-palette with
// dynamic options (providers, sessions, fixed values). Port of v1's palette
// stack onto v3's SelectList component.
//
// Key behaviors (mirroring v1):
//   - Enter on a leaf command → execute; on a list command → push sub-palette;
//     on a free/value command → prefill the editor.
//   - Tab → autocomplete into the editor without executing.
//   - Esc / Backspace-to-empty → pop one level, or close at the root.
type paletteController struct {
	tui  *TUI
	list *components.SelectList
	open bool

	// levels is the navigation stack. levels[0] is the root; pushing a
	// sub-palette appends. Each level remembers the command that opened it.
	levels []paletteLevel
}

// paletteLevel is one frame of the palette stack.
type paletteLevel struct {
	items     []components.SelectItem
	parentCmd string // command that opened this level ("" for root)
}

func newPaletteController(t *TUI) *paletteController {
	p := &paletteController{tui: t}
	p.list = components.NewSelectList(nil, 8)
	p.list.OnSelect = p.onEnter
	p.list.OnCancel = p.onCancel
	return p
}

// IsOpen reports whether the palette is currently showing.
func (p *paletteController) IsOpen() bool { return p.open }

// depth returns the number of stacked levels.
func (p *paletteController) depth() int { return len(p.levels) }

// Render shows the list when open, nothing otherwise.
func (p *paletteController) Render(width int) []string {
	if !p.open {
		return nil
	}
	return p.list.Render(width)
}

// HandleInputConsumed forwards navigation/Tab/select/cancel keys to the palette
// while open. Printable characters fall through to the editor so the user keeps
// typing the filter. Returns true to consume the input.
func (p *paletteController) HandleInputConsumed(data string) bool {
	if !p.open {
		return false
	}
	switch data {
	case "\t": // Tab → autocomplete into the editor without executing.
		p.onTab()
		return true
	case "\x1b[A", "\x1b[B", "\x1bOA", "\x1bOB", "\r", "\n", "\x1b":
		p.list.HandleInput(data)
		p.tui.tui.RequestRender(false)
		return true
	case "\x7f", "\x08": // Backspace.
		// In a sub-palette with an empty filter, Backspace pops back to the
		// parent level instead of deleting (mirrors v1). Otherwise it falls
		// through to the editor so the filter shrinks normally.
		if p.depth() > 1 && p.tui.editor.Value() == "" {
			p.popLevel()
			p.list.SetFilter("")
			p.tui.tui.RequestRender(false)
			return true
		}
		return false
	}
	return false
}

// onEditorChange opens/filters/closes the palette based on editor content.
func (p *paletteController) onEditorChange(text string) {
	// Root level: open + filter while typing "/word" (no space yet).
	if strings.HasPrefix(text, "/") && !strings.Contains(text, " ") {
		if !p.open {
			p.openRoot()
		}
		if p.depth() == 1 {
			p.list.SetFilter(strings.TrimPrefix(text, "/"))
		}
		p.tui.tui.RequestRender(false)
		return
	}
	// Sub-level: the editor was cleared on push, so any typed text here is the
	// sub-palette filter (e.g. narrowing a provider/session list).
	if p.open && p.depth() > 1 {
		p.list.SetFilter(text)
		p.tui.tui.RequestRender(false)
		return
	}
	// Root level with a space / non-slash text → close so the user can type
	// free-form arguments.
	if p.open && p.depth() == 1 {
		p.close()
	}
}

func (p *paletteController) openRoot() {
	p.open = true
	p.levels = []paletteLevel{{items: p.tui.rootCommandItems()}}
	p.list.SetItems(p.levels[0].items)
	p.tui.tui.RequestRender(false)
}

func (p *paletteController) close() {
	if !p.open {
		return
	}
	p.open = false
	p.levels = nil
	p.tui.tui.RequestRender(false)
}

// pushSub opens a level-2 palette for parentCmd with the given items.
func (p *paletteController) pushSub(parentCmd string, items []components.SelectItem) {
	p.levels = append(p.levels, paletteLevel{items: items, parentCmd: parentCmd})
	p.list.SetItems(items)
	p.tui.editor.Clear()
	p.tui.tui.RequestRender(false)
}

// popLevel removes the top level; returns false if already at the root.
func (p *paletteController) popLevel() bool {
	if len(p.levels) <= 1 {
		return false
	}
	p.levels = p.levels[:len(p.levels)-1]
	p.list.SetItems(p.levels[len(p.levels)-1].items)
	return true
}

// curParent returns the parentCmd of the current (top) level.
func (p *paletteController) curParent() string {
	if len(p.levels) == 0 {
		return ""
	}
	return p.levels[len(p.levels)-1].parentCmd
}

// onCancel handles Esc / Ctrl+C from the list: pop one level, or close at root.
func (p *paletteController) onCancel() {
	if p.popLevel() {
		p.tui.editor.SetValue("/")
		p.tui.tui.RequestRender(false)
		return
	}
	p.tui.editor.Clear()
	p.close()
}

// onEnter handles confirming the highlighted item.
func (p *paletteController) onEnter(item components.SelectItem) {
	if p.depth() == 1 {
		p.enterRoot(item)
		return
	}
	p.enterSub(item)
}

// enterRoot processes Enter on a root-level command.
func (p *paletteController) enterRoot(item components.SelectItem) {
	cmd := item.Value
	switch p.tui.cmdType(cmd) {
	case "quit":
		p.tui.editor.Clear()
		p.close()
		p.tui.quit()
	case "list", "list-free":
		subs := p.tui.getSubItems(cmd)
		p.pushSub(cmd, subs)
	case "free":
		p.tui.editor.SetValue("/" + cmd + " ")
		p.close()
	default: // "none", "optional"
		p.tui.editor.Clear()
		p.close()
		p.tui.runCommand(cmd, nil)
	}
}

// enterSub processes Enter on a level-2 item.
func (p *paletteController) enterSub(item components.SelectItem) {
	parent := p.curParent()
	token := item.Value
	if item.ID != "" {
		token = item.ID
	}
	// connect to a non-subscription provider needs an API key typed by hand.
	if parent == "connect" && !item.Flag {
		p.tui.editor.SetValue("/connect " + token + " ")
		p.close()
		return
	}
	p.tui.editor.Clear()
	p.close()
	p.tui.runCommand(parent, []string{token})
}

// onTab autocompletes the highlighted item into the editor without executing.
func (p *paletteController) onTab() {
	sel, ok := p.list.Selected()
	if !ok {
		return
	}
	if p.depth() == 1 {
		cmd := sel.Value
		switch p.tui.cmdType(cmd) {
		case "list", "list-free":
			subs := p.tui.getSubItems(cmd)
			p.pushSub(cmd, subs)
		case "free":
			p.tui.editor.SetValue("/" + cmd + " ")
			p.close()
		default: // none, optional, quit
			p.tui.editor.SetValue("/" + cmd)
			p.close()
		}
		p.tui.tui.RequestRender(false)
		return
	}
	// Level 2 → prefill "/parent token" in the editor.
	token := sel.Value
	if sel.ID != "" {
		token = sel.ID
	}
	p.tui.editor.SetValue("/" + p.curParent() + " " + token)
	p.close()
	p.tui.tui.RequestRender(false)
}

// ── Command classification + dynamic sub-items (ported from v1) ──────────────

// cmdType classifies a command for palette flow:
//
//	quit      → exit
//	none      → execute immediately, no args
//	optional  → execute immediately, arg optional
//	free      → prefill editor, user types a value
//	list      → push sub-palette of fixed/dynamic values
//	list-free → push sub-palette, then (for connect) maybe type an API key
func (t *TUI) cmdType(cmd string) string {
	switch cmd {
	case "quit", "exit":
		return "quit"
	case "connect":
		return "list-free"
	case "disconnect", "resume", "delete":
		return "list"
	}
	for _, c := range t.sessionCmds {
		if c.Name != cmd {
			continue
		}
		if len(c.Params) == 0 {
			return "none"
		}
		p := c.Params[0]
		if !p.Required {
			return "optional"
		}
		if len(p.Values) > 0 {
			return "list"
		}
		return "free"
	}
	return "none"
}

// getSubItems returns the level-2 options for a command.
func (t *TUI) getSubItems(cmd string) []components.SelectItem {
	switch cmd {
	case "connect":
		return t.providersByActive(false)
	case "disconnect":
		return t.providersByActive(true)
	case "resume", "delete":
		return t.sessionsForCWD(true)
	}
	for _, c := range t.sessionCmds {
		if c.Name != cmd {
			continue
		}
		for _, p := range c.Params {
			if len(p.Values) > 0 {
				var items []components.SelectItem
				for _, v := range p.Values {
					items = append(items, components.SelectItem{Value: v, Label: v})
				}
				return items
			}
		}
	}
	return nil
}

// providersByActive lists providers filtered by active state. When active is
// true it returns connected providers (for disconnect) and tags subscriptions;
// when false it returns inactive providers (for connect).
func (t *TUI) providersByActive(active bool) []components.SelectItem {
	data, err := t.client.GetProviders()
	if err != nil {
		return nil
	}
	var providers []map[string]any
	json.Unmarshal(data, &providers)
	var items []components.SelectItem
	for _, p := range providers {
		isActive, _ := p["active"].(bool)
		if isActive != active {
			continue
		}
		name, _ := p["name"].(string)
		// Prefer the human-friendly display name for the label; fall back to the
		// slug. The dynamic description ("API key · 12 models", ...) comes straight
		// from the core, uniform across providers. Value stays the slug — that's
		// what /connect needs. Flag marks subscriptions so connect can branch
		// (OAuth = execute directly; API key = prompt for a key) without parsing
		// the human-readable description.
		label, _ := p["display_name"].(string)
		if label == "" {
			label = name
		}
		desc, _ := p["description"].(string)
		isSub, _ := p["is_subscription"].(bool)
		items = append(items, components.SelectItem{Value: name, Label: label, Description: desc, Flag: isSub})
	}
	return items
}

// sessionsForCWD lists sessions in the current directory (optionally excluding
// the active one) for resume/delete sub-palettes.
func (t *TUI) sessionsForCWD(excludeActive bool) []components.SelectItem {
	cwd, _ := os.Getwd()
	data, err := t.client.ListSessionsByCWD(cwd)
	if err != nil {
		return nil
	}
	var sessions []map[string]any
	json.Unmarshal(data, &sessions)
	var items []components.SelectItem
	for _, s := range sessions {
		id, _ := s["id"].(string)
		if excludeActive && id == t.sessionID {
			continue
		}
		name, _ := s["name"].(string)
		if name == "" && len(id) >= 8 {
			name = id[:8]
		}
		// Description: "<relative time> · <short model> · <cwd>" — the most
		// distinguishing signals when picking a session to resume.
		sessCWD, _ := s["cwd"].(string)
		model, _ := s["model"].(string)
		lastActive, _ := s["last_active_at"].(string)
		var segs []string
		if rel := relativeTime(lastActive); rel != "" {
			segs = append(segs, rel)
		}
		if sm := shortModel(model); sm != "" {
			segs = append(segs, sm)
		}
		segs = append(segs, shortenPath(sessCWD))
		items = append(items, components.SelectItem{
			Value:       name,
			Label:       name,
			Description: strings.Join(segs, " · "),
			ID:          id,
		})
	}
	return items
}
