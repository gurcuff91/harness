package tui

import (
	"fmt"
	"os"

	goclip "golang.design/x/clipboard"
)

var clipboardReady bool

// initClipboard initializes the clipboard backend once.
func initClipboard() {
	if clipboardReady {
		return
	}
	if err := goclip.Init(); err == nil {
		clipboardReady = true
	}
}

// PasteImageFromClipboard checks if the clipboard contains a PNG image.
// If so, saves it to a temp file and returns the path.
// Returns ("", nil) if clipboard has no image.
func PasteImageFromClipboard() (string, error) {
	initClipboard()
	if !clipboardReady {
		return "", fmt.Errorf("clipboard not available on this system")
	}
	data := goclip.Read(goclip.FmtImage)
	if len(data) == 0 {
		return "", nil
	}
	f, err := os.CreateTemp("", "harness-clip-*.png")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", fmt.Errorf("write temp file: %w", err)
	}
	return f.Name(), nil
}
