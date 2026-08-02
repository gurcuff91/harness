package tui

import (
	"fmt"
	"os"
	"sync"

	goclip "golang.design/x/clipboard"
)

// Clipboard image paste. Ported from the v1 TUI (transport/tui/image.go): the
// clipboard's PNG is written to a temp file and its path is inserted into the
// editor as text. The Read tool resolves image paths, so the agent receives the
// image by reading that path — hence path-as-text rather than an inline attach.

var (
	clipOnce  sync.Once
	clipReady bool
)

// initClipboard initializes the clipboard backend exactly once. golang.design's
// clipboard.Init() must run before any Read; it can fail on headless systems.
func initClipboard() {
	clipOnce.Do(func() {
		if err := goclip.Init(); err == nil {
			clipReady = true
		}
	})
}

// PasteImageFromClipboard checks whether the clipboard holds a PNG image and, if
// so, writes it to a temp file and returns the path. Returns ("", nil) when the
// clipboard has no image, and ("", err) when the clipboard is unavailable.
func PasteImageFromClipboard() (string, error) {
	initClipboard()
	if !clipReady {
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
