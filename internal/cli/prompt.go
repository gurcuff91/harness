package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ErrNoTTY is returned by PromptSecret when stdin is not an interactive
// terminal (e.g. a pipe or CI), so the caller can surface a clean "pass it as
// an argument" message instead of blocking on input that will never arrive.
var ErrNoTTY = errors.New("not a terminal")

// PromptSecret reads a secret from the terminal WITHOUT echoing it (like sudo).
// It only prompts when stdin is an interactive TTY; otherwise it returns
// ErrNoTTY. The returned value is trimmed of surrounding whitespace.
func PromptSecret(label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", ErrNoTTY
	}
	fmt.Print(label)
	b, err := term.ReadPassword(fd)
	fmt.Println() // ReadPassword swallows the Enter; emit the newline ourselves
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
