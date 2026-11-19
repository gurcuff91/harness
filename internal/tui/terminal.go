package tui

import (
	"os"
	"strings"
)

// TerminalName is a readable name for the terminal this process runs in, for
// the SessionInfo tool: best effort, from what terminals put in the
// environment (there is no universal way to ask). Herdr is checked first: it
// sets TERM_PROGRAM=ghostty too.
func TerminalName() string { return terminalName(os.Getenv) }

func terminalName(getenv func(string) string) string {
	env := func(k string) string { return strings.TrimSpace(getenv(k)) }
	if id := env(herdrTerminalEnv); id != "" {
		return "Herdr (" + id + ")"
	}
	if p := env("TERM_PROGRAM"); p != "" {
		switch strings.ToLower(p) {
		case "ghostty":
			return "Ghostty"
		case "wezterm":
			return "WezTerm"
		case "iterm.app":
			return "iTerm2"
		case "apple_terminal":
			return "Terminal.app"
		case "vscode":
			return "VS Code"
		case "tmux":
			return "tmux"
		}
		return p
	}
	switch {
	case env("WT_SESSION") != "":
		return "Windows Terminal"
	case env("KITTY_WINDOW_ID") != "":
		return "kitty"
	case env("ALACRITTY_SOCKET") != "", env("ALACRITTY_LOG") != "":
		return "Alacritty"
	case env("KONSOLE_VERSION") != "":
		return "Konsole"
	case env("VTE_VERSION") != "":
		return "VTE terminal"
	case env("TERMINAL_EMULATOR") == "JetBrains-JediTerm":
		return "JetBrains"
	}
	return "Generic terminal"
}
