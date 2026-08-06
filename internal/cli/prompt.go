package cli

import (
	"bufio"
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

// PromptLine reads a single visible line from the terminal (the value IS
// echoed — unlike PromptSecret). Used for values the user pastes and wants to
// see, e.g. the OAuth authorization code. Requires an interactive TTY;
// returns ErrNoTTY otherwise so the caller can print a clean message instead
// of blocking on input that will never arrive.
func PromptLine(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", ErrNoTTY
	}
	fmt.Print(label)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
