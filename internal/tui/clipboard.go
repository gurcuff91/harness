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
//
// It can also PANIC rather than return an error: when the binary was built
// with CGO_ENABLED=0 (harness's own cross-compiled release binaries for
// darwin/*, linux/arm64 — see .github/workflows/release.yml), the library's
// non-Windows backend compiles to clipboard_nocgo.go, whose every function
// (initialize/read/write/watch) is a bare `panic("clipboard: cannot use when
// CGO_ENABLED=0")` — there is no error-returning path in that build at all.
// A panic inside this goroutine (pasteClipboardImage in layout.go runs it in
// one) would otherwise be unrecoverable and crash the entire harness
// process — Go does not let an unrecovered panic in one goroutine be caught
// from another. recoverClipboardPanic guards against exactly that: clipReady
// simply stays false, and callers see the same "clipboard not available on
// this system" error they'd get from any other init failure.
func initClipboard() {
	clipOnce.Do(func() {
		defer recoverClipboardPanic()
		if err := goclip.Init(); err == nil {
			clipReady = true
		}
	})
}

// recoverClipboardPanic turns a panic from the underlying clipboard library
// (see initClipboard's doc comment) into a no-op — clipReady already
// defaults to false, so nothing further is needed once the panic is caught.
func recoverClipboardPanic() {
	_ = recover()
}

// PasteImageFromClipboard checks whether the clipboard holds a PNG image and, if
// so, writes it to a temp file and returns the path. Returns ("", nil) when the
// clipboard has no image, and ("", err) when the clipboard is unavailable.
func PasteImageFromClipboard() (path string, err error) {
	initClipboard()
	if !clipReady {
		return "", fmt.Errorf("clipboard not available on this system")
	}
	// goclip.Read can ALSO panic under the same CGO_ENABLED=0 condition
	// initClipboard's doc comment describes — clipReady being true only
	// guarantees Init() itself succeeded, not that every subsequent call is
	// panic-free against every backend. Guard this call too, independently.
	var data []byte
	func() {
		defer recoverClipboardPanic()
		data = goclip.Read(goclip.FmtImage)
	}()
	if len(data) == 0 {
		return "", nil
	}
	f, ferr := os.CreateTemp("", "harness-clip-*.png")
	if ferr != nil {
		return "", fmt.Errorf("create temp file: %w", ferr)
	}
	defer f.Close()
	if _, ferr := f.Write(data); ferr != nil {
		return "", fmt.Errorf("write temp file: %w", ferr)
	}
	return f.Name(), nil
}
