package cli

import (
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
)

// rawInput reads a line from the terminal in raw mode, allowing
// interception of Ctrl+V for clipboard image paste.
type rawInput struct {
	onPaste func() string // called on Ctrl+V, returns text to insert
}

func newRawInput(onPaste func() string) *rawInput {
	return &rawInput{onPaste: onPaste}
}

// ReadLine reads a single line with raw input handling.
// Returns the line text and whether the user wants to quit (Ctrl+C/Ctrl+D).
func (r *rawInput) ReadLine() (string, bool) {
	// Switch to raw mode using x/term (cross-platform)
	fd := os.Stdin.Fd()
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		// Fallback: can't set raw mode
		return "", true
	}
	defer term.Restore(fd, oldState)

	var buf []byte
	b := make([]byte, 4) // enough for multi-byte UTF-8

	for {
		n, err := os.Stdin.Read(b)
		if err != nil || n == 0 {
			continue
		}

		ch := b[0]

		switch {
		case ch == 13 || ch == 10: // Enter
			fmt.Print("\r\n")
			return string(buf), false

		case ch == 3: // Ctrl+C
			fmt.Print("\r\n")
			return "", true

		case ch == 4: // Ctrl+D (EOF)
			fmt.Print("\r\n")
			return "", true

		case ch == 22: // Ctrl+V — clipboard paste
			if r.onPaste != nil {
				text := r.onPaste()
				if text != "" {
					buf = append(buf, []byte(text)...)
					fmt.Print(text)
				}
			}

		case ch == 127 || ch == 8: // Backspace / Delete
			if len(buf) > 0 {
				_, size := utf8.DecodeLastRune(buf)
				buf = buf[:len(buf)-size]
				fmt.Print("\b \b")
			}

		case ch == 27: // Escape sequence (arrow keys, etc.)
			// Read remaining bytes of escape sequence and discard
			discard := make([]byte, 8)
			os.Stdin.Read(discard)

		case ch == 21: // Ctrl+U — clear line
			for range utf8.RuneCount(buf) {
				fmt.Print("\b \b")
			}
			buf = buf[:0]

		case ch == 23: // Ctrl+W — delete word
			for len(buf) > 0 {
				_, size := utf8.DecodeLastRune(buf)
				was := buf[len(buf)-size]
				buf = buf[:len(buf)-size]
				fmt.Print("\b \b")
				if was == ' ' {
					break
				}
			}

		case ch >= 32: // Printable characters
			buf = append(buf, b[:n]...)
			fmt.Print(string(b[:n]))
		}
	}
}
