package tuiv2

import (
	"strings"
)

type Input struct {
	value       string
	placeholder string
	onSubmit    func(string)
	onQuit      func() // Ctrl+C / Ctrl+D handler
}

func NewInput(placeholder string, _ int, onSubmit func(string)) *Input {
	return &Input{
		placeholder: placeholder,
		onSubmit:    onSubmit,
	}
}

func (i *Input) Value() string { return i.value }

func (i *Input) HandleKey(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	switch {
	case data[0] == 3: // Ctrl+C
		if i.onQuit != nil {
			i.onQuit()
		}
		return true
	case data[0] == 4 && i.value == "": // Ctrl+D empty
		if i.onQuit != nil {
			i.onQuit()
		}
		return true
	case data[0] == '\r' || data[0] == '\n':
		if i.value != "" && i.onSubmit != nil {
			i.onSubmit(strings.TrimSpace(i.value))
		}
		i.value = ""
		return true
	case data[0] == 127 || data[0] == 8: // Backspace
		if len(i.value) > 0 {
			i.value = i.value[:len(i.value)-1]
		}
		return true
	case data[0] == 27: // Escape — ignore
		return true
	case data[0] >= 32 && data[0] < 127:
		i.value += string(data)
		return true
	case data[0] >= 0xC0: // UTF-8 multi-byte
		i.value += string(data)
		return true
	}
	return false
}

func (i *Input) Render(width int) []string {
	if i.value == "" {
		return []string{" " + "\033[32m> \033[0m" + "\033[90m" + i.placeholder + "\033[0m"}
	}
	return []string{" " + "\033[32m> \033[0m" + i.value + "\033[7m \033[0m"}
}
