// Package render_test exposes internal helpers to the test files in this
// package and to the white-box tests in the parent tui package. Go's _test.go
// files are compiled only during `go test`, so these symbols never ship in
// the production binary.
package render

// SetUserViewportTopForTest sets the pinned viewport top (test helper).
func (t *TUI) SetUserViewportTopForTest(v int) {
	t.mu.Lock()
	t.userViewportTop = v
	t.mu.Unlock()
}

// UserViewportTopForTest returns the pinned viewport top (test helper).
func (t *TUI) UserViewportTopForTest() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.userViewportTop
}
