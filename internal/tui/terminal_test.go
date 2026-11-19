package tui

import "testing"

func TestTerminalName(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"HC_TERMINAL_ID": "t16", "TERM_PROGRAM": "ghostty"}, "Herdr (t16)"}, // Herdr first
		{map[string]string{"TERM_PROGRAM": "ghostty"}, "Ghostty"},
		{map[string]string{"TERM_PROGRAM": "WezTerm"}, "WezTerm"},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, "iTerm2"},
		{map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, "Terminal.app"},
		{map[string]string{"TERM_PROGRAM": "vscode"}, "VS Code"},
		{map[string]string{"TERM_PROGRAM": "tmux", "KITTY_WINDOW_ID": "1"}, "tmux"},
		{map[string]string{"TERM_PROGRAM": "Hyper"}, "Hyper"}, // unknown: as is
		{map[string]string{"WT_SESSION": "x"}, "Windows Terminal"},
		{map[string]string{"KITTY_WINDOW_ID": "1"}, "kitty"},
		{map[string]string{"ALACRITTY_LOG": "/tmp/x"}, "Alacritty"},
		{map[string]string{"KONSOLE_VERSION": "230"}, "Konsole"},
		{map[string]string{"VTE_VERSION": "7000"}, "VTE terminal"},
		{map[string]string{"TERMINAL_EMULATOR": "JetBrains-JediTerm"}, "JetBrains"},
		{map[string]string{"TERM": "xterm-256color"}, "Generic terminal"},
		{map[string]string{"HC_TERMINAL_ID": "  "}, "Generic terminal"},
	} {
		if got := terminalName(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v → %q, want %q", c.env, got, c.want)
		}
	}
}
