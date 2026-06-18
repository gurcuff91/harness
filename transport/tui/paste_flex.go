package tui

import "github.com/rivo/tview"

// pasteableFlex wraps *tview.Flex and overrides PasteHandler so that
// bracketed paste is routed to the TUI input buffer instead of being dropped.
type pasteableFlex struct {
	*tview.Flex
	onPaste func(text string)
}

func newPasteableFlex(onPaste func(text string)) *pasteableFlex {
	return &pasteableFlex{
		Flex:    tview.NewFlex(),
		onPaste: onPaste,
	}
}

func (p *pasteableFlex) PasteHandler() func(pastedText string, setFocus func(tview.Primitive)) {
	return func(pastedText string, _ func(tview.Primitive)) {
		if p.onPaste != nil {
			p.onPaste(pastedText)
		}
	}
}
