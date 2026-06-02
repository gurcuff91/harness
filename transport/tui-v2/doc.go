// Package tuiv2 implements a minimalist TUI engine with differential rendering,
// inspired by @mariozechner/pi-tui. Zero external TUI dependencies — only x/term + stdlib.
//
// Architecture:
//
//	Terminal (raw mode, stdin, resize signals)
//	   ↓
//	TUI (event loop + diff render)
//	   ↓
//	Components: Output | Footer | Input
//
// Each component implements Render(width int) []string and returns ANSI-styled lines.
// The TUI diffs previous vs new lines and writes only changed rows to the terminal.
package tuiv2
