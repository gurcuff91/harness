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
	// Any of the three possible outcomes is acceptable here — the assertion
	// this test exists for is "NO PANIC EVER ESCAPES", which the defer above
	// enforces:
	//   1. CGO_ENABLED=0 build (release binaries): the nocgo stub panics and
	//      the recover() inside the function turns it into a clean error.
	//   2. cgo build, headless/no display (CI runners): Init() fails with a
	//      normal error.
	//   3. cgo build, real display (a dev machine): the clipboard works, and
	//      the result legitimately depends on what's IN it right now — an
	//      image yields (path, nil); an empty/text-only clipboard yields
	//      ("", nil). A machine's clipboard state at test time is outside
	//      the test's control (a screenshot taken a minute ago makes path
	//      non-empty), so neither specific outcome may be asserted here —
	//      only that err==nil && path=="" together is IMPOSSIBLE (an
	//      available clipboard with no image returns "" and nil; anything
	//      else carries an error).
	if err == nil && path == "" {
		t.Log("clipboard available, no image in it — valid outcome")
	}
}
