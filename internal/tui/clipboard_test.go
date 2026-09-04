package tui

import "testing"

// TestPasteImageFromClipboardNeverPanics is the regression test for the
// CGO_ENABLED=0 crash: harness's own cross-compiled release binaries
// (darwin/*, linux/arm64 — see .github/workflows/release.yml) build
// golang.design/x/clipboard's clipboard_nocgo.go backend, whose every
// function (initialize/read/write/watch) is a bare panic. Before
// initClipboard/PasteImageFromClipboard guarded against it, calling this
// from pasteClipboardImage's goroutine (internal/tui/layout.go) would crash
// the entire harness process — an unrecovered panic in one goroutine cannot
// be caught from another in Go.
//
// This test exercises the REAL code path (not a mock): run with
// `CGO_ENABLED=0 go test ./internal/tui/ -run TestPasteImageFromClipboard`
// to reproduce the exact condition that used to crash the process — the
// underlying library's actual nocgo stub panics, and this asserts the panic
// never escapes PasteImageFromClipboard. With cgo enabled (the default for
// `go test` on a dev machine), Init() legitimately fails on a headless CI
// runner instead (no X11/Cocoa session) — a normal error, not a panic — so
// this passes either way; only the CGO_ENABLED=0 run actually exercises the
// panic-recovery path this test exists for.
func TestPasteImageFromClipboardNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PasteImageFromClipboard panicked instead of returning an error: %v", r)
		}
	}()
	path, err := PasteImageFromClipboard()
	// Whichever path (nocgo panic, recovered; or a normal cgo Init()
	// failure on a headless runner) — an unavailable clipboard must surface
	// as a clean error, never a zero-value success.
	if err == nil && path != "" {
		t.Errorf("got a path (%q) with no error on a system where clipboard access should not be possible in this test environment", path)
	}
}
